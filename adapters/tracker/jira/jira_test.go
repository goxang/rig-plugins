package jira

import (
	"context"
	"testing"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/forge/forgetest"
)

func TestJQL(t *testing.T) {
	for _, c := range []struct{ query, assignee, want string }{
		{"", "", "project = OPS AND resolution = Unresolved ORDER BY updated DESC"},
		{"login fails", "me", `text ~ "login fails" AND project = OPS AND resolution = Unresolved AND assignee = currentUser() ORDER BY updated DESC`},
		{"status = Done order by created", "ann", `status = Done AND assignee = "ann" order by created`},
	} {
		if got := JQL(c.query, c.assignee, "OPS"); got != c.want {
			t.Errorf("JQL(%q, %q)\n got %s\nwant %s", c.query, c.assignee, got, c.want)
		}
	}
}

func TestJira(t *testing.T) {
	srv := forgetest.New(t, map[string]string{
		"/rest/api/2/search":                      `{"issues":[{"key":"OPS-1","fields":{"summary":"Disk full","status":{"name":"To Do"},"priority":{"name":"High"},"updated":"2026-01-02T10:00:00.000+0000"}}]}`,
		"GET /rest/api/2/issue/OPS-1/transitions": `{"transitions":[{"id":"21","name":"Start","to":{"name":"In Progress"}},{"id":"31","name":"Finish","to":{"name":"Done"}}]}`,
	})
	j := &Jira{Client: &forge.Client{Base: srv.URL}}
	ctx := context.Background()

	is, err := j.Search(ctx, "project = OPS")
	if err != nil || len(is) != 1 || is[0].Status != "To Do" || is[0].Updated.Hour() != 10 {
		t.Fatalf("search %+v %v", is, err)
	}
	if err := j.transition(ctx, "OPS-1", "in progress"); err != nil || !srv.Called(`POST /rest/api/2/issue/OPS-1/transitions {"transition":{"id":"21"}}`) {
		t.Fatalf("transition %v", err)
	}
	if err := j.transition(ctx, "OPS-1", "Blocked"); err == nil {
		t.Fatal("no transition leads to Blocked")
	}
}
