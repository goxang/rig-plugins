// Package github is GitHub as scm/1 and tracker/1: pull requests, Actions runs and job logs, releases
// and issues of the repository the origin remote (or plugins.github.project) names, over the REST API.
package github

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

type GitHub struct{ *forge.Client }

// Open reads plugins.github (url, project, token) with GITHUB_TOKEN or GH_TOKEN as the token's
// fallback; a url other than github.com is GitHub Enterprise (its /api/v3).
func Open(c *sdk.Ctx) (forge.SCM, error) {
	host, project, err := forge.Repo(c, "https://github.com")
	if err != nil {
		return nil, err
	}
	api := "https://api.github.com"
	if !strings.HasSuffix(host, "://github.com") {
		if forge.Config(c, "url") == "" {
			return nil, sdk.Unavailable("origin is on "+host+", not GitHub", "set plugins.github.url for GitHub Enterprise")
		}
		api = host + "/api/v3"
	}
	cl := &forge.Client{Host: host, Project: project, Base: api + "/repos/" + project,
		Endpoint: httpx.Endpoint{Headers: map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}}}
	cl.Token = forge.Token(c, "GITHUB_TOKEN", "GH_TOKEN")
	return GitHub{cl}, nil
}

type user struct {
	Login string `json:"login"`
}

func num(s string) string { return strings.TrimLeft(s, "#!") }

func (g GitHub) MR(ctx context.Context, mr string) (string, error) {
	var p struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		User   user   `json:"user"`
		Head   struct{ Ref string }
		Base   struct{ Ref string }
		URL    string `json:"html_url"`
	}
	if err := g.Get(ctx, "/pulls/"+num(mr), &p); err != nil {
		return "", err
	}
	return fmt.Sprintf("# #%d %s\n\n**%s** · %s → %s · %s\n\n%s\n\n%s\n", p.Number, p.Title, p.State, p.Head.Ref, p.Base.Ref, p.User.Login, strings.TrimSpace(p.Body), p.URL), nil
}

func (g GitHub) Rebase(context.Context, string) error {
	return fmt.Errorf("GitHub updates a pull request's branch from its page")
}

func (g GitHub) MRs(ctx context.Context, state, author, query string) ([]forge.MR, error) {
	ghState := map[string]string{"": "open", "opened": "open", "open": "open", "merged": "closed", "closed": "closed", "all": "all"}[state]
	if ghState == "" {
		return nil, fmt.Errorf("state is opened, merged, closed or all")
	}
	var raw []struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		User      user       `json:"user"`
		State     string     `json:"state"`
		MergedAt  *time.Time `json:"merged_at"`
		Head      struct{ Ref string }
		Base      struct{ Ref string }
		UpdatedAt time.Time `json:"updated_at"`
		HTMLURL   string    `json:"html_url"`
	}
	if err := g.Get(ctx, "/pulls?per_page=50&sort=updated&direction=desc&state="+ghState, &raw); err != nil {
		return nil, err
	}
	var out []forge.MR
	for _, p := range raw {
		st := map[string]string{"open": "opened", "closed": "closed"}[p.State]
		if p.MergedAt != nil {
			st = "merged"
		}
		if state == "merged" && st != "merged" || state == "closed" && st != "closed" || author != "" && author != "me" && author != "review" && p.User.Login != author || query != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Head.Ref), strings.ToLower(query)) {
			continue
		}
		out = append(out, forge.MR{ID: strconv.Itoa(p.Number), Title: p.Title, Author: p.User.Login, State: st,
			Source: p.Head.Ref, Target: p.Base.Ref, Updated: p.UpdatedAt, URL: p.HTMLURL})
	}
	return out, nil
}

func (g GitHub) Files(ctx context.Context, mr string) ([]forge.File, error) {
	var raw []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
		Changes  int    `json:"changes"`
		Patch    string `json:"patch"`
	}
	if err := g.Get(ctx, "/pulls/"+num(mr)+"/files?per_page=100", &raw); err != nil {
		return nil, err
	}
	out := make([]forge.File, 0, len(raw))
	for _, f := range raw {
		out = append(out, forge.File{Path: f.Filename, Status: f.Status, Patch: f.Patch, Changed: f.Changes})
	}
	return out, nil
}

func (g GitHub) Approve(ctx context.Context, mr string) error {
	return g.Send(ctx, "POST", "/pulls/"+num(mr)+"/reviews", map[string]string{"event": "APPROVE"}, nil)
}

func (g GitHub) Merge(ctx context.Context, mr string) error {
	return g.Send(ctx, "PUT", "/pulls/"+num(mr)+"/merge", nil, nil)
}

func (g GitHub) Comment(ctx context.Context, mr, body string) error {
	return g.IssueComment(ctx, mr, body)
}

func (g GitHub) Open(ctx context.Context, source, target, title, body string) (*forge.MR, error) {
	if target == "" {
		var r struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.Get(ctx, "", &r); err != nil {
			return nil, err
		}
		target = r.DefaultBranch
	}
	var pr struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.Send(ctx, "POST", "/pulls", map[string]string{"head": source, "base": target, "title": title, "body": body}, &pr); err != nil {
		return nil, err
	}
	return &forge.MR{ID: strconv.Itoa(pr.Number), Title: title, Source: source, Target: target, URL: pr.HTMLURL}, nil
}

// status speaks GitLab's words, so one pipelines view reads the same for both forges.
func status(status, conclusion string) string {
	if status == "completed" && conclusion != "" {
		status = conclusion
	}
	if s, ok := map[string]string{"failure": "failed", "cancelled": "canceled", "in_progress": "running", "queued": "pending", "timed_out": "failed"}[status]; ok {
		return s
	}
	return status
}

func (g GitHub) Pipelines(ctx context.Context, ref string, limit int) ([]forge.Pipeline, error) {
	q := url.Values{"per_page": {strconv.Itoa(limit)}}
	if ref != "" {
		q.Set("branch", ref)
	}
	var raw struct {
		WorkflowRuns []struct {
			ID           int64     `json:"id"`
			Name         string    `json:"name"`
			HeadBranch   string    `json:"head_branch"`
			Status       string    `json:"status"`
			Conclusion   string    `json:"conclusion"`
			RunStartedAt time.Time `json:"run_started_at"`
			UpdatedAt    time.Time `json:"updated_at"`
			HTMLURL      string    `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if err := g.Get(ctx, "/actions/runs?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Pipeline, 0, len(raw.WorkflowRuns))
	for _, r := range raw.WorkflowRuns {
		out = append(out, forge.Pipeline{ID: strconv.FormatInt(r.ID, 10), Ref: r.HeadBranch, Status: status(r.Status, r.Conclusion),
			Started: r.RunStartedAt, Duration: r.UpdatedAt.Sub(r.RunStartedAt).Round(time.Second), URL: r.HTMLURL})
	}
	return out, nil
}

type job struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	WorkflowName string    `json:"workflow_name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at"`
}

func (g GitHub) Jobs(ctx context.Context, pipeline string) ([]forge.Job, error) {
	var raw struct{ Jobs []job }
	if err := g.Get(ctx, "/actions/runs/"+pipeline+"/jobs?per_page=100", &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Job, 0, len(raw.Jobs))
	for _, j := range raw.Jobs {
		var d time.Duration
		if !j.CompletedAt.IsZero() {
			d = j.CompletedAt.Sub(j.StartedAt)
		}
		out = append(out, forge.Job{ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Stage: j.WorkflowName, Status: status(j.Status, j.Conclusion), Duration: d})
	}
	return out, nil
}

// Log reads a job's log; GitHub serves it only once the job has finished.
func (g GitHub) Log(ctx context.Context, id string) ([]string, bool, error) {
	var j job
	if err := g.Get(ctx, "/actions/jobs/"+id, &j); err != nil {
		return nil, false, err
	}
	if j.Status != "completed" {
		return []string{"(job " + j.Status + ": GitHub serves the log when it finishes)"}, false, nil
	}
	ls, err := g.Text(ctx, "/actions/jobs/"+id+"/logs")
	return ls, true, err
}

func (g GitHub) Retry(ctx context.Context, id string) error {
	return g.Send(ctx, "POST", "/actions/jobs/"+id+"/rerun", nil, nil)
}

func (g GitHub) Releases(ctx context.Context, query string) ([]forge.Release, error) {
	var raw []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		PublishedAt time.Time `json:"published_at"`
		HTMLURL     string    `json:"html_url"`
	}
	if err := g.Get(ctx, "/releases?per_page=100", &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Release, 0, len(raw))
	for _, r := range raw {
		if query != "" && !strings.Contains(strings.ToLower(r.TagName+" "+r.Name), strings.ToLower(query)) {
			continue
		}
		out = append(out, forge.Release{Tag: r.TagName, Name: r.Name, Released: r.PublishedAt, URL: r.HTMLURL})
	}
	return out, nil
}

type issue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Body      string `json:"body"`
	Assignee  *user  `json:"assignee"`
	Labels    []struct{ Name string }
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	PR        any       `json:"pull_request"`
}

func (g GitHub) Issues(ctx context.Context, query, assignee string) ([]forge.Issue, error) {
	var raw []issue
	if query != "" {
		// search finds text; the issues list cannot
		q := "repo:" + g.Project + " is:issue is:open " + query
		if assignee != "" {
			q += " assignee:" + assignee
		}
		var res struct{ Items []issue }
		base := strings.TrimSuffix(g.Base, "/repos/"+g.Project)
		if err := g.DoURL(ctx, "GET", base+"/search/issues?per_page=50&q="+url.QueryEscape(q), nil, &res); err != nil {
			return nil, err
		}
		raw = res.Items
	} else {
		q := url.Values{"per_page": {"50"}, "state": {"open"}, "sort": {"updated"}}
		if assignee != "" {
			q.Set("assignee", assignee)
		}
		if err := g.Get(ctx, "/issues?"+q.Encode(), &raw); err != nil {
			return nil, err
		}
	}
	var out []forge.Issue
	for _, i := range raw {
		if i.PR != nil {
			continue
		}
		is := forge.Issue{Key: "#" + strconv.Itoa(i.Number), Title: i.Title, Status: i.State, Updated: i.UpdatedAt, URL: i.HTMLURL}
		if i.Assignee != nil {
			is.Assignee = i.Assignee.Login
		}
		for _, l := range i.Labels {
			if p, ok := strings.CutPrefix(strings.ToLower(l.Name), "priority:"); ok {
				is.Priority = strings.TrimSpace(p)
			}
		}
		out = append(out, is)
	}
	return out, nil
}

func (g GitHub) Issue(ctx context.Context, key string) (*forge.IssueDetail, error) {
	var i issue
	if err := g.Get(ctx, "/issues/"+num(key), &i); err != nil {
		return nil, err
	}
	var comments []struct {
		User      user      `json:"user"`
		CreatedAt time.Time `json:"created_at"`
		Body      string    `json:"body"`
	}
	if err := g.Get(ctx, "/issues/"+num(key)+"/comments?per_page=100", &comments); err != nil {
		return nil, err
	}
	d := &forge.IssueDetail{Key: "#" + strconv.Itoa(i.Number), Title: i.Title, Status: i.State, Description: i.Body}
	for _, c := range comments {
		d.Comments = append(d.Comments, forge.Comment{Author: c.User.Login, Time: c.CreatedAt, Body: c.Body})
	}
	return d, nil
}

func (g GitHub) Transition(ctx context.Context, key, status string) error {
	st := map[string]string{"closed": "closed", "close": "closed", "open": "open", "opened": "open", "reopen": "open"}[status]
	if st == "" {
		return fmt.Errorf("a GitHub issue is closed or open, not %q", status)
	}
	return g.Send(ctx, "PATCH", "/issues/"+num(key), map[string]string{"state": st}, nil)
}

func (g GitHub) IssueComment(ctx context.Context, key, body string) error {
	return g.Send(ctx, "POST", "/issues/"+num(key)+"/comments", map[string]string{"body": body}, nil)
}
