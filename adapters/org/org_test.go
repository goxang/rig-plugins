package org

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/protocol"
)

type memState map[string][]byte

func (s memState) get(key string, v any) error {
	if b, ok := s[key]; ok {
		return json.Unmarshal(b, v)
	}
	return nil
}

func (s memState) set(key string, v any) error {
	b, err := json.Marshal(v)
	s[key] = b
	return err
}

func testNode(cfg *Config, rows []map[string]any) (*node, *[]string) {
	var topics []string
	now := time.Unix(1_800_000_000, 0)
	return &node{
		cfg: cfg, state: memState{}, caller: "alice", now: now,
		query: func(string, map[string]any) (*protocol.Frame, error) {
			return frames.FromRows([]protocol.Field{{Name: "updated", Type: "time"}, {Name: "minutes", Type: "number"}}, rows), nil
		},
		emit: func(topic string, _ map[string]any) { topics = append(topics, topic) },
	}, &topics
}

func TestMeets(t *testing.T) {
	for cond, want := range map[string]bool{">= 99.5": true, "< 2": false, "==100": true, "!= 100": false, "> 99.9": true} {
		if got, err := meets(cond, 100); err != nil || got != want {
			t.Errorf("meets(%q, 100) = %v, %v; want %v", cond, got, err, want)
		}
	}
	if _, err := meets("about 3", 1); err == nil {
		t.Error("a condition without an operator passed")
	}
}

func TestReduceAndDuration(t *testing.T) {
	vals := []float64{4, 1, 7}
	for fn, want := range map[string]float64{"count": 5, "sum": 12, "avg": 4, "min": 1, "max": 7, "last": 7} {
		if got, err := reduce(fn, vals, 5); err != nil || got != want {
			t.Errorf("reduce(%s) = %v, %v; want %v", fn, got, err, want)
		}
	}
	if _, err := reduce("median", vals, 3); err == nil {
		t.Error("an unknown reduce passed")
	}
	if d, err := duration("7d"); err != nil || d != 7*24*time.Hour {
		t.Errorf("duration(7d) = %v, %v", d, err)
	}
}

func TestEval(t *testing.T) {
	now := float64(time.Unix(1_800_000_000, 0).Unix())
	rows := []map[string]any{
		{"updated": now - 3600, "minutes": 30.0},
		{"updated": now - 2*86400, "minutes": 90.0},
		{"updated": now - 30*86400, "minutes": 10.0},
	}
	n, _ := testNode(&Config{Unit: "switch"}, rows)
	week := n.eval(Metric{Name: "deploys_week", Source: "x", Since: "7d", OK: ">= 2", Warn: ">= 1"}, "unit.kpis")
	if week.Value != 2 || week.Status != "ok" {
		t.Errorf("deploys_week = %+v", week)
	}
	avg := n.eval(Metric{Name: "lead", Source: "x", Field: "minutes", Reduce: "avg", Scale: 1.0 / 60, OK: "< 0.5"}, "unit.kpis")
	if avg.Value != 130.0/3/60 || avg.Status != "err" {
		t.Errorf("lead = %+v", avg)
	}
	if k := n.eval(Metric{Name: "empty"}, "unit.kpis"); k.Status != "unknown" {
		t.Errorf("a metric with nothing to read = %+v", k)
	}
}

func TestRequestsAndItems(t *testing.T) {
	n, topics := testNode(&Config{Unit: "switch", Parent: "shaparak-software"}, nil)
	res, err := n.request(requestArgs{Title: "Q4 plan"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Outputs["id"].(string)
	if rs := n.requests(); len(rs) != 1 || rs[0].From != "shaparak-software" || rs[0].Status != "open" {
		t.Fatalf("requests = %+v", rs)
	}
	if _, err := n.answer(answerArgs{ID: id, Status: "maybe"}); err == nil {
		t.Error("an unknown status passed")
	}
	if _, err := n.answer(answerArgs{ID: id, Status: "answered", Answer: "attached"}); err != nil {
		t.Fatal(err)
	}
	if rs := n.requests(); rs[0].Answer != "attached" || rs[0].Status != "answered" {
		t.Errorf("answered request = %+v", rs[0])
	}
	if len(*topics) != 2 || (*topics)[0] != "unit.request" || (*topics)[1] != "unit.answered" {
		t.Errorf("events = %v", *topics)
	}
	if _, err := n.request(requestArgs{To: "nobody", Title: "x"}); err == nil {
		t.Error("a request to a unit that is not a child passed")
	}

	if _, err := n.item(itemArgs{Kind: "risk", Title: "HSM renewal", Severity: "urgent"}); err == nil {
		t.Error("an unknown severity passed")
	}
	low, _ := n.item(itemArgs{Kind: "highlight", Title: "release out", Severity: "low"})
	_, _ = n.item(itemArgs{Kind: "risk", Title: "HSM renewal", Severity: "critical"})
	if its := n.items(); len(its) != 2 || its[0].Severity != "critical" {
		t.Errorf("items = %+v", its)
	}
	_, _ = n.item(itemArgs{ID: low.Outputs["id"].(string), Kind: "highlight", Title: "release out", Status: "closed"})
	if its := n.items(); len(its) != 1 {
		t.Errorf("a closed item is still listed: %+v", its)
	}
}

func TestMayDrill(t *testing.T) {
	n, _ := testNode(&Config{Unit: "shaparak-software", Drill: []string{"executive"}}, nil)
	for roles, want := range map[string]bool{"": true, "owner": true, "executive": true, "parent": false, "hr": false} {
		n.roles = nil
		if roles != "" {
			n.roles = []string{roles}
		}
		if got := n.mayDrill(); got != want {
			t.Errorf("roles %q may drill = %v", roles, got)
		}
	}
	n.roles = []string{"parent"}
	if _, err := n.childFrame("switch", "unit.kpis", nil); err == nil {
		t.Error("a parent read a child's figures")
	}
}
