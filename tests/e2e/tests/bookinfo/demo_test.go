// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	multierror "github.com/hashicorp/go-multierror"

	"istio.io/istio/pkg/log"
	"istio.io/istio/tests/e2e/framework"
	"istio.io/istio/tests/util"
)

const (
	u1                                 = "normal-user"
	u2                                 = "test-user"
	bookinfoSampleDir                  = "samples/bookinfo"
	yamlExtension                      = "yaml"
	deploymentDir                      = "kube"
	routeRulesDir                      = "kube"
	tutorialDir                        = "istio.io_tutorial"
	bookinfoYaml                       = "bookinfo"
	bookinfoRatingsv2Yaml              = "bookinfo-ratings-v2"
	bookinfoRatingsMysqlYaml           = "bookinfo-ratings-v2-mysql"
	bookinfoDbYaml                     = "bookinfo-db"
	bookinfoMysqlYaml                  = "bookinfo-mysql"
	bookinfoDetailsExternalServiceYaml = "bookinfo-details-v2"
	modelDir                           = "tests/apps/bookinfo/output"
	allRule                            = routeRulesDir + "/" + "route-rule-all-v1"
	delayRule                          = routeRulesDir + "/" + "route-rule-ratings-test-delay"
	tenRule                            = tutorialDir + "/" + "/route-rule-reviews-90-10"
	twentyRule                         = tutorialDir + "/" + "route-rule-reviews-80-20"
	fiftyRule                          = routeRulesDir + "/" + "route-rule-reviews-50-v3"
	testRule                           = routeRulesDir + "/" + "route-rule-reviews-test-v2"
	testDbRule                         = routeRulesDir + "/" + "route-rule-ratings-db"
	testMysqlRule                      = routeRulesDir + "/" + "route-rule-ratings-mysql"
	detailsExternalServiceRouteRule    = routeRulesDir + "/" + "route-rule-details-v2"
	detailsExternalServiceEgressRule   = routeRulesDir + "/" + "egress-rule-google-apis"
)

var (
	tc             *testConfig
	testRetryTimes = 5
	defaultRules   = []string{allRule}
)

type testConfig struct {
	*framework.CommonConfig
	rulesDir string
}

func getWithCookie(url string, cookies []http.Cookie) (*http.Response, error) {
	// Declare http client
	client := &http.Client{}

	// Declare HTTP Method and Url
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for _, c := range cookies {
		// Set cookie
		req.AddCookie(&c)
	}
	return client.Do(req)
}

func closeResponseBody(r *http.Response) {
	if err := r.Body.Close(); err != nil {
		log.Errora(err)
	}
}

func (t *testConfig) Setup() error {
	//generate rule yaml files, replace "jason" with actual user
	for _, rule := range []string{allRule, delayRule, tenRule, twentyRule, fiftyRule, testRule,
		testDbRule, testMysqlRule, detailsExternalServiceRouteRule, detailsExternalServiceEgressRule} {
		src := util.GetResourcePath(filepath.Join(bookinfoSampleDir, rule+"."+yamlExtension))
		dest := filepath.Join(t.rulesDir, rule+"."+yamlExtension)
		ori, err := ioutil.ReadFile(src)
		if err != nil {
			log.Errorf("Failed to read original rule file %s", src)
			return err
		}
		content := string(ori)
		content = strings.Replace(content, "jason", u2, -1)

		err = os.MkdirAll(filepath.Dir(dest), 0700)
		if err != nil {
			log.Errorf("Failed to create the directory %s", filepath.Dir(dest))
			return err
		}

		err = ioutil.WriteFile(dest, []byte(content), 0600)
		if err != nil {
			log.Errorf("Failed to write into new rule file %s", dest)
			return err
		}

	}

	if !util.CheckPodsRunning(tc.Kube.Namespace) {
		return fmt.Errorf("can't get all pods running")
	}

	return setUpDefaultRouting()
}

func (t *testConfig) Teardown() error {
	if err := deleteRules(defaultRules); err != nil {
		// don't report errors if the rule being deleted doesn't exist
		if notFound := strings.Contains(err.Error(), "not found"); notFound {
			return nil
		}
		return err
	}
	return nil
}

func check(err error, msg string) {
	if err != nil {
		log.Errorf("%s. Error %s", msg, err)
		os.Exit(-1)
	}
}

func inspect(err error, fMsg, sMsg string, t *testing.T) {
	if err != nil {
		log.Errorf("%s. Error %s", fMsg, err)
		t.Error(err)
	} else if sMsg != "" {
		log.Info(sMsg)
	}
}

func setUpDefaultRouting() error {
	if err := applyRules(defaultRules); err != nil {
		return fmt.Errorf("could not apply rule '%s': %v", allRule, err)
	}
	standby := 0
	for i := 0; i <= testRetryTimes; i++ {
		time.Sleep(time.Duration(standby) * time.Second)
		gateway, errGw := tc.Kube.Ingress()
		if errGw != nil {
			return errGw
		}
		resp, err := http.Get(fmt.Sprintf("%s/productpage", gateway))
		if err != nil {
			log.Infof("Error talking to productpage: %s", err)
		} else {
			log.Infof("Get from page: %d", resp.StatusCode)
			if resp.StatusCode == http.StatusOK {
				log.Info("Get response from product page!")
				break
			}
			closeResponseBody(resp)
		}
		if i == testRetryTimes {
			return errors.New("unable to set default route")
		}
		standby += 5
		log.Errorf("Couldn't get to the bookinfo product page, trying again in %d second", standby)
	}
	log.Info("Success! Default route got expected response")
	return nil
}

func checkRoutingResponse(user, version, gateway, modelFile string) (int, error) {
	startT := time.Now()
	cookies := []http.Cookie{
		{
			Name:  "foo",
			Value: "bar",
		},
		{
			Name:  "user",
			Value: user,
		},
	}
	resp, err := getWithCookie(fmt.Sprintf("%s/productpage", gateway), cookies)
	if err != nil {
		return -1, err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("status code is %d", resp.StatusCode)
	}
	duration := int(time.Since(startT) / (time.Second / time.Nanosecond))
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}

	if err = util.CompareToFile(body, modelFile); err != nil {
		log.Errorf("Error: User %s in version %s didn't get expected response", user, version)
		duration = -1
	}
	return duration, err
}

func checkHTTPResponse(user, gateway, expr string, count int) (int, error) {
	resp, err := http.Get(fmt.Sprintf("%s/productpage", gateway))
	if err != nil {
		return -1, err
	}

	defer closeResponseBody(resp)
	log.Infof("Get from page: %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		log.Errorf("Get response from product page failed!")
		return -1, fmt.Errorf("status code is %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return -1, err
	}

	if expr == "" {
		return 1, nil
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		return -1, err
	}

	ref := re.FindAll(body, -1)
	if ref == nil {
		log.Infof("%v", string(body))
		return -1, fmt.Errorf("could not find %v in response", expr)
	}
	if count > 0 && len(ref) < count {
		log.Infof("%v", string(body))
		return -1, fmt.Errorf("could not find %v # of %v in response. found %v", count, expr, len(ref))
	}
	return 1, nil
}

func deleteRules(ruleKeys []string) error {
	var err error
	for _, ruleKey := range ruleKeys {
		rule := filepath.Join(tc.rulesDir, ruleKey+"."+yamlExtension)
		if e := util.KubeDelete(tc.Kube.Namespace, rule); e != nil {
			err = multierror.Append(err, e)
		}
	}
	log.Info("Waiting for rule to be cleaned up...")
	time.Sleep(time.Duration(30) * time.Second)
	return err
}

func applyRules(ruleKeys []string) error {
	for _, ruleKey := range ruleKeys {
		rule := filepath.Join(tc.rulesDir, ruleKey+"."+yamlExtension)
		if err := util.KubeApply(tc.Kube.Namespace, rule); err != nil {
			//log.Errorf("Kubectl apply %s failed", rule)
			return err
		}
	}
	log.Info("Waiting for rules to propagate...")
	time.Sleep(time.Duration(30) * time.Second)
	return nil
}

type userVersion struct {
	user    string
	version string
	model   string
}

type versionRoutingRule struct {
	key          string
	userVersions []userVersion
}

func TestVersionRouting(t *testing.T) {
	v1Model := util.GetResourcePath(filepath.Join(modelDir, "productpage-normal-user-v1.html"))
	v2TestModel := util.GetResourcePath(filepath.Join(modelDir, "productpage-test-user-v2.html"))

	var rules = []versionRoutingRule{
		{key: testRule,
			userVersions: []userVersion{
				{
					user:    u1,
					version: "v1",
					model:   v1Model,
				},
				{
					user:    u2,
					version: "v2",
					model:   v2TestModel,
				},
			},
		},
	}

	for _, rule := range rules {
		doTestVersionRouting(t, rule)
	}
}

func doTestVersionRouting(t *testing.T, rule versionRoutingRule) {
	inspect(applyRules([]string{rule.key}), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules([]string{rule.key}), fmt.Sprintf("failed to delete rules"), "", t)
	}()

	for _, userVersion := range rule.userVersions {
		_, err := checkRoutingResponse(userVersion.user, userVersion.version, tc.Kube.IngressOrFail(t),
			userVersion.model)
		inspect(
			err, fmt.Sprintf("Failed version routing! %s in %s", userVersion.user, userVersion.version),
			fmt.Sprintf("Success! Response matches with expected! %s in %s", userVersion.user,
				userVersion.version), t)
	}
}

func TestFaultDelay(t *testing.T) {
	var rules = []string{testRule, delayRule}
	inspect(applyRules(rules), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules(rules), "failed to delete rules", "", t)
	}()
	minDuration := 5
	maxDuration := 8
	standby := 10
	testModel := util.GetResourcePath(
		filepath.Join(modelDir, "productpage-test-user-v1-review-timeout.html"))
	for i := 0; i < testRetryTimes; i++ {
		duration, err := checkRoutingResponse(
			u2, "v1-timeout", tc.Kube.IngressOrFail(t),
			testModel)
		log.Infof("Get response in %d second", duration)
		if err == nil && duration >= minDuration && duration <= maxDuration {
			log.Info("Success! Fault delay as expected")
			break
		}

		if i == testRetryTimes-1 {
			t.Errorf("Fault delay failed! Delay in %ds while expected between %ds and %ds, %s",
				duration, minDuration, maxDuration, err)
			break
		}

		log.Infof("Unexpected response, retry in %ds", standby)
		time.Sleep(time.Duration(standby) * time.Second)
	}
}

type migrationRule struct {
	key            string
	rate           float64
	modelToMigrate string
}

func TestVersionMigration(t *testing.T) {
	modelV2 := util.GetResourcePath(filepath.Join(modelDir, "productpage-normal-user-v2.html"))
	modelV3 := util.GetResourcePath(filepath.Join(modelDir, "productpage-normal-user-v3.html"))

	var rules = []migrationRule{
		{
			key:            fiftyRule,
			modelToMigrate: modelV3,
			rate:           0.5,
		},
		{
			key:            twentyRule,
			modelToMigrate: modelV2,
			rate:           0.2,
		},
		{
			key:            tenRule,
			modelToMigrate: modelV2,
			rate:           0.1,
		},
	}

	for _, rule := range rules {
		doTestVersionMigration(t, rule)
	}
}

func doTestVersionMigration(t *testing.T, rule migrationRule) {
	inspect(applyRules([]string{rule.key}), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules([]string{rule.key}), fmt.Sprintf("failed to delete rules"), "", t)
	}()
	modelV1 := util.GetResourcePath(filepath.Join(modelDir, "productpage-normal-user-v1.html"))
	tolerance := 0.05
	totalShot := 100
	cookies := []http.Cookie{
		{
			Name:  "foo",
			Value: "bar",
		},
		{
			Name:  "user",
			Value: "normal-user",
		},
	}

	for i := 0; i < testRetryTimes; i++ {
		c1, cVersionToMigrate := 0, 0
		for c := 0; c < totalShot; c++ {
			resp, err := getWithCookie(fmt.Sprintf("%s/productpage", tc.Kube.IngressOrFail(t)), cookies)
			inspect(err, "Failed to record", "", t)
			if resp.StatusCode != http.StatusOK {
				log.Errorf("unexpected response status %d", resp.StatusCode)
				continue
			}
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				log.Errora(err)
				continue
			}
			if err = util.CompareToFile(body, modelV1); err == nil {
				c1++
			} else if err = util.CompareToFile(body, rule.modelToMigrate); err == nil {
				cVersionToMigrate++
			}
			closeResponseBody(resp)
		}

		if isWithinPercentage(c1, totalShot, 1.0-rule.rate, tolerance) &&
			isWithinPercentage(cVersionToMigrate, totalShot, rule.rate, tolerance) {
			log.Infof(
				"Success! Version migration acts as expected, "+
					"old version hit %d, new version hit %d", c1, cVersionToMigrate)
			break
		}

		if i == testRetryTimes-1 {
			t.Errorf("Failed version migration test, "+
				"old version hit %d, new version hit %d", c1, cVersionToMigrate)
		}
	}
}

func getBookinfoResourcePath(resource string) string {
	return util.GetResourcePath(filepath.Join(bookinfoSampleDir, deploymentDir,
		resource+"."+yamlExtension))
}

func isWithinPercentage(count int, total int, rate float64, tolerance float64) bool {
	minimum := int((rate - tolerance) * float64(total))
	maximum := int((rate + tolerance) * float64(total))
	return count >= minimum && count <= maximum
}

func setTestConfig() error {
	cc, err := framework.NewCommonConfig("demo_test")
	if err != nil {
		return err
	}
	tc = new(testConfig)
	tc.CommonConfig = cc
	tc.rulesDir, err = ioutil.TempDir(os.TempDir(), "demo_test")
	if err != nil {
		return err
	}
	demoApps := []framework.App{{AppYaml: getBookinfoResourcePath(bookinfoYaml),
		KubeInject: true,
	},
		{AppYaml: getBookinfoResourcePath(bookinfoRatingsv2Yaml),
			KubeInject: true,
		},
		{AppYaml: getBookinfoResourcePath(bookinfoRatingsMysqlYaml),
			KubeInject: true,
		},
		{AppYaml: getBookinfoResourcePath(bookinfoDbYaml),
			KubeInject: true,
		},
		{AppYaml: getBookinfoResourcePath(bookinfoMysqlYaml),
			KubeInject: true,
		},
		{AppYaml: getBookinfoResourcePath(bookinfoDetailsExternalServiceYaml),
			KubeInject: true,
		},
	}
	for i := range demoApps {
		tc.Kube.AppManager.AddApp(&demoApps[i])
	}
	return nil
}

func TestDbRoutingMongo(t *testing.T) {
	var err error
	var rules = []string{testDbRule}
	inspect(applyRules(rules), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules(rules), "failed to delete rules", "", t)
	}()

	// TODO: update the rating in the db and check the value on page

	respExpr := "glyphicon-star" // not great test for v2 or v3 being alive

	_, err = checkHTTPResponse(u1, tc.Kube.IngressOrFail(t), respExpr, 10)
	inspect(
		err, fmt.Sprintf("Failed database routing! %s in v1", u1),
		fmt.Sprintf("Success! Response matches with expected! %s", respExpr), t)
}

func TestDbRoutingMysql(t *testing.T) {
	var err error
	var rules = []string{testMysqlRule}
	inspect(applyRules(rules), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules(rules), "failed to delete rules", "", t)
	}()

	// TODO: update the rating in the db and check the value on page

	respExpr := "glyphicon-star" // not great test for v2 or v3 being alive

	_, err = checkHTTPResponse(u1, tc.Kube.IngressOrFail(t), respExpr, 10)
	inspect(
		err, fmt.Sprintf("Failed database routing! %s in v1", u1),
		fmt.Sprintf("Success! Response matches with expected! %s", respExpr), t)
}

func TestVMExtendsIstio(t *testing.T) {
	t.Skip("issue https://github.com/istio/istio/issues/4794")
	if *framework.TestVM {
		// TODO (chx) vm_provider flag to select venders
		vm, err := framework.NewGCPRawVM(tc.CommonConfig.Kube.Namespace)
		inspect(err, "unable to configure VM", "VM configured correctly", t)
		// VM setup and teardown is manual for now
		// will be replaced with preprovision server calls
		err = vm.Setup()
		inspect(err, "VM setup failed", "VM setup succeeded", t)
		_, err = vm.SecureShell("curl -v istio-pilot:8080")
		inspect(err, "VM failed to extend istio", "VM extends istio service mesh", t)
		_, err2 := vm.SecureShell(fmt.Sprintf(
			"host istio-pilot.%s.svc.cluster.local.", vm.Namespace))
		inspect(err2, "VM failed to extend istio", "VM extends istio service mesh", t)
		err = vm.Teardown()
		inspect(err, "VM teardown failed", "VM teardown succeeded", t)
	}
}

func TestExternalDetailsService(t *testing.T) {
	var err error
	var rules = []string{detailsExternalServiceRouteRule, detailsExternalServiceEgressRule}
	inspect(applyRules(rules), "failed to apply rules", "", t)
	defer func() {
		inspect(deleteRules(rules), "failed to delete rules", "", t)
	}()

	isbnFetchedFromExternalService := "0486424618"

	_, err = checkHTTPResponse(u1, tc.Kube.IngressOrFail(t), isbnFetchedFromExternalService, 1)
	inspect(
		err, fmt.Sprintf("Failed external details routing! %s in v1", u1),
		fmt.Sprintf("Success! Response matches with expected! %s", isbnFetchedFromExternalService), t)
}

func TestMain(m *testing.M) {
	flag.Parse()
	check(framework.InitLogging(), "cannot setup logging")
	check(setTestConfig(), "could not create TestConfig")
	tc.Cleanup.RegisterCleanable(tc)
	os.Exit(tc.RunTest(m))
}
