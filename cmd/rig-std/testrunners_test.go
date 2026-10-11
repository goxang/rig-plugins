package main

import (
	"strings"
	"testing"
)

func TestRunnerResults(t *testing.T) {
	junit := `<?xml version="1.0"?><testsuites><testsuite name="tests/test_api.py">
<testcase classname="tests.test_api" name="test_ok" time="0.01"/>
<testcase classname="tests.test_api" name="test_bad" time="0.02"><failure message="assert 1 == 2">E  assert 1 == 2</failure></testcase>
<testcase classname="tests.test_api" name="test_later"><skipped/></testcase></testsuite></testsuites>`
	jest := `{"testResults":[{"name":"/w/sum.test.js","assertionResults":[{"fullName":"sum adds","status":"passed","duration":3},{"fullName":"sum fails","status":"failed","failureMessages":["Expected 3"]}]}]}`
	k6 := `{"root_group":{"name":"","checks":{"status is 200":{"passes":9,"fails":1}},"groups":{"login":{"name":"login","checks":{"has token":{"passes":5,"fails":0}},"groups":{}}}},
"metrics":{"http_req_duration":{"thresholds":{"p(95)<200":false}},"http_req_failed":{"thresholds":{"rate<0.01":true}}}}`
	for name, c := range map[string]struct {
		events []testEvent
		want   string
	}{
		"junit": {junitEvents([]byte(junit), "pytest"), "tests.test_api test_ok pass|tests.test_api test_bad fail|tests.test_api test_later skip"},
		"jest":  {jestEvents([]byte(jest), "jest"), "sum.test.js sum adds pass|sum.test.js sum fails fail"},
		"k6":    {k6Events([]byte(k6), "k6"), "k6 status is 200 fail|k6/login has token pass|k6/thresholds http_req_duration p(95)<200 pass|k6/thresholds http_req_failed rate<0.01 fail"},
	} {
		var got []string
		for _, e := range c.events {
			if e.Action == "pass" || e.Action == "fail" || e.Action == "skip" {
				got = append(got, e.Package+" "+e.Test+" "+e.Action)
			}
		}
		if g := strings.Join(got, "|"); g != c.want {
			t.Errorf("%s:\n got %s\nwant %s", name, g, c.want)
		}
	}
}
