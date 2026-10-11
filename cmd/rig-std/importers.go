package main

import (
	"os"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/scaffold"
	"github.com/goxang/rig/sdk"
	"github.com/goxang/rig/spec"
)

type importer struct {
	files []string
	run   func(path, dir string) (map[string]any, error)
}

// importers are the plugins' importers, by plugin then importer name.
var importers = map[string]map[string]importer{
	"compose":    {"compose": {[]string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}, services(scaffold.ImportCompose)}},
	"kubernetes": {"kubernetes": {nil, services(scaffold.ImportManifests)}, "manifests": {nil, services(scaffold.ImportManifests)}},
	"godev":      {"godev": {[]string{".godev.yaml"}, services(importGodev)}},
	"rig":        {"rig": {[]string{"rig.yaml"}, importRig}},
}

func registerImporters(p *sdk.Plugin, name string) {
	for n, im := range importers[name] {
		p.Importer(n, im.files, func(_ *sdk.Ctx, in sdk.ImportParams) (*sdk.ImportResult, error) {
			frag := map[string]any{}
			for _, f := range in.Files {
				part, err := im.run(f, in.Dir)
				if err != nil {
					return nil, err
				}
				for k, v := range part {
					frag[k] = v
				}
			}
			return &sdk.ImportResult{Fragment: frag, Watch: in.Files}, nil
		})
	}
}

func services(f func(path, dir string) (map[string]*spec.Service, error)) func(string, string) (map[string]any, error) {
	return func(path, dir string) (map[string]any, error) {
		svcs, err := f(path, dir)
		if err != nil {
			return nil, err
		}
		b, err := yaml.Marshal(map[string]any{"services": svcs})
		if err != nil {
			return nil, err
		}
		var out map[string]any
		return out, yaml.Unmarshal(b, &out)
	}
}

// importRig takes a rig.yaml as it is: rig.yaml grew out of it, so its keys mean the same.
func importRig(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	delete(out, "version")
	return out, nil
}

func importGodev(path, _ string) (map[string]*spec.Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Services map[string]struct {
			Path    string            `yaml:"path"`
			Command []string          `yaml:"command"`
			Args    []string          `yaml:"args"`
			Env     map[string]string `yaml:"env"`
			Group   []string          `yaml:"group"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	out := map[string]*spec.Service{}
	for n, g := range f.Services {
		s := &spec.Service{Name: n, Groups: g.Group, Env: g.Env, Role: spec.RoleApp}
		switch {
		case len(g.Command) > 0:
			s.Run = &spec.Run{Command: g.Command}
		case g.Path != "":
			s.Build = &spec.Build{Go: g.Path}
			s.Run = &spec.Run{Args: g.Args}
		}
		sort.Strings(s.Groups)
		out[n] = s
	}
	return out, nil
}
