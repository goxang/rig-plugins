// Package markdown is docs/1 over the repository's own Markdown: docs/, ADRs and READMEs, searched,
// read, written and listed as a tree. A page's id is its path from the project directory.
package markdown

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig/sdk"
)

// DefaultDirs are read when plugins.markdown-docs.dirs is not set; missing ones are skipped.
var DefaultDirs = []string{"docs", "doc", "adr", "docs/adr", "README.md", "CONTRIBUTING.md"}

type Page struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Path    string    `json:"path"`
	Updated time.Time `json:"updated"`
	URL     string    `json:"url"`
	Depth   int       `json:"depth"`
	body    string
}

var heading = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// Pages walks dirs under root for .md files, each titled by its first # heading.
func Pages(root string, dirs []string) []Page {
	var out []Page
	seen := map[string]bool{}
	for _, d := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if e.IsDir() {
				if n := e.Name(); n == "node_modules" || n == "vendor" || strings.HasPrefix(n, ".") && p != filepath.Join(root, d) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(e.Name(), ".md") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			raw, _ := os.ReadFile(p)
			info, _ := e.Info()
			pg := Page{ID: rel, Path: filepath.Dir(rel), Title: strings.TrimSuffix(e.Name(), ".md"), Depth: strings.Count(rel, string(filepath.Separator)), URL: "file://" + p, body: string(raw)}
			if info != nil {
				pg.Updated = info.ModTime()
			}
			if m := heading.FindStringSubmatch(pg.body); m != nil {
				pg.Title = strings.TrimSpace(m[1])
			}
			out = append(out, pg)
			return nil
		})
	}
	slices.SortFunc(out, func(a, b Page) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Search ranks pages whose title holds every word first, then those whose text does.
func Search(pages []Page, query string) []Page {
	words := strings.Fields(strings.ToLower(query))
	match := func(s string) bool {
		s = strings.ToLower(s)
		for _, w := range words {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return true
	}
	var title, body []Page
	for _, p := range pages {
		switch {
		case match(p.Title):
			title = append(title, p)
		case match(p.body):
			body = append(body, p)
		}
	}
	return append(title, body...)
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func dirs(c *sdk.Ctx) []string {
	if v, ok := c.Config().Config["dirs"].([]any); ok {
		var out []string
		for _, d := range v {
			out = append(out, fmt.Sprint(d))
		}
		return out
	}
	return DefaultDirs
}

// inside keeps a page id within the project.
func inside(root, id string) (string, error) {
	p := filepath.Join(root, id)
	if rel, err := filepath.Rel(root, p); err != nil || strings.HasPrefix(rel, "..") || !strings.HasSuffix(p, ".md") {
		return "", fmt.Errorf("%s is not a Markdown page in the project", id)
	}
	return p, nil
}

func Register(p *sdk.Plugin) {
	p.Source("docs.search", func(c *sdk.Ctx, in struct {
		Query string `json:"query" desc:"words; empty lists every page"`
	}) (*sdk.Frame, error) {
		return frames.Of(Search(Pages(git.Dir(), dirs(c)), in.Query)), nil
	}, sdk.Describe("the repository's Markdown pages"))

	p.Source("docs.read", func(c *sdk.Ctx, in struct {
		ID string `json:"id" rig:"required"`
	}) (*sdk.Frame, error) {
		path, err := inside(git.Dir(), in.ID)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		title := in.ID
		if m := heading.FindSubmatch(raw); m != nil {
			title = string(m[1])
		}
		type doc struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Markdown string `json:"markdown"`
		}
		return frames.Of([]doc{{in.ID, title, string(raw)}}), nil
	})

	p.Action("docs.write", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		ID       string `json:"id" desc:"the page's path; empty makes a new page"`
		Title    string `json:"title" rig:"required"`
		Markdown string `json:"markdown" rig:"required"`
		Parent   string `json:"parent" desc:"directory for a new page (default the first docs dir)"`
	}) (*sdk.Result, error) {
		id := in.ID
		if id == "" {
			parent := in.Parent
			if parent == "" {
				parent = dirs(c)[0]
			}
			id = filepath.Join(parent, strings.Trim(slugRE.ReplaceAllString(strings.ToLower(in.Title), "-"), "-")+".md")
		}
		path, err := inside(git.Dir(), id)
		if err != nil {
			return nil, err
		}
		body := in.Markdown
		if !heading.MatchString(body) {
			body = "# " + in.Title + "\n\n" + body
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(strings.TrimRight(body, "\n")+"\n"), 0o644); err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "wrote " + id, Outputs: sdk.Args{"id": id}}, nil
	})

	p.Source("docs.tree", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		return frames.Of(Pages(git.Dir(), dirs(c))), nil
	}, sdk.Describe("every page by path, with its depth"))
}
