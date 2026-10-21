package layers

import (
	"math"
	"slices"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var objective = orgkit.Kind{Name: "objective", Plural: "objectives", Title: "Objectives", Sort: "quarter",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "quarter", Required: true, Description: "e.g. 2026-Q4"},
		{Name: "owner"},
		{Name: "status", Options: []string{"active", "done", "dropped"}, Default: "active"},
	}}

// keyresult reads its current value from a KPI a unit below reports, so progress needs no one to type it in.
var keyresult = orgkit.Kind{Name: "keyresult", Plural: "keyresults", Title: "Key results",
	Fields: []orgkit.Field{
		{Name: "objective", Required: true, Description: "the objective's id or title"},
		{Name: "title", Required: true},
		{Name: "unit", Description: "the unit below whose KPI measures it"},
		{Name: "kpi", Description: "the KPI's name in that unit's org.unit.kpis"},
		{Name: "baseline", Type: "number"},
		{Name: "target", Type: "number", Required: true},
		{Name: "current", Type: "number", Description: "only when no KPI measures it"},
	}}

var decision = orgkit.Kind{Name: "decision", Plural: "decisions", Title: "Decisions",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "context"},
		{Name: "options", Description: "the options weighed, one per line"},
		{Name: "decision"},
		{Name: "status", Options: []string{"open", "decided", "deferred"}, Default: "open"},
		{Name: "due", Type: "time"},
		{Name: "from", Description: "the unit that escalated it"},
	}}

var briefing = orgkit.Kind{Name: "briefing", Plural: "briefings", Title: "Briefings", Sort: "-period",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "period", Required: true, Description: "e.g. 2026-W43"},
		{Name: "body", Description: "markdown"},
	}}

// Executive is org-executive: the company in a page, objectives and key results measured live,
// the decisions waiting, and the critical risks the units below escalated.
func Executive(p *sdk.Plugin) {
	for _, k := range []orgkit.Kind{objective, keyresult, decision, briefing} {
		orgkit.Records(p, k)
	}
	health(p)
	p.Source("okrs", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		rows, err := okrs(c)
		if err != nil {
			return nil, err
		}
		return orgkit.Table(rows, "objective", "title", "unit", "current", "target", "progress", "status", "note"), nil
	}, sdk.Describe("key results with their live value from the units' KPIs and progress from baseline to target"))
	p.Source("risks", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return orgkit.Table(escalated(c, func(r map[string]any) bool { return r["kind"] != "decision" && r["kind"] != "highlight" }),
			"unit", "kind", "title", "severity", "status", "owner", "updated"), nil
	}, sdk.Describe("high and critical items the units below report, open ones only"))
	p.Source("agenda", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		var out []map[string]any
		own, err := orgkit.List(c, decision)
		if err != nil {
			return nil, err
		}
		for _, r := range own {
			if open(r) {
				out = append(out, map[string]any{"id": r["id"], "title": r["title"], "from": r["from"], "due": r["due"], "status": r["status"], "source": "decisions"})
			}
		}
		for _, r := range escalated(c, func(r map[string]any) bool { return r["kind"] == "decision" }) {
			out = append(out, map[string]any{"title": r["title"], "from": r["unit"], "status": "open", "source": "escalated"})
		}
		return orgkit.Table(out, "id", "title", "from", "due", "status", "source"), nil
	}, sdk.Describe("decisions waiting: this office's open ones and those the units below escalated"))
	p.Source("scorecard", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		h, err := orgkit.HealthOf(c)
		if err != nil {
			return nil, err
		}
		healthy := 0
		for _, u := range h {
			if u.Status == "ok" {
				healthy++
			}
		}
		krs, _ := okrs(c)
		progress := 0.0
		for _, k := range krs {
			progress += orgkit.Num(k["progress"])
		}
		if len(krs) > 0 {
			progress = math.Round(progress / float64(len(krs)))
		}
		critical := 0
		for _, r := range escalated(c, nil) {
			if r["severity"] == "critical" {
				critical++
			}
		}
		decisions, _ := orgkit.List(c, decision)
		waiting := 0
		for _, r := range decisions {
			if open(r) {
				waiting++
			}
		}
		return tiles(
			tile("Units healthy", healthy, of(len(h)), worse(float64(len(h)-healthy), 1, 2), "every KPI on target"),
			tile("Key results", progress, "% on the way", worse(100-progress, 40, 70), "average progress from baseline to target"),
			tile("Critical items", critical, "open", worse(float64(critical), 1, 2), "escalated by the units below"),
			tile("Decisions waiting", waiting, "", worse(float64(waiting), 3, 6), ""),
		), nil
	}, sdk.Describe("the company in four figures"))
	page(p, "Company", func(c *sdk.Ctx) (*sdk.Node, error) {
		return live(c, []sdk.Node{
			bound("tiles", "Company", "org-executive.scorecard", "60s", nil),
			bound("tiles", "Units", "org-executive.health", "60s", nil),
		}, func(r map[string]any) bool { return kind(r) == "source" })
	})
}

func okrs(c unit) ([]map[string]any, error) {
	krs, err := orgkit.List(c, keyresult)
	if err != nil {
		return nil, err
	}
	objs, _ := orgkit.List(c, objective)
	title := map[string]string{}
	for _, o := range objs {
		title[orgkit.Str(o["id"])] = orgkit.Str(o["title"])
	}
	kpi := map[string]float64{}
	for _, r := range orgkit.Each(c, "org.unit.kpis") {
		kpi[orgkit.Str(r["unit"])+"/"+orgkit.Str(r["name"])] = orgkit.Num(r["value"])
	}
	out := []map[string]any{}
	for _, k := range krs {
		cur, note := orgkit.Num(k["current"]), "entered by hand"
		if v, ok := kpi[orgkit.Str(k["unit"])+"/"+orgkit.Str(k["kpi"])]; ok {
			cur, note = v, "live from "+orgkit.Str(k["unit"])+" "+orgkit.Str(k["kpi"])
		} else if orgkit.Str(k["kpi"]) != "" {
			note = orgkit.Str(k["unit"]) + " does not report " + orgkit.Str(k["kpi"])
		}
		base, target := orgkit.Num(k["baseline"]), orgkit.Num(k["target"])
		progress := 0.0
		if target != base {
			progress = math.Round(math.Max(0, math.Min(100, 100*(cur-base)/(target-base))))
		}
		obj := orgkit.Str(k["objective"])
		if t := title[obj]; t != "" {
			obj = t
		}
		out = append(out, map[string]any{"objective": obj, "title": k["title"], "unit": k["unit"], "current": cur, "target": target,
			"progress": progress, "status": worse(100-progress, 40, 70), "note": note})
	}
	return out, nil
}

var serious = []string{"high", "critical"}

// escalated are the open high and critical items of the units below, keep narrowing them.
func escalated(c unit, keep func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	for _, r := range orgkit.Each(c, "org.unit.items") {
		if open(r) && slices.Contains(serious, orgkit.Str(r["severity"])) && (keep == nil || keep(r)) {
			out = append(out, r)
		}
	}
	return out
}
