package layers

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

var (
	member = orgkit.Kind{Name: "member", Plural: "members", Title: "The team", Sort: "name",
		Fields: []orgkit.Field{
			{Name: "name", Required: true},
			{Name: "email", Format: "email"},
			{Name: "role", Options: []string{"developer", "qa", "devops", "designer", "analyst", "product-owner", "project-manager", "lead", "stakeholder", "other"}, Default: "developer"},
			{Name: "team", Description: "the unit below they work in"},
			{Name: "note"},
		}}
	meeting = orgkit.Kind{Name: "meeting", Plural: "meetings", Title: "Meetings", Sort: "start",
		Fields: []orgkit.Field{
			{Name: "title", Required: true},
			{Name: "kind", Options: []string{"standup", "planning", "refinement", "review", "retro", "one-on-one", "stakeholder", "other"}, Default: "other"},
			{Name: "start", Type: "time", Format: "date-time", Required: true},
			{Name: "end", Type: "time", Format: "date-time"},
			{Name: "attendees", Type: "list", OptionsFrom: "org-delivery.members email name"},
			{Name: "location", Description: "a room or a call link"},
			{Name: "agenda", Format: "markdown"},
			{Name: "status", Options: []string{"scheduled", "held", "cancelled"}, Default: "scheduled"},
			{Name: "invite", Type: "bool", Description: "email the attendees a calendar invite (mail plugin)"},
		}}
	milestone = orgkit.Kind{Name: "milestone", Plural: "milestones", Title: "Roadmap", Sort: "date",
		Fields: []orgkit.Field{
			{Name: "title", Required: true},
			{Name: "date", Type: "time", Required: true},
			{Name: "status", Options: []string{"planned", "on-track", "at-risk", "done", "missed"}, Default: "planned"},
			{Name: "team", Description: "the unit below that delivers it"},
			{Name: "epic", Description: "the Jira epic key"},
			{Name: "note"},
		}}
	note = orgkit.Kind{Name: "note", Plural: "notes", Title: "My notes", Sort: "-updated", Private: true,
		Fields: []orgkit.Field{
			{Name: "title", Required: true},
			{Name: "body", Format: "markdown"},
			{Name: "tags"},
			{Name: "pinned", Type: "bool"},
		}}
)

// Message is one email to the team, sent now or kept until At.
type Message struct {
	ID      string    `json:"id"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	Status  string    `json:"status"`
	Sent    time.Time `json:"sent"`
	Error   string    `json:"error"`
	By      string    `json:"by"`
}

type messageArgs struct {
	To       []string `json:"to" title:"To" options:"org-delivery.members email name" desc:"pick members, or leave empty with everyone"`
	Everyone bool     `json:"everyone" title:"Everyone in the team"`
	Extra    string   `json:"extra" title:"Other addresses" desc:"comma separated"`
	Subject  string   `json:"subject" rig:"required"`
	Body     string   `json:"body" format:"textarea" rig:"required"`
	At       string   `json:"at" title:"Send at" format:"date-time" desc:"empty sends now"`
}

// deliverer is what the plugin works through: the host for queries, runs and its own state.
type deliverer interface {
	unit
	Run(action string, args sdk.Args) (*sdk.Result, error)
}

// ponytail: one lock for the outbox in this process; two rig processes on one project could both send a due message
var outboxMu sync.Mutex

func outbox(c unit) ([]Message, error) {
	sv, err := c.State("outbox")
	if err != nil || !sv.Found {
		return nil, err
	}
	var ms []Message
	b, _ := json.Marshal(sv.Value)
	return ms, json.Unmarshal(b, &ms)
}

func recipients(c unit, a messageArgs) (string, error) {
	var to []string
	if a.Everyone {
		ms, err := orgkit.List(c, member)
		if err != nil {
			return "", err
		}
		for _, m := range ms {
			if e := orgkit.Str(m["email"]); e != "" {
				to = append(to, e)
			}
		}
	}
	to = append(to, a.To...)
	for _, x := range strings.Split(a.Extra, ",") {
		if x = strings.TrimSpace(x); x != "" {
			to = append(to, x)
		}
	}
	slices.Sort(to)
	to = slices.Compact(to)
	if len(to) == 0 {
		return "", &sdk.Error{Code: sdk.CodeInvalidParams, Message: "nobody to send to: pick members, everyone, or other addresses (members need an email)"}
	}
	return strings.Join(to, ", "), nil
}

// queueMessage sends m now, or keeps it in the outbox until its time.
func queueMessage(c deliverer, user string, a messageArgs, now time.Time) (*sdk.Result, error) {
	to, err := recipients(c, a)
	if err != nil {
		return nil, err
	}
	m := Message{ID: fmt.Sprintf("m%d", now.UnixNano()), To: to, Subject: a.Subject, Body: a.Body, At: now, Status: "scheduled", By: user}
	if a.At != "" {
		at := orgkit.Time(a.At)
		if at.IsZero() {
			return nil, &sdk.Error{Code: sdk.CodeInvalidParams, Message: "at: want 2026-10-27T10:00"}
		}
		m.At = at
	}
	outboxMu.Lock()
	ms, err := outbox(c)
	if err == nil {
		err = c.SetState("outbox", append(ms, m))
	}
	outboxMu.Unlock()
	if err != nil {
		return nil, err
	}
	if m.At.After(now) {
		return &sdk.Result{Message: fmt.Sprintf("scheduled for %s to %s", m.At.Local().Format("Mon 2 Jan 15:04"), to), Outputs: sdk.Args{"id": m.ID}}, nil
	}
	dispatch(c, now)
	ms, _ = outbox(c)
	for _, x := range ms {
		if x.ID == m.ID && x.Status == "failed" {
			return nil, fmt.Errorf("not sent (kept in the outbox as failed): %s", x.Error)
		}
	}
	return &sdk.Result{Message: "sent to " + to, Outputs: sdk.Args{"id": m.ID}}, nil
}

// dispatch sends the outbox messages that are due.
// ponytail: runs when someone opens a page that reads the outbox or sends; a host timer would send on time with nobody looking
func dispatch(c deliverer, now time.Time) {
	outboxMu.Lock()
	defer outboxMu.Unlock()
	ms, err := outbox(c)
	if err != nil {
		return
	}
	changed := false
	for i, m := range ms {
		if m.Status != "scheduled" || m.At.After(now) {
			continue
		}
		_, err := c.Run("mail.send", sdk.Args{"to": m.To, "subject": m.Subject, "body": m.Body})
		ms[i].Status, ms[i].Sent, changed = "sent", now, true
		if err != nil {
			ms[i].Status, ms[i].Error = "failed", err.Error()
		}
	}
	if changed {
		_ = c.SetState("outbox", ms)
	}
}

// Agenda is a dated thing on the calendar.
type Agenda struct {
	Time   time.Time `json:"time"`
	End    time.Time `json:"end"`
	Title  string    `json:"title"`
	Kind   string    `json:"kind"`
	Status string    `json:"status"`
	ID     string    `json:"id"`
	Note   string    `json:"note"`
}

func agenda(c deliverer, now time.Time) []Agenda {
	dispatch(c, now)
	var out []Agenda
	ms, _ := orgkit.List(c, meeting)
	for _, m := range ms {
		out = append(out, Agenda{orgkit.Time(m["start"]), orgkit.Time(m["end"]), orgkit.Str(m["title"]), "meeting", orgkit.Str(m["status"]), orgkit.Str(m["id"]),
			strings.TrimSuffix(orgkit.Str(m["kind"])+" · "+orgkit.Str(m["location"]), " · ")})
	}
	mss, _ := orgkit.List(c, milestone)
	for _, m := range mss {
		out = append(out, Agenda{Time: orgkit.Time(m["date"]), Title: "◆ " + orgkit.Str(m["title"]), Kind: "milestone", Status: orgkit.Str(m["status"]), ID: orgkit.Str(m["id"]), Note: orgkit.Str(m["team"])})
	}
	if f, err := c.Query("org.unit.goals", nil); err == nil {
		for _, g := range orgkit.Rows(f) {
			if t := orgkit.Time(g["due"]); !t.IsZero() {
				out = append(out, Agenda{Time: t, Title: "◎ " + orgkit.Str(g["title"]), Kind: "goal", Status: orgkit.Str(g["status"]), ID: orgkit.Str(g["id"]), Note: orgkit.Str(g["from"]) + " → " + orgkit.Str(g["to"])})
			}
		}
	}
	if f, err := c.Query("org.unit.requests", nil); err == nil {
		for _, r := range orgkit.Rows(f) {
			if t := orgkit.Time(r["due"]); !t.IsZero() {
				out = append(out, Agenda{Time: t, Title: "? " + orgkit.Str(r["title"]), Kind: "request", Status: orgkit.Str(r["status"]), ID: orgkit.Str(r["id"]), Note: orgkit.Str(r["from"]) + " → " + orgkit.Str(r["to"])})
			}
		}
	}
	box, _ := outbox(c)
	for _, m := range box {
		if m.Status == "scheduled" {
			out = append(out, Agenda{Time: m.At, Title: "✉ " + m.Subject, Kind: "message", Status: m.Status, ID: m.ID, Note: m.To})
		}
	}
	out = slices.DeleteFunc(out, func(a Agenda) bool { return a.Time.IsZero() })
	slices.SortStableFunc(out, func(a, b Agenda) int { return a.Time.Compare(b.Time) })
	return out
}

func doneStatus(s string) bool {
	switch strings.ToLower(s) {
	case "done", "closed", "resolved", "released":
		return true
	}
	return false
}

func overview(c deliverer, now time.Time) *sdk.Frame {
	var ts []map[string]any
	f, err := c.Query("jira.tracker.board", nil)
	switch {
	case err != nil:
		ts = append(ts, tile("Sprint", nil, "", "warn", "Jira: "+err.Error()))
	case len(orgkit.Rows(f)) > 0:
		rows := orgkit.Rows(f)
		done := 0
		for _, r := range rows {
			if doneStatus(orgkit.Str(r["status"])) {
				done++
			}
		}
		ts = append(ts, tile("Sprint done", 100*done/len(rows), "%", "ok", fmt.Sprintf("%d of %d issues in the open sprint", done, len(rows))))
	default:
		ts = append(ts, tile("Sprint", nil, "", "warn", "no open sprint"))
	}
	var got, gotOK, gave, gaveOK int
	if f, err := c.Query("org.unit.goals", nil); err == nil {
		info, _ := c.Query("org.unit.info", nil)
		me := ""
		if rs := orgkit.Rows(info); len(rs) > 0 {
			me = orgkit.Str(rs[0]["name"])
		}
		for _, g := range orgkit.Rows(f) {
			if st := orgkit.Str(g["status"]); st == "dropped" || st == "met" {
				continue
			}
			ok := orgkit.Str(g["on_track"]) == "ok"
			if orgkit.Str(g["to"]) == me {
				got++
				if ok {
					gotOK++
				}
			} else {
				gave++
				if ok {
					gaveOK++
				}
			}
		}
	}
	ts = append(ts, tile("Goals from above", gotOK, of(got), worse(float64(got-gotOK), 1, 2), "on track, measured on this unit's KPIs"))
	ts = append(ts, tile("Goals given to teams", gaveOK, of(gave), worse(float64(gave-gaveOK), 1, 3), "on track at the teams"))
	reports := 0
	if f, err := c.Query("org.unit.reports", sdk.Args{"child": "*"}); err == nil {
		for _, r := range orgkit.Rows(f) {
			if now.Sub(orgkit.Time(r["created"])) < 7*24*time.Hour {
				reports++
			}
		}
	}
	ts = append(ts, tile("Team reports", reports, "this week", "ok", "published by the units below"))
	today, upcoming := 0, 0
	for _, a := range agenda(c, now) {
		if a.Kind != "meeting" || a.Status == "cancelled" {
			continue
		}
		if y, m, d := a.Time.Local().Date(); y == now.Local().Year() && m == now.Local().Month() && d == now.Local().Day() {
			today++
		} else if a.Time.After(now) && a.Time.Sub(now) < 7*24*time.Hour {
			upcoming++
		}
	}
	ts = append(ts, tile("Meetings today", today, "", "ok", fmt.Sprintf("%d more this week", upcoming)))
	atRisk := 0
	mss, _ := orgkit.List(c, milestone)
	for _, m := range mss {
		if st := orgkit.Str(m["status"]); st == "at-risk" || st == "missed" || st != "done" && orgkit.Time(m["date"]).Before(now) {
			atRisk++
		}
	}
	ts = append(ts, tile("Milestones at risk", atRisk, of(len(mss)), worse(float64(atRisk), 1, 2), "at risk, missed, or past their date"))
	open := 0
	if f, err := c.Query("org.unit.requests", nil); err == nil {
		for _, r := range orgkit.Rows(f) {
			if st := orgkit.Str(r["status"]); st == "open" || st == "accepted" {
				open++
			}
		}
	}
	ts = append(ts, tile("Requests open", open, "", worse(float64(open), 5, 10), "from above and to the teams"))
	return tiles(ts...)
}

// milestoneTimeline is the milestones as a timeline.
func milestoneTimeline(c unit) *sdk.Frame {
	mss, _ := orgkit.List(c, milestone)
	rows := make([]map[string]any, 0, len(mss))
	for _, m := range mss {
		rows = append(rows, map[string]any{"time": orgkit.Time(m["date"]), "title": m["title"], "kind": first(orgkit.Str(m["team"]), "milestone"),
			"status": map[string]string{"done": "ok", "on-track": "ok", "at-risk": "warn", "missed": "err"}[orgkit.Str(m["status"])], "note": m["note"]})
	}
	return orgkit.Table(rows, "time", "title", "kind", "status", "note")
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// Delivery is org-delivery: the product owner's, project manager's and product analyst's desk above
// a team: goals down and reports up, the calendar, the team and its messages, the roadmap, notes.
func Delivery(p *sdk.Plugin) {
	for _, k := range []orgkit.Kind{member, milestone, note} {
		orgkit.Records(p, k)
	}
	m := meeting
	m.Saved = func(c *sdk.Ctx, row map[string]any, res *sdk.Result) *sdk.Result {
		if row["invite"] != true || orgkit.Str(row["attendees"]) == "" || row["status"] == "cancelled" {
			return res
		}
		_, err := c.Run("mail.send", sdk.Args{"to": row["attendees"], "subject": row["title"], "body": orgkit.Str(row["agenda"]),
			"start": orgkit.Time(row["start"]).Format(time.RFC3339), "end": orgkit.Time(row["end"]).Format(time.RFC3339), "location": row["location"]})
		if err != nil {
			res.Message += "; the invite was not sent: " + err.Error()
		} else {
			res.Message += "; invite sent to " + orgkit.Str(row["attendees"])
		}
		return res
	}
	orgkit.Records(p, m)

	p.Action("message", sdk.Write, func(c *sdk.Ctx, a messageArgs) (*sdk.Result, error) {
		return queueMessage(c, c.Call.User, a, time.Now())
	}, sdk.Describe("email the whole team, picked members or other addresses, now or at a time"))
	p.Action("unschedule", sdk.Write, func(c *sdk.Ctx, a struct {
		ID string `json:"id" rig:"required"`
	}) (*sdk.Result, error) {
		outboxMu.Lock()
		defer outboxMu.Unlock()
		ms, err := outbox(c)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(ms, func(m Message) bool { return m.ID == a.ID && m.Status == "scheduled" })
		if i < 0 {
			return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no scheduled message " + a.ID}
		}
		ms[i].Status = "cancelled"
		return &sdk.Result{Message: "cancelled " + ms[i].Subject}, c.SetState("outbox", ms)
	}, sdk.Describe("cancel a scheduled message"))
	p.Source("messages", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		dispatch(c, time.Now())
		ms, err := outbox(c)
		slices.Reverse(ms)
		return frames.Of(ms), err
	}, sdk.Describe("messages sent to the team and the ones waiting for their time"))
	p.Source("agenda", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return frames.Of(agenda(c, time.Now())), nil
	}, sdk.Describe("everything with a date: meetings, milestones, goals and requests due, scheduled messages"))
	p.Source("overview", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return overview(c, time.Now()), nil
	}, sdk.Describe("the desk in seven figures: sprint, goals up and down, reports, meetings, milestones, requests"))
	p.Source("roadmap", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		return milestoneTimeline(c), nil
	}, sdk.Describe("milestones as a timeline"))
	p.Source("read", func(c *sdk.Ctx, a struct {
		Kind string `json:"kind" rig:"required" desc:"note or report"`
		ID   string `json:"id" rig:"required"`
		Unit string `json:"unit" desc:"the unit a report came from"`
	}) (*sdk.Frame, error) {
		return read(c, c.Call.User, a.Kind, a.ID, a.Unit)
	}, sdk.Describe("one note or report as markdown, for a preview"))
	health(p)
	page(p, "Live", func(c *sdk.Ctx) (*sdk.Node, error) { return live(c, nil, nil) })
}

func read(c unit, user, kind, id, unitName string) (*sdk.Frame, error) {
	text := ""
	switch kind {
	case "note":
		ns, err := orgkit.List(c, note)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if n["id"] == id && orgkit.Str(n["by"]) == user {
				text = "# " + orgkit.Str(n["title"]) + "\n\n" + orgkit.Str(n["body"])
			}
		}
	case "report":
		args := sdk.Args{"child": unitName}
		if unitName == "" {
			args = nil
		}
		f, err := c.Query("org.unit.reports", args)
		if err != nil {
			return nil, err
		}
		for _, r := range orgkit.Rows(f) {
			if r["id"] == id {
				text = fmt.Sprintf("# %s\n\n%s · %s · %s\n\n%s", orgkit.Str(r["title"]), first(orgkit.Str(r["unit"]), unitName), orgkit.Str(r["period"]), orgkit.Str(r["by"]), orgkit.Str(r["body"]))
			}
		}
	}
	if text == "" {
		return nil, &sdk.Error{Code: sdk.CodeNotFound, Message: "no " + kind + " " + id}
	}
	return orgkit.Table([]map[string]any{{"markdown": text}}, "markdown"), nil
}
