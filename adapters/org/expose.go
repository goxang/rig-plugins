package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
)

// Exposure is something this unit offers the units above: a source to read, an action to run, or
// a child's exposure passed up (from: child/name).
type Exposure struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Source      string         `json:"source"`
	Action      string         `json:"action"`
	From        string         `json:"from"`
	Args        map[string]any `json:"args"`
	Inputs      []Input        `json:"inputs"`
	Widget      string         `json:"widget"`
	Refresh     string         `json:"refresh"`
	// To are the roles that see it (default parent).
	To []string `json:"to"`
}

// Input is an argument a caller may set; a plain string in rig.yaml is its name.
type Input struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`
}

func (in *Input) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &in.Name)
	}
	type plain Input
	return json.Unmarshal(b, (*plain)(in))
}

// Exposed is a row of unit.exposed.
type Exposed struct {
	Unit        string   `json:"unit"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	Widget      string   `json:"widget"`
	Refresh     string   `json:"refresh"`
	Description string   `json:"description"`
	Inputs      []Input  `json:"inputs"`
	Origin      string   `json:"origin"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Error       string   `json:"error"`
}

type unitClient interface {
	Query(ctx context.Context, source string, args protocol.Args) (*protocol.Frame, error)
	Run(ctx context.Context, action string, args protocol.Args, o api.RunOpts) (*jobs.Job, *protocol.RunResult, error)
}

var exposeArgs = sdk.Schema{
	"type": "object",
	"properties": map[string]any{
		"name":  map[string]any{"type": "string", "description": "the exposure's name (unit.exposed lists them)"},
		"child": map[string]any{"type": "string", "description": "a child unit whose exposure this is"},
	},
	"required":             []any{"name"},
	"additionalProperties": true,
}

func (e Exposure) kind() string {
	if e.Action != "" {
		return "action"
	}
	return "source"
}

func (n *node) local() bool { return len(n.roles) == 0 || slices.Contains(n.roles, "owner") }

func (n *node) sees(e Exposure) bool {
	if n.local() {
		return true
	}
	to := e.To
	if len(to) == 0 {
		to = []string{"parent"}
	}
	return slices.ContainsFunc(n.roles, func(r string) bool { return slices.Contains(to, r) })
}

func (n *node) checkExposures() error {
	seen := map[string]bool{}
	for _, e := range n.cfg.Expose {
		set := 0
		for _, v := range []string{e.Source, e.Action, e.From} {
			if v != "" {
				set++
			}
		}
		switch {
		case e.Name == "":
			return fmt.Errorf("plugins.org.expose: an item has no name")
		case seen[e.Name]:
			return fmt.Errorf("plugins.org.expose: %s twice", e.Name)
		case set != 1:
			return fmt.Errorf("plugins.org.expose.%s: set one of source, action or from", e.Name)
		case e.From != "" && !strings.Contains(e.From, "/"):
			return fmt.Errorf("plugins.org.expose.%s: from is child/name", e.Name)
		}
		seen[e.Name] = true
	}
	return nil
}

// exposed lists what this unit offers the caller, or with child (a name or *) what the children offer this unit.
func (n *node) exposed(child string) ([]Exposed, error) {
	if err := n.checkExposures(); err != nil {
		return nil, err
	}
	if child != "" {
		return n.childExposed(child)
	}
	out := []Exposed{}
	for _, e := range n.cfg.Expose {
		if !n.sees(e) {
			continue
		}
		row := Exposed{Unit: n.cfg.Unit, Origin: n.cfg.Unit, Name: e.Name, Title: e.Title, Kind: e.kind(), Widget: e.Widget,
			Refresh: e.Refresh, Description: e.Description, Inputs: e.Inputs}
		if e.From != "" {
			if up, err := n.upstream(e); err != nil {
				row.Error = err.Error()
			} else {
				row.Kind, row.Origin = up.Kind, first(up.Origin, up.Unit)
				row.Title, row.Widget, row.Refresh, row.Description = first(row.Title, up.Title), first(row.Widget, up.Widget), first(row.Refresh, up.Refresh), first(row.Description, up.Description)
				if row.Inputs == nil {
					row.Inputs = up.Inputs
				}
			}
		}
		row.Title = first(row.Title, e.Name)
		if n.local() {
			row.From, row.To = e.From, e.To
		}
		out = append(out, row)
	}
	return out, nil
}

func (n *node) childExposed(child string) ([]Exposed, error) {
	if !n.mayDrill() {
		return nil, &sdk.Error{Code: sdk.CodeDenied, Message: n.caller + " may not read below " + n.cfg.Unit + " (org.drill)"}
	}
	names := []string{child}
	if child == "*" {
		names = n.childNames()
	}
	per := make([][]Exposed, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			fr, err := n.child(name, "unit.exposed", nil)
			if err != nil {
				errs[i] = err
				return
			}
			for _, r := range frames.Rows(fr) {
				e := convert[Exposed](r)
				e.Unit, e.Origin = name, first(e.Origin, name)
				per[i] = append(per[i], *e)
			}
		})
	}
	wg.Wait()
	out := []Exposed{}
	for i, rows := range per {
		if errs[i] != nil {
			if child != "*" {
				return nil, errs[i]
			}
			out = append(out, Exposed{Unit: names[i], Name: "-", Title: names[i] + " is unreachable", Error: errs[i].Error()})
		}
		out = append(out, rows...)
	}
	return out, nil
}

// upstream is the child's row for a re-export.
func (n *node) upstream(e Exposure) (*Exposed, error) {
	name, up, _ := strings.Cut(e.From, "/")
	fr, err := n.child(name, "unit.exposed", nil)
	if err != nil {
		return nil, err
	}
	for _, r := range frames.Rows(fr) {
		if r["name"] == up {
			return convert[Exposed](r), nil
		}
	}
	return nil, fmt.Errorf("%s exposes no %s to %s", name, up, n.cfg.Unit)
}

// target is the exposure called name that the caller may use, and the arguments to call it with:
// the caller's inputs, only those declared, under the fixed args.
func (n *node) target(a protocol.Args, kind string) (Exposure, protocol.Args, error) {
	if err := n.checkExposures(); err != nil {
		return Exposure{}, nil, err
	}
	name := fmt.Sprint(a["name"])
	i := slices.IndexFunc(n.cfg.Expose, func(e Exposure) bool { return e.Name == name })
	if i < 0 || !n.sees(n.cfg.Expose[i]) {
		return Exposure{}, nil, &sdk.Error{Code: sdk.CodeNotFound, Message: n.cfg.Unit + " exposes nothing called " + name + " to " + first(n.caller, "you")}
	}
	e := n.cfg.Expose[i]
	if e.From == "" && e.kind() != kind {
		return e, nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: name + " is not a " + kind + " (unit.show reads a source, unit.invoke runs an action)"}
	}
	in := maps.Clone(a)
	delete(in, "name")
	delete(in, "child")
	if e.From == "" || e.Inputs != nil {
		names := make([]string, len(e.Inputs))
		for j, x := range e.Inputs {
			names[j] = x.Name
			v, set := in[x.Name]
			if x.Required && (!set || v == "") {
				return e, nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: name + " needs " + x.Name}
			}
			if set && len(x.Options) > 0 && !slices.Contains(x.Options, fmt.Sprint(v)) {
				return e, nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: x.Name + " is one of " + strings.Join(x.Options, ", ")}
			}
		}
		for k := range in {
			if !slices.Contains(names, k) {
				return e, nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: k + " is not an input of " + name + " (inputs: " + strings.Join(names, ", ") + ")"}
			}
		}
	}
	maps.Copy(in, e.Args)
	return e, in, nil
}

func (n *node) remote(name string) (Child, unitClient, error) {
	ch, ok := n.cfg.Children[name]
	if !ok {
		return ch, nil, &sdk.Error{Code: sdk.CodeNotFound, Message: fmt.Sprintf("%s is not a child of %s (have %s)", name, n.cfg.Unit, strings.Join(n.childNames(), ", "))}
	}
	return ch, n.dial(ch), nil
}

func (n *node) show(a protocol.Args) (*protocol.Frame, error) {
	if child, _ := a["child"].(string); child != "" {
		if !n.mayDrill() {
			return nil, &sdk.Error{Code: sdk.CodeDenied, Message: n.caller + " may not read below " + n.cfg.Unit + " (org.drill)"}
		}
		ch, c, err := n.remote(child)
		if err != nil {
			return nil, err
		}
		args := maps.Clone(a)
		delete(args, "child")
		return c.Query(n.ctx, plugin(ch)+".unit.show", args)
	}
	e, args, err := n.target(a, "source")
	if err != nil {
		return nil, err
	}
	if e.From != "" {
		child, up, _ := strings.Cut(e.From, "/")
		ch, c, err := n.remote(child)
		if err != nil {
			return nil, err
		}
		args["name"] = up
		return c.Query(n.ctx, plugin(ch)+".unit.show", args)
	}
	return n.query(e.Source, args)
}

func (n *node) invoke(a protocol.Args) (*sdk.Result, error) {
	child, _ := a["child"].(string)
	args := maps.Clone(a)
	delete(args, "child")
	if child == "" {
		e, in, err := n.target(a, "action")
		if err != nil {
			return nil, err
		}
		if e.From == "" {
			return n.run(e.Action, in)
		}
		child, args = strings.Split(e.From, "/")[0], in
		args["name"] = strings.SplitN(e.From, "/", 2)[1]
	} else if !n.mayDrill() {
		return nil, &sdk.Error{Code: sdk.CodeDenied, Message: n.caller + " may not act below " + n.cfg.Unit + " (org.drill)"}
	}
	ch, c, err := n.remote(child)
	if err != nil {
		return nil, err
	}
	_, res, err := c.Run(n.ctx, plugin(ch)+".unit.invoke", args, api.RunOpts{})
	forget(child)
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.Code == protocol.CodeNeedsInput {
		return &sdk.Result{Message: child + " holds it for a person there to approve: " + pe.Message, Outputs: map[string]any{"pending": true}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", child, err)
	}
	return convert[sdk.Result](res), nil
}
