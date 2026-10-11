package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/sdk"
)

// testIn is what core's tests/1 asks a runner: the suite's dir, env, a -run regex and its with: options.
type testIn struct {
	Run  string            `json:"run"`
	Dir  string            `json:"dir"`
	Env  map[string]string `json:"env"`
	With struct {
		Args      []string          `json:"args"`
		Files     []string          `json:"files"`
		Script    string            `json:"script"`
		Command   string            `json:"command"`
		Reports   string            `json:"reports"`
		Variables map[string]string `json:"variables"`
	} `json:"with"`
}

// runner turns a suite into a shell command writing its results to out, and those results into events.
type runner struct {
	desc    string
	command func(in testIn, out string) (string, error)
	parse   func(raw []byte, pkg string) []testEvent
}

var testRunners = map[string]runner{
	"pytest": {"pytest, through its JUnit XML", func(in testIn, out string) (string, error) {
		cmd := "pytest --junitxml=" + q(out)
		if in.Run != "" && in.Run != ".*" {
			cmd += " -k " + q(in.Run)
		}
		return cmd + args(in.With.Args), nil
	}, junitEvents},
	"jest": {"jest, through its JSON results", func(in testIn, out string) (string, error) {
		cmd := "npx jest --ci --json --outputFile=" + q(out)
		if in.Run != "" && in.Run != ".*" {
			cmd += " -t " + q(in.Run)
		}
		return cmd + args(in.With.Args), nil
	}, jestEvents},
	"vitest": {"vitest, through its JUnit reporter", func(in testIn, out string) (string, error) {
		cmd := "npx vitest run --reporter=default --reporter=junit --outputFile.junit=" + q(out)
		if in.Run != "" && in.Run != ".*" {
			cmd += " -t " + q(in.Run)
		}
		return cmd + args(in.With.Args), nil
	}, junitEvents},
	"hurl": {"hurl HTTP tests; the suite's env are hurl variables", func(in testIn, out string) (string, error) {
		if len(in.With.Files) == 0 {
			return "", fmt.Errorf("hurl: with.files lists the .hurl files (globs)")
		}
		cmd := "hurl --test --report-junit " + q(out)
		for _, k := range slices.Sorted(maps.Keys(in.Env)) {
			cmd += " --variable " + q(k+"="+in.Env[k])
		}
		for _, k := range slices.Sorted(maps.Keys(in.With.Variables)) {
			cmd += " --variable " + q(k+"="+in.With.Variables[k])
		}
		for _, f := range in.With.Files {
			cmd += " " + f // a glob the shell expands
		}
		return cmd + args(in.With.Args), nil
	}, junitEvents},
	"k6": {"k6 checks and thresholds as tests", func(in testIn, out string) (string, error) {
		if in.With.Script == "" {
			return "", fmt.Errorf("k6: with.script is the test script")
		}
		return "k6 run --quiet --summary-export=" + q(out) + args(in.With.Args) + " " + q(in.With.Script), nil
	}, k6Events},
	"junit": {"any command that writes JUnit XML ($RIG_JUNIT or with.reports)", func(in testIn, out string) (string, error) {
		if in.With.Command == "" {
			return "", fmt.Errorf("junit: with.command runs the tests")
		}
		cmd := in.With.Command
		if in.With.Reports != "" {
			cmd = "(" + cmd + "); st=$?; cat " + in.With.Reports + " > " + q(out) + " 2>/dev/null; exit $st"
		}
		return cmd, nil
	}, junitEvents},
}

func init() {
	for name, r := range testRunners {
		actions[name] = func(p *sdk.Plugin) {
			p.Stream("tests.events", func(c *sdk.Ctx, in testIn, o *sdk.Out) error { return runTests(c, name, r, in, o) },
				sdk.Describe("runs the suite with "+r.desc+"; each line is a go test -json event"))
		}
	}
}

type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test,omitempty"`
	Output  string  `json:"Output,omitempty"`
	Elapsed float64 `json:"Elapsed,omitempty"`
}

func runTests(c *sdk.Ctx, name string, r runner, in testIn, o *sdk.Out) error {
	f, err := os.CreateTemp("", "rig-"+name+"-*")
	if err != nil {
		return err
	}
	out := f.Name()
	f.Close()
	os.Remove(out) // a stale empty file must not read as "no tests"
	defer os.Remove(out)
	script, err := r.command(in, out)
	if err != nil {
		return err
	}
	emit := func(e testEvent) error {
		b, _ := json.Marshal(e)
		return o.Line(string(b))
	}
	started := time.Now()
	_ = emit(testEvent{Action: "run", Package: name})
	cmd := exec.CommandContext(c, "sh", "-c", script)
	cmd.Dir = in.Dir
	cmd.Env = append(os.Environ(), "RIG_JUNIT="+out, "RIG_RUN="+in.Run)
	for k, v := range in.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		_ = emit(testEvent{Action: "output", Package: name, Output: sc.Text() + "\n"})
	}
	werr := cmd.Wait()
	raw, _ := os.ReadFile(out)
	carried := false // failed tests carry the failure, not the run as a whole
	for _, e := range r.parse(raw, name) {
		carried = carried || e.Action == "fail"
		_ = emit(e)
	}
	end := "pass"
	if werr != nil {
		if _, ok := werr.(*exec.ExitError); !ok {
			return werr
		}
		if !carried {
			end = "fail"
		}
	}
	return emit(testEvent{Action: end, Package: name, Elapsed: time.Since(started).Seconds()})
}

type junitSuites struct {
	Suites []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

type junitCase struct {
	Name    string  `xml:"name,attr"`
	Class   string  `xml:"classname,attr"`
	Time    float64 `xml:"time,attr"`
	Failure *struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	} `xml:"failure"`
	Error *struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	} `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

// junitEvents reads one or more JUnit documents (a reports glob concatenates them), <testsuites> or
// bare <testsuite>; each class (else suite) is a package.
func junitEvents(raw []byte, pkg string) []testEvent {
	var suites []junitSuite
	d := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "testsuites":
			var s junitSuites
			if d.DecodeElement(&s, &se) == nil {
				suites = append(suites, s.Suites...)
			}
		case "testsuite":
			var s junitSuite
			if d.DecodeElement(&s, &se) == nil {
				suites = append(suites, s)
			}
		}
	}
	var out []testEvent
	var walk func(s junitSuite)
	walk = func(s junitSuite) {
		for _, c := range s.Cases {
			p := cmp.Or(c.Class, s.Name, pkg)
			out = append(out, testEvent{Action: "run", Package: p, Test: c.Name})
			status := "pass"
			for _, f := range []*struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			}{c.Failure, c.Error} {
				if f != nil {
					status = "fail"
					for _, l := range strings.Split(strings.TrimSpace(cmp.Or(f.Text, f.Message)), "\n") {
						out = append(out, testEvent{Action: "output", Package: p, Test: c.Name, Output: l + "\n"})
					}
				}
			}
			if c.Skipped != nil && status == "pass" {
				status = "skip"
			}
			out = append(out, testEvent{Action: status, Package: p, Test: c.Name, Elapsed: c.Time})
		}
		for _, sub := range s.Suites {
			walk(sub)
		}
	}
	for _, s := range suites {
		walk(s)
	}
	return out
}

func jestEvents(raw []byte, pkg string) []testEvent {
	var res struct {
		TestResults []struct {
			Name       string `json:"name"`
			Assertions []struct {
				FullName string   `json:"fullName"`
				Status   string   `json:"status"`
				Duration float64  `json:"duration"`
				Failures []string `json:"failureMessages"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return nil
	}
	var out []testEvent
	for _, f := range res.TestResults {
		p := cmp.Or(filepath.Base(f.Name), pkg)
		for _, a := range f.Assertions {
			status := map[string]string{"passed": "pass", "failed": "fail"}[a.Status]
			if status == "" {
				status = "skip"
			}
			out = append(out, testEvent{Action: "run", Package: p, Test: a.FullName})
			for _, m := range a.Failures {
				for _, l := range strings.Split(m, "\n") {
					out = append(out, testEvent{Action: "output", Package: p, Test: a.FullName, Output: l + "\n"})
				}
			}
			out = append(out, testEvent{Action: status, Package: p, Test: a.FullName, Elapsed: a.Duration / 1000})
		}
	}
	return out
}

// k6Events makes each check (by group) and each threshold a test of the summary export.
func k6Events(raw []byte, pkg string) []testEvent {
	type group struct {
		Name   string `json:"name"`
		Checks map[string]struct {
			Passes int `json:"passes"`
			Fails  int `json:"fails"`
		} `json:"checks"`
		Groups map[string]json.RawMessage `json:"groups"`
	}
	var sum struct {
		Root    json.RawMessage `json:"root_group"`
		Metrics map[string]struct {
			Thresholds map[string]bool `json:"thresholds"`
		} `json:"metrics"`
	}
	if json.Unmarshal(raw, &sum) != nil {
		return nil
	}
	var out []testEvent
	add := func(p, name string, ok bool, why string) {
		out = append(out, testEvent{Action: "run", Package: p, Test: name})
		status := "pass"
		if !ok {
			status = "fail"
			out = append(out, testEvent{Action: "output", Package: p, Test: name, Output: why + "\n"})
		}
		out = append(out, testEvent{Action: status, Package: p, Test: name})
	}
	var walk func(raw json.RawMessage, path string)
	walk = func(raw json.RawMessage, path string) {
		var g group
		if json.Unmarshal(raw, &g) != nil {
			return
		}
		p := pkg
		if g.Name != "" {
			path = strings.TrimPrefix(path+"::"+g.Name, "::")
			p = pkg + "/" + path
		}
		for _, n := range slices.Sorted(maps.Keys(g.Checks)) {
			c := g.Checks[n]
			add(p, n, c.Fails == 0, strconv.Itoa(c.Fails)+" of "+strconv.Itoa(c.Passes+c.Fails)+" failed")
		}
		for _, n := range slices.Sorted(maps.Keys(g.Groups)) {
			walk(g.Groups[n], path)
		}
	}
	walk(sum.Root, "")
	for _, m := range slices.Sorted(maps.Keys(sum.Metrics)) {
		for _, th := range slices.Sorted(maps.Keys(sum.Metrics[m].Thresholds)) {
			// k6 marks a threshold true when it was crossed
			add(pkg+"/thresholds", m+" "+th, !sum.Metrics[m].Thresholds[th], "threshold crossed")
		}
	}
	return out
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func args(a []string) string {
	var b strings.Builder
	for _, s := range a {
		b.WriteString(" " + q(s))
	}
	return b.String()
}
