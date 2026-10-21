package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goxang/rig-plugins/adapters/runtime/kind"
	"github.com/goxang/rig-plugins/adapters/runtime/kubernetes"
	"github.com/goxang/rig/contracts"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/host"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/protocol"
)

var (
	_ core.Manifester = (*kubernetes.Runtime)(nil)
	_ core.Manifester = (*kind.Runtime)(nil)
)

func TestEveryPluginServesItsContracts(t *testing.T) {
	registered := map[string]bool{}
	for _, a := range plugin.List() {
		registered[string(a.Kind)+"/"+a.Type] = true
	}
	for _, n := range names() {
		for _, k := range plugins[n] {
			if !registered[string(k.kind)+"/"+n] {
				t.Errorf("%s: no %s adapter registered as %q", n, k.kind, n)
			}
			s, err := contracts.Serve(k.kind, k.probe, contracts.Lookup{})
			if err != nil {
				t.Fatal(err)
			}
			pr := &protocol.Provides{Sources: map[string]protocol.SourceSpec{}, Actions: map[string]protocol.ActionSpec{}}
			for x := range s.Sources {
				pr.Sources[x] = protocol.SourceSpec{}
			}
			for x := range s.Streams {
				pr.Sources[x] = protocol.SourceSpec{Stream: true}
			}
			for x := range s.Actions {
				pr.Actions[x] = protocol.ActionSpec{Effect: s.Effects[x]}
			}
			ct := contracts.ForKind(string(k.kind))
			if ps := ct.Check(pr); len(ps) > 0 {
				t.Errorf("%s as %s: %v", n, ct.ID, ps)
			}
		}
	}
}

func TestImportersReturnFragments(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	rig := filepath.Join(dir, "rig.yaml")
	os.WriteFile(compose, []byte("services:\n  web: { build: ., ports: [\"8000:8000\"] }\n"), 0o644)
	os.WriteFile(rig, []byte("version: 1\nproject: x\nservices:\n  api: { ports: { http: 80 } }\n"), 0o644)
	frag, err := importers["compose"]["compose"].run(compose, dir)
	if err != nil {
		t.Fatal(err)
	}
	web, _ := frag["services"].(map[string]any)["web"].(map[string]any)
	if web["ports"].(map[string]any)["http"] != 8000 {
		t.Fatalf("compose fragment %v", frag)
	}
	if frag, err = importers["rig"]["rig"].run(rig, dir); err != nil || frag["version"] != nil || frag["project"] != "x" {
		t.Fatalf("rig fragment %v %v", frag, err)
	}
}

func TestInstalledManifestsKeepTheirFragments(t *testing.T) {
	frag, err := fragment([]byte("views: {}\ntemplates: {po: {instructions: x}}\nui: {lock: true}\n"))
	if err != nil || frag["templates"] == nil || frag["ui"] == nil || frag["views"] != nil {
		t.Fatalf("fragment: %v %v", frag, err)
	}
	dir := t.TempDir()
	if err := install(dir, true); err != nil {
		t.Fatal(err)
	}
	for _, n := range names() {
		file := filepath.Join(dir, n, "rig-plugin.yaml")
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		m, err := host.ParseManifest(raw, file)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if len(m.Warnings) > 0 {
			t.Errorf("%s: %v", n, m.Warnings)
		}
	}
}
