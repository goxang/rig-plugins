package org

import (
	"testing"
)

func TestGoalsAndReports(t *testing.T) {
	avail := 99.2
	team, topics := testNode(&Config{Unit: "switch", Parent: "shaparak-software", Cache: "0s",
		KPIs: []Metric{{Name: "availability", Value: &avail, OK: ">= 99"}}}, nil)
	dept, _ := testNode(&Config{Unit: "shaparak-software", Cache: "0s", Children: map[string]Child{"switch": {URL: "x"}}}, nil)
	dept.dial = func(Child) unitClient { return asChild{team, []string{"parent"}} }

	if _, err := dept.goal(goalArgs{To: "switch", Title: "Five nines", KPI: "availability", Target: ">= 99.5", Due: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dept.goal(goalArgs{To: "switch", Title: "bad", Target: "about 3"}); err == nil {
		t.Error("a target without an operator passed")
	}
	got := team.goals()
	if len(got) != 1 || got[0].From != "shaparak-software" || got[0].Value != 99.2 || got[0].OnTrack != "err" {
		t.Fatalf("team sees %+v", got)
	}
	if len(*topics) != 1 || (*topics)[0] != "unit.goal" {
		t.Errorf("topics %v", *topics)
	}
	sent := dept.goals()
	if len(sent) != 1 || sent[0].To != "switch" || sent[0].OnTrack != "err" {
		t.Errorf("dept sees %+v", sent)
	}
	if _, err := team.goal(goalArgs{ID: got[0].ID, Status: "accepted", Note: "plan in Q4"}); err != nil {
		t.Fatal(err)
	}
	if g := team.goals()[0]; g.Status != "accepted" || g.Note != "plan in Q4" {
		t.Errorf("after update %+v", g)
	}

	if _, err := team.report(reportArgs{Title: "Week 44", Body: "shipped **bill v2**"}); err != nil {
		t.Fatal(err)
	}
	rs, err := dept.reports("*")
	if err != nil || len(rs) != 1 || rs[0].Unit != "switch" || rs[0].Kind != "status" {
		t.Fatalf("dept reads %+v %v", rs, err)
	}
	if _, err := (asChild{dept, []string{"parent"}}).as().reports("*"); err == nil {
		t.Error("a parent read its grandchildren's reports without drill")
	}
}
