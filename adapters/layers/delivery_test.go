package layers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

// desk is the fake unit with a mail plugin that records what it sends, or fails.
type desk struct {
	*fake
	sent []sdk.Args
	fail bool
}

func (d *desk) Run(action string, args sdk.Args) (*sdk.Result, error) {
	if d.fail {
		return nil, errors.New("mail is not connected")
	}
	d.sent = append(d.sent, args)
	return &sdk.Result{}, nil
}

func TestMessagesNowAndLater(t *testing.T) {
	d := &desk{fake: newFake()}
	for _, m := range []sdk.Args{{"name": "Ali", "email": "ali@pep.ir"}, {"name": "Sara", "email": "sara@pep.ir"}, {"name": "No mail"}} {
		if _, err := orgkit.Save(d, "pm", member, m); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if _, err := queueMessage(d, "pm", messageArgs{Everyone: true, Extra: "boss@pep.ir, ali@pep.ir", Subject: "standup", Body: "10:00"}, now); err != nil {
		t.Fatal(err)
	}
	if len(d.sent) != 1 || d.sent[0]["to"] != "ali@pep.ir, boss@pep.ir, sara@pep.ir" {
		t.Fatalf("sent %v", d.sent)
	}
	if _, err := queueMessage(d, "pm", messageArgs{To: []string{"sara@pep.ir"}, Subject: "review", Body: "x", At: "2026-10-27T08:00:00Z"}, now); err != nil {
		t.Fatal(err)
	}
	if len(d.sent) != 1 {
		t.Fatal("a scheduled message went out early")
	}
	if a := agenda(d, now); len(a) != 1 || a[0].Kind != "message" {
		t.Fatalf("agenda %+v", a)
	}
	dispatch(d, now.Add(24*time.Hour))
	if len(d.sent) != 2 || d.sent[1]["subject"] != "review" {
		t.Fatalf("not dispatched: %v", d.sent)
	}
	if _, err := queueMessage(d, "pm", messageArgs{Subject: "x", Body: "y"}, now); err == nil {
		t.Error("a message with nobody to send to passed")
	}
	d.fail = true
	if _, err := queueMessage(d, "pm", messageArgs{To: []string{"ali@pep.ir"}, Subject: "x", Body: "y"}, now); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("a failed send: %v", err)
	}
}

func TestAgendaAndOverview(t *testing.T) {
	d := &desk{fake: newFake()}
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	_, _ = orgkit.Save(d, "pm", meeting, sdk.Args{"title": "Planning", "start": "2026-10-26T11:00:00Z", "kind": "planning"})
	_, _ = orgkit.Save(d, "pm", milestone, sdk.Args{"title": "Bill v2", "date": "2026-10-20", "status": "on-track"})
	d.org["org.unit.info"] = []map[string]any{{"name": "shaparak-software"}}
	d.org["org.unit.goals"] = []map[string]any{
		{"id": "g1", "title": "Five nines", "to": "shaparak-software", "from": "it-deputy", "on_track": "err", "status": "open", "due": "2026-12-31"},
		{"id": "g2", "title": "Fewer reversals", "to": "switch", "from": "shaparak-software", "on_track": "ok", "status": "accepted"},
	}
	a := agenda(d, now)
	if len(a) != 3 || a[0].Kind != "milestone" || a[1].Title != "Planning" || a[2].Kind != "goal" {
		t.Fatalf("agenda %+v", a)
	}
	got := map[string]map[string]any{}
	for _, r := range orgkit.Rows(overview(d, now)) {
		got[orgkit.Str(r["title"])] = r
	}
	if got["Goals from above"]["value"] != 0.0 || got["Goals given to teams"]["value"] != 1.0 || got["Meetings today"]["value"] != 1.0 {
		t.Errorf("overview %v", got)
	}
	if got["Milestones at risk"]["value"] != 1.0 || got["Sprint"]["status"] != "warn" {
		t.Errorf("overview %v", got)
	}
}

func TestNotesArePrivate(t *testing.T) {
	d := &desk{fake: newFake()}
	res, err := orgkit.Save(d, "sara", note, sdk.Args{"title": "1:1 with Ali", "body": "growth plan"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Outputs["id"].(string)
	if _, err := orgkit.Save(d, "ali", note, sdk.Args{"id": id, "title": "mine now"}); err == nil {
		t.Error("another person changed a private note")
	}
	if _, err := read(d, "ali", "note", id, ""); err == nil {
		t.Error("another person read a private note")
	}
	if f, err := read(d, "sara", "note", id, ""); err != nil || !strings.Contains(orgkit.Str(orgkit.Rows(f)[0]["markdown"]), "growth plan") {
		t.Errorf("own note: %v", err)
	}
}
