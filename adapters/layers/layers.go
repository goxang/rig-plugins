// Package layers holds the plugins of the layers above a team: a department (org-management), the
// product office (org-product), the IT deputy (org-technology), HR (org-people), the executive
// (org-executive) and the stakeholders' portal (org-portal). Each keeps its own records and reads
// the units below only through the unit's org plugin, so it sees what they report and expose, no more.
package layers

import (
	"strconv"

	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

// page registers a tab the plugin draws on each render, for content that depends on what the units below expose.
func page(p *sdk.Plugin, name string, build func(c *sdk.Ctx) (*sdk.Node, error)) {
	p.View(name, sdk.ViewSpec{Title: name, Slot: "tab"}, func(c *sdk.Ctx, _ sdk.RenderParams) (*sdk.Node, error) {
		n, err := build(c)
		if err != nil {
			return md("⚠ " + err.Error()), nil
		}
		return n, nil
	})
}

func grid(children ...sdk.Node) *sdk.Node { return &sdk.Node{Widget: "grid", Children: children} }

func row(children ...sdk.Node) sdk.Node { return sdk.Node{Widget: "row", Children: children} }

func bound(widget, title, source string, every string, props sdk.Args) sdk.Node {
	if props == nil {
		props = sdk.Args{}
	}
	props["title"] = title
	return sdk.Node{Widget: widget, Props: props, Bind: &sdk.Binding{Source: source, Every: every}}
}

func md(text string) *sdk.Node { return &sdk.Node{Widget: "markdown", Props: sdk.Args{"text": text}} }

// live is a page of what the units below expose to this one, keep picking which.
func live(c *sdk.Ctx, head []sdk.Node, keep func(map[string]any) bool) (*sdk.Node, error) {
	cards, err := orgkit.Cards(c, "*", keep)
	if err != nil {
		return nil, err
	}
	if cards.Widget == "grid" {
		return grid(append(head, cards.Children...)...), nil
	}
	return grid(append(head, *cards)...), nil
}

func kind(r map[string]any) string { return orgkit.Str(r["kind"]) }

// open items are the ones not closed, done or dropped.
func open(r map[string]any) bool {
	switch orgkit.Str(r["status"]) {
	case "closed", "done", "dropped", "decided", "filled", "cancelled", "shipped", "declined", "promoted", "superseded", "rejected":
		return false
	}
	return true
}

func tile(title string, value any, measure, status, note string) map[string]any {
	return map[string]any{"title": title, "value": value, "measure": measure, "status": status, "note": note}
}

var tileCols = []string{"title", "value", "measure", "status", "note"}

func tiles(rows ...map[string]any) *sdk.Frame { return orgkit.Table(rows, tileCols...) }

// worse is err when n reaches bad, warn when it reaches warn, else ok.
func worse(n, warn, bad float64) string {
	switch {
	case bad > 0 && n >= bad:
		return "err"
	case warn > 0 && n >= warn:
		return "warn"
	}
	return "ok"
}

func of(n int) string { return "of " + strconv.Itoa(n) }

// health is <plugin>.health: a row per unit below with its share of KPIs on target.
func health(p *sdk.Plugin) {
	p.Source("health", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		h, err := orgkit.HealthOf(c)
		if err != nil {
			return nil, err
		}
		return frames.Of(h), nil
	}, sdk.Describe("a row per unit below: share of KPIs on target, coloured by its worst sign"))
}

// unit is what a layer reads through: the unit's own org and the plugin's state (an *sdk.Ctx).
type unit interface {
	Query(source string, args sdk.Args) (*sdk.Frame, error)
	State(key string) (sdk.StateValue, error)
	SetState(key string, v any) error
}
