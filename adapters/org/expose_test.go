package org

import (
	"context"
	"strings"
	"testing"

	pframes "github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/jobs"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
)

// asChild serves a node's unit/1 the way its rig serve would to a token with roles.
type asChild struct {
	n     *node
	roles []string
}

func (c asChild) as() *node {
	cp := *c.n
	cp.roles, cp.caller = c.roles, "token"
	return &cp
}

func (c asChild) Query(_ context.Context, source string, args protocol.Args) (*protocol.Frame, error) {
	switch source {
	case "org.unit.exposed":
		child, _ := args["child"].(string)
		rows, err := c.as().exposed(child)
		if err != nil {
			return nil, err
		}
		return convert[protocol.Frame](pframes.Of(rows)), nil
	case "org.unit.show":
		return c.as().show(args)
	}
	return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: source}
}

func (c asChild) Run(_ context.Context, action string, args protocol.Args, _ api.RunOpts) (*jobs.Job, *protocol.RunResult, error) {
	res, err := c.as().invoke(args)
	return nil, convert[protocol.RunResult](res), err
}

func TestExposures(t *testing.T) {
	var ran []map[string]any
	team, _ := testNode(&Config{Unit: "switch", Cache: "0s", Expose: []Exposure{
		{Name: "payments-flow", Source: "flow.graph", Widget: "graph", To: []string{"parent", "product"}},
		{Name: "transactions-today", Source: "flow.summary", Inputs: []Input{{Name: "type", Options: []string{"purchase", "bill"}}}},
		{Name: "restart-parser", Action: "svc.restart", Args: map[string]any{"service": "parsersvc"}, Inputs: []Input{{Name: "reason", Required: true}}},
		{Name: "secrets", Source: "kv.get", To: []string{"owner"}},
	}}, []map[string]any{{"minutes": 1.0}})
	team.run = func(action string, args map[string]any) (*sdk.Result, error) {
		ran = append(ran, map[string]any{"action": action, "args": args})
		return &sdk.Result{Message: "ran " + action}, nil
	}
	dept, _ := testNode(&Config{Unit: "shaparak-software", Cache: "0s", Children: map[string]Child{"switch": {URL: "x"}}, Expose: []Exposure{
		{Name: "payments-flow", From: "switch/payments-flow", Title: "Live payments"},
		{Name: "restart-parser", From: "switch/restart-parser"},
	}}, nil)
	dept.dial = func(Child) unitClient { return asChild{team, []string{"parent"}} }
	top, _ := testNode(&Config{Unit: "it-deputy", Cache: "0s", Children: map[string]Child{"shaparak-software": {URL: "y"}}}, nil)
	top.dial = func(Child) unitClient { return asChild{dept, []string{"parent"}} }

	asParent := asChild{team, []string{"parent"}}.as()
	rows, err := asParent.exposed("")
	if err != nil || len(rows) != 3 || rows[0].To != nil {
		t.Fatalf("a parent sees %+v, %v", rows, err)
	}
	if rows, _ := (asChild{team, []string{"product"}}).as().exposed(""); len(rows) != 1 || rows[0].Name != "payments-flow" {
		t.Errorf("product sees %+v", rows)
	}
	if _, err := asParent.show(protocol.Args{"name": "secrets"}); err == nil || !strings.Contains(err.Error(), "exposes nothing") {
		t.Errorf("a parent read an owner-only exposure: %v", err)
	}
	if _, err := asParent.show(protocol.Args{"name": "transactions-today", "type": "refund"}); err == nil {
		t.Error("an input outside its options passed")
	}
	if _, err := asParent.show(protocol.Args{"name": "transactions-today", "since": "1y"}); err == nil {
		t.Error("an undeclared input passed")
	}
	if _, err := asParent.show(protocol.Args{"name": "restart-parser"}); err == nil {
		t.Error("show ran an action")
	}
	if _, err := asParent.invoke(protocol.Args{"name": "restart-parser", "service": "hsmsvc", "reason": "x"}); err == nil {
		t.Error("a fixed arg was overridden by an undeclared input")
	}

	// two levels up: it-deputy lists what shaparak-software passes up from switch, reads and runs it
	got, err := top.exposed("*")
	if err != nil || len(got) != 2 || got[0].Title != "Live payments" || got[0].Origin != "switch" || got[0].Widget != "graph" || got[0].Unit != "shaparak-software" {
		t.Fatalf("it-deputy sees %+v, %v", got, err)
	}
	if f, err := top.show(protocol.Args{"name": "payments-flow", "child": "shaparak-software"}); err != nil || len(frames.Rows(f)) != 1 {
		t.Errorf("show two levels up = %v, %v", f, err)
	}
	if _, err := top.invoke(protocol.Args{"name": "restart-parser", "child": "shaparak-software", "reason": "stuck"}); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0]["action"] != "svc.restart" || ran[0]["args"].(protocol.Args)["service"] != "parsersvc" {
		t.Errorf("ran %+v", ran)
	}

	top.roles = []string{"parent"}
	if _, err := top.exposed("*"); err == nil {
		t.Error("a remote parent listed the children's exposures without drill")
	}
}
