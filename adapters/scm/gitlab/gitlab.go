// Package gitlab is GitLab as scm/1 and tracker/1: merge requests, pipelines, job logs, releases and
// issues of the project the origin remote (or plugins.gitlab.project) names, over the REST API v4.
package gitlab

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

type GitLab struct{ *forge.Client }

// Open reads plugins.gitlab (url, project, token) with GITLAB_TOKEN as the token's fallback.
func Open(c *sdk.Ctx) (forge.SCM, error) {
	host, project, err := forge.Repo(c, "https://gitlab.com")
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(host, "://github.com") && forge.Config(c, "url") == "" {
		return nil, sdk.Unavailable("origin is on GitHub, not GitLab", "set plugins.gitlab.url")
	}
	cl := &forge.Client{Host: host, Project: project, Base: host + "/api/v4/projects/" + url.PathEscape(project)}
	if t := forge.Token(c, "GITLAB_TOKEN"); t != "" {
		cl.Endpoint = httpx.Endpoint{Headers: map[string]string{"PRIVATE-TOKEN": t}}
	}
	return GitLab{cl}, nil
}

type user struct {
	Username string `json:"username"`
}

func iid(s string) string { return strings.TrimLeft(s, "!#") }

func (g GitLab) MRs(ctx context.Context, state, author, query string) ([]forge.MR, error) {
	q := url.Values{"per_page": {"50"}, "order_by": {"updated_at"}}
	if state == "" {
		state = "opened"
	}
	q.Set("state", state)
	switch author {
	case "":
	case "me":
		q.Set("scope", "created_by_me")
	case "review":
		var u user
		if err := g.DoURL(ctx, "GET", g.Host+"/api/v4/user", nil, &u); err != nil {
			return nil, err
		}
		q.Set("reviewer_username", u.Username)
	default:
		q.Set("author_username", author)
	}
	if query != "" {
		q.Set("search", query)
	}
	var raw []struct {
		IID          int       `json:"iid"`
		Title        string    `json:"title"`
		Author       user      `json:"author"`
		State        string    `json:"state"`
		SourceBranch string    `json:"source_branch"`
		TargetBranch string    `json:"target_branch"`
		UpdatedAt    time.Time `json:"updated_at"`
		WebURL       string    `json:"web_url"`
		Draft        bool      `json:"draft"`
		Conflicts    bool      `json:"has_conflicts"`
		Detailed     string    `json:"detailed_merge_status"`
		Notes        int       `json:"user_notes_count"`
	}
	if err := g.Get(ctx, "/merge_requests?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]forge.MR, 0, len(raw))
	for _, m := range raw {
		merge := strings.ReplaceAll(m.Detailed, "_", " ")
		switch {
		case m.State == "merged" || m.State == "closed":
			merge = ""
		case m.Draft:
			merge = "draft"
		case m.Conflicts:
			merge = "conflicts"
		}
		out = append(out, forge.MR{ID: strconv.Itoa(m.IID), Title: m.Title, Author: m.Author.Username, State: m.State, Merge: merge, Notes: m.Notes,
			Source: m.SourceBranch, Target: m.TargetBranch, Updated: m.UpdatedAt, URL: m.WebURL})
	}
	return out, nil
}

func (g GitLab) MR(ctx context.Context, mr string) (string, error) {
	var m struct {
		IID         int       `json:"iid"`
		Title       string    `json:"title"`
		Description string    `json:"description"`
		State       string    `json:"state"`
		Author      user      `json:"author"`
		Source      string    `json:"source_branch"`
		Target      string    `json:"target_branch"`
		WebURL      string    `json:"web_url"`
		Detailed    string    `json:"detailed_merge_status"`
		Created     time.Time `json:"created_at"`
		Reviewers   []user    `json:"reviewers"`
		Labels      []string  `json:"labels"`
		Pipeline    *struct {
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		} `json:"head_pipeline"`
	}
	if err := g.Get(ctx, "/merge_requests/"+iid(mr), &m); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# !%d %s\n\n**%s** · %s → %s · %s · %s\n\n", m.IID, m.Title, m.State, m.Source, m.Target, m.Author.Username, m.Created.Format("2006-01-02"))
	fmt.Fprintf(&b, "merge: %s", strings.ReplaceAll(m.Detailed, "_", " "))
	if m.Pipeline != nil {
		fmt.Fprintf(&b, " · pipeline: %s", m.Pipeline.Status)
	}
	if len(m.Reviewers) > 0 {
		var rs []string
		for _, r := range m.Reviewers {
			rs = append(rs, r.Username)
		}
		fmt.Fprintf(&b, " · reviewers: %s", strings.Join(rs, ", "))
	}
	if len(m.Labels) > 0 {
		fmt.Fprintf(&b, " · labels: %s", strings.Join(m.Labels, ", "))
	}
	fmt.Fprintf(&b, "\n\n%s\n\n%s\n", strings.TrimSpace(m.Description), m.WebURL)
	var ap struct {
		ApprovedBy []struct{ User user } `json:"approved_by"`
		Left       int                   `json:"approvals_left"`
	}
	if g.Get(ctx, "/merge_requests/"+iid(mr)+"/approvals", &ap) == nil {
		var who []string
		for _, a := range ap.ApprovedBy {
			who = append(who, a.User.Username)
		}
		fmt.Fprintf(&b, "\n## Approvals\n\n%s, %d more needed\n", cmp.Or(strings.Join(who, ", "), "none yet"), ap.Left)
	}
	var ds []struct {
		Notes []struct {
			Author     user      `json:"author"`
			Body       string    `json:"body"`
			Created    time.Time `json:"created_at"`
			System     bool      `json:"system"`
			Resolvable bool      `json:"resolvable"`
			Resolved   bool      `json:"resolved"`
			Position   *struct {
				NewPath string `json:"new_path"`
				NewLine int    `json:"new_line"`
			} `json:"position"`
		} `json:"notes"`
	}
	if g.Get(ctx, "/merge_requests/"+iid(mr)+"/discussions?per_page=100", &ds) == nil {
		var open, other strings.Builder
		for _, d := range ds {
			if len(d.Notes) == 0 || d.Notes[0].System {
				continue
			}
			n := d.Notes[0]
			dst := &other
			if n.Resolvable && !n.Resolved {
				dst = &open
			}
			where := ""
			if n.Position != nil {
				where = fmt.Sprintf(" `%s:%d`", n.Position.NewPath, n.Position.NewLine)
			}
			fmt.Fprintf(dst, "\n**%s**%s %s\n\n%s\n", n.Author.Username, where, n.Created.Format("01-02 15:04"), strings.TrimSpace(n.Body))
			for _, r := range d.Notes[1:] {
				fmt.Fprintf(dst, "\n> **%s** %s\n", r.Author.Username, strings.ReplaceAll(strings.TrimSpace(r.Body), "\n", "\n> "))
			}
		}
		if open.Len() > 0 {
			b.WriteString("\n## Open threads\n" + open.String())
		}
		if other.Len() > 0 {
			b.WriteString("\n## Comments\n" + other.String())
		}
	}
	if files, err := g.Files(ctx, mr); err == nil && len(files) > 0 {
		fmt.Fprintf(&b, "\n## Files (%d)\n\n", len(files))
		for _, f := range files {
			fmt.Fprintf(&b, "- %s `%s` (%d lines)\n", f.Status, f.Path, f.Changed)
		}
	}
	return b.String(), nil
}

func (g GitLab) Rebase(ctx context.Context, mr string) error {
	return g.Send(ctx, "PUT", "/merge_requests/"+iid(mr)+"/rebase", nil, nil)
}

func (g GitLab) Files(ctx context.Context, mr string) ([]forge.File, error) {
	var raw []struct {
		OldPath     string `json:"old_path"`
		NewPath     string `json:"new_path"`
		NewFile     bool   `json:"new_file"`
		DeletedFile bool   `json:"deleted_file"`
		RenamedFile bool   `json:"renamed_file"`
		Diff        string `json:"diff"`
	}
	if err := g.Get(ctx, "/merge_requests/"+iid(mr)+"/diffs?per_page=100", &raw); err != nil {
		return nil, err
	}
	out := make([]forge.File, 0, len(raw))
	for _, f := range raw {
		st := "modified"
		switch {
		case f.NewFile:
			st = "added"
		case f.DeletedFile:
			st = "deleted"
		case f.RenamedFile:
			st = "renamed"
		}
		out = append(out, forge.File{Path: f.NewPath, Status: st, Patch: f.Diff, Changed: changed(f.Diff)})
	}
	return out, nil
}

// changed counts a patch's added and removed lines.
func changed(patch string) int {
	n := 0
	for _, l := range strings.Split(patch, "\n") {
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
			n++
		}
	}
	return n
}

func (g GitLab) Approve(ctx context.Context, mr string) error {
	return g.Send(ctx, "POST", "/merge_requests/"+iid(mr)+"/approve", nil, nil)
}

func (g GitLab) Merge(ctx context.Context, mr string) error {
	return g.Send(ctx, "PUT", "/merge_requests/"+iid(mr)+"/merge", nil, nil)
}

func (g GitLab) Comment(ctx context.Context, mr, body string) error {
	return g.Send(ctx, "POST", "/merge_requests/"+iid(mr)+"/notes", map[string]string{"body": body}, nil)
}

func (g GitLab) Open(ctx context.Context, source, target, title, body string) (*forge.MR, error) {
	if target == "" {
		var p struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := g.Get(ctx, "", &p); err != nil {
			return nil, err
		}
		target = p.DefaultBranch
	}
	var m struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	err := g.Send(ctx, "POST", "/merge_requests", map[string]any{"source_branch": source, "target_branch": target,
		"title": title, "description": body, "remove_source_branch": true}, &m)
	if err != nil {
		return nil, err
	}
	return &forge.MR{ID: strconv.Itoa(m.IID), Title: title, Source: source, Target: target, URL: m.WebURL}, nil
}

func (g GitLab) Pipelines(ctx context.Context, ref string, limit int) ([]forge.Pipeline, error) {
	q := url.Values{"per_page": {strconv.Itoa(limit)}}
	if ref != "" {
		q.Set("ref", ref)
	}
	var raw []struct {
		ID        int       `json:"id"`
		Ref       string    `json:"ref"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		WebURL    string    `json:"web_url"`
	}
	if err := g.Get(ctx, "/pipelines?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Pipeline, 0, len(raw))
	for _, p := range raw {
		// the list has no duration; created to last update is close enough for a finished pipeline
		out = append(out, forge.Pipeline{ID: strconv.Itoa(p.ID), Ref: p.Ref, Status: p.Status, Started: p.CreatedAt,
			Duration: p.UpdatedAt.Sub(p.CreatedAt).Round(time.Second), URL: p.WebURL})
	}
	return out, nil
}

func (g GitLab) Jobs(ctx context.Context, pipeline string) ([]forge.Job, error) {
	var raw []struct {
		ID       int     `json:"id"`
		Name     string  `json:"name"`
		Stage    string  `json:"stage"`
		Status   string  `json:"status"`
		Duration float64 `json:"duration"`
	}
	if err := g.Get(ctx, "/pipelines/"+pipeline+"/jobs?per_page=100&include_retried=false", &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Job, 0, len(raw))
	for _, j := range raw {
		out = append(out, forge.Job{ID: strconv.Itoa(j.ID), Name: j.Name, Stage: j.Stage, Status: j.Status, Duration: time.Duration(j.Duration * float64(time.Second))})
	}
	return out, nil
}

func (g GitLab) Log(ctx context.Context, job string) ([]string, bool, error) {
	var j struct {
		Status string `json:"status"`
	}
	if err := g.Get(ctx, "/jobs/"+job, &j); err != nil {
		return nil, false, err
	}
	ls, err := g.Text(ctx, "/jobs/"+job+"/trace")
	done := j.Status != "running" && j.Status != "pending" && j.Status != "created"
	return ls, done, err
}

func (g GitLab) Retry(ctx context.Context, job string) error {
	return g.Send(ctx, "POST", "/jobs/"+job+"/retry", nil, nil)
}

func (g GitLab) Releases(ctx context.Context, query string) ([]forge.Release, error) {
	tags, err := g.tags(ctx, query)
	if err != nil || query != "" {
		return tags, err
	}
	var raw []struct {
		TagName    string    `json:"tag_name"`
		Name       string    `json:"name"`
		ReleasedAt time.Time `json:"released_at"`
		Links      struct {
			Self string `json:"self"`
		} `json:"_links"`
	}
	if err := g.Get(ctx, "/releases?per_page=50&order_by=released_at&sort=desc", &raw); err != nil {
		return tags, nil
	}
	have := map[string]bool{}
	for _, t := range tags {
		have[t.Tag] = true
	}
	for _, r := range raw {
		if !have[r.TagName] {
			tags = append(tags, forge.Release{Tag: r.TagName, Name: r.Name, Released: r.ReleasedAt, URL: r.Links.Self})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].Released.After(tags[j].Released) })
	return tags, nil
}

// tags searches every tag by name (a project's releases are its tags, and the releases API cannot search).
func (g GitLab) tags(ctx context.Context, query string) ([]forge.Release, error) {
	q := url.Values{"per_page": {"100"}, "order_by": {"updated"}, "sort": {"desc"}}
	if query != "" {
		q.Set("search", query)
	}
	var raw []struct {
		Name   string `json:"name"`
		Commit struct {
			CreatedAt time.Time `json:"created_at"`
		} `json:"commit"`
	}
	if err := g.Get(ctx, "/repository/tags?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Release, 0, len(raw))
	for _, t := range raw {
		out = append(out, forge.Release{Tag: t.Name, Name: t.Name, Released: t.Commit.CreatedAt, URL: g.Host + "/" + g.Project + "/-/tags/" + url.PathEscape(t.Name)})
	}
	return out, nil
}

type issue struct {
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	Assignee    *user     `json:"assignee"`
	Labels      []string  `json:"labels"`
	UpdatedAt   time.Time `json:"updated_at"`
	WebURL      string    `json:"web_url"`
}

func (g GitLab) Issues(ctx context.Context, query, assignee string) ([]forge.Issue, error) {
	q := url.Values{"per_page": {"50"}, "state": {"opened"}, "order_by": {"updated_at"}}
	if query != "" {
		q.Set("search", query)
	}
	if assignee != "" {
		q.Set("assignee_username", assignee)
	}
	var raw []issue
	if err := g.Get(ctx, "/issues?"+q.Encode(), &raw); err != nil {
		return nil, err
	}
	out := make([]forge.Issue, 0, len(raw))
	for _, i := range raw {
		is := forge.Issue{Key: "#" + strconv.Itoa(i.IID), Title: i.Title, Status: i.State, Updated: i.UpdatedAt, URL: i.WebURL}
		if i.Assignee != nil {
			is.Assignee = i.Assignee.Username
		}
		for _, l := range i.Labels {
			if p, ok := strings.CutPrefix(strings.ToLower(l), "priority::"); ok {
				is.Priority = p
			}
		}
		out = append(out, is)
	}
	return out, nil
}

func (g GitLab) Issue(ctx context.Context, key string) (*forge.IssueDetail, error) {
	var i issue
	if err := g.Get(ctx, "/issues/"+iid(key), &i); err != nil {
		return nil, err
	}
	var notes []struct {
		Author    user      `json:"author"`
		CreatedAt time.Time `json:"created_at"`
		Body      string    `json:"body"`
		System    bool      `json:"system"`
	}
	if err := g.Get(ctx, "/issues/"+iid(key)+"/notes?sort=asc&per_page=100", &notes); err != nil {
		return nil, err
	}
	d := &forge.IssueDetail{Key: "#" + strconv.Itoa(i.IID), Title: i.Title, Status: i.State, Description: i.Description}
	for _, n := range notes {
		if !n.System {
			d.Comments = append(d.Comments, forge.Comment{Author: n.Author.Username, Time: n.CreatedAt, Body: n.Body})
		}
	}
	return d, nil
}

func (g GitLab) Transition(ctx context.Context, key, status string) error {
	ev := map[string]string{"closed": "close", "close": "close", "opened": "reopen", "open": "reopen", "reopen": "reopen"}[status]
	if ev == "" {
		return fmt.Errorf("a GitLab issue is closed or open, not %q", status)
	}
	return g.Send(ctx, "PUT", "/issues/"+iid(key), map[string]string{"state_event": ev}, nil)
}

func (g GitLab) IssueComment(ctx context.Context, key, body string) error {
	return g.Send(ctx, "POST", "/issues/"+iid(key)+"/notes", map[string]string{"body": body}, nil)
}
