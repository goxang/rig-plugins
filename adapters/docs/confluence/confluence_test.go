package confluence

import (
	"strings"
	"testing"

	"github.com/goxang/rig-plugins/internal/forge"
)

func TestConvert(t *testing.T) {
	md := "# Deploy\n\nRun **make** then `rig up`.\nSee [guide](http://x/g).\n\n- one\n- two\n\n```\necho <hi>\n```\n"
	st := Storage(md)
	for _, want := range []string{"<h1>Deploy</h1>", "<strong>make</strong>", "<code>rig up</code>", `<a href="http://x/g">guide</a>`,
		"<ul><li>one</li><li>two</li></ul>", "<![CDATA[echo <hi>\n]]>"} {
		if !strings.Contains(st, want) {
			t.Errorf("storage misses %s:\n%s", want, st)
		}
	}
	back := Markdown(st)
	for _, want := range []string{"# Deploy", "**make**", "`rig up`", "[guide](http://x/g)", "- one\n- two", "```\necho <hi>\n```"} {
		if !strings.Contains(back, want) {
			t.Errorf("markdown misses %q:\n%s", want, back)
		}
	}
}

func TestTree(t *testing.T) {
	page := func(id, title string, anc ...string) content {
		c := content{ID: id, Title: title}
		for _, a := range anc {
			c.Ancestors = append(c.Ancestors, struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}{ID: a})
		}
		return c
	}
	cf := &Confluence{Client: &forge.Client{}}
	file := content{ID: "att9", Title: "spec.pdf", Container: &struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}{ID: "2"}}
	rows := cf.tree([]content{page("3", "b child", "1"), page("1", "Home"), page("2", "a child", "1"), page("4", "deep", "1", "2")}, []content{file}, "space:P")
	var got []string
	for _, r := range rows {
		got = append(got, r.Parent+">"+r.Title)
	}
	want := []string{"space:P>Home", "1>a child", "2>deep", "2>spec.pdf", "1>b child"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if k := spaceKeys("[PROD SWIT]"); len(k) != 2 || k[1] != "SWIT" {
		t.Fatalf("spaceKeys = %q", k)
	}
}
