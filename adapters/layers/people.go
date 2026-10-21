package layers

import (
	"strings"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var position = orgkit.Kind{Name: "position", Plural: "positions", Title: "Open positions", Sort: "due",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "team", Required: true, Description: "the unit it is for"},
		{Name: "level", Options: []string{"junior", "mid", "senior", "lead", "manager"}, Default: "mid"},
		{Name: "stage", Options: []string{"requested", "approved", "sourcing", "interviewing", "offer", "filled", "cancelled"}, Default: "requested"},
		{Name: "manager", Description: "the hiring manager"},
		{Name: "due", Type: "time"},
	}}

var plan = orgkit.Kind{Name: "plan", Plural: "plans", Title: "Headcount plans", Sort: "team",
	Fields: []orgkit.Field{
		{Name: "team", Required: true},
		{Name: "quarter", Required: true, Description: "e.g. 2026-Q4"},
		{Name: "headcount", Type: "number", Required: true},
		{Name: "note"},
	}}

var training = orgkit.Kind{Name: "training", Plural: "trainings", Title: "Trainings", Sort: "date",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "team", Description: "empty for everyone"},
		{Name: "date", Type: "time"},
		{Name: "seats", Type: "number"},
		{Name: "status", Options: []string{"planned", "open", "full", "done", "cancelled"}, Default: "planned"},
	}}

// People is org-people: HR's figures per unit (headcount against plan, load, open roles), hiring and trainings.
// It reads org.unit.people only, which units report as unit-level figures, never per person.
func People(p *sdk.Plugin) {
	for _, k := range []orgkit.Kind{position, plan, training} {
		orgkit.Records(p, k)
	}
	p.Source("headcount", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) { return headcount(c) },
		sdk.Describe("per unit below: headcount reported, planned, open positions and the gap"))
	p.Source("figures", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return orgkit.Table(orgkit.Each(c, "org.unit.people"), "unit", "title", "value", "measure", "target", "status", "note"), nil
	}, sdk.Describe("every people figure the units below report (headcount, open roles, load, attrition)"))
	p.Source("overview", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		f, err := headcount(c)
		if err != nil {
			return nil, err
		}
		actual, planned, gap, positions := 0.0, 0.0, 0.0, 0.0
		for _, r := range orgkit.Rows(f) {
			actual += orgkit.Num(r["actual"])
			planned += orgkit.Num(r["planned"])
			gap += orgkit.Num(r["gap"])
			positions += orgkit.Num(r["open"])
		}
		loaded := 0
		for _, r := range orgkit.Each(c, "org.unit.people") {
			if strings.Contains(strings.ToLower(orgkit.Str(r["name"])+orgkit.Str(r["title"])), "load") && r["status"] != "ok" {
				loaded++
			}
		}
		trs, _ := orgkit.List(c, training)
		upcoming := 0
		for _, r := range trs {
			if s := r["status"]; s == "planned" || s == "open" {
				upcoming++
			}
		}
		return tiles(
			tile("Headcount", actual, "people", "ok", "as the units report it"),
			tile("Planned", planned, "people", "ok", "this quarter's plans"),
			tile("Gap", gap, "people", worse(gap, 2, 5), "planned minus actual"),
			tile("Positions open", positions, "", "ok", ""),
			tile("Units overloaded", loaded, "", worse(float64(loaded), 1, 2), "a load figure off target"),
			tile("Trainings ahead", upcoming, "", "ok", ""),
		), nil
	}, sdk.Describe("the workforce in six figures"))
	page(p, "Workforce", func(c *sdk.Ctx) (*sdk.Node, error) {
		return grid(
			bound("tiles", "Workforce", "org-people.overview", "5m", nil),
			bound("table", "Headcount by unit", "org-people.headcount", "5m", nil),
		), nil
	})
}

func headcount(c unit) (*sdk.Frame, error) {
	kids, err := orgkit.Children(c)
	if err != nil {
		return nil, err
	}
	actual := map[string]float64{}
	for _, r := range orgkit.Each(c, "org.unit.people") {
		if strings.Contains(strings.ToLower(orgkit.Str(r["name"])), "headcount") {
			actual[orgkit.Str(r["unit"])] = orgkit.Num(r["value"])
		}
	}
	plans, _ := orgkit.List(c, plan)
	planned := map[string]float64{}
	latest := map[string]string{}
	for _, r := range plans {
		team, q := orgkit.Str(r["team"]), orgkit.Str(r["quarter"])
		if q >= latest[team] {
			latest[team], planned[team] = q, orgkit.Num(r["headcount"])
		}
	}
	pos, _ := orgkit.List(c, position)
	openPos := map[string]float64{}
	for _, r := range pos {
		if open(r) {
			openPos[orgkit.Str(r["team"])]++
		}
	}
	var out []map[string]any
	for _, k := range kids {
		name := orgkit.Str(k["name"])
		gap := planned[name] - actual[name]
		out = append(out, map[string]any{"unit": name, "actual": actual[name], "planned": planned[name], "quarter": latest[name],
			"open": openPos[name], "gap": gap, "status": worse(gap-openPos[name], 1, 3)})
	}
	return orgkit.Table(out, "unit", "actual", "planned", "quarter", "open", "gap", "status"), nil
}
