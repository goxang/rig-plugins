package orgkit

import (
	"fmt"

	"github.com/goxang/rig/sdk"
)

// Cards renders what the units below expose to this one (child "*") or one child's, as a card each:
// a source's widget bound to org.unit.show, an action's form bound to org.unit.invoke. keep, when
// set, picks the exposures to show.
func Cards(c querier, child string, keep func(row map[string]any) bool) (*sdk.Node, error) {
	f, err := c.Query("org.unit.exposed", sdk.Args{"child": child})
	if err != nil {
		return nil, err
	}
	var wide, small []sdk.Node
	for _, r := range Rows(f) {
		if keep != nil && !keep(r) {
			continue
		}
		if child == "" {
			delete(r, "unit") // this unit's own: no child to route through
		}
		n := Card(r)
		if w := n.Widget; w == "graph" || w == "timeline" || w == "table" {
			wide = append(wide, n)
		} else {
			small = append(small, n)
		}
	}
	if len(wide)+len(small) == 0 {
		return &sdk.Node{Widget: "markdown", Props: sdk.Args{"text": "Nothing is exposed to this unit yet. A unit below shares a source or an action with `expose:` in its `plugins.org` config."}}, nil
	}
	grid := &sdk.Node{Widget: "grid"}
	grid.Children = append(grid.Children, wide...)
	for i := 0; i < len(small); i += 2 {
		grid.Children = append(grid.Children, sdk.Node{Widget: "row", Children: small[i:min(i+2, len(small))]})
	}
	return grid, nil
}

// Card is one exposure row (org.unit.exposed) as a widget.
func Card(r map[string]any) sdk.Node {
	get := func(k string) string { s, _ := r[k].(string); return s }
	title := first(get("title"), get("name"))
	if o := first(get("origin"), get("unit")); o != "" {
		title += " · " + o
	}
	if e := get("error"); e != "" {
		return sdk.Node{Widget: "markdown", Props: sdk.Args{"title": title, "text": "⚠ " + e}}
	}
	args := sdk.Args{"name": get("name")}
	if u := get("unit"); u != "" {
		args["child"] = u
	}
	if get("kind") == "action" {
		props, req := map[string]any{}, []any{}
		inputs, _ := r["inputs"].([]any)
		for _, x := range inputs {
			in, _ := x.(map[string]any)
			name := fmt.Sprint(in["name"])
			p := map[string]any{"type": first(Str(in["type"]), "string"), "description": first(Str(in["title"]), Str(in["description"]))}
			if opts, ok := in["options"].([]any); ok && len(opts) > 0 {
				p["enum"] = opts
			}
			if req2, _ := in["required"].(bool); req2 {
				req = append(req, name)
			}
			props[name] = p
		}
		return sdk.Node{Widget: "form", Props: sdk.Args{"title": title, "description": get("description"), "action": "org.unit.invoke", "args": args,
			"schema": map[string]any{"type": "object", "properties": props, "required": req}, "submit": first(get("title"), "Run")}}
	}
	return sdk.Node{Widget: first(get("widget"), "table"), Props: sdk.Args{"title": title, "description": get("description")},
		Bind: &sdk.Binding{Source: "org.unit.show", Args: args, Every: get("refresh")}}
}
