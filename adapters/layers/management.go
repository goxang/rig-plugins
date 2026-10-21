package layers

import (
	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var commitment = orgkit.Kind{Name: "commitment", Plural: "commitments", Title: "Commitments to the units above", Sort: "due",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "team", Description: "the unit below that delivers it"},
		{Name: "due", Type: "time", Description: "YYYY-MM-DD"},
		{Name: "status", Options: []string{"planned", "on-track", "at-risk", "done", "dropped"}, Default: "planned"},
		{Name: "owner"},
		{Name: "note"},
	}}

// Management is org-management: a department's view of its teams, its commitments and their capacity.
func Management(p *sdk.Plugin) {
	orgkit.Records(p, commitment)
	health(p)
	p.Source("capacity", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return orgkit.Table(orgkit.Each(c, "org.unit.people"), "unit", "title", "value", "measure", "target", "status", "note"), nil
	}, sdk.Describe("people figures (headcount, open roles, load) of every team below"))
	p.Source("overview", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		h, err := orgkit.HealthOf(c)
		if err != nil {
			return nil, err
		}
		healthy, high, reqs := 0, 0, 0
		for _, t := range h {
			if t.Status == "ok" {
				healthy++
			}
			high, reqs = high+t.High, reqs+t.OpenRequests
		}
		rows, _ := orgkit.List(c, commitment)
		atRisk, due := 0, 0
		for _, r := range rows {
			if open(r) {
				due++
			}
			if orgkit.Str(r["status"]) == "at-risk" {
				atRisk++
			}
		}
		return tiles(
			tile("Teams healthy", healthy, of(len(h)), worse(float64(len(h)-healthy), 1, 2), "every KPI of the team on target"),
			tile("High items", high, "open", worse(float64(high), 1, 3), "risks, blockers and incidents of high or critical severity"),
			tile("Commitments open", due, "", "ok", "made to the units above"),
			tile("At risk", atRisk, "commitments", worse(float64(atRisk), 1, 2), ""),
			tile("Requests open", reqs, "at the teams", worse(float64(reqs), 5, 10), ""),
		), nil
	}, sdk.Describe("the department in five figures"))
	page(p, "Department", func(c *sdk.Ctx) (*sdk.Node, error) {
		return live(c, []sdk.Node{
			bound("tiles", "Department", "org-management.overview", "60s", nil),
			bound("tiles", "Teams", "org-management.health", "60s", sdk.Args{"label": "title"}),
		}, nil)
	})
	page(p, "Live", func(c *sdk.Ctx) (*sdk.Node, error) { return live(c, nil, nil) })
}
