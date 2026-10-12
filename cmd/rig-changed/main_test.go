package main

import (
	"slices"
	"testing"
)

func TestAffected(t *testing.T) {
	for _, c := range []struct {
		files []string
		want  []string
		goBin bool
	}{
		{[]string{"README.md", "adapters/scm/gitlab/gitlab_test.go"}, []string{}, false},
		{[]string{"plugins/arcade/rig-plugin.yaml"}, []string{"arcade"}, false},
		{[]string{"cmd/rig-std/manifests/jira.yaml"}, []string{"jira"}, true},
		{[]string{"go.sum"}, []string{"@go"}, true},
		{[]string{"cmd/rig-std/games.go"}, []string{"@go"}, true},
	} {
		r, err := affected(c.files, "../..")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Plugins, c.want) || r.Go != c.goBin {
			t.Errorf("%v: got %v go=%v, want %v go=%v", c.files, r.Plugins, r.Go, c.want, c.goBin)
		}
	}
}

func TestAffectedByImports(t *testing.T) {
	r, err := affected([]string{"adapters/tracker/jira/jira.go"}, "../..")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Plugins, "jira") || slices.Contains(r.Plugins, "gitlab") || !r.Go {
		t.Errorf("a jira change should touch jira only: %v", r.Plugins)
	}
	r, err = affected([]string{"internal/forge/forge.go"}, "../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"gitlab", "github", "jira"} {
		if !slices.Contains(r.Plugins, n) {
			t.Errorf("a forge change should touch %s: %v", n, r.Plugins)
		}
	}
	if slices.Contains(r.Plugins, "redis") {
		t.Errorf("a forge change should not touch redis: %v", r.Plugins)
	}
}
