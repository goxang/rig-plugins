package github

import (
	"context"
	"testing"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/forge/forgetest"
)

func TestGitHub(t *testing.T) {
	r := "/repos/o/r"
	srv := forgetest.New(t, map[string]string{
		r + "/pulls":        `[{"number":4,"title":"Add x","user":{"login":"bo"},"state":"open","head":{"ref":"x"},"base":{"ref":"main"},"updated_at":"2026-01-02T00:00:00Z"}]`,
		r + "/actions/runs": `{"workflow_runs":[{"id":9,"head_branch":"main","status":"completed","conclusion":"failure","run_started_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:02:00Z"}]}`,
		r + "/issues":       `[{"number":1,"title":"Bug","state":"open","labels":[{"name":"priority: high"}],"updated_at":"2026-01-02T00:00:00Z"},{"number":4,"title":"Add x","state":"open","pull_request":{}}]`,
		"/search/issues":    `{"items":[{"number":2,"title":"Found","state":"open"}]}`,
	})
	g := GitHub{&forge.Client{Base: srv.URL + r, Project: "o/r"}}
	ctx := context.Background()

	mrs, err := g.MRs(ctx, "", "", "")
	if err != nil || len(mrs) != 1 || mrs[0].Author != "bo" || mrs[0].Target != "main" {
		t.Fatalf("mrs %+v %v", mrs, err)
	}
	ps, err := g.Pipelines(ctx, "", 10)
	if err != nil || len(ps) != 1 || ps[0].Status != "failed" || ps[0].Duration.Minutes() != 2 {
		t.Fatalf("pipelines %+v %v", ps, err)
	}
	is, err := g.Issues(ctx, "", "")
	if err != nil || len(is) != 1 || is[0].Key != "#1" || is[0].Priority != "high" {
		t.Fatalf("pull requests are not issues: %+v %v", is, err)
	}
	if is, err = g.Issues(ctx, "crash", ""); err != nil || len(is) != 1 || is[0].Key != "#2" {
		t.Fatalf("search %+v %v", is, err)
	}
	if err := g.Transition(ctx, "#1", "close"); err != nil || !srv.Called(`PATCH `+r+`/issues/1 {"state":"closed"}`) {
		t.Fatalf("transition %v", err)
	}
	if g.Transition(ctx, "#1", "in review") == nil {
		t.Fatal("GitHub has no in-review state")
	}
}
