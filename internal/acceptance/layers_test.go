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
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

type unit struct {
	*world
	url string
}

func newUnit(t *testing.T, rig, std, bin, project string, plugins map[string]any) *unit {
	t.Helper()
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
	spec, _ := yaml.Marshal(map[string]any{
		"version": 1, "project": project, "default": "dev",
		"environments": map[string]any{"dev": map[string]any{"runtime": map[string]any{"type": "local"}}},
		"plugins":      plugins,
	})
	if err := os.WriteFile(filepath.Join(w.dir, "rig.yaml"), spec, 0o600); err != nil {
		t.Fatal(err)
	}
	return &unit{world: w}
}

// serve adds users with their roles and starts rig serve; login gives a user's client.
func (u *unit) serve(ctx context.Context, users map[string]string) {
	t := u.t
	for user, role := range users {
		add := exec.Command(u.rig, "admin", "users", "add", user, "--role", role)
		add.Dir, add.Env, add.Stdin = u.dir, append(os.Environ(), u.env...), strings.NewReader("a long enough password\n")
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
	ctx, cancel := context.WithCancel(ctx)
	s := exec.CommandContext(ctx, u.rig, "serve", "--plain-http", "--addr", addr, "--no-agents")
	s.Dir, s.Env = u.dir, append(os.Environ(), u.env...)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = s.Wait() })
	u.url = "http://" + addr
}

func (u *unit) login(ctx context.Context, user string) *api.Client {
	c := &api.Client{URL: u.url}
	var out struct{ Token string }
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		err := c.Do(ctx, "/v1/login", nil, map[string]string{"user": user, "password": "a long enough password"}, &out)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			u.t.Fatalf("login %s: %v", user, err)
		}
	}
	c.Token = out.Token
	return c
}

func code(err error) int {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return 0
}

// Three layers: the switch exposes its live payments and an action, its department passes the
// payments up, and the IT deputy sees them two levels up. Every role sees only what is exposed to it.
func TestLayersExpose(t *testing.T) {
	if testing.Short() {
		t.Skip("builds rig")
	}
	bin := t.TempDir()
	rig := build(t, bin, "github.com/goxang/rig/cmd/rig")
	std := build(t, bin, "github.com/goxang/rig-plugins/cmd/rig-std")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	team := newUnit(t, rig, std, bin, "switch", map[string]any{
		"org": map[string]any{"unit": "switch", "layer": 1, "parent": "shaparak-software", "expose": []any{
			map[string]any{"name": "payments-flow", "title": "Live payments", "source": "flow.graph", "widget": "graph", "refresh": "5s", "to": []any{"parent", "product"}},
			map[string]any{"name": "transactions-today", "source": "flow.summary"},
			map[string]any{"name": "report-incident", "action": "org.unit.item", "args": map[string]any{"kind": "incident"},
				"inputs": []any{"title", map[string]any{"name": "severity", "options": []any{"low", "medium", "high", "critical"}}}},
			map[string]any{"name": "unit-card", "source": "org.unit.info", "to": []any{"owner"}},
		}},
		"flow": map[string]any{"simulate": true, "edges": []any{
			map[string]any{"from": "pos", "to": "sessionsvc", "sim": 120}, map[string]any{"from": "sessionsvc", "to": "dispatchersvc"},
		}, "types": []any{map[string]any{"name": "purchase"}}},
	})
	team.serve(ctx, map[string]string{"shaparak": "parent", "office": "product"})
	parent, product := team.login(ctx, "shaparak"), team.login(ctx, "office")

	listed := func(c *api.Client, args protocol.Args) string {
		f, err := c.Query(ctx, "org.unit.exposed", args)
		if err != nil {
			return "error: " + err.Error()
		}
		var names []string
		for _, r := range frames.Rows(f) {
			names = append(names, r["name"].(string))
		}
		return strings.Join(names, ",")
	}
	if got := listed(parent, nil); got != "payments-flow,transactions-today,report-incident" {
		t.Errorf("the parent sees %s", got)
	}
	if got := listed(product, nil); got != "payments-flow" {
		t.Errorf("the product office sees %s", got)
	}
	if _, err := product.Query(ctx, "org.unit.show", protocol.Args{"name": "transactions-today"}); code(err) != protocol.CodeNotFound {
		t.Errorf("the product office read an exposure not meant for it: %v", err)
	}
	if _, err := parent.Query(ctx, "flow.graph", nil); code(err) != protocol.CodeDenied {
		t.Errorf("the parent read a source outside org.unit.*: %v", err)
	}
	if f, err := parent.Query(ctx, "org.unit.show", protocol.Args{"name": "payments-flow"}); err != nil || len(frames.Rows(f)) != 5 {
		t.Errorf("payments-flow = %v, %v", f, err)
	}

	// a parent's invoke waits at the team until a person there approves it
	_, _, err := parent.Run(ctx, "org.unit.invoke", protocol.Args{"name": "report-incident", "title": "Shaparak link down", "severity": "critical"}, api.RunOpts{})
	if code(err) != protocol.CodeNeedsInput {
		t.Fatalf("invoke did not wait: %v", err)
	}
	if out := team.rigOK(team.env, "--brief", "call", "org.unit.items"); strings.Contains(out, "Shaparak link") {
		t.Fatal("the incident was recorded before approval")
	}
	if ran := team.approveAll(); len(ran) != 1 {
		t.Fatalf("approved %v", ran)
	}
	if out := team.rigOK(team.env, "--brief", "call", "org.unit.items"); !strings.Contains(out, "Shaparak link down") || !strings.Contains(out, "incident") {
		t.Errorf("the approved incident is missing:\n%s", out)
	}

	dept := newUnit(t, rig, std, bin, "shaparak-software", map[string]any{"org": map[string]any{
		"unit": "shaparak-software", "layer": 2, "cache": "1s",
		"children": map[string]any{"switch": map[string]any{"url": team.url, "token": parent.Token}},
		"expose":   []any{map[string]any{"name": "payments-flow", "from": "switch/payments-flow"}},
	}})
	dept.serve(ctx, map[string]string{"deputy": "parent"})
	deputyToken := dept.login(ctx, "deputy")
	if _, err := deputyToken.Query(ctx, "org.unit.show", protocol.Args{"name": "payments-flow", "child": "switch"}); code(err) != protocol.CodeDenied {
		t.Errorf("the deputy reached past the department: %v", err)
	}
	if got := listed(deputyToken, protocol.Args{"child": "switch"}); !strings.HasPrefix(got, "error") {
		t.Errorf("the deputy listed the team's exposures: %s", got)
	}

	top := newUnit(t, rig, std, bin, "it-deputy", map[string]any{"org": map[string]any{
		"unit": "it-deputy", "layer": 3,
		"children": map[string]any{"shaparak-software": map[string]any{"url": dept.url, "token": deputyToken.Token}},
	}})
	if out := top.rigOK(top.env, "--brief", "call", "org.unit.exposed", "child=*"); !strings.Contains(out, "payments-flow") || !strings.Contains(out, "Live payments") || strings.Contains(out, "report-incident") {
		t.Errorf("two levels up, exposed:\n%s", out)
	}
	if out := top.rigOK(top.env, "--brief", "call", "org.unit.show", "name=payments-flow", "child=shaparak-software"); !strings.Contains(out, "dispatchersvc") || !strings.Contains(out, "true") {
		t.Errorf("two levels up, payments-flow:\n%s", out)
	}
}
