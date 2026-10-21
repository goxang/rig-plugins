package layers

import (
	"encoding/json"
	"testing"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

// fake is a unit with two teams below and state in memory.
type fake struct {
	state map[string]any
	org   map[string][]map[string]any
}

func (f *fake) Query(source string, args sdk.Args) (*sdk.Frame, error) {
	key := source
	if c, _ := args["child"].(string); c != "" {
		key += ":" + c
	}
	rows := f.org[key]
	cols := map[string]bool{}
	var names []string
	for _, r := range rows {
		for k := range r {
			if !cols[k] {
				cols[k] = true
				names = append(names, k)
			}
		}
	}
	return orgkit.Table(rows, names...), nil
}

func (f *fake) State(key string) (sdk.StateValue, error) {
	v, ok := f.state[key]
	return sdk.StateValue{Found: ok, Value: v}, nil
}

func (f *fake) SetState(key string, v any) error {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	f.state[key] = out
	return nil
}

func newFake() *fake {
	return &fake{state: map[string]any{}, org: map[string][]map[string]any{
		"org.unit.children": {
			{"name": "switch", "title": "Switch", "reachable": true, "ok": 3.0, "warn": 1.0, "err": 0.0, "high": 0.0, "open_requests": 1.0},
			{"name": "wallet", "title": "Wallet", "reachable": false, "ok": 0.0, "warn": 0.0, "err": 0.0, "error": "unreachable"},
		},
		"org.unit.kpis:switch":   {{"name": "success-rate", "value": 97.0}},
		"org.unit.people:switch": {{"name": "headcount", "title": "Headcount", "value": 6.0}},
		"org.unit.items:switch": {
			{"kind": "incident", "title": "Shaparak link down", "severity": "critical", "status": "open"},
			{"kind": "decision", "title": "Move to HSM v2", "severity": "high", "status": "open"},
			{"kind": "risk", "title": "old", "severity": "high", "status": "closed"},
		},
	}}
}

func TestHealth(t *testing.T) {
	h, err := orgkit.HealthOf(newFake())
	if err != nil || len(h) != 2 {
		t.Fatalf("health = %v, %v", h, err)
	}
	if h[0].Value != 75 || h[0].Status != "warn" || h[1].Status != "err" || h[1].Note != "unreachable" {
		t.Errorf("health = %+v", h)
	}
}

func TestProduct(t *testing.T) {
	f := newFake()
	_, _ = orgkit.Save(f, "ana", initiative, sdk.Args{"title": "QR", "reach": "1000", "impact": "2", "confidence": "80", "effort": "4", "stage": "now", "public": "true", "due": "2026-12-01"})
	_, _ = orgkit.Save(f, "ana", initiative, sdk.Args{"title": "Secret", "reach": 10.0, "effort": 1.0, "impact": 1.0, "stage": "next", "due": "2026-11-01"})
	rows, _ := orgkit.List(f, initiative)
	if rows[0]["title"] != "QR" || rows[0]["score"] != 400.0 || rows[1]["score"] != 5.0 {
		t.Fatalf("RICE = %v", rows)
	}
	pub, _ := roadmap(f, true)
	all, _ := roadmap(f, false)
	if r := orgkit.Rows(pub); len(r) != 1 || r[0]["title"] != "QR" || r[0]["note"] != nil {
		t.Errorf("public roadmap = %v", r)
	}
	if len(orgkit.Rows(all)) != 2 {
		t.Errorf("roadmap = %v", orgkit.Rows(all))
	}
}

func TestExecutive(t *testing.T) {
	f := newFake()
	o, _ := orgkit.Save(f, "ceo", objective, sdk.Args{"title": "Reliable payments", "quarter": "2026-Q4"})
	_, _ = orgkit.Save(f, "ceo", keyresult, sdk.Args{"objective": o.Outputs["id"], "title": "Success rate 99", "unit": "switch", "kpi": "success-rate", "baseline": "95", "target": "99"})
	_, _ = orgkit.Save(f, "ceo", keyresult, sdk.Args{"objective": "x", "title": "Wallet NPS", "unit": "wallet", "kpi": "nps", "target": "50"})
	krs, _ := okrs(f)
	if krs[0]["objective"] != "Reliable payments" || krs[0]["current"] != 97.0 || krs[0]["progress"] != 50.0 {
		t.Errorf("okr = %v", krs[0])
	}
	if krs[1]["note"] != "wallet does not report nps" || krs[1]["progress"] != 0.0 {
		t.Errorf("unmeasured okr = %v", krs[1])
	}
	if got := escalated(f, nil); len(got) != 2 {
		t.Errorf("escalated = %v", got)
	}
}

func TestHeadcount(t *testing.T) {
	f := newFake()
	_, _ = orgkit.Save(f, "hr", plan, sdk.Args{"team": "switch", "quarter": "2026-Q3", "headcount": "7"})
	_, _ = orgkit.Save(f, "hr", plan, sdk.Args{"team": "switch", "quarter": "2026-Q4", "headcount": "9"})
	_, _ = orgkit.Save(f, "hr", position, sdk.Args{"title": "Go engineer", "team": "switch", "stage": "sourcing"})
	fr, err := headcount(f)
	if err != nil {
		t.Fatal(err)
	}
	r := orgkit.Rows(fr)[0]
	if r["actual"] != 6.0 || r["planned"] != 9.0 || r["gap"] != 3.0 || r["open"] != 1.0 || r["status"] != "warn" {
		t.Errorf("headcount = %v", r)
	}
}

func TestRisk(t *testing.T) {
	f := newFake()
	_, _ = orgkit.Save(f, "cto", risk, sdk.Args{"title": "single HSM", "likelihood": "4", "impact": "5"})
	_, _ = orgkit.Save(f, "cto", risk, sdk.Args{"title": "docs", "likelihood": "1", "impact": "2"})
	rows, _ := orgkit.List(f, risk)
	if rows[0]["level"] != "high" || rows[1]["level"] != "low" {
		t.Errorf("risks = %v", rows)
	}
}
