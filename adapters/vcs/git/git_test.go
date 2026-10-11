package git

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]string{
		"git@gitlab.com:group/sub/app.git":  "gitlab.com group/sub/app",
		"https://github.com/goxang/rig.git": "github.com goxang/rig",
		"ssh://git@git.corp:2222/team/svc":  "git.corp team/svc",
		"https://user:tok@gitlab.corp/a/b":  "gitlab.corp a/b",
	} {
		h, p, err := ParseRemote(in)
		if err != nil || h+" "+p != want {
			t.Errorf("%s: %s %s %v", in, h, p, err)
		}
	}
	st := Status(" M a.go\nA  b.go\nMM c.go\n?? d.go\nR  old -> new.go\n")
	got := ""
	for _, c := range st {
		got += c.Path + "=" + c.State + ";"
	}
	if got != "a.go=modified;b.go=staged added;c.go=staged modified, modified;d.go=untracked;new.go=staged renamed;" {
		t.Errorf("status %s", got)
	}
	log := Log("* \x1fabcdef0123456789\x1fAnn\x1f1700000000\x1ffix it\x1fHEAD -> main\n|\\\n")
	if len(log) != 2 || log[0].Commit != "abcdef012345" || log[0].Graph != "*" || log[1].Graph != "|\\" {
		t.Errorf("log %+v", log)
	}
	b := Blame(strings.Join([]string{"0123456789012345678901234567890123456789 1 1 1", "author Ann", "author-time 1700000000", "\tline one"}, "\n"))
	if len(b) != 1 || b[0].Author != "Ann" || b[0].Text != "line one" || b[0].Commit != "012345678901" {
		t.Errorf("blame %+v", b)
	}
}
