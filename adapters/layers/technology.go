package layers

import (
	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var program = orgkit.Kind{Name: "program", Plural: "programs", Title: "Technology programs", Sort: "due",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "unit", Description: "the unit below that runs it"},
		{Name: "status", Options: []string{"planned", "on-track", "at-risk", "done", "dropped"}, Default: "planned"},
		{Name: "budget", Type: "number"},
		{Name: "spent", Type: "number"},
		{Name: "burn", Type: "number", Computed: true, Description: "% of budget spent"},
		{Name: "due", Type: "time"},
	},
	Derive: func(r map[string]any) {
		if b := orgkit.Num(r["budget"]); b > 0 {
			r["burn"] = float64(int(100 * orgkit.Num(r["spent"]) / b))
		}
	}}

var adr = orgkit.Kind{Name: "adr", Plural: "adrs", Title: "Architecture decisions", Fields: []orgkit.Field{
	{Name: "title", Required: true},
	{Name: "status", Options: []string{"proposed", "accepted", "superseded", "rejected"}, Default: "proposed"},
	{Name: "context"},
	{Name: "decision"},
	{Name: "consequences"},
}}

var radar = orgkit.Kind{Name: "radar", Plural: "radar", Title: "Technology radar", Sort: "name", Fields: []orgkit.Field{
	{Name: "name", Required: true},
	{Name: "ring", Options: []string{"adopt", "trial", "assess", "hold"}, Default: "assess"},
	{Name: "quadrant", Options: []string{"languages", "platforms", "tools", "techniques"}, Default: "tools"},
	{Name: "note"},
}}

// risk scores likelihood × impact, each 1 to 5.
var risk = orgkit.Kind{Name: "risk", Plural: "risks", Title: "Technology risks", Sort: "-score",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "likelihood", Type: "number", Default: 3.0},
		{Name: "impact", Type: "number", Default: 3.0},
		{Name: "score", Type: "number", Computed: true},
		{Name: "level", Computed: true},
		{Name: "owner"},
		{Name: "mitigation"},
		{Name: "status", Options: []string{"open", "mitigating", "accepted", "closed"}, Default: "open"},
	},
	Derive: func(r map[string]any) {
		s := orgkit.Num(r["likelihood"]) * orgkit.Num(r["impact"])
		r["score"], r["level"] = s, map[bool]string{true: "high", false: "medium"}[s >= 15]
		if s < 8 {
			r["level"] = "low"
		}
	}}

// Technology is org-technology: the IT deputy's scorecard of the units below, programs, architecture and risks.
func Technology(p *sdk.Plugin) {
	for _, k := range []orgkit.Kind{program, adr, radar, risk} {
		orgkit.Records(p, k)
	}
	health(p)
	p.Source("scorecard", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		h, err := orgkit.HealthOf(c)
		if err != nil {
			return nil, err
		}
		ok, down, high := 0.0, 0, 0
		for _, u := range h {
			ok += u.Value
			high += u.High
			if u.Status == "err" {
				down++
			}
		}
		if len(h) > 0 {
			ok /= float64(len(h))
		}
		incidents := 0
		for _, it := range orgkit.Each(c, "org.unit.items") {
			if it["kind"] == "incident" && open(it) {
				incidents++
			}
		}
		risks, _ := orgkit.List(c, risk)
		highRisks := 0
		for _, r := range risks {
			if r["level"] == "high" && open(r) {
				highRisks++
			}
		}
		progs, _ := orgkit.List(c, program)
		atRisk := 0
		for _, r := range progs {
			if r["status"] == "at-risk" || orgkit.Num(r["burn"]) > 100 {
				atRisk++
			}
		}
		return tiles(
			tile("KPIs on target", float64(int(ok)), "%", worse(100-ok, 10, 25), "averaged over the units below"),
			tile("Units in trouble", down, of(len(h)), worse(float64(down), 1, 2), "unreachable or with a KPI in err"),
			tile("Open incidents", incidents, "", worse(float64(incidents), 1, 3), "reported by the units below"),
			tile("High items", high, "", worse(float64(high), 2, 5), ""),
			tile("High risks", highRisks, "", worse(float64(highRisks), 1, 3), "likelihood × impact ≥ 15"),
			tile("Programs at risk", atRisk, of(len(progs)), worse(float64(atRisk), 1, 2), "at risk or over budget"),
		), nil
	}, sdk.Describe("technology in six figures: KPIs, units in trouble, incidents, high items and risks, programs"))
	p.Source("status", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		h, err := orgkit.HealthOf(c)
		if err != nil {
			return nil, err
		}
		words := map[string]string{"ok": "operational", "warn": "degraded", "err": "disrupted"}
		var out []map[string]any
		for _, u := range h {
			out = append(out, map[string]any{"title": u.Title, "value": words[u.Status], "status": u.Status, "note": ""})
		}
		return orgkit.Table(out, tileCols...), nil
	}, sdk.Describe("a service status per unit below (operational, degraded, disrupted): what stakeholders may see"))
	page(p, "Scorecard", func(c *sdk.Ctx) (*sdk.Node, error) {
		return live(c, []sdk.Node{
			bound("tiles", "Technology", "org-technology.scorecard", "60s", nil),
			bound("tiles", "Units", "org-technology.health", "60s", nil),
		}, func(r map[string]any) bool { return kind(r) == "source" })
	})
	page(p, "Live", func(c *sdk.Ctx) (*sdk.Node, error) { return live(c, nil, nil) })
}
