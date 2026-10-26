// Package confluence is Confluence as docs/1: CQL search, pages read as Markdown, written from
// Markdown, and the page tree, over the REST API (Cloud's /wiki or a Server base URL).
package confluence

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

type Confluence struct {
	*forge.Client
	space  string
	spaces []string
}

// Open reads plugins.confluence: url (https://org.atlassian.net/wiki), user (Cloud: the account
// email), token (CONFLUENCE_TOKEN or ATLASSIAN_TOKEN) and space (space keys, a list or comma-separated;
// the first is where new pages go).
func Open(c *sdk.Ctx) (*Confluence, error) {
	base := strings.TrimSuffix(forge.Setting(c, "CONFLUENCE", "url"), "/")
	if base == "" {
		return nil, sdk.Unavailable("confluence has no address", "connect it on the Confluence tab, or set plugins.confluence.url (https://your-org.atlassian.net/wiki)")
	}
	ep := httpx.Endpoint{Headers: map[string]string{"Accept": "application/json"}}
	tok := forge.Token(c, "CONFLUENCE_TOKEN", "ATLASSIAN_TOKEN")
	if u := forge.Setting(c, "CONFLUENCE", "user"); u != "" {
		ep.User, ep.Password = u, tok
	} else {
		ep.Token = tok
	}
	spaces := spaceKeys(forge.Setting(c, "CONFLUENCE", "space"))
	cf := &Confluence{Client: &forge.Client{Base: base, Endpoint: ep}, spaces: spaces}
	if len(spaces) > 0 {
		cf.space = spaces[0]
	}
	return cf, nil
}

// spaceKeys reads "PROD, SWIT" and a YAML list (which arrives as "[PROD SWIT]").
func spaceKeys(s string) []string {
	return strings.FieldsFunc(strings.Trim(s, "[]"), func(r rune) bool { return r == ',' || r == ' ' })
}

func (cf *Confluence) spaceCQL() string {
	if len(cf.spaces) == 0 {
		return ""
	}
	return " AND space in (" + strings.Join(cf.spaces, ",") + ")"
}

type content struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Container *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"container"`
	Extensions struct {
		MediaType string `json:"mediaType"`
		FileSize  int64  `json:"fileSize"`
	} `json:"extensions"`
	Space   struct{ Key string }
	Version struct {
		Number int       `json:"number"`
		When   time.Time `json:"when"`
	} `json:"version"`
	Ancestors []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"ancestors"`
	Body struct {
		Storage struct{ Value string } `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI    string `json:"webui"`
		Download string `json:"download"`
	} `json:"_links"`
}

type page struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Path    string    `json:"path"`
	Updated time.Time `json:"updated"`
	URL     string    `json:"url"`
}

func (cf *Confluence) page(c content) page {
	path := []string{c.Space.Key}
	for _, a := range c.Ancestors {
		path = append(path, a.Title)
	}
	if c.Container != nil {
		path = append(path, c.Container.Title)
	}
	return page{ID: c.ID, Kind: kind(c), Title: c.Title, Path: strings.Join(path, " / "), Updated: c.Version.When, URL: cf.Base + cmp.Or(c.Links.WebUI, c.Links.Download)}
}

func kind(c content) string {
	if c.Type == "attachment" {
		return "file"
	}
	return "page"
}

func (cf *Confluence) list(ctx context.Context, path string) ([]page, error) {
	var res struct{ Results []content }
	if err := cf.Get(ctx, path, &res); err != nil {
		return nil, err
	}
	out := make([]page, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, cf.page(r))
	}
	return out, nil
}

var (
	tagRE   = regexp.MustCompile(`(?s)<[^>]+>`)
	codeRE  = regexp.MustCompile(`(?s)<ac:structured-macro ac:name="code".*?<!\[CDATA\[(.*?)\]\]>.*?</ac:structured-macro>`)
	blankRE = regexp.MustCompile(`\n{3,}`)
	toMD    = []struct {
		re  *regexp.Regexp
		out string
	}{
		{regexp.MustCompile(`(?s)<h([1-6])[^>]*>(.*?)</h[1-6]>`), "\n\n${H}$2\n\n"},
		{regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`), "\n- $1"},
		{regexp.MustCompile(`(?s)<(strong|b)>(.*?)</(strong|b)>`), "**$2**"},
		{regexp.MustCompile(`(?s)<(em|i)>(.*?)</(em|i)>`), "_$2_"},
		{regexp.MustCompile(`(?s)<code>(.*?)</code>`), "`$1`"},
		{regexp.MustCompile(`(?s)<a [^>]*href="([^"]*)"[^>]*>(.*?)</a>`), "[$2]($1)"},
		{regexp.MustCompile(`<br\s*/?>`), "\n"},
		{regexp.MustCompile(`(?s)</?(p|ul|ol|div|table|tr)[^>]*>`), "\n\n"},
		{regexp.MustCompile(`(?s)</t[dh]>`), " | "},
	}
)

// Markdown is a storage-format page as Markdown: headings, lists, emphasis, code, links and
// paragraphs; other macros keep only their text.
func Markdown(storage string) string {
	// code bodies are set aside so tag stripping leaves them as they are
	var code []string
	s := codeRE.ReplaceAllStringFunc(storage, func(m string) string {
		code = append(code, codeRE.FindStringSubmatch(m)[1])
		return fmt.Sprintf("\n\n\x00%d\x00\n\n", len(code)-1)
	})
	for _, r := range toMD {
		if strings.Contains(r.out, "${H}") {
			s = r.re.ReplaceAllStringFunc(s, func(m string) string {
				g := r.re.FindStringSubmatch(m)
				return "\n\n" + strings.Repeat("#", int(g[1][0]-'0')) + " " + g[2] + "\n\n"
			})
			continue
		}
		s = r.re.ReplaceAllString(s, r.out)
	}
	s = html.UnescapeString(tagRE.ReplaceAllString(s, ""))
	for i, c := range code {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), "```\n"+strings.TrimRight(c, "\n")+"\n```", 1)
	}
	return strings.TrimSpace(blankRE.ReplaceAllString(s, "\n\n"))
}

var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdBold    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdCode    = regexp.MustCompile("`([^`]+)`")
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// Storage is Markdown as Confluence storage format: headings, lists, fenced code, bold, code and links.
func Storage(md string) string {
	var b strings.Builder
	inList, inCode := false, false
	var para []string
	flush := func() {
		if len(para) > 0 {
			b.WriteString("<p>" + inline(strings.Join(para, " ")) + "</p>")
			para = nil
		}
	}
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "```") {
			flush()
			if inCode {
				b.WriteString("]]></ac:plain-text-body></ac:structured-macro>")
			} else {
				b.WriteString(`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[`)
			}
			inCode = !inCode
			continue
		}
		if inCode {
			b.WriteString(l + "\n")
			continue
		}
		item, isItem := strings.CutPrefix(strings.TrimLeft(l, " "), "- ")
		if !isItem {
			item, isItem = strings.CutPrefix(strings.TrimLeft(l, " "), "* ")
		}
		if inList && !isItem {
			b.WriteString("</ul>")
			inList = false
		}
		switch m := mdHeading.FindStringSubmatch(l); {
		case m != nil:
			flush()
			n := len(m[1])
			fmt.Fprintf(&b, "<h%d>%s</h%d>", n, inline(m[2]), n)
		case isItem:
			flush()
			if !inList {
				b.WriteString("<ul>")
				inList = true
			}
			b.WriteString("<li>" + inline(item) + "</li>")
		case strings.TrimSpace(l) == "":
			flush()
		default:
			para = append(para, strings.TrimSpace(l))
		}
	}
	flush()
	if inList {
		b.WriteString("</ul>")
	}
	return b.String()
}

func inline(s string) string {
	s = html.EscapeString(s)
	s = mdBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = mdCode.ReplaceAllString(s, "<code>$1</code>")
	return mdLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
}

// fileDoc describes an attachment: what it is, where it lives and the link that downloads it.
func (cf *Confluence) fileDoc(ct content) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", ct.Title)
	if ct.Extensions.MediaType != "" {
		fmt.Fprintf(&b, "- type: %s\n", ct.Extensions.MediaType)
	}
	fmt.Fprintf(&b, "- size: %.1f KB\n- updated: %s\n", float64(ct.Extensions.FileSize)/1024, ct.Version.When.Format("2006-01-02 15:04"))
	if ct.Container != nil {
		fmt.Fprintf(&b, "- attached to: %s (%s)\n", ct.Container.Title, ct.Container.ID)
	}
	fmt.Fprintf(&b, "\n[download](%s)\n", cf.Base+ct.Links.Download)
	return b.String()
}

func Register(p *sdk.Plugin) {
	forge.Connect(p, "CONFLUENCE", "space", func(c *sdk.Ctx) (string, error) {
		cf, err := Open(c)
		if err != nil {
			return "", err
		}
		var me struct {
			DisplayName string `json:"displayName"`
		}
		return me.DisplayName, cf.Get(c, "/rest/api/user/current", &me)
	})
	p.Source("docs.search", func(c *sdk.Ctx, in struct {
		Query string `json:"query" desc:"words, a file name, or CQL"`
	}) (*sdk.Frame, error) {
		cf, err := Open(c)
		if err != nil {
			return nil, err
		}
		cql := in.Query
		if !strings.ContainsAny(cql, "=~") {
			cql = "type in (page, attachment)"
			if in.Query != "" {
				cql += fmt.Sprintf(" AND (title ~ %q OR text ~ %q)", in.Query, in.Query)
			}
			cql += cf.spaceCQL()
			cql += " ORDER BY lastmodified DESC"
		}
		rows, err := cf.list(c, "/rest/api/content/search?limit=50&expand=space,version,ancestors,container&cql="+url.QueryEscape(cql))
		return frames.Of(rows), err
	}, sdk.Cache("30s"))

	p.Source("docs.read", func(c *sdk.Ctx, in struct {
		ID string `json:"id" rig:"required"`
	}) (*sdk.Frame, error) {
		cf, err := Open(c)
		if err != nil {
			return nil, err
		}
		if key, ok := strings.CutPrefix(in.ID, "space:"); ok {
			var sp struct{ Homepage struct{ ID string } }
			if err := cf.Get(c, "/rest/api/space/"+url.PathEscape(key)+"?expand=homepage", &sp); err != nil {
				return nil, err
			}
			in.ID = sp.Homepage.ID
		}
		var ct content
		if err := cf.Get(c, "/rest/api/content/"+url.PathEscape(in.ID)+"?expand=body.storage,version,container", &ct); err != nil {
			return nil, err
		}
		if ct.Type == "attachment" {
			return frames.Of([]struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Markdown string `json:"markdown"`
			}{{ct.ID, ct.Title, cf.fileDoc(ct)}}), nil
		}
		type doc struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Markdown string `json:"markdown"`
		}
		md := Markdown(ct.Body.Storage.Value)
		if !strings.HasPrefix(md, "# ") {
			md = "# " + ct.Title + "\n\n" + md
		}
		return frames.Of([]doc{{ct.ID, ct.Title, md}}), nil
	})

	p.Action("docs.write", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		ID       string `json:"id" desc:"the page to update; empty makes a new one"`
		Title    string `json:"title" rig:"required"`
		Markdown string `json:"markdown" rig:"required" format:"markdown"`
		Parent   string `json:"parent" desc:"parent page id for a new page"`
		Space    string `json:"space" desc:"space key (default plugins.confluence.space)"`
	}) (*sdk.Result, error) {
		cf, err := Open(c)
		if err != nil {
			return nil, err
		}
		body := map[string]any{"type": "page", "title": in.Title,
			"body": map[string]any{"storage": map[string]string{"value": Storage(in.Markdown), "representation": "storage"}}}
		var out content
		if in.ID != "" {
			var cur content
			if err := cf.Get(c, "/rest/api/content/"+url.PathEscape(in.ID)+"?expand=version", &cur); err != nil {
				return nil, err
			}
			body["version"] = map[string]int{"number": cur.Version.Number + 1}
			err = cf.Send(c, "PUT", "/rest/api/content/"+url.PathEscape(in.ID), body, &out)
		} else {
			space := in.Space
			if space == "" {
				space = cf.space
			}
			if space == "" {
				return nil, fmt.Errorf("which space? set space or plugins.confluence.space")
			}
			body["space"] = map[string]string{"key": space}
			if in.Parent != "" {
				body["ancestors"] = []map[string]string{{"id": in.Parent}}
			}
			err = cf.Send(c, "POST", "/rest/api/content", body, &out)
		}
		if err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "saved " + out.Title, Outputs: sdk.Args{"id": out.ID, "url": cf.Base + out.Links.WebUI}}, nil
	})

	p.Source("docs.pages", func(c *sdk.Ctx, _ struct{}) (*sdk.Frame, error) {
		cf, err := Open(c)
		if err != nil {
			return nil, err
		}
		if len(cf.spaces) == 0 {
			return nil, sdk.Unavailable("confluence has no space", "set plugins.confluence.space (one key or a list)")
		}
		var rows []treeRow
		for _, key := range cf.spaces {
			var sp struct {
				Name     string
				Homepage struct{ ID string }
			}
			_ = cf.Get(c, "/rest/api/space/"+url.PathEscape(key)+"?expand=homepage", &sp)
			var all []content
			for start := 0; ; start += 100 {
				var res struct {
					Results []content
					Size    int
				}
				path := fmt.Sprintf("/rest/api/space/%s/content/page?limit=100&start=%d&expand=space,version,ancestors", url.PathEscape(key), start)
				if err := cf.Get(c, path, &res); err != nil {
					return nil, err
				}
				all = append(all, res.Results...)
				if res.Size < 100 {
					break
				}
			}
			files, _ := cf.attachments(c, key)
			rows = append(rows, treeRow{ID: "space:" + key, Kind: "space", Title: cmp.Or(sp.Name, key) + " (" + key + ")", URL: cf.Base + "/display/" + key})
			rows = append(rows, cf.tree(all, files, "space:"+key)...)
		}
		return frames.Of(rows), nil
	}, sdk.Cache("5m"))

	p.Source("docs.tree", func(c *sdk.Ctx, in struct {
		Parent string `json:"parent" desc:"a page id (default the space's top pages)"`
	}) (*sdk.Frame, error) {
		cf, err := Open(c)
		if err != nil {
			return nil, err
		}
		path := "/rest/api/content/" + url.PathEscape(in.Parent) + "/child/page?limit=100&expand=space,version,ancestors"
		if in.Parent == "" {
			if cf.space == "" {
				return nil, fmt.Errorf("set parent or plugins.confluence.space")
			}
			path = "/rest/api/space/" + url.PathEscape(cf.space) + "/content/page?depth=root&limit=100&expand=space,version,ancestors"
		}
		rows, err := cf.list(c, path)
		return frames.Of(rows), err
	}, sdk.Cache("60s"))
}

type treeRow struct {
	ID      string    `json:"id"`
	Parent  string    `json:"parent"`
	Title   string    `json:"title"`
	Kind    string    `json:"kind"`
	Updated time.Time `json:"updated"`
	URL     string    `json:"url"`
}

// attachments lists a space's files (up to 1000, the newest first).
func (cf *Confluence) attachments(ctx context.Context, space string) ([]content, error) {
	var all []content
	for start := 0; start < 1000; start += 100 {
		var res struct {
			Results []content
			Size    int
		}
		cql := url.QueryEscape(fmt.Sprintf("type = attachment AND space = %q ORDER BY lastmodified DESC", space))
		path := fmt.Sprintf("/rest/api/content/search?limit=100&start=%d&expand=container,version&cql=%s", start, cql)
		if err := cf.Get(ctx, path, &res); err != nil {
			return all, err
		}
		all = append(all, res.Results...)
		if res.Size < 100 {
			break
		}
	}
	return all, nil
}

// tree lays a space's pages out depth first under their parents (the last ancestor), each page's
// files under it; the space's id is the parent of the top pages.
func (cf *Confluence) tree(pages, files []content, root string) []treeRow {
	kids := map[string][]content{}
	ids := map[string]bool{}
	for _, p := range pages {
		ids[p.ID] = true
	}
	for _, p := range pages {
		parent := ""
		if n := len(p.Ancestors); n > 0 && ids[p.Ancestors[n-1].ID] {
			parent = p.Ancestors[n-1].ID
		}
		kids[parent] = append(kids[parent], p)
	}
	for _, k := range kids {
		slices.SortFunc(k, func(a, b content) int { return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })
	}
	attached := map[string][]content{}
	for _, f := range files {
		owner := ""
		if f.Container != nil && ids[f.Container.ID] {
			owner = f.Container.ID
		}
		attached[owner] = append(attached[owner], f)
	}
	var out []treeRow
	add := func(parent string, cs []content, kind string) {
		for _, c := range cs {
			out = append(out, treeRow{ID: c.ID, Parent: parent, Kind: kind, Title: c.Title, Updated: c.Version.When, URL: cf.Base + cmp.Or(c.Links.WebUI, c.Links.Download)})
		}
	}
	var walk func(parent string)
	walk = func(parent string) {
		for _, p := range kids[parent] {
			up := parent
			if up == "" {
				up = root
			}
			add(up, []content{p}, "page")
			walk(p.ID)
			add(p.ID, attached[p.ID], "file")
		}
	}
	walk("")
	add(root, attached[""], "file")
	return out
}
