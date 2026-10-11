// Package git is the vcs/1 plugin for the project's repository: status, log graph, branches, diff,
// blame, stashes and worktrees; commit, checkout, pull, push and the export of a ref.
package git

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/sh"
	"github.com/goxang/rig/sdk"
)

// Dir is the repository commands run in: the project rig opened the plugin for.
func Dir() string {
	if d := os.Getenv("RIG_PROJECT"); d != "" {
		return d
	}
	return "."
}

// Git runs git in the project and returns its trimmed output.
func Git(ctx context.Context, args ...string) (string, error) {
	c := sh.New("git", args...)
	c.Dir = Dir()
	out, err := c.Output(ctx)
	return strings.TrimRight(string(out), "\n"), err
}

// Remote is the forge host and project path of a remote's URL (ssh or https): gitlab.com, group/app.
func Remote(ctx context.Context, name string) (host, path string, err error) {
	u, err := Git(ctx, "remote", "get-url", name)
	if err != nil {
		return "", "", err
	}
	return ParseRemote(u)
}

func ParseRemote(u string) (host, path string, err error) {
	u = strings.TrimSuffix(strings.TrimSpace(u), ".git")
	if !strings.Contains(u, "://") {
		// scp-like: git@host:group/app
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		h, p, ok := strings.Cut(u, ":")
		if !ok {
			return "", "", fmt.Errorf("cannot read remote %q", u)
		}
		return h, p, nil
	}
	pu, err := url.Parse(u)
	if err != nil {
		return "", "", err
	}
	return pu.Hostname(), strings.TrimPrefix(pu.Path, "/"), nil
}

type change struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

type commit struct {
	Commit  string    `json:"commit"`
	Author  string    `json:"author"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject"`
	Refs    string    `json:"refs"`
	Graph   string    `json:"graph"`
}

type branch struct {
	Name     string    `json:"name"`
	Current  bool      `json:"current"`
	Upstream string    `json:"upstream"`
	Track    string    `json:"track"`
	Updated  time.Time `json:"updated"`
}

type blame struct {
	N      int       `json:"n"`
	Commit string    `json:"commit"`
	Author string    `json:"author"`
	Time   time.Time `json:"time"`
	Text   string    `json:"text"`
}

type stash struct {
	Ref     string `json:"ref"`
	Subject string `json:"subject"`
}

type worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

var states = map[byte]string{'M': "modified", 'A': "added", 'D': "deleted", 'R': "renamed", 'C': "copied", 'U': "conflict", '?': "untracked", 'T': "type changed"}

// Status parses `git status --porcelain=v1`: a path's state, staged or not.
func Status(out string) []change {
	var cs []change
	for _, l := range strings.Split(out, "\n") {
		if len(l) < 4 {
			continue
		}
		x, y, path := l[0], l[1], l[3:]
		if _, to, ok := strings.Cut(path, " -> "); ok {
			path = to
		}
		state := states[y]
		if x != ' ' && x != '?' {
			state = "staged " + states[x]
			if y != ' ' {
				state += ", " + states[y]
			}
		}
		cs = append(cs, change{Path: strings.Trim(path, `"`), State: state})
	}
	return cs
}

// matching keeps the changes whose path has q or whose file content does (one git grep over them all).
func matching(c *sdk.Ctx, cs []change, q string) []change {
	byName := map[string]bool{}
	var paths []string
	for _, ch := range cs {
		if strings.Contains(strings.ToLower(ch.Path), strings.ToLower(q)) {
			byName[ch.Path] = true
		}
		paths = append(paths, ch.Path)
	}
	args := append([]string{"grep", "-I", "-l", "-i", "-F", "--untracked", "-e", q, "--"}, paths...)
	out, _ := Git(c, args...)
	for _, l := range strings.Split(out, "\n") {
		byName[l] = true
	}
	var keep []change
	for _, ch := range cs {
		if byName[ch.Path] {
			keep = append(keep, ch)
		}
	}
	return keep
}

const logFormat = "%x1f%H%x1f%an%x1f%at%x1f%s%x1f%D"

// Log parses `git log --graph --format=logFormat`: graph lines with no commit keep only their graph.
func Log(out string) []commit {
	var cs []commit
	for _, l := range strings.Split(out, "\n") {
		parts := strings.Split(l, "\x1f")
		c := commit{Graph: strings.TrimRight(parts[0], " ")}
		if len(parts) >= 6 {
			sec, _ := strconv.ParseInt(parts[3], 10, 64)
			c.Commit, c.Author, c.Time, c.Subject, c.Refs = parts[1][:min(12, len(parts[1]))], parts[2], time.Unix(sec, 0), parts[4], parts[5]
		}
		if c.Graph != "" || c.Commit != "" {
			cs = append(cs, c)
		}
	}
	return cs
}

// Blame parses `git blame --line-porcelain`.
func Blame(out string) []blame {
	var bs []blame
	var cur blame
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "\t"):
			cur.Text = l[1:]
			cur.N = len(bs) + 1
			bs = append(bs, cur)
		case strings.HasPrefix(l, "author "):
			cur.Author = l[7:]
		case strings.HasPrefix(l, "author-time "):
			sec, _ := strconv.ParseInt(l[12:], 10, 64)
			cur.Time = time.Unix(sec, 0)
		default:
			if f := strings.Fields(l); len(f) >= 3 && len(f[0]) == 40 {
				cur.Commit = f[0][:12]
			}
		}
	}
	return bs
}

// Export checks a ref out into a cached directory and returns it; the core engine does it.
type Export func(ctx context.Context, ref string) (string, error)

func Register(p *sdk.Plugin, export Export) {
	p.Source("vcs.status", func(c *sdk.Ctx, in struct {
		Query string `json:"query" desc:"keep the files whose path or content has this text"`
	}) (*sdk.Frame, error) {
		out, err := Git(c, "status", "--porcelain=v1", "--untracked-files=all")
		cs := Status(out)
		if in.Query != "" && err == nil {
			cs = matching(c, cs, in.Query)
		}
		return frames.Of(cs), err
	}, sdk.Describe("changed files: path and state; query keeps those with the text in their path or content"))

	p.Source("vcs.log", func(c *sdk.Ctx, in struct {
		Limit int    `json:"limit" desc:"commits (default 100)"`
		Path  string `json:"path" desc:"only commits touching this path"`
		Ref   string `json:"ref" desc:"branch or ref (default HEAD; all for every branch)"`
		Graph bool   `json:"graph" desc:"draw the branch graph"`
	}) (*sdk.Frame, error) {
		args := []string{"log", "--format=" + logFormat, "-n", strconv.Itoa(cmp.Or(in.Limit, 100))}
		if in.Graph {
			args = append(args, "--graph")
		}
		switch in.Ref {
		case "":
		case "all":
			args = append(args, "--all")
		default:
			args = append(args, in.Ref)
		}
		if in.Path != "" {
			args = append(args, "--", in.Path)
		}
		out, err := Git(c, args...)
		return frames.Of(Log(out)), err
	}, sdk.Describe("commits, newest first, with the graph on request"))

	p.Source("vcs.branches", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		out, err := Git(c, "for-each-ref", "--sort=-committerdate", "--format=%(HEAD)%1f%(refname:short)%1f%(upstream:short)%1f%(upstream:track)%1f%(committerdate:unix)", "refs/heads", "refs/remotes")
		var bs []branch
		for _, l := range strings.Split(out, "\n") {
			f := strings.Split(l, "\x1f")
			if len(f) < 5 || strings.HasSuffix(f[1], "/HEAD") {
				continue
			}
			sec, _ := strconv.ParseInt(f[4], 10, 64)
			bs = append(bs, branch{Name: f[1], Current: f[0] == "*", Upstream: f[2], Track: f[3], Updated: time.Unix(sec, 0)})
		}
		return frames.Of(bs), err
	}, sdk.Describe("local and remote branches, most recent first"))

	p.Source("vcs.diff", func(c *sdk.Ctx, in struct {
		Path   string `json:"path"`
		Ref    string `json:"ref" desc:"compare with this ref (default: the index); a..b for a range"`
		Staged bool   `json:"staged"`
	}) (*sdk.Frame, error) {
		args := []string{"diff", "--no-color"}
		if in.Staged {
			args = append(args, "--cached")
		}
		if in.Ref != "" {
			args = append(args, in.Ref)
		}
		if in.Path != "" {
			args = append(args, "--", in.Path)
		}
		out, err := Git(c, args...)
		if out == "" && err == nil && in.Path != "" && (in.Ref == "" || in.Ref == "HEAD") && !in.Staged {
			if u, _ := Git(c, "ls-files", "--others", "--exclude-standard", "--", in.Path); u != "" {
				// --no-index exits 1 when the files differ, which a new file always does
				out, _ = Git(c, "diff", "--no-color", "--no-index", "--", os.DevNull, in.Path)
			}
		}
		return frames.Text(out), err
	}, sdk.Describe("a unified diff, a row per line; an untracked path shows as all added"))

	p.Source("vcs.blame", func(c *sdk.Ctx, in struct {
		Path string `json:"path" rig:"required"`
		Ref  string `json:"ref"`
	}) (*sdk.Frame, error) {
		args := []string{"blame", "--line-porcelain"}
		if in.Ref != "" {
			args = append(args, in.Ref)
		}
		out, err := Git(c, append(args, "--", in.Path)...)
		return frames.Of(Blame(out)), err
	}, sdk.Describe("who last changed each line of a file"))

	p.Source("vcs.stashes", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		out, err := Git(c, "stash", "list", "--format=%gd%x1f%s")
		var ss []stash
		for _, l := range strings.Split(out, "\n") {
			if r, s, ok := strings.Cut(l, "\x1f"); ok {
				ss = append(ss, stash{r, s})
			}
		}
		return frames.Of(ss), err
	})

	p.Source("vcs.worktrees", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		out, err := Git(c, "worktree", "list", "--porcelain")
		var ws []worktree
		for _, block := range strings.Split(out, "\n\n") {
			var w worktree
			for _, l := range strings.Split(block, "\n") {
				k, v, _ := strings.Cut(l, " ")
				switch k {
				case "worktree":
					w.Path = v
				case "HEAD":
					w.Commit = v[:min(12, len(v))]
				case "branch":
					w.Branch = strings.TrimPrefix(v, "refs/heads/")
				}
			}
			if w.Path != "" {
				ws = append(ws, w)
			}
		}
		return frames.Of(ws), err
	})

	p.Action("vcs.checkout", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Branch string `json:"branch" rig:"required"`
		Create bool   `json:"create" desc:"create the branch"`
	}) (*sdk.Result, error) {
		if in.Create {
			return run(c, "switch", "-c", in.Branch)
		}
		if _, err := Git(c, "rev-parse", "--verify", "-q", "refs/tags/"+in.Branch); err == nil {
			return run(c, "switch", "--detach", "refs/tags/"+in.Branch)
		}
		_, err := Git(c, "switch", in.Branch)
		if err != nil && !strings.Contains(err.Error(), "overwritten") {
			// not local yet: a remote branch or a release tag
			Git(c, "fetch", "--tags", "--quiet", "origin")
			if _, err = Git(c, "switch", in.Branch); err != nil && !strings.Contains(err.Error(), "overwritten") {
				return run(c, "switch", "--detach", in.Branch)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("uncommitted changes would be overwritten: commit or stash them first (Changes tab)")
		}
		return &sdk.Result{Message: "on " + in.Branch}, nil
	}, sdk.Describe("switch to a branch, a remote branch or a release tag (detached)"))

	p.Action("vcs.commit", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Message string   `json:"message" rig:"required"`
		All     bool     `json:"all" desc:"stage every tracked change first (the default when nothing is staged)"`
		Paths   []string `json:"paths" desc:"stage these first"`
	}) (*sdk.Result, error) {
		if len(in.Paths) > 0 {
			if _, err := Git(c, append([]string{"add", "--"}, in.Paths...)...); err != nil {
				return nil, err
			}
		}
		if !in.All && len(in.Paths) == 0 {
			staged, _ := Git(c, "diff", "--cached", "--name-only")
			in.All = staged == ""
		}
		args := []string{"commit", "-m", in.Message}
		if in.All {
			args = append(args, "--all")
		}
		return run(c, args...)
	})

	p.Action("vcs.stage", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Path    string `json:"path" desc:"default: every change"`
		Unstage bool   `json:"unstage"`
	}) (*sdk.Result, error) {
		switch {
		case in.Unstage && in.Path == "":
			return run(c, "restore", "--staged", ".")
		case in.Unstage:
			return run(c, "restore", "--staged", "--", in.Path)
		case in.Path == "":
			return run(c, "add", "--all")
		}
		return run(c, "add", "--", in.Path)
	}, sdk.Describe("stage a path for the next commit, or every change; unstage takes it back out"))

	p.Action("vcs.discard", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Path string `json:"path" rig:"required"`
	}) (*sdk.Result, error) {
		if u, _ := Git(c, "ls-files", "--others", "--exclude-standard", "--", in.Path); u != "" {
			return run(c, "clean", "-f", "--", in.Path)
		}
		return run(c, "restore", "--staged", "--worktree", "--source=HEAD", "--", in.Path)
	}, sdk.Describe("throw away a path's changes, staged or not (confirmed: they are not recoverable)"))

	p.Action("vcs.pull", sdk.EffectWrite, func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Result, error) {
		return run(c, "pull", "--ff-only")
	}, sdk.Describe("fast-forward the current branch from its upstream"))

	p.Action("vcs.push", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Remote string `json:"remote" desc:"default origin"`
		Branch string `json:"branch" desc:"default the current branch"`
		Force  bool   `json:"force" desc:"--force-with-lease"`
	}) (*sdk.Result, error) {
		args := []string{"push", "--set-upstream", cmp.Or(in.Remote, "origin"), cmp.Or(in.Branch, "HEAD")}
		if in.Force {
			args = append(args, "--force-with-lease")
		}
		return run(c, args...)
	}, sdk.Describe("push a branch (confirmed: it changes the shared remote)"))

	p.Action("vcs.stash", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Pop     string `json:"pop" desc:"apply and drop this stash (stash@{0}) instead of saving one"`
		Message string `json:"message"`
	}) (*sdk.Result, error) {
		if in.Pop != "" {
			return run(c, "stash", "pop", in.Pop)
		}
		return run(c, "stash", "push", "--include-untracked", "-m", cmp.Or(in.Message, "rig"))
	})

	p.Action("vcs.fetch", sdk.EffectWrite, func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Result, error) {
		return run(c, "fetch", "--all", "--tags", "--prune")
	}, sdk.Describe("fetch every remote, with its tags, dropping branches deleted there"))

	p.Action("vcs.merge", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Branch string `json:"branch" rig:"required"`
	}) (*sdk.Result, error) {
		return run(c, "merge", "--no-edit", in.Branch)
	}, sdk.Describe("merge a branch into the current one"))

	p.Action("vcs.rebase", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Onto string `json:"onto" rig:"required"`
	}) (*sdk.Result, error) {
		return run(c, "rebase", in.Onto)
	}, sdk.Describe("replay the current branch on another (it rewrites the branch's commits)"))

	p.Action("vcs.branch-delete", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Branch string `json:"branch" rig:"required"`
		Force  bool   `json:"force" desc:"also when it is not merged"`
	}) (*sdk.Result, error) {
		if strings.HasPrefix(in.Branch, "origin/") {
			return run(c, "push", "origin", "--delete", strings.TrimPrefix(in.Branch, "origin/"))
		}
		return run(c, "branch", map[bool]string{true: "-D", false: "-d"}[in.Force], in.Branch)
	}, sdk.Describe("delete a local branch, or a remote one named origin/x"))

	p.Action("vcs.cherry-pick", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Commit string `json:"commit" rig:"required"`
	}) (*sdk.Result, error) {
		return run(c, "cherry-pick", in.Commit)
	}, sdk.Describe("apply a commit onto the current branch"))

	p.Action("vcs.revert", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Commit string `json:"commit" rig:"required"`
	}) (*sdk.Result, error) {
		return run(c, "revert", "--no-edit", in.Commit)
	}, sdk.Describe("a new commit that undoes one"))

	p.Action("vcs.tag", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Name string `json:"name" rig:"required"`
		Ref  string `json:"ref" desc:"default HEAD"`
		Push bool   `json:"push"`
	}) (*sdk.Result, error) {
		if _, err := run(c, "tag", in.Name, cmp.Or(in.Ref, "HEAD")); err != nil {
			return nil, err
		}
		if in.Push {
			return run(c, "push", "origin", in.Name)
		}
		return &sdk.Result{Message: "tagged " + in.Name}, nil
	}, sdk.Describe("tag a commit, and push the tag on request"))

	p.Action("vcs.stash-drop", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Ref string `json:"ref" rig:"required" desc:"stash@{0}"`
	}) (*sdk.Result, error) {
		return run(c, "stash", "drop", in.Ref)
	}, sdk.Describe("throw a stash away"))

	p.Action("vcs.stash-apply", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		Ref string `json:"ref" rig:"required"`
	}) (*sdk.Result, error) {
		return run(c, "stash", "apply", in.Ref)
	}, sdk.Describe("apply a stash and keep it"))

	p.Action("vcs.amend", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		Message string `json:"message" desc:"a new message; empty keeps it"`
	}) (*sdk.Result, error) {
		if in.Message == "" {
			return run(c, "commit", "--amend", "--no-edit", "--all")
		}
		return run(c, "commit", "--amend", "-m", in.Message, "--all")
	}, sdk.Describe("fold the changes into the last commit (it rewrites that commit)"))

	p.Action("vcs.export", sdk.EffectRead, func(c *sdk.Ctx, in struct {
		Ref string `json:"ref" rig:"required"`
	}) (*sdk.Result, error) {
		dir, err := export(c, in.Ref)
		if err != nil {
			return nil, err
		}
		return &sdk.Result{Message: dir, Outputs: sdk.Args{"dir": dir}}, nil
	}, sdk.Describe("check a ref out into a cached directory (built, run or tested without touching the working tree)"))
}

func run(c *sdk.Ctx, args ...string) (*sdk.Result, error) {
	out, err := Git(c, args...)
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			c.Line(l)
		}
	}
	if err != nil {
		return nil, err
	}
	return &sdk.Result{Message: "git " + args[0] + " done"}, nil
}
