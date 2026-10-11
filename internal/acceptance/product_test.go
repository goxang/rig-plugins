// Package acceptance runs rig's built-in AI end to end over the official plugins, with a scripted
// stand-in for the model (testdata/fakeai) and recorded forge APIs (testdata/*.json).
package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig-plugins/internal/forge/forgetest"
)

type step struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Expect string         `json:"expect,omitempty"`
	Fails  bool           `json:"fails,omitempty"`
}

func call(action string, args map[string]any, expect string) step {
	return step{Tool: "rig_call", Args: map[string]any{"action": action, "args": args, "user_request": "run the agent"}, Expect: expect}
}

func read(source string, args map[string]any, expect string) step {
	return step{Tool: "rig_read", Args: map[string]any{"source": source, "args": args}, Expect: expect}
}

type world struct {
	t   *testing.T
	rig string
	dir string
	env []string
}

func (w *world) run(script []step, args ...string) string {
	w.t.Helper()
	file := filepath.Join(w.t.TempDir(), "script.json")
	raw, _ := json.Marshal(script)
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	return w.rigOK(append([]string{"FAKEAI_SCRIPT=" + file}, w.env...), args...)
}

func (w *world) rigOK(env []string, args ...string) string {
	w.t.Helper()
	c := exec.Command(w.rig, args...)
	c.Dir, c.Env = w.dir, append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		w.t.Fatalf("rig %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// approveAll approves every pending inbox item as the person at the keyboard and returns what ran.
func (w *world) approveAll() []string {
	w.t.Helper()
	var ran []string
	for _, line := range strings.Split(w.rigOK(w.env, "--brief", "inbox"), "\n")[1:] {
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[1] != "pending" {
			continue
		}
		w.rigOK(w.env, "inbox", "approve", f[0])
		ran = append(ran, f[3])
	}
	return ran
}

func build(t *testing.T, out, pkg string) string {
	t.Helper()
	bin := filepath.Join(out, filepath.Base(pkg))
	if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

// The product flow of docs/plan.md section 10.8, spec to stories to MR link to status to report, run
// by pack-product's agents: every write waits in the inbox until a person approves it.
func TestProductFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("builds rig")
	}
	bin := t.TempDir()
	rig := build(t, bin, "github.com/goxang/rig/cmd/rig")
	std := build(t, bin, "github.com/goxang/rig-plugins/cmd/rig-std")
	fake := build(t, bin, "./testdata/fakeai")
	if err := os.Rename(fake, filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}

	var recorded map[string]json.RawMessage
	raw, err := os.ReadFile("testdata/product.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	routes := map[string]string{}
	for k, v := range recorded {
		routes[k] = string(v)
	}
	api := forgetest.New(t, routes)

	home := t.TempDir()
	w := &world{t: t, rig: rig, dir: t.TempDir(), env: []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "RIG_TRUST=project", "NO_COLOR=1",
	}}
	inst := exec.Command(std, "install")
	inst.Env = append(os.Environ(), w.env...)
	if out, err := inst.CombinedOutput(); err != nil {
		t.Fatalf("rig-std install: %v\n%s", err, out)
	}
	w.rigOK(w.env, "ai", "config", "backend=claude")

	var pack struct {
		Agents map[string]any `yaml:"agents"`
	}
	raw, err = os.ReadFile("../../plugins/pack-product/rig-plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	project, _ := yaml.Marshal(map[string]any{
		"version":      1,
		"project":      "shop",
		"default":      "dev",
		"environments": map[string]any{"dev": map[string]any{"runtime": map[string]any{"type": "local"}}},
		"plugins": map[string]any{
			"jira":       map[string]any{"url": api.URL, "project": "SHOP"},
			"confluence": map[string]any{"url": api.URL + "/wiki", "space": "ENG"},
			"github":     map[string]any{"url": api.URL, "project": "acme/shop"},
		},
		"agents": pack.Agents,
	})
	if err := os.WriteFile(filepath.Join(w.dir, "rig.yaml"), project, 0o600); err != nil {
		t.Fatal(err)
	}

	// spec to stories: the agent drafts from the page; nothing reaches Jira before approval
	w.run([]step{
		{Tool: "rig_find", Args: map[string]any{"query": "create issue"}, Expect: "jira.tracker.create"},
		read("confluence.docs.read", map[string]any{"id": "100"}, "Retry on timeout and 5xx only"),
		call("jira.tracker.create", map[string]any{"summary": "Retry failed bank calls", "description": "Retry on timeout and 5xx only. Spec: confluence page 100", "type": "Story"}, "waits for approval"),
		call("jira.tracker.create", map[string]any{"summary": "Retry count on the receipt", "description": "Show the retry count on the receipt page. Spec: confluence page 100", "type": "Story"}, "waits for approval"),
	}, "agent", "run", "spec-to-backlog")
	if api.Called("POST /rest/api/2/issue") {
		t.Fatal("a story was created before approval")
	}
	if ran := w.approveAll(); len(ran) != 2 || !api.Called(`POST /rest/api/2/issue {"fields":{"description":"Retry on timeout`) {
		t.Fatalf("approved %v; calls missing", ran)
	}

	// MR link and status: the open MR moves its issue to In review, the merged one to Ready for QA
	w.run([]step{
		read("github.scm.mrs", map[string]any{"state": "all"}, "SHOP-7-retries"),
		call("jira.tracker.transition", map[string]any{"key": "SHOP-7", "status": "In review"}, "waits for approval"),
		call("jira.tracker.comment", map[string]any{"key": "SHOP-7", "body": "MR: https://git.example/acme/shop/pull/12"}, "waits for approval"),
		call("jira.tracker.transition", map[string]any{"key": "SHOP-8", "status": "Ready for QA"}, "waits for approval"),
	}, "agent", "run", "mr-sync")
	if api.Called("POST /rest/api/2/issue/SHOP-7") {
		t.Fatal("an issue moved before approval")
	}
	w.approveAll()
	for _, want := range []string{
		`POST /rest/api/2/issue/SHOP-7/transitions {"transition":{"id":"41"}}`,
		`POST /rest/api/2/issue/SHOP-7/comment {"body":"MR: https://git.example/acme/shop/pull/12"}`,
		`POST /rest/api/2/issue/SHOP-8/transitions {"transition":{"id":"51"}}`,
	} {
		if !api.Called(want) {
			t.Errorf("not called after approval: %s", want)
		}
	}

	// report: a Confluence page and a comment linking it
	w.run([]step{
		read("github.scm.mrs", map[string]any{"state": "merged"}, "SHOP-8 retry count"),
		read("jira.tracker.issues", map[string]any{"query": "project = SHOP"}, "In review"),
		call("confluence.docs.write", map[string]any{"title": "Release report", "markdown": "# Release report\n\n- SHOP-8 merged (MR 11)\n- SHOP-7 in review (MR 12)"}, "waits for approval"),
		call("jira.tracker.comment", map[string]any{"key": "SHOP-8", "body": "Release report: page 200"}, "waits for approval"),
	}, "agent", "run", "release-report")
	if api.Called("POST /wiki/rest/api/content") {
		t.Fatal("the report was saved before approval")
	}
	w.approveAll()
	if !api.Called(`POST /wiki/rest/api/content {"body":{"storage":`) || !api.Called(`POST /rest/api/2/issue/SHOP-8/comment`) {
		t.Fatal("the report was not saved after approval")
	}

	// an agent's write outside its approve list is refused, not queued
	w.run([]step{
		{Tool: "rig_call", Args: map[string]any{"action": "jira.tracker.assign", "args": map[string]any{"key": "SHOP-7", "assignee": "me"}}, Fails: true},
	}, "agent", "run", "analyst")

	// the built-in AI finds the install action and installs a plugin, once a person approves it
	arcade, _ := filepath.Abs("../../plugins/arcade")
	w.run([]step{
		{Tool: "rig_find", Args: map[string]any{"query": "install plugin"}, Expect: "plugin.add"},
		call("plugin.add", map[string]any{"names": []string{arcade}}, "waits for approval"),
	}, "ai", "install the arcade plugin")
	if strings.Contains(w.rigOK(w.env, "plugin", "ls"), "arcade") {
		t.Fatal("arcade was installed before approval")
	}
	if ran := w.approveAll(); len(ran) != 1 || !strings.Contains(w.rigOK(w.env, "plugin", "ls"), "arcade") {
		t.Fatalf("approved %v; arcade not installed:\n%s", ran, w.rigOK(w.env, "plugin", "ls"))
	}

	audit, _ := os.ReadFile(filepath.Join(w.dir, ".rig", "dev", "audit.jsonl"))
	if n := strings.Count(string(audit), `"approver":"user:`); n != 8 {
		t.Errorf("audit records %d approvals, want 8:\n%s", n, audit)
	}
}
