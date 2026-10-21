package layers

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var stages = []string{"idea", "discovery", "next", "now", "shipped", "dropped"}

// initiative is scored RICE: reach × impact × confidence ÷ effort.
var initiative = orgkit.Kind{Name: "initiative", Plural: "initiatives", Title: "Product initiatives", Sort: "-score",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "problem", Description: "whose problem, and how we know"},
		{Name: "stage", Options: stages, Default: "idea"},
		{Name: "team", Description: "the unit that builds it"},
		{Name: "reach", Type: "number", Description: "people or transactions a quarter"},
		{Name: "impact", Type: "number", Description: "0.25 minimal, 0.5 low, 1 medium, 2 high, 3 massive"},
		{Name: "confidence", Type: "number", Description: "percent"},
		{Name: "effort", Type: "number", Description: "person-months"},
		{Name: "score", Type: "number", Computed: true},
		{Name: "start", Type: "time"},
		{Name: "due", Type: "time"},
		{Name: "public", Type: "bool", Description: "on the stakeholders' roadmap"},
	},
	Derive: func(r map[string]any) {
		if e := orgkit.Num(r["effort"]); e > 0 {
			conf := orgkit.Num(r["confidence"])
			if conf == 0 {
				conf = 50
			}
			r["score"] = math.Round(orgkit.Num(r["reach"])*orgkit.Num(r["impact"])*conf/100/e*10) / 10
		}
	}}

var intake = orgkit.Kind{Name: "intake", Plural: "intakes", Title: "Requests to the product office",
	Fields: []orgkit.Field{
		{Name: "title", Required: true},
		{Name: "body"},
		{Name: "from", Description: "the unit or person asking"},
		{Name: "status", Options: []string{"new", "triaged", "promoted", "declined"}, Default: "new"},
		{Name: "initiative", Description: "the initiative it became"},
		{Name: "answer"},
	}}

var spec = orgkit.Kind{Name: "spec", Plural: "specs", Title: "Specs", Fields: []orgkit.Field{
	{Name: "title", Required: true},
	{Name: "initiative"},
	{Name: "status", Options: []string{"draft", "review", "approved"}, Default: "draft"},
	{Name: "body", Description: "markdown: problem, users, scope, acceptance criteria, metrics"},
}}

var release = orgkit.Kind{Name: "release", Plural: "releases", Title: "Releases", Sort: "-date", Fields: []orgkit.Field{
	{Name: "title", Required: true},
	{Name: "team"},
	{Name: "date", Type: "time"},
	{Name: "notes", Description: "markdown, what changed for whom"},
	{Name: "public", Type: "bool", Description: "in the stakeholders' release notes"},
}}

// Product is org-product: the product office's portfolio, intake, specs, releases and the teams' product metrics.
func Product(p *sdk.Plugin) {
	for _, k := range []orgkit.Kind{initiative, intake, spec, release} {
		orgkit.Records(p, k)
	}
	p.Action("submit", sdk.Write, func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
		return orgkit.Save(c, c.Call.User, intake, sdk.Args{"title": a["title"], "body": a["body"], "from": a["from"], "status": "new"})
	}, sdk.Describe("ask the product office for something: it lands in intake as new"), sdk.Arguments(sdk.Schema{"type": "object",
		"properties": map[string]any{"title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}},
		"required":   []any{"title"}}))
	p.Action("promote", sdk.Write, func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
		id := orgkit.Str(a["id"])
		rows, err := orgkit.List(c, intake)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r["id"] != id {
				continue
			}
			res, err := orgkit.Save(c, c.Call.User, initiative, sdk.Args{"title": r["title"], "problem": r["body"], "stage": "idea"})
			if err != nil {
				return nil, err
			}
			ini := res.Outputs["id"]
			if _, err := orgkit.Save(c, c.Call.User, intake, sdk.Args{"id": id, "status": "promoted", "initiative": ini}); err != nil {
				return nil, err
			}
			return &sdk.Result{Message: fmt.Sprintf("intake %s is initiative %s", id, ini), Outputs: map[string]any{"initiative": ini}}, nil
		}
		return nil, sdk.Errorf(sdk.CodeNotFound, "no intake %s", id)
	}, sdk.Describe("turn an intake into an initiative (stage idea)"), sdk.Arguments(sdk.Schema{"type": "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []any{"id"}}))
	p.Source("metrics", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return orgkit.Table(orgkit.Each(c, "org.unit.kpis"), "unit", "title", "value", "measure", "target", "status", "note"), nil
	}, sdk.Describe("the KPIs every team below reports"))
	p.Source("pipeline", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		ins, err := orgkit.List(c, initiative)
		if err != nil {
			return nil, err
		}
		reqs, _ := orgkit.List(c, intake)
		count := map[string]int{}
		for _, r := range ins {
			count[orgkit.Str(r["stage"])]++
		}
		fresh := 0
		for _, r := range reqs {
			if r["status"] == "new" {
				fresh++
			}
		}
		rels, _ := orgkit.List(c, release)
		shipped := 0
		for _, r := range rels {
			if t, err := time.Parse("2006-01-02", strings.SplitN(orgkit.Str(r["date"]), "T", 2)[0]); err == nil && time.Since(t) < 30*24*time.Hour {
				shipped++
			}
		}
		return tiles(
			tile("New requests", fresh, "to triage", worse(float64(fresh), 5, 15), "intake waiting for the product office"),
			tile("Discovery", count["discovery"], "initiatives", "ok", ""),
			tile("Next", count["next"], "initiatives", "ok", "committed, not started"),
			tile("Now", count["now"], "initiatives", worse(float64(count["now"]), 6, 10), "in build: many at once slows each"),
			tile("Released", shipped, "last 30 days", "ok", ""),
		), nil
	}, sdk.Describe("the product pipeline in figures: intake, discovery, next, now and releases"))
	p.Source("roadmap", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) { return roadmap(c, false) },
		sdk.Describe("initiatives on a timeline by due date"))
	p.Source("public-roadmap", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) { return roadmap(c, true) },
		sdk.Describe("the public initiatives only, without scores: for stakeholders"))
	p.Source("release-notes", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		rows, err := orgkit.List(c, release)
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		for _, r := range rows {
			if r["public"] == true {
				out = append(out, map[string]any{"time": orgkit.Str(r["date"]), "title": r["title"], "kind": r["team"], "note": r["notes"], "status": "ok"})
			}
		}
		return orgkit.Table(out, "time", "title", "kind", "note", "status"), nil
	}, sdk.Describe("public releases, latest first: for stakeholders"))
	page(p, "Overview", func(c *sdk.Ctx) (*sdk.Node, error) {
		return live(c, []sdk.Node{
			bound("tiles", "Pipeline", "org-product.pipeline", "60s", nil),
			bound("tiles", "Product metrics of the teams", "org-product.metrics", "5m", nil),
		}, func(r map[string]any) bool { return kind(r) == "source" })
	})
}

var stageStatus = map[string]string{"idea": "none", "discovery": "warn", "next": "warn", "now": "ok", "shipped": "ok", "dropped": "err"}

func roadmap(c unit, public bool) (*sdk.Frame, error) {
	rows, err := orgkit.List(c, initiative)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, r := range rows {
		if (public && r["public"] != true) || r["stage"] == "dropped" || (public && r["stage"] == "idea") {
			continue
		}
		t := orgkit.Str(r["due"])
		if t == "" {
			t = orgkit.Str(r["start"])
		}
		o := map[string]any{"time": t, "title": r["title"], "kind": r["stage"], "status": stageStatus[orgkit.Str(r["stage"])], "unit": r["team"]}
		if !public {
			o["note"] = fmt.Sprintf("score %v", r["score"])
		}
		out = append(out, o)
	}
	return orgkit.Table(out, "time", "title", "kind", "status", "unit", "note"), nil
}
