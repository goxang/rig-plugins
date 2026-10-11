package gitlab

import (
	"context"
	"testing"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/forge/forgetest"
)

func TestGitLab(t *testing.T) {
	p := "/api/v4/projects/g/p"
	srv := forgetest.New(t, map[string]string{
		p + "/merge_requests":         `[{"iid":7,"title":"Fix parser","author":{"username":"ann"},"state":"opened","source_branch":"fix","target_branch":"main","updated_at":"2026-01-02T00:00:00Z","web_url":"u"}]`,
		p + "/merge_requests/7/diffs": `[{"new_path":"parser/x.go","diff":"@@ -1 +1 @@\n-a\n+b\n","new_file":false}]`,
		p + "/pipelines":              `[{"id":11,"ref":"main","status":"success","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:01:30Z","web_url":"u"}]`,
		p + "/jobs/5":                 `{"id":5,"status":"running"}`,
		p + "/jobs/5/trace":           "line one\nline two\n",
		p + "/issues":                 `[{"iid":3,"title":"Crash","state":"opened","labels":["priority::high"],"updated_at":"2026-01-02T00:00:00Z","web_url":"u"}]`,
	})
	g := GitLab{&forge.Client{Base: srv.URL + p, Project: "g/p"}}
	ctx := context.Background()

	mrs, err := g.MRs(ctx, "", "", "")
	if err != nil || len(mrs) != 1 || mrs[0].ID != "7" || mrs[0].Author != "ann" {
		t.Fatalf("mrs %+v %v", mrs, err)
	}
	files, err := g.Files(ctx, "!7")
	if err != nil || len(files) != 1 || files[0].Path != "parser/x.go" || files[0].Changed != 2 {
		t.Fatalf("files %+v %v", files, err)
	}
	ps, err := g.Pipelines(ctx, "", 10)
	if err != nil || len(ps) != 1 || ps[0].Duration.Seconds() != 90 {
		t.Fatalf("pipelines %+v %v", ps, err)
	}
	lines, done, err := g.Log(ctx, "5")
	if err != nil || done || len(lines) != 2 {
		t.Fatalf("log %v %v %v", lines, done, err)
	}
	is, err := g.Issues(ctx, "", "")
	if err != nil || len(is) != 1 || is[0].Priority != "high" {
		t.Fatalf("issues %+v %v", is, err)
	}
	if err := g.Approve(ctx, "7"); err != nil || !srv.Called("POST "+p+"/merge_requests/7/approve") {
		t.Fatalf("approve %v", err)
	}
	if err := g.Transition(ctx, "#3", "closed"); err != nil || !srv.Called(`PUT `+p+`/issues/3 {"state_event":"close"}`) {
		t.Fatalf("transition %v", err)
	}
}
