// Package jira is Jira as tracker/1: JQL search with completion, issues, transitions, comments,
// create, assign and link, and agile boards and sprints, over the REST API v2 (Cloud and Server).
package jira

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

type Jira struct {
	*forge.Client
	project string
}

// Open reads plugins.jira: url, user (Cloud: the account email, with an API token), token
// (JIRA_TOKEN; without user it is a Server personal access token) and project (the default JQL scope).
func Open(c *sdk.Ctx) (*Jira, error) {
	base := strings.TrimSuffix(forge.Setting(c, "JIRA", "url"), "/")
	if base == "" {
		return nil, sdk.Unavailable("jira has no address", "connect it on the Jira tab, or set plugins.jira.url (https://your-org.atlassian.net)")
	}
	ep := httpx.Endpoint{Headers: map[string]string{"Accept": "application/json"}}
	tok := forge.Token(c, "JIRA_TOKEN", "JIRA_API_TOKEN", "ATLASSIAN_TOKEN")
	if u := forge.Setting(c, "JIRA", "user"); u != "" {
		ep.User, ep.Password = u, tok
	} else {
		ep.Token = tok
	}
	return &Jira{Client: &forge.Client{Base: base, Host: base, Endpoint: ep}, project: forge.Setting(c, "JIRA", "project")}, nil
}

type fields struct {
	Summary     string `json:"summary"`
	Description any    `json:"description"`
	Status      struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"status"`
	IssueType *struct {
		Name string `json:"name"`
	} `json:"issuetype"`
	Assignee *struct {
		DisplayName string `json:"displayName"`
	} `json:"assignee"`
	Priority *struct {
		Name string `json:"name"`
	} `json:"priority"`
	Updated string `json:"updated"`
	Comment struct {
		Comments []struct {
			Author struct {
				DisplayName string `json:"displayName"`
			} `json:"author"`
			Created string `json:"created"`
			Body    any    `json:"body"`
		} `json:"comments"`
	} `json:"comment"`
}

type rawIssue struct {
	Key    string `json:"key"`
	Fields fields `json:"fields"`
}

// Jira times look like 2026-10-10T12:00:00.000+0000.
func jiraTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000-0700", s)
	return t
}

func (j *Jira) row(i rawIssue) forge.Issue {
	is := forge.Issue{Key: i.Key, Title: i.Fields.Summary, Status: i.Fields.Status.Name, Updated: jiraTime(i.Fields.Updated), URL: j.Base + "/browse/" + i.Key}
	if i.Fields.Assignee != nil {
		is.Assignee = i.Fields.Assignee.DisplayName
	}
	if i.Fields.Priority != nil {
		is.Priority = i.Fields.Priority.Name
	}
	if i.Fields.IssueType != nil {
		is.Type = i.Fields.IssueType.Name
	}
	return is
}

// JQL turns the query argument into JQL: JQL as is, other text a search in the default project.
func JQL(query, assignee, project string) string {
	q := strings.TrimSpace(query)
	isJQL := strings.ContainsAny(q, "=~<>") || strings.Contains(strings.ToLower(q), "order by") || strings.Contains(q, " in (")
	order := " ORDER BY updated DESC"
	if i := strings.Index(strings.ToLower(q), "order by"); isJQL && i >= 0 {
		q, order = strings.TrimSpace(q[:i]), " "+q[i:]
	}
	var parts []string
	switch {
	case isJQL && q != "":
		parts = append(parts, q)
	case q != "":
		parts = append(parts, fmt.Sprintf("text ~ %q", q))
	}
	if !isJQL {
		if project != "" {
			parts = append(parts, "project = "+project)
		}
		parts = append(parts, "resolution = Unresolved")
	}
	switch assignee {
	case "":
	case "me":
		parts = append(parts, "assignee = currentUser()")
	default:
		parts = append(parts, fmt.Sprintf("assignee = %q", assignee))
	}
	return strings.TrimSpace(strings.Join(parts, " AND ") + order)
}

func (j *Jira) Search(ctx context.Context, jql string) ([]forge.Issue, error) {
	q := url.Values{"jql": {jql}, "maxResults": {"100"}, "fields": {"summary,status,assignee,priority,updated,issuetype"}}
	var res struct{ Issues []rawIssue }
	if err := j.Get(ctx, "/rest/api/2/search?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	out := make([]forge.Issue, 0, len(res.Issues))
	for _, i := range res.Issues {
		out = append(out, j.row(i))
	}
	return out, nil
}

// text flattens a v2 string or a v3 Atlassian document into plain text.
func text(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
		var b strings.Builder
		if c, ok := v["content"].([]any); ok {
			for _, n := range c {
				b.WriteString(text(n))
			}
		}
		if v["type"] == "paragraph" || v["type"] == "heading" {
			b.WriteString("\n")
		}
		return b.String()
	}
	return ""
}

func (j *Jira) transition(ctx context.Context, key, status string) error {
	var res struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := j.Get(ctx, "/rest/api/2/issue/"+key+"/transitions", &res); err != nil {
		return err
	}
	var names []string
	for _, t := range res.Transitions {
		if strings.EqualFold(t.Name, status) || strings.EqualFold(t.To.Name, status) {
			return j.Send(ctx, "POST", "/rest/api/2/issue/"+key+"/transitions", map[string]any{"transition": map[string]string{"id": t.ID}}, nil)
		}
		names = append(names, t.To.Name)
	}
	return fmt.Errorf("%s cannot move to %q; it can go to %s", key, status, strings.Join(names, ", "))
}

// user is the field Jira wants for a person: accountId on Cloud, name on Server; "me" is the caller.
func (j *Jira) user(ctx context.Context, who string) (map[string]string, error) {
	var us []struct {
		AccountID string `json:"accountId"`
		Name      string `json:"name"`
	}
	if who == "me" {
		var u struct {
			AccountID string `json:"accountId"`
			Name      string `json:"name"`
		}
		if err := j.Get(ctx, "/rest/api/2/myself", &u); err != nil {
			return nil, err
		}
		us = append(us, u)
	} else if err := j.Get(ctx, "/rest/api/2/user/search?query="+url.QueryEscape(who)+"&username="+url.QueryEscape(who), &us); err != nil {
		return nil, err
	}
	if len(us) == 0 {
		return nil, fmt.Errorf("no Jira user %q", who)
	}
	if us[0].AccountID != "" {
		return map[string]string{"accountId": us[0].AccountID}, nil
	}
	return map[string]string{"name": us[0].Name}, nil
}

var jqlWords = []string{"project", "status", "assignee", "reporter", "priority", "issuetype", "labels", "sprint", "resolution",
	"created", "updated", "text", "summary", "component", "fixVersion", "epic", "parent", "AND", "OR", "NOT", "IN", "IS", "EMPTY",
	"ORDER BY", "currentUser()", "openSprints()", "startOfDay()", "endOfDay()", "now()", "DESC", "ASC"}

func Register(p *sdk.Plugin) {
	open := func(c *sdk.Ctx) (*Jira, error) { return Open(c) }

	forge.Connect(p, "JIRA", "project", func(c *sdk.Ctx) (string, error) {
		j, err := open(c)
		if err != nil {
			return "", err
		}
		var me struct {
			DisplayName string `json:"displayName"`
		}
		return me.DisplayName, j.Get(c, "/rest/api/2/myself", &me)
	})

	p.Source("tracker.backlog", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		jql := "(sprint is EMPTY OR sprint not in openSprints()) AND statusCategory != Done"
		if j.project != "" {
			jql = "project = " + j.project + " AND " + jql
		}
		rows, err := j.Search(c, jql+" ORDER BY rank")
		return frames.Of(rows), err
	}, sdk.Cache("30s"), sdk.Describe("issues in no open sprint and not done, in rank order"))

	p.Source("tracker.epics", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		jql := "issuetype = Epic"
		if j.project != "" {
			jql = "project = " + j.project + " AND " + jql
		}
		rows, err := j.Search(c, jql+" ORDER BY rank")
		return frames.Of(rows), err
	}, sdk.Cache("5m"))

	p.Action("tracker.sprint-move", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key    string `json:"key" rig:"required"`
		Sprint string `json:"sprint" rig:"required" desc:"a sprint id, or backlog"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		return done(in.Key+" → "+in.Sprint, j.toSprint(c, in.Key, in.Sprint))
	})

	p.Source("tracker.issues", func(c *sdk.Ctx, in struct {
		Query    string `json:"query" desc:"JQL, or words to search"`
		Assignee string `json:"assignee" desc:"a name, or me"`
	}) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		rows, err := j.Search(c, JQL(in.Query, in.Assignee, j.project))
		return frames.Of(rows), err
	}, sdk.Cache("30s"), sdk.Language("jql"), sdk.Describe("issues matching JQL"))

	p.Completer("jql", func(_ *sdk.Ctx, in sdk.CompleteParams) ([]sdk.Completion, error) {
		pos := min(max(in.Pos, 0), len(in.Text))
		start := pos
		for start > 0 && !strings.ContainsRune(" (,=", rune(in.Text[start-1])) {
			start--
		}
		word := strings.ToLower(in.Text[start:pos])
		var out []sdk.Completion
		for _, w := range jqlWords {
			if strings.HasPrefix(strings.ToLower(w), word) {
				out = append(out, sdk.Completion{Label: w, Insert: w})
			}
		}
		return out, nil
	})

	p.Source("tracker.issue", func(c *sdk.Ctx, in struct {
		Key string `json:"key" rig:"required"`
	}) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		var i rawIssue
		if err := j.Get(c, "/rest/api/2/issue/"+url.PathEscape(in.Key)+"?fields=summary,status,assignee,description,comment", &i); err != nil {
			return nil, err
		}
		d := forge.IssueDetail{Key: i.Key, Title: i.Fields.Summary, Status: i.Fields.Status.Name, Description: text(i.Fields.Description), URL: j.Base + "/browse/" + i.Key}
		if i.Fields.Assignee != nil {
			d.Assignee = i.Fields.Assignee.DisplayName
		}
		for _, cm := range i.Fields.Comment.Comments {
			d.Comments = append(d.Comments, forge.Comment{Author: cm.Author.DisplayName, Time: jiraTime(cm.Created), Body: text(cm.Body)})
		}
		return frames.Of([]forge.IssueDetail{*d.Render()}), nil
	})

	p.Action("tracker.transition", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key    string `json:"key" rig:"required"`
		Status string `json:"status" rig:"required" desc:"the target status or transition name"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		return done(in.Key+" → "+in.Status, j.transition(c, in.Key, in.Status))
	})

	p.Action("tracker.comment", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key  string `json:"key" rig:"required"`
		Body string `json:"body" rig:"required"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		return done("commented on "+in.Key, j.Send(c, "POST", "/rest/api/2/issue/"+in.Key+"/comment", map[string]string{"body": in.Body}, nil))
	})

	p.Action("tracker.update", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key         string `json:"key" rig:"required"`
		Summary     string `json:"summary" desc:"new title; empty keeps it"`
		Description string `json:"description" desc:"new description; empty keeps it"`
		Append      string `json:"append" desc:"a line added under the description (a branch, an MR link)"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		set := map[string]any{}
		if in.Summary != "" {
			set["summary"] = in.Summary
		}
		desc := in.Description
		if in.Append != "" {
			if desc == "" {
				var i rawIssue
				if err := j.Get(c, "/rest/api/2/issue/"+url.PathEscape(in.Key)+"?fields=description", &i); err != nil {
					return nil, err
				}
				desc = text(i.Fields.Description)
			}
			if !strings.Contains(desc, in.Append) {
				desc = strings.TrimRight(desc, "\n") + "\n\n" + in.Append
			}
		}
		if desc != "" {
			set["description"] = desc
		}
		if len(set) == 0 {
			return nil, fmt.Errorf("nothing to change: give summary, description or append")
		}
		return done("updated "+in.Key, j.Send(c, "PUT", "/rest/api/2/issue/"+url.PathEscape(in.Key), map[string]any{"fields": set}, nil))
	})

	p.Action("tracker.create", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Summary     string `json:"summary" rig:"required"`
		Description string `json:"description" format:"textarea"`
		Project     string `json:"project" desc:"default plugins.jira.project"`
		Type        string `json:"type" desc:"issue type (default Task)"`
		Assignee    string `json:"assignee" desc:"a name or email, or me"`
		Priority    string `json:"priority" enum:"Highest,High,Medium,Low,Lowest"`
		Labels      string `json:"labels" desc:"comma separated"`
		Epic        string `json:"epic" desc:"the epic's key"`
		Due         string `json:"due" format:"date"`
		Sprint      string `json:"sprint" desc:"a sprint id; empty leaves it in the backlog"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		proj := in.Project
		if proj == "" {
			proj = j.project
		}
		if proj == "" {
			return nil, fmt.Errorf("which project? set project or plugins.jira.project")
		}
		typ := in.Type
		if typ == "" {
			typ = "Task"
		}
		f := map[string]any{"project": map[string]string{"key": proj}, "issuetype": map[string]string{"name": typ},
			"summary": in.Summary, "description": in.Description}
		if in.Priority != "" {
			f["priority"] = map[string]string{"name": in.Priority}
		}
		if ls := strings.FieldsFunc(in.Labels, func(r rune) bool { return r == ',' || r == ' ' }); len(ls) > 0 {
			f["labels"] = ls
		}
		if in.Epic != "" {
			f["parent"] = map[string]string{"key": in.Epic}
		}
		if in.Due != "" {
			f["duedate"] = in.Due
		}
		var res struct{ Key string }
		if err = j.Send(c, "POST", "/rest/api/2/issue", map[string]any{"fields": f}, &res); err != nil {
			return nil, err
		}
		msg := "created " + res.Key
		if in.Sprint != "" {
			if err := j.toSprint(c, res.Key, in.Sprint); err != nil {
				msg += ", not in the sprint: " + err.Error()
			}
		}
		if in.Assignee != "" {
			u, err := j.user(c, in.Assignee)
			if err == nil {
				err = j.Send(c, "PUT", "/rest/api/2/issue/"+res.Key+"/assignee", u, nil)
			}
			if err != nil {
				msg += ", not assigned: " + err.Error()
			} else {
				msg += " for " + in.Assignee
			}
		}
		return &sdk.Result{Message: msg, Outputs: sdk.Args{"key": res.Key, "url": j.Base + "/browse/" + res.Key}}, nil
	})

	p.Action("tracker.assign", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key      string `json:"key" rig:"required"`
		Assignee string `json:"assignee" rig:"required" desc:"a name or email, or me"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		u, err := j.user(c, in.Assignee)
		if err != nil {
			return nil, err
		}
		return done(in.Key+" assigned to "+in.Assignee, j.Send(c, "PUT", "/rest/api/2/issue/"+in.Key+"/assignee", u, nil))
	})

	p.Action("tracker.link", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Key  string `json:"key" rig:"required"`
		To   string `json:"to" rig:"required"`
		Type string `json:"type" desc:"link type (default Relates)"`
	}) (*sdk.Result, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		typ := in.Type
		if typ == "" {
			typ = "Relates"
		}
		return done(in.Key+" "+typ+" "+in.To, j.Send(c, "POST", "/rest/api/2/issueLink", map[string]any{
			"type": map[string]string{"name": typ}, "inwardIssue": map[string]string{"key": in.Key}, "outwardIssue": map[string]string{"key": in.To}}, nil))
	})

	type board struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	p.Source("tracker.boards", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		q := "/rest/agile/1.0/board?maxResults=100"
		if j.project != "" {
			q += "&projectKeyOrId=" + url.QueryEscape(j.project)
		}
		var res struct {
			Values []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"values"`
		}
		if err := j.Get(c, q, &res); err != nil {
			return nil, err
		}
		var rows []board
		for _, b := range res.Values {
			rows = append(rows, board{strconv.Itoa(b.ID), b.Name, b.Type})
		}
		return frames.Of(rows), nil
	}, sdk.Cache("5m"))

	type sprint struct {
		ID    string    `json:"id"`
		Name  string    `json:"name"`
		State string    `json:"state"`
		Start time.Time `json:"start"`
		End   time.Time `json:"end"`
	}
	p.Source("tracker.sprints", func(c *sdk.Ctx, in struct {
		Board string `json:"board" rig:"required"`
	}) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		var res struct {
			Values []struct {
				ID        int       `json:"id"`
				Name      string    `json:"name"`
				State     string    `json:"state"`
				StartDate time.Time `json:"startDate"`
				EndDate   time.Time `json:"endDate"`
			} `json:"values"`
		}
		if err := j.Get(c, "/rest/agile/1.0/board/"+in.Board+"/sprint?state=active,future", &res); err != nil {
			return nil, err
		}
		var rows []sprint
		for _, s := range res.Values {
			rows = append(rows, sprint{strconv.Itoa(s.ID), s.Name, s.State, s.StartDate, s.EndDate})
		}
		return frames.Of(rows), nil
	}, sdk.Cache("5m"))

	p.Source("tracker.board", func(c *sdk.Ctx, in struct {
		Sprint string `json:"sprint" desc:"sprint id (default: the open ones)"`
	}) (*sdk.Frame, error) {
		j, err := open(c)
		if err != nil {
			return nil, err
		}
		jql := "sprint in openSprints()"
		if in.Sprint != "" {
			jql = "sprint = " + in.Sprint
		}
		if j.project != "" {
			jql += " AND project = " + j.project
		}
		q := url.Values{"jql": {jql + " ORDER BY rank"}, "maxResults": {"200"}, "fields": {"summary,status,assignee,priority,updated,issuetype"}}
		var res struct{ Issues []rawIssue }
		if err := j.Get(c, "/rest/api/2/search?"+q.Encode(), &res); err != nil {
			return nil, err
		}
		pos := j.columnOrder(c)
		sort.SliceStable(res.Issues, func(a, b int) bool {
			at := func(i int) int {
				if p, ok := pos[res.Issues[i].Fields.Status.ID]; ok {
					return p
				}
				return len(pos)
			}
			return at(a) < at(b)
		})
		rows := make([]forge.Issue, 0, len(res.Issues))
		for _, i := range res.Issues {
			rows = append(rows, j.row(i))
		}
		return frames.Of(rows), nil
	}, sdk.Cache("30s"), sdk.Describe("the open sprint's issues, for the board widget (by: status)"))
}

// columnOrder is each status id's place in the project's first board, left to right, so the board
// widget lists its columns as Jira does; empty when the board cannot be read.
func (j *Jira) columnOrder(ctx context.Context) map[string]int {
	pos := map[string]int{}
	var bs struct{ Values []struct{ ID int } }
	q := "/rest/agile/1.0/board?maxResults=1"
	if j.project != "" {
		q += "&projectKeyOrId=" + url.QueryEscape(j.project)
	}
	if j.Get(ctx, q, &bs) != nil || len(bs.Values) == 0 {
		return pos
	}
	var cfg struct {
		ColumnConfig struct {
			Columns []struct{ Statuses []struct{ ID string } }
		}
	}
	if j.Get(ctx, "/rest/agile/1.0/board/"+strconv.Itoa(bs.Values[0].ID)+"/configuration", &cfg) != nil {
		return pos
	}
	for n, col := range cfg.ColumnConfig.Columns {
		for _, st := range col.Statuses {
			pos[st.ID] = n
		}
	}
	return pos
}

func (j *Jira) toSprint(ctx context.Context, key, sprint string) error {
	body := map[string][]string{"issues": {key}}
	if strings.EqualFold(sprint, "backlog") {
		return j.Send(ctx, "POST", "/rest/agile/1.0/backlog/issue", body, nil)
	}
	return j.Send(ctx, "POST", "/rest/agile/1.0/sprint/"+url.PathEscape(sprint)+"/issue", body, nil)
}

func done(msg string, err error) (*sdk.Result, error) {
	if err != nil {
		return nil, err
	}
	return &sdk.Result{Message: msg}, nil
}
