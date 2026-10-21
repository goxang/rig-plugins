package acceptance

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/protocol"
)

// Two layers of pack-org: a team serving its unit over rig serve, and the management unit above it
// reading it with a parent token, asking it for something through an agent, and seeing the answer.
func TestOrgFlow(t *testing.T) {
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
	pack := struct {
		Templates map[string]any `yaml:"templates"`
	}{Templates: map[string]any{}}
	for _, name := range []string{"org-team", "pack-org"} {
		raw, err := os.ReadFile("../../plugins/" + name + "/rig-plugin.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(raw, &pack); err != nil {
			t.Fatal(err)
		}
	}

	newWorld := func(project string, org map[string]any, agents map[string]any) *world {
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
		spec, _ := yaml.Marshal(map[string]any{
			"version": 1, "project": project, "default": "dev",
			"environments": map[string]any{"dev": map[string]any{"runtime": map[string]any{"type": "local"}}},
			"plugins":      map[string]any{"org": org},
			"templates":    pack.Templates,
			"agents":       agents,
		})
		if err := os.WriteFile(filepath.Join(w.dir, "rig.yaml"), spec, 0o600); err != nil {
			t.Fatal(err)
		}
		return w
	}

	team := newWorld("wallet", map[string]any{
		"unit": "wallet", "title": "Podicom wallet", "layer": 1, "parent": "product",
		"kpis":   []any{map[string]any{"name": "deploys_week", "value": 3, "ok": ">= 2"}},
		"people": []any{map[string]any{"name": "headcount", "value": 7}},
	}, map[string]any{"po": map[string]any{"extends": "product-owner"}})
	for user, role := range map[string]string{"product": "parent", "people": "hr"} {
		add := exec.Command(rig, "admin", "users", "add", user, "--role", role)
		add.Dir, add.Env, add.Stdin = team.dir, append(os.Environ(), team.env...), strings.NewReader("a long enough password\n")
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("users add %s: %v\n%s", user, err, out)
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serve := exec.CommandContext(ctx, rig, "serve", "--plain-http", "--addr", addr, "--no-agents")
	serve.Dir, serve.Env = team.dir, append(os.Environ(), team.env...)
	var serveLog strings.Builder
	serve.Stdout, serve.Stderr = &serveLog, &serveLog
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = serve.Wait() })
	url := "http://" + addr
	login := func(user string) *api.Client {
		c := &api.Client{URL: url}
		var out struct{ Token string }
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
			err := c.Do(ctx, "/v1/login", nil, map[string]string{"user": user, "password": "a long enough password"}, &out)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("login %s: %v\n%s", user, err, serveLog.String())
			}
		}
		c.Token = out.Token
		return c
	}

	// the parent's token reads the unit and nothing else; HR's reads people figures only
	parent, hr := login("product"), login("people")
	denied := func(err error) bool {
		var pe *protocol.Error
		return errors.As(err, &pe) && pe.Code == protocol.CodeDenied
	}
	if _, err := parent.Catalog(ctx); !denied(err) {
		t.Errorf("the parent token read the catalog: %v", err)
	}
	if _, err := hr.Query(ctx, "org.unit.kpis", nil); !denied(err) {
		t.Errorf("HR read delivery KPIs: %v", err)
	}
	if _, err := parent.Query(ctx, "org.unit.kpis", protocol.Args{"child": "anything"}); !denied(err) {
		t.Errorf("the parent token drilled below this unit: %v", err)
	}
	if _, err := hr.Query(ctx, "org.unit.people", nil); err != nil {
		t.Errorf("HR cannot read people figures: %v", err)
	}

	mgmt := newWorld("product", map[string]any{
		"unit": "product", "title": "Podicom product management", "layer": 2,
		"children": map[string]any{"wallet": map[string]any{"url": url, "token": parent.Token}},
		"kpis":     []any{map[string]any{"name": "deploys_week", "from": "children", "ok": ">= 2"}},
	}, map[string]any{"pm": map[string]any{"extends": "portfolio-manager"}})

	if out := mgmt.rigOK(mgmt.env, "--brief", "call", "org.unit.kpis"); !strings.Contains(out, "deploys_week") || !strings.Contains(out, "\t3\t") {
		t.Errorf("the roll-up misses the team's deploys:\n%s", out)
	}
	if out := mgmt.rigOK(mgmt.env, "--brief", "call", "org.unit.children"); !strings.Contains(out, "Podicom wallet") || !strings.Contains(out, "true") {
		t.Errorf("the team is not listed as reachable:\n%s", out)
	}

	// a critical risk at the team escalates; a low one stays there
	team.rigOK(team.env, "call", "org.unit.item", "kind=risk", "title=HSM certificate expires", "severity=critical")
	team.rigOK(team.env, "call", "org.unit.item", "kind=highlight", "title=release out", "severity=low")
	if out := mgmt.rigOK(mgmt.env, "--brief", "call", "org.unit.items"); !strings.Contains(out, "HSM certificate") || strings.Contains(out, "release out") {
		t.Errorf("escalation:\n%s", out)
	}

	// the manager's agent asks the team; nothing reaches it before approval
	mgmt.run([]step{
		read("org.unit.children", nil, "wallet"),
		call("org.unit.request", map[string]any{"to": "wallet", "title": "Q4 plan for wallet top-up", "body": "scope, dates, risks"}, "waits for approval"),
	}, "agent", "run", "pm")
	if out := team.rigOK(team.env, "--brief", "call", "org.unit.requests"); strings.Contains(out, "Q4 plan") {
		t.Fatal("the request reached the team before approval")
	}
	if ran := mgmt.approveAll(); len(ran) != 1 {
		t.Fatalf("approved %v", ran)
	}
	out := team.rigOK(team.env, "--brief", "call", "org.unit.requests")
	if !strings.Contains(out, "Q4 plan") || !strings.Contains(out, "product") {
		t.Fatalf("the team did not get the request:\n%s", out)
	}
	id := strings.Fields(strings.Split(out, "\n")[1])[0]

	// the team's product owner answers; the manager sees it
	team.run([]step{
		read("org.unit.requests", nil, "Q4 plan"),
		call("org.unit.answer", map[string]any{"id": id, "status": "answered", "answer": "3 stories, ready by sprint 42"}, "waits for approval"),
	}, "agent", "run", "po")
	team.approveAll()
	if out := mgmt.rigOK(mgmt.env, "--brief", "call", "org.unit.requests"); !strings.Contains(out, "answered") || !strings.Contains(out, "sprint 42") {
		t.Errorf("the manager does not see the answer:\n%s", out)
	}
}
