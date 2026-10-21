package flow

import (
	"testing"
	"time"
)

var pipeline = &Config{Simulate: true, Warn: 1, Err: 5,
	Edges: []Edge{{From: "pos", To: "sessionsvc", Sim: 120}, {From: "sessionsvc", To: "dispatchersvc"}, {From: "dispatchersvc", To: "shaparak"}},
	Types: []Type{{Name: "purchase", Share: 0.8}, {Name: "bill"}},
}

func TestGraph(t *testing.T) {
	rows := pipeline.graph(pipeline.simulated(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)))
	if len(rows) != 7 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].From != "pos" || rows[0].Value <= 0 || !rows[0].Simulated || rows[0].Status == "unknown" {
		t.Errorf("edge = %+v", rows[0])
	}
	if n := rows[4]; n.Node != "sessionsvc" || n.Value <= 0 {
		t.Errorf("node = %+v", n)
	}
	broken := func(kind, _, _ string) (float64, bool) {
		return map[string]float64{"rate": 100, "errors": 7, "latency": 90}[kind], true
	}
	if r := pipeline.graph(broken)[0]; r.Errors != 7 || r.Status != "err" {
		t.Errorf("7%% errors = %+v", r)
	}
	if n := pipeline.graph(broken)[4]; n.Status != "err" {
		t.Errorf("a node behind a failing edge = %+v", n)
	}
	none := func(string, string, string) (float64, bool) { return 0, false }
	if r := pipeline.graph(none)[0]; r.Status != "unknown" {
		t.Errorf("no metrics = %+v", r)
	}
}

func TestSummary(t *testing.T) {
	s := pipeline.summary(pipeline.simulated(time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC)))
	if len(s) != 2 || s[0].Count <= s[1].Count || s[0].Success < 90 || s[0].Success > 100 || s[0].P95 <= 0 {
		t.Errorf("summary = %+v", s)
	}
}
