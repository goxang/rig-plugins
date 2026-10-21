// Command rig-std is every official Go plugin in one binary: `rig-std <name>` serves one plugin over
// rig/1 on stdio, and `rig-std install` writes each plugin's manifest where rig finds it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
	"github.com/goxang/rig/spec"
)

var version = "dev"

func main() {
	const usage = "usage: rig-std <plugin> | rig-std install [--relative] [dir] | rig-std list"
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "install":
		args := os.Args[2:]
		// --relative writes manifests for release archives, which carry the binary as bin/rig-std
		relative := len(args) > 0 && args[0] == "--relative"
		if relative {
			args = args[1:]
		}
		dir := filepath.Join(host.ConfigDir(), "plugins")
		if len(args) > 0 {
			dir = args[0]
		}
		if strings.HasPrefix(dir, "-") {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		err = install(dir, relative)
	case "tool":
		if len(os.Args) < 3 || tools[os.Args[2]] == nil {
			fmt.Fprintln(os.Stderr, "usage: rig-std tool usage|plugintree")
			os.Exit(2)
		}
		err = tools[os.Args[2]](os.Args[3:])
	case "list":
		for _, n := range names() {
			fmt.Printf("%-12s %s\n", n, describe(n))
		}
	default:
		err = serve(os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rig-std:", err)
		os.Exit(1)
	}
}

func names() []string {
	var out []string
	for n := range plugins {
		out = append(out, n)
	}
	for n := range importers {
		if plugins[n] == nil {
			out = append(out, n)
		}
	}
	for n := range actions {
		if plugins[n] == nil && importers[n] == nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func describe(name string) string {
	var ds []string
	for _, a := range plugin.List() {
		if a.Type == name {
			ds = append(ds, string(a.Kind)+": "+a.Description)
		}
	}
	for n := range importers[name] {
		ds = append(ds, "imports "+n)
	}
	if r, ok := testRunners[name]; ok {
		ds = append(ds, "tests: "+r.desc)
	} else if cs := contractsOf[name]; len(cs) > 0 {
		ds = append(ds, "serves "+strings.Join(cs, ", "))
	} else if _, ok := actions[name]; ok {
		ds = append(ds, "actions: "+name+".*")
	}
	slices.Sort(ds)
	return strings.Join(ds, "; ")
}

func implements(name string) []string {
	var out []string
	for _, s := range plugins[name] {
		out = append(out, contracts.ForKind(string(s.kind)).ID)
	}
	if _, ok := testRunners[name]; ok {
		out = append(out, "tests/1")
	}
	return append(out, contractsOf[name]...)
}

// install writes one manifest per plugin, each running this binary.
func install(dir string, relative bool) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if relative {
		self = "bin/rig-std"
	}
	for _, n := range names() {
		m := host.Manifest{
			Name: n, Version: version, Description: describe(n), Publisher: "goxang", Implements: implements(n),
			Server: &host.Server{Command: host.Command{self, n}},
			// ponytail: official plugins run tools (kubectl, docker, go) with the user's own access; narrow per plugin later
			Permissions: host.Permissions{Env: []string{"*"}, FS: []string{"/"}},
		}
		for in, im := range importers[n] {
			if m.Importers == nil {
				m.Importers = map[string]host.ImporterDecl{}
			}
			m.Importers[in] = host.ImporterDecl{Files: im.files}
		}
		var frag map[string]any
		if o, err := overlays.ReadFile("manifests/" + n + ".yaml"); err == nil {
			if err := yaml.Unmarshal(o, &m); err != nil {
				return fmt.Errorf("manifests/%s.yaml: %w", n, err)
			}
			if frag, err = fragment(o); err != nil {
				return fmt.Errorf("manifests/%s.yaml: %w", n, err)
			}
		}
		b, err := yaml.Marshal(m)
		if err != nil {
			return err
		}
		if len(frag) > 0 {
			more, err := yaml.Marshal(frag)
			if err != nil {
				return err
			}
			b = append(b, more...)
		}
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, n, "rig-plugin.yaml"), b, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("installed %d plugins in %s\n", len(names()), dir)
	return nil
}

// app is the project rig opened this plugin for, opened in-process on first use.
var app = sync.OnceValues(func() (*engine.App, error) {
	return engine.Open(os.Getenv("RIG_FILE"), os.Getenv("RIG_ENV"))
})

func lookup() contracts.Lookup {
	return contracts.Lookup{
		Component: func(_ context.Context, name string) (any, error) {
			a, err := app()
			if err != nil {
				return nil, err
			}
			return a.Component(name)
		},
		Service: func(name string) (*spec.Service, error) {
			a, err := app()
			if err != nil {
				return nil, err
			}
			if s := a.Spec.Services[name]; s != nil {
				return s, nil
			}
			return nil, fmt.Errorf("no service %q", name)
		},
	}
}

func serve(name string) error {
	kinds, ok := plugins[name]
	if !ok && importers[name] == nil && actions[name] == nil {
		return fmt.Errorf("no plugin %q (have %s)", name, strings.Join(names(), ", "))
	}
	p := sdk.New(name).Version(version).Implements(implements(name)...)
	for _, k := range kinds {
		s, err := contracts.Serve(k.kind, k.probe, lookup())
		if err != nil {
			return err
		}
		register(p, s)
	}
	registerImporters(p, name)
	if f := actions[name]; f != nil {
		f(p)
	}
	p.Serve()
	return nil
}

func register(p *sdk.Plugin, s *contracts.Served) {
	for n, fn := range s.Sources {
		p.Source(n, func(c *sdk.Ctx, a sdk.Args) (*sdk.Frame, error) {
			f, err := fn(c, a)
			return wire[sdk.Frame](f), unmet(err)
		}, sdk.Arguments(s.Contract.ArgsSchema(n)))
	}
	for n, fn := range s.Streams {
		p.Stream(n, func(c *sdk.Ctx, a sdk.Args, out *sdk.Out) error {
			return fn(c, a, func(f *protocol.Frame) error { return out.Frame(wire[sdk.Frame](f)) })
		}, sdk.Arguments(s.Contract.ArgsSchema(n)))
	}
	for n, fn := range s.Actions {
		p.Action(n, s.Effects[n], func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
			w := &lines{line: c.Line}
			out, err := fn(c, a, w)
			w.Flush()
			return &sdk.Result{Outputs: out}, err
		}, sdk.Arguments(s.Contract.ArgsSchema(n)))
	}
}

// unmet marks a source asked outside any project as unavailable, which rig plugin test and the AI
// tell apart from a bug (the SDK does the same for a tool that is missing or fails).
func unmet(err error) error {
	if !errors.Is(err, spec.ErrNotFound) {
		return err
	}
	return &sdk.Error{Code: sdk.CodeUnavailable, Message: err.Error()}
}

func wire[T any](v any) *T {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out := new(T)
	_ = json.Unmarshal(b, out)
	return out
}

// lines turns what an adapter writes into the run's output lines.
type lines struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	line func(string)
}

func (l *lines) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(b)
	for {
		i := bytes.IndexByte(l.buf.Bytes(), '\n')
		if i < 0 {
			return len(b), nil
		}
		l.line(strings.TrimRight(string(l.buf.Next(i+1)), "\r\n"))
	}
}

func (l *lines) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 0 {
		l.line(l.buf.String())
		l.buf.Reset()
	}
}

type served struct {
	kind  core.Kind
	probe any
}

// fragment is what an overlay gives the project as defaults (agent templates, policy roles, a ui
// profile): rig reads these keys from the manifest itself, so they pass through as written.
func fragment(overlay []byte) (map[string]any, error) {
	var all map[string]any
	if err := yaml.Unmarshal(overlay, &all); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, k := range []string{"tasks", "queries", "dashboards", "alerts", "reports", "tests", "agents", "templates", "defaults", "policy", "ai", "ui"} {
		if v, ok := all[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
