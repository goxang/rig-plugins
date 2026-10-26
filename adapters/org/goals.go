package org

import (
	"fmt"
	"slices"
	"strings"
	"time"

	pframes "github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
)

// Goal is a target a unit above set for this one, tracked against one of its KPIs when KPI names one.
type Goal struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	By      string    `json:"by"`
	Title   string    `json:"title"`
	KPI     string    `json:"kpi"`
	Target  string    `json:"target"`
	Due     string    `json:"due"`
	Note    string    `json:"note"`
	Status  string    `json:"status"`
	Value   float64   `json:"value"`
	Measure string    `json:"measure"`
	OnTrack string    `json:"on_track"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Report is what a unit tells the one above: a weekly status, a release note, a retrospective.
type Report struct {
	ID      string    `json:"id"`
	Unit    string    `json:"unit"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Period  string    `json:"period"`
	Body    string    `json:"body"`
	By      string    `json:"by"`
	Created time.Time `json:"created"`
}

type goalArgs struct {
	ID     string `json:"id" desc:"an existing goal: change its status or note"`
	To     string `json:"to" desc:"the child unit to give it to; empty records it here" options:"org.unit.children name title"`
	Title  string `json:"title"`
	KPI    string `json:"kpi" desc:"the unit's KPI that measures it"`
	Target string `json:"target" desc:"a condition on that KPI: >= 99.5"`
	Due    string `json:"due" format:"date"`
	Note   string `json:"note"`
	Status string `json:"status" desc:"open, accepted, met, missed or dropped"`
	From   string `json:"from" desc:"the giving unit (a parent fills it)"`
	By     string `json:"by"`
}

type reportArgs struct {
	Kind   string `json:"kind" desc:"status, release, retro, incident" default:"status"`
	Title  string `json:"title" rig:"required"`
	Period string `json:"period" desc:"2026-W44, October, Sprint 12"`
	Body   string `json:"body" format:"markdown" rig:"required"`
	By     string `json:"by"`
}

var goalStatuses = []string{"open", "accepted", "met", "missed", "dropped"}

func (n *node) goal(a goalArgs) (*sdk.Result, error) {
	if a.Status != "" && !slices.Contains(goalStatuses, a.Status) {
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "status is one of " + strings.Join(goalStatuses, ", ")}
	}
	if a.To != "" && a.To != n.cfg.Unit {
		ch, ok := n.cfg.Children[a.To]
		if !ok {
			return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: fmt.Sprintf("%s is not a child of %s (have %s)", a.To, n.cfg.Unit, strings.Join(n.childNames(), ", "))}
		}
		args := protocol.Args{"id": a.ID, "title": a.Title, "kpi": a.KPI, "target": a.Target, "due": a.Due, "note": a.Note, "status": a.Status, "from": n.cfg.Unit, "by": first(a.By, n.caller)}
		_, res, err := n.dial(ch).Run(n.ctx, plugin(ch)+".unit.goal", args, api.RunOpts{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.To, err)
		}
		forget(a.To)
		return &sdk.Result{Message: "goal for " + a.To + ": " + res.Message, Outputs: res.Outputs}, nil
	}
	mu.Lock()
	defer mu.Unlock()
	var got []Goal
	if err := n.state.get("goals", &got); err != nil {
		return nil, err
	}
	if a.ID != "" {
		i := slices.IndexFunc(got, func(g Goal) bool { return g.ID == a.ID })
		if i < 0 {
			return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no goal " + a.ID + " here"}
		}
		g := &got[i]
		g.Status, g.Note, g.Target, g.Due = first(a.Status, g.Status), first(a.Note, g.Note), first(a.Target, g.Target), first(a.Due, g.Due)
		g.Updated = n.now
		if err := n.state.set("goals", got); err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "goal " + g.ID + " " + g.Status, Outputs: map[string]any{"id": g.ID}}, nil
	}
	if a.Title == "" {
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "a new goal needs a title"}
	}
	if a.Target != "" {
		if _, err := meets(a.Target, 0); err != nil {
			return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "target: " + err.Error()}
		}
	}
	g := Goal{ID: newID(), From: first(a.From, n.cfg.Parent, n.caller), To: n.cfg.Unit, By: first(a.By, n.caller), Title: a.Title,
		KPI: a.KPI, Target: a.Target, Due: a.Due, Note: a.Note, Status: first(a.Status, "open"), Created: n.now, Updated: n.now}
	if err := n.state.set("goals", append(got, g)); err != nil {
		return nil, err
	}
	n.emit("unit.goal", map[string]any{"id": g.ID, "from": g.From, "title": g.Title, "kpi": g.KPI, "target": g.Target, "due": g.Due})
	return &sdk.Result{Message: "goal " + g.ID + " recorded for " + n.cfg.Unit, Outputs: map[string]any{"id": g.ID}}, nil
}

// goals are the ones this unit got, each measured on its KPI now, and the ones it gave its children
// as they track them.
func (n *node) goals() []Goal {
	var got []Goal
	_ = n.state.get("goals", &got)
	var kpis map[string]KPI
	for i, g := range got {
		if g.KPI == "" {
			continue
		}
		if kpis == nil {
			kpis = map[string]KPI{}
			for _, m := range n.cfg.KPIs {
				if slices.ContainsFunc(got, func(g Goal) bool { return g.KPI == m.Name }) {
					kpis[m.Name] = n.eval(m, "unit.kpis")
				}
			}
		}
		k, ok := kpis[g.KPI]
		if !ok {
			got[i].OnTrack = "no such KPI"
			continue
		}
		got[i].Value, got[i].Measure = k.Value, k.Measure
		got[i].OnTrack = k.Status
		if g.Target != "" && k.Status != "unknown" {
			if ok, err := meets(g.Target, k.Value); err == nil {
				got[i].OnTrack = map[bool]string{true: "ok", false: "err"}[ok]
			}
		}
	}
	for _, name := range n.childNames() {
		fr, err := n.child(name, "unit.goals", nil)
		if err != nil {
			continue
		}
		for _, r := range frames.Rows(fr) {
			g, created, updated := fromRow[Goal](r)
			if g.From != n.cfg.Unit {
				continue
			}
			g.To, g.Created, g.Updated = name, created, updated
			got = append(got, *g)
		}
	}
	slices.SortStableFunc(got, func(a, b Goal) int { return b.Updated.Compare(a.Updated) })
	return got
}

func (n *node) report(a reportArgs) (*sdk.Result, error) {
	mu.Lock()
	defer mu.Unlock()
	var got []Report
	if err := n.state.get("reports", &got); err != nil {
		return nil, err
	}
	r := Report{ID: newID(), Unit: n.cfg.Unit, Kind: first(a.Kind, "status"), Title: a.Title, Period: a.Period, Body: a.Body, By: first(a.By, n.caller), Created: n.now}
	// ponytail: the newest 200 reports are kept; an archive would hold the rest
	got = append(got, r)
	if len(got) > 200 {
		got = got[len(got)-200:]
	}
	if err := n.state.set("reports", got); err != nil {
		return nil, err
	}
	n.emit("unit.report", map[string]any{"id": r.ID, "kind": r.Kind, "title": r.Title, "period": r.Period})
	return &sdk.Result{Message: r.Kind + " report " + r.ID + " published to " + first(n.cfg.Parent, "the units above"), Outputs: map[string]any{"id": r.ID}}, nil
}

// reports are this unit's own, or with child (a name or *) what the units below published.
func (n *node) reports(child string) ([]Report, error) {
	if child == "" {
		var got []Report
		err := n.state.get("reports", &got)
		slices.Reverse(got)
		return got, err
	}
	if !n.mayDrill() {
		return nil, &sdk.Error{Code: sdk.CodeDenied, Message: n.caller + " may not read below " + n.cfg.Unit + " (org.drill)"}
	}
	names := []string{child}
	if child == "*" {
		names = n.childNames()
	}
	var out []Report
	for _, name := range names {
		fr, err := n.child(name, "unit.reports", nil)
		if err != nil {
			if child != "*" {
				return nil, err
			}
			continue
		}
		for _, r := range frames.Rows(fr) {
			rp, created, _ := fromRow[Report](r)
			rp.Unit, rp.Created = first(rp.Unit, name), created
			out = append(out, *rp)
		}
	}
	slices.SortStableFunc(out, func(a, b Report) int { return b.Created.Compare(a.Created) })
	return out, nil
}

func registerGoals(p *sdk.Plugin) {
	p.Source("unit.goals", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		if a.Child != "" {
			return n.childFrame(a.Child, "unit.goals", nil)
		}
		return pframes.Of(n.goals()), nil
	}, sdk.Describe("goals set for this unit from above, measured on its KPIs, and the ones it set for its children"))
	p.Action("unit.goal", sdk.Write, func(c *sdk.Ctx, a goalArgs) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.goal(a)
	}, sdk.Describe("give a child unit (to) a goal, record one from above here, or change a goal's status (id)"))
	p.Source("unit.reports", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		rows, err := n.reports(a.Child)
		if err != nil {
			return nil, err
		}
		return pframes.Of(rows), nil
	}, sdk.Describe("reports this unit published upward; with child (a name or *) the units below's"))
	p.Action("unit.report", sdk.Write, func(c *sdk.Ctx, a reportArgs) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.report(a)
	}, sdk.Describe("publish a report (status, release, retro) for the units above"))
}
