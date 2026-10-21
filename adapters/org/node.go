package org

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/sdk"
)

// node is one call's view of the unit: its config, the local sources, its state and its children.
type node struct {
	ctx   context.Context
	cfg   *Config
	query func(source string, args map[string]any) (*protocol.Frame, error)
	state interface {
		get(key string, v any) error
		set(key string, v any) error
	}
	emit   func(topic string, data map[string]any)
	caller string
	roles  []string
	now    time.Time
}

type ctxState struct{ c *sdk.Ctx }

func (s ctxState) get(key string, v any) error {
	sv, err := s.c.State(key)
	if err != nil || !sv.Found {
		return err
	}
	b, _ := json.Marshal(sv.Value)
	return json.Unmarshal(b, v)
}

func (s ctxState) set(key string, v any) error { return s.c.SetState(key, v) }

func open(c *sdk.Ctx) (*node, error) {
	cfg, err := load(c)
	if err != nil {
		return nil, err
	}
	return &node{
		ctx: c, cfg: cfg, state: ctxState{c}, caller: c.Call.User, roles: c.Call.Roles, now: time.Now(),
		query: func(source string, args map[string]any) (*protocol.Frame, error) {
			f, err := c.Query(source, args)
			if err != nil {
				return nil, err
			}
			return convert[protocol.Frame](f), nil
		},
		emit: func(topic string, data map[string]any) {
			_ = c.Emit(sdk.Event{Topic: topic, Time: time.Now().Format(time.RFC3339), Data: data})
		},
	}, nil
}

func convert[T any](v any) *T {
	b, _ := json.Marshal(v)
	out := new(T)
	_ = json.Unmarshal(b, out)
	return out
}

// fromRow decodes a frame row; wire times are Unix seconds, so they are read apart.
func fromRow[T any](r map[string]any) (v *T, created, updated time.Time) {
	rest := maps.Clone(r)
	delete(rest, "created")
	delete(rest, "updated")
	return convert[T](rest), unix(r["created"]), unix(r["updated"])
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// items are this unit's own (recorded and fed from sources) and its children's at or above Escalate.
func (n *node) items() []Item {
	var own []Item
	_ = n.state.get("items", &own)
	out := slices.DeleteFunc(own, func(i Item) bool { return i.Status == "closed" })
	for _, f := range n.cfg.Items {
		out = append(out, n.feed(f)...)
	}
	floor := rank(n.cfg.Escalate)
	for _, name := range n.childNames() {
		fr, err := n.child(name, "unit.items", nil)
		if err != nil {
			continue
		}
		for _, r := range frames.Rows(fr) {
			it, _, updated := fromRow[Item](r)
			it.Updated = updated
			if rank(it.Severity) >= floor {
				out = append(out, *it)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int { return rank(b.Severity) - rank(a.Severity) })
	return out
}

func (n *node) feed(f Feed) []Item {
	fr, err := n.query(f.Source, f.Args)
	if err == nil {
		fr, err = transform(fr, f.Transform)
	}
	if err != nil {
		return []Item{{Unit: n.cfg.Unit, ID: f.Source, Kind: first(f.Kind, "item"), Title: "cannot read " + f.Source + ": " + err.Error(), Severity: "medium", Status: "open", Updated: n.now}}
	}
	get := func(r map[string]any, key string) string {
		if v := r[first(f.Map[key], key)]; v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	var out []Item
	for _, r := range frames.Rows(fr) {
		out = append(out, Item{Unit: n.cfg.Unit, ID: get(r, "id"), Kind: first(get(r, "kind"), f.Kind, "item"), Title: get(r, "title"),
			Severity: first(get(r, "severity"), f.Severity, "medium"), Status: first(get(r, "status"), "open"), Owner: get(r, "owner"),
			Updated: unix(r[first(f.Map["updated"], "updated")]), Ref: get(r, "ref")})
	}
	return out
}

func (n *node) item(a itemArgs) (*sdk.Result, error) {
	mu.Lock()
	defer mu.Unlock()
	var own []Item
	if err := n.state.get("items", &own); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(own, func(it Item) bool { return a.ID != "" && it.ID == a.ID })
	if a.ID != "" && i < 0 {
		return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no item " + a.ID}
	}
	it := Item{Unit: n.cfg.Unit, ID: first(a.ID, newID()), Kind: a.Kind, Title: a.Title, Severity: first(a.Severity, "medium"),
		Status: first(a.Status, "open"), Owner: first(a.Owner, n.caller), Ref: a.Ref, Updated: n.now}
	if rank(it.Severity) < 0 {
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "severity is one of " + strings.Join(severities, ", ")}
	}
	if i >= 0 {
		own[i] = it
	} else {
		own = append(own, it)
	}
	if err := n.state.set("items", own); err != nil {
		return nil, err
	}
	return &sdk.Result{Message: it.Kind + " " + it.ID + " recorded", Outputs: map[string]any{"id": it.ID}}, nil
}

// requests are the ones this unit got and the ones it sent to its children, as they answer them.
func (n *node) requests() []Request {
	var got []Request
	_ = n.state.get("requests", &got)
	for _, name := range n.childNames() {
		fr, err := n.child(name, "unit.requests", nil)
		if err != nil {
			continue
		}
		for _, r := range frames.Rows(fr) {
			rq, created, updated := fromRow[Request](r)
			if rq.From != n.cfg.Unit {
				continue
			}
			rq.To, rq.Created, rq.Updated = name, created, updated
			got = append(got, *rq)
		}
	}
	slices.SortStableFunc(got, func(a, b Request) int { return b.Updated.Compare(a.Updated) })
	return got
}

func (n *node) request(a requestArgs) (*sdk.Result, error) {
	if a.To != "" && a.To != n.cfg.Unit {
		ch, ok := n.cfg.Children[a.To]
		if !ok {
			return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: fmt.Sprintf("%s is not a child of %s (have %s)", a.To, n.cfg.Unit, strings.Join(n.childNames(), ", "))}
		}
		args := protocol.Args{"title": a.Title, "body": a.Body, "due": a.Due, "from": n.cfg.Unit, "by": first(a.By, n.caller)}
		_, res, err := client(ch).Run(n.ctx, plugin(ch)+".unit.request", args, api.RunOpts{})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.To, err)
		}
		forget(a.To)
		msg := "asked " + a.To
		if res != nil && res.Outputs["id"] != nil {
			msg += " (request " + fmt.Sprint(res.Outputs["id"]) + ")"
		}
		return &sdk.Result{Message: msg, Outputs: res.Outputs}, nil
	}
	mu.Lock()
	defer mu.Unlock()
	var got []Request
	if err := n.state.get("requests", &got); err != nil {
		return nil, err
	}
	rq := Request{ID: newID(), From: first(a.From, n.cfg.Parent, n.caller), To: n.cfg.Unit, By: first(a.By, n.caller), Title: a.Title,
		Body: a.Body, Due: a.Due, Status: "open", Created: n.now, Updated: n.now}
	if err := n.state.set("requests", append(got, rq)); err != nil {
		return nil, err
	}
	n.emit("unit.request", map[string]any{"id": rq.ID, "from": rq.From, "by": rq.By, "title": rq.Title, "body": rq.Body, "due": rq.Due})
	return &sdk.Result{Message: "request " + rq.ID + " recorded for " + n.cfg.Unit, Outputs: map[string]any{"id": rq.ID}}, nil
}

func (n *node) answer(a answerArgs) (*sdk.Result, error) {
	if !slices.Contains([]string{"open", "accepted", "answered", "declined", "done"}, a.Status) {
		return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "status is one of open, accepted, answered, declined, done"}
	}
	mu.Lock()
	defer mu.Unlock()
	var got []Request
	if err := n.state.get("requests", &got); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(got, func(r Request) bool { return r.ID == a.ID })
	if i < 0 {
		return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no request " + a.ID + " here (answer a request where it was sent)"}
	}
	got[i].Status, got[i].Updated = a.Status, n.now
	if a.Answer != "" {
		got[i].Answer = a.Answer
	}
	if err := n.state.set("requests", got); err != nil {
		return nil, err
	}
	n.emit("unit.answered", map[string]any{"id": a.ID, "from": got[i].From, "status": a.Status, "answer": got[i].Answer})
	return &sdk.Result{Message: "request " + a.ID + " " + a.Status}, nil
}

func (n *node) children() []ChildRow {
	names := n.childNames()
	out := make([]ChildRow, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { out[i] = n.childRow(name) })
	}
	wg.Wait()
	return out
}

func (n *node) childRow(name string) ChildRow {
	row := ChildRow{Name: name, Title: name}
	info, err := n.child(name, "unit.info", nil)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	if rs := frames.Rows(info); len(rs) > 0 {
		in := convert[Info](rs[0])
		row.Title, row.Layer = first(in.Title, name), in.Layer
	}
	row.Reachable, row.Updated = true, n.now
	if at, stale := cachedAt(name, "unit.info"); stale {
		row.Reachable, row.Updated, row.Error = false, at, "unreachable; showing its answer from "+at.Format("2006-01-02 15:04")
	}
	if fr, err := n.child(name, "unit.kpis", nil); err == nil {
		for _, r := range frames.Rows(fr) {
			switch r["status"] {
			case "ok":
				row.OK++
			case "warn":
				row.Warn++
			case "err", "unknown":
				row.Err++
			}
		}
	}
	if fr, err := n.child(name, "unit.items", nil); err == nil {
		for _, r := range frames.Rows(fr) {
			if rank(fmt.Sprint(r["severity"])) >= rank("high") {
				row.High++
			}
		}
	}
	if fr, err := n.child(name, "unit.requests", nil); err == nil {
		for _, r := range frames.Rows(fr) {
			if r["status"] == "open" || r["status"] == "accepted" {
				row.OpenRequests++
			}
		}
	}
	return row
}

func (n *node) childNames() []string {
	names := make([]string, 0, len(n.cfg.Children))
	for k := range n.cfg.Children {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// childFrame is a child's own answer to source, for drilling one level down.
func (n *node) childFrame(name, source string, args protocol.Args) (*sdk.Frame, error) {
	if !n.mayDrill() {
		return nil, &sdk.Error{Code: sdk.CodeDenied, Message: n.caller + " may not read below " + n.cfg.Unit + " (org.drill)"}
	}
	f, err := n.child(name, source, args)
	if err != nil {
		return nil, err
	}
	return convert[sdk.Frame](f), nil
}

// mayDrill keeps a unit above to what this one reports: a remote caller reads a child's own figures only with a role in drill.
func (n *node) mayDrill() bool {
	if len(n.roles) == 0 {
		return true
	}
	for _, r := range n.roles {
		if r == "owner" || slices.Contains(n.cfg.Drill, r) {
			return true
		}
	}
	return false
}

func unix(v any) time.Time {
	switch t := v.(type) {
	case float64:
		return time.Unix(0, int64(t*1e9))
	case int64:
		return time.Unix(t, 0)
	}
	return time.Time{}
}
