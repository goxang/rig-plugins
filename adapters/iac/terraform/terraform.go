// Package terraform is iac/1 over Terraform or OpenTofu: stacks, plan, apply, outputs, drift and
// state. A rig environment selects the stack's workspace of the same name when there is one.
package terraform

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/sh"
	"github.com/goxang/rig/sdk"
)

type Stack struct {
	Stack     string `json:"stack"`
	Dir       string `json:"dir"`
	Workspace string `json:"workspace"`
	Resources int    `json:"resources"`
	// Drift and Updated are from the last iac.drift.
	Drift   bool      `json:"drift"`
	Updated time.Time `json:"updated"`
}

type driftState struct {
	Drift   bool      `json:"drift"`
	Checked time.Time `json:"checked"`
}

// Binary is plugins.terraform.binary, else terraform, else tofu.
func Binary(c *sdk.Ctx) string {
	if b := forge.Config(c, "binary"); b != "" {
		return b
	}
	if !sh.Have("terraform") && sh.Have("tofu") {
		return "tofu"
	}
	return "terraform"
}

// Discover finds directories holding .tf files, skipping modules and .terraform caches.
func Discover(root string) []string {
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if n := e.Name(); p != root && (strings.HasPrefix(n, ".") || n == "modules" || n == "node_modules" || n == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(e.Name(), ".tf") {
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			seen[rel] = true
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

func stackName(dir string) string {
	if dir == "." {
		return "root"
	}
	return filepath.ToSlash(dir)
}

// Stacks reads plugins.terraform.stacks (name: dir), else discovers them.
func Stacks(c *sdk.Ctx) map[string]string {
	out := map[string]string{}
	if m, ok := c.Config().Config["stacks"].(map[string]any); ok {
		for n, d := range m {
			out[n] = fmt.Sprint(d)
		}
		return out
	}
	for _, d := range Discover(git.Dir()) {
		out[stackName(d)] = d
	}
	return out
}

type tf struct {
	bin, dir, ws string
	env          []string
}

func open(c *sdk.Ctx, stack string) (*tf, error) {
	stacks := Stacks(c)
	if len(stacks) == 0 {
		return nil, sdk.Unavailable("no terraform stacks: no *.tf files in this repository", "set plugins.terraform.stacks")
	}
	if stack == "" && len(stacks) == 1 {
		for n := range stacks {
			stack = n
		}
	}
	dir, ok := stacks[stack]
	if !ok {
		names := slices.Sorted(func(y func(string) bool) {
			for n := range stacks {
				if !y(n) {
					return
				}
			}
		})
		return nil, fmt.Errorf("no stack %q; stacks: %s", stack, strings.Join(names, ", "))
	}
	t := &tf{bin: Binary(c), dir: filepath.Join(git.Dir(), dir), env: []string{"TF_IN_AUTOMATION=1"}}
	if t.ws = t.workspace(c, cmp.Or(c.Call.Env, os.Getenv("RIG_ENV"))); t.ws != "" {
		t.env = append(t.env, "TF_WORKSPACE="+t.ws)
	}
	return t, nil
}

// workspace is env when the stack has a workspace by that name.
func (t *tf) workspace(ctx context.Context, env string) string {
	if env == "" {
		return ""
	}
	out, err := t.output(ctx, "workspace", "list")
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "*")) == env {
			return env
		}
	}
	return ""
}

func (t *tf) cmd(args ...string) *sh.Cmd {
	return &sh.Cmd{Name: t.bin, Args: args, Dir: t.dir, Env: t.env}
}

func (t *tf) output(ctx context.Context, args ...string) ([]byte, error) {
	return t.cmd(args...).Output(ctx)
}

// stream runs the command, handing each output line to line, and returns the exit code.
func (t *tf) stream(ctx context.Context, line func(string), args ...string) (int, error) {
	x := t.cmd(args...).Exec(ctx)
	out, err := x.StdoutPipe()
	if err != nil {
		return 0, err
	}
	x.Stderr = x.Stdout
	if err := x.Start(); err != nil {
		return 0, fmt.Errorf("%s: %s", t.bin, sh.WithHint(err.Error()))
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(sc.Text())
	}
	err = x.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// Changes reads `show -json` of a saved plan into the resources it would touch.
func Changes(planJSON []byte) ([]sdk.Change, error) {
	var p struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(planJSON, &p); err != nil {
		return nil, err
	}
	var out []sdk.Change
	for _, rc := range p.ResourceChanges {
		a := rc.Change.Actions
		switch {
		case slices.Equal(a, []string{"create"}):
			out = append(out, sdk.Change{Op: sdk.OpCreate, Target: rc.Address})
		case slices.Equal(a, []string{"update"}):
			out = append(out, sdk.Change{Op: sdk.OpUpdate, Target: rc.Address})
		case slices.Equal(a, []string{"delete"}):
			out = append(out, sdk.Change{Op: sdk.OpDelete, Target: rc.Address})
		case slices.Contains(a, "delete") && slices.Contains(a, "create"):
			out = append(out, sdk.Change{Op: sdk.OpUpdate, Target: rc.Address, Detail: "replace"})
		}
	}
	return out, nil
}

type stackArg struct {
	Stack string `json:"stack" desc:"stack name (default the only one)"`
}

func (t *tf) plan(c *sdk.Ctx) (*sdk.Result, error) {
	planFile := filepath.Join(t.dir, ".terraform", "rig.tfplan")
	code, err := t.stream(c, c.Line, "plan", "-input=false", "-no-color", "-out="+planFile)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%s plan failed (exit %d)", t.bin, code)
	}
	raw, err := t.output(c, "show", "-json", planFile)
	if err != nil {
		return nil, err
	}
	changes, err := Changes(raw)
	if err != nil {
		return nil, err
	}
	return &sdk.Result{Message: fmt.Sprintf("%d changes", len(changes)), Changes: changes}, nil
}

func (t *tf) drift(ctx context.Context, line func(string)) (bool, error) {
	// -detailed-exitcode: 0 no changes, 1 error, 2 changes
	code, err := t.stream(ctx, line, "plan", "-refresh-only", "-input=false", "-no-color", "-detailed-exitcode", "-lock=false")
	if err != nil {
		return false, err
	}
	if code == 1 {
		return false, fmt.Errorf("%s plan -refresh-only failed", t.bin)
	}
	return code == 2, nil
}

func Register(p *sdk.Plugin) {
	p.Source("iac.stacks", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		var out []Stack
		for n, d := range Stacks(c) {
			s := Stack{Stack: n, Dir: d}
			if _, err := os.Stat(filepath.Join(git.Dir(), d, ".terraform")); err == nil {
				if t, err := open(c, n); err == nil {
					s.Workspace = t.ws
					if raw, err := t.output(c, "state", "list"); err == nil {
						s.Resources = len(strings.Fields(string(raw)))
					}
				}
			}
			if v, err := c.State("drift/" + n); err == nil && v.Found {
				if b, err := json.Marshal(v.Value); err == nil {
					var ds driftState
					_ = json.Unmarshal(b, &ds)
					s.Drift, s.Updated = ds.Drift, ds.Checked
				}
			}
			out = append(out, s)
		}
		slices.SortFunc(out, func(a, b Stack) int { return strings.Compare(a.Stack, b.Stack) })
		return frames.Of(out), nil
	}, sdk.Describe("Terraform/OpenTofu root modules"))

	p.Action("iac.init", sdk.EffectWrite, func(c *sdk.Ctx, in stackArg) (*sdk.Result, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		if code, err := t.stream(c, c.Line, "init", "-input=false", "-no-color"); err != nil || code != 0 {
			return nil, cmpErr(err, fmt.Errorf("init failed (exit %d)", code))
		}
		return &sdk.Result{Message: "initialized"}, nil
	})

	p.Action("iac.plan", sdk.EffectRead, func(c *sdk.Ctx, in stackArg) (*sdk.Result, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		return t.plan(c)
	}, sdk.Describe("plan a stack; lines stream, changes come back"))

	p.Action("iac.apply", sdk.EffectDanger, func(c *sdk.Ctx, in stackArg) (*sdk.Result, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		code, err := t.stream(c, c.Line, "apply", "-input=false", "-no-color", "-auto-approve")
		if err != nil || code != 0 {
			return nil, cmpErr(err, fmt.Errorf("apply failed (exit %d)", code))
		}
		res := &sdk.Result{Message: "applied", Outputs: sdk.Args{}}
		if outs, err := outputs(c, t); err == nil {
			for _, o := range outs {
				if !o.Sensitive {
					res.Outputs[o.Name] = o.Value
				}
			}
		}
		return res, nil
	})
	p.Plan("iac.apply", func(c *sdk.Ctx, in stackArg) (*sdk.Result, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		return t.plan(c)
	})

	p.Source("iac.outputs", func(c *sdk.Ctx, in stackArg) (*sdk.Frame, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		outs, err := outputs(c, t)
		return frames.Of(outs), err
	}, sdk.Cache("30s"))

	p.Source("iac.drift", func(c *sdk.Ctx, in stackArg) (*sdk.Frame, error) {
		type row struct {
			Stack   string    `json:"stack"`
			Drift   bool      `json:"drift"`
			Checked time.Time `json:"checked"`
		}
		names := []string{in.Stack}
		if in.Stack == "" {
			names = nil
			for n := range Stacks(c) {
				names = append(names, n)
			}
			slices.Sort(names)
		}
		var out []row
		for _, n := range names {
			t, err := open(c, n)
			if err != nil {
				return nil, err
			}
			d, err := t.drift(c, c.Line)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
			out = append(out, row{n, d, time.Now()})
			_ = c.SetState("drift/"+n, driftState{d, time.Now()})
		}
		return frames.Of(out), nil
	}, sdk.Describe("does real infrastructure differ from state"))

	p.Source("iac.state", func(c *sdk.Ctx, in stackArg) (*sdk.Frame, error) {
		t, err := open(c, in.Stack)
		if err != nil {
			return nil, err
		}
		raw, err := t.output(c, "state", "list")
		if err != nil {
			return nil, err
		}
		type res struct {
			Address string `json:"address"`
			Module  string `json:"module"`
			Type    string `json:"type"`
			Name    string `json:"name"`
		}
		var out []res
		for _, a := range strings.Fields(string(raw)) {
			r := res{Address: a}
			parts := strings.Split(a, ".")
			i := len(parts) - 2
			if i >= 1 && parts[i-1] == "data" {
				i--
			}
			if i >= 0 {
				r.Module = strings.TrimSuffix(strings.Join(parts[:i], "."), ".")
				r.Type, r.Name = strings.Join(parts[i:len(parts)-1], "."), parts[len(parts)-1]
			}
			out = append(out, r)
		}
		return frames.Of(out), nil
	}, sdk.Cache("30s"))

	p.Doctor(func(c *sdk.Ctx) []sdk.Check {
		if !sh.Have(Binary(c)) {
			return []sdk.Check{{Name: "terraform", Message: Binary(c) + " is not on PATH", Hint: sh.Hint(Binary(c))}}
		}
		var out []sdk.Check
		for n := range Stacks(c) {
			ck := sdk.Check{Name: "drift " + n, Ok: true, Message: "in sync"}
			t, err := open(c, n)
			if err == nil {
				ctx, cancel := context.WithTimeout(c, 2*time.Minute)
				var d bool
				d, err = t.drift(ctx, func(string) {})
				cancel()
				if d {
					ck.Ok, ck.Message, ck.Hint = false, "infrastructure drifted from state", "rig run iac.plan stack="+n
				}
			}
			if err != nil {
				ck.Ok, ck.Message = false, err.Error()
			}
			out = append(out, ck)
		}
		return out
	})
}

type output struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Type      string `json:"type"`
	Sensitive bool   `json:"sensitive"`
}

func outputs(ctx context.Context, t *tf) ([]output, error) {
	raw, err := t.output(ctx, "output", "-json")
	if err != nil {
		return nil, err
	}
	var m map[string]struct {
		Value     any             `json:"value"`
		Type      json.RawMessage `json:"type"`
		Sensitive bool            `json:"sensitive"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := make([]output, 0, len(m))
	for n, o := range m {
		v, ok := o.Value.(string)
		if !ok {
			b, _ := json.Marshal(o.Value)
			v = string(b)
		}
		if o.Sensitive {
			v = "(sensitive)"
		}
		out = append(out, output{Name: n, Value: v, Type: strings.Trim(string(o.Type), `"`), Sensitive: o.Sensitive})
	}
	slices.SortFunc(out, func(a, b output) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func cmpErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
