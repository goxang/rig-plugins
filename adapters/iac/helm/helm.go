// Package helm is Helm releases: list, history, values, a values diff between revisions, upgrade
// and rollback, in plugins.helm's kube context and namespace.
package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/sh"
	"github.com/goxang/rig/sdk"
)

type Release struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Revision   int       `json:"revision"`
	Status     string    `json:"status"`
	Chart      string    `json:"chart"`
	AppVersion string    `json:"app_version"`
	Updated    time.Time `json:"updated"`
}

type Revision struct {
	Revision    int       `json:"revision"`
	Status      string    `json:"status"`
	Chart       string    `json:"chart"`
	AppVersion  string    `json:"app_version"`
	Description string    `json:"description"`
	Updated     time.Time `json:"updated"`
}

// helmTime is helm's "2026-01-02 15:04:05.999 +0000 UTC" time.
type helmTime struct{ time.Time }

func (t *helmTime) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil || s == "" {
		return err
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999 -0700 MST", time.RFC3339Nano} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return nil
}

type helm struct{ context, namespace string }

func open(c *sdk.Ctx, namespace string) helm {
	h := helm{context: forge.Config(c, "context"), namespace: forge.Config(c, "namespace")}
	if namespace != "" {
		h.namespace = namespace
	}
	return h
}

func (h helm) args(args ...string) []string {
	if h.context != "" {
		args = append(args, "--kube-context", h.context)
	}
	if h.namespace != "" {
		args = append(args, "-n", h.namespace)
	}
	return args
}

func (h helm) run(ctx context.Context, out any, args ...string) error {
	raw, err := sh.New("helm", h.args(args...)...).Output(ctx)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// Releases lists releases in the namespace, or every namespace when none is set.
func (h helm) Releases(ctx context.Context) ([]Release, error) {
	args := []string{"list", "-o", "json", "-a"}
	if h.namespace == "" {
		args = append(args, "-A")
	}
	var raw []struct {
		Name, Namespace, Status, Chart string
		Revision                       string   `json:"revision"`
		AppVersion                     string   `json:"app_version"`
		Updated                        helmTime `json:"updated"`
	}
	if err := h.run(ctx, &raw, args...); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		rev, _ := strconv.Atoi(r.Revision)
		out = append(out, Release{r.Name, r.Namespace, rev, r.Status, r.Chart, r.AppVersion, r.Updated.Time})
	}
	return out, nil
}

func (h helm) History(ctx context.Context, release string) ([]Revision, error) {
	var raw []struct {
		Revision                   int
		Status, Chart, Description string
		AppVersion                 string   `json:"app_version"`
		Updated                    helmTime `json:"updated"`
	}
	if err := h.run(ctx, &raw, "history", release, "-o", "json", "--max", "50"); err != nil {
		return nil, err
	}
	out := make([]Revision, 0, len(raw))
	for i := len(raw) - 1; i >= 0; i-- {
		r := raw[i]
		out = append(out, Revision{r.Revision, r.Status, r.Chart, r.AppVersion, r.Description, r.Updated.Time})
	}
	return out, nil
}

// Values is a revision's user-supplied values as YAML; revision 0 is the current one.
func (h helm) Values(ctx context.Context, release string, revision int, all bool) (string, error) {
	args := []string{"get", "values", release, "-o", "yaml"}
	if revision > 0 {
		args = append(args, "--revision", strconv.Itoa(revision))
	}
	if all {
		args = append(args, "--all")
	}
	out, err := sh.New("helm", h.args(args...)...).Output(ctx)
	return string(out), err
}

// Diff is a unified diff of two revisions' values; from 0 is the one before to, to 0 the current.
func (h helm) Diff(ctx context.Context, release string, from, to int) (string, error) {
	if to == 0 || from == 0 {
		hist, err := h.History(ctx, release)
		if err != nil {
			return "", err
		}
		if len(hist) == 0 {
			return "", fmt.Errorf("release %s has no history", release)
		}
		if to == 0 {
			to = hist[0].Revision
		}
		if from == 0 {
			from = to - 1
		}
	}
	dir, err := os.MkdirTemp("", "rig-helm")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	var files []string
	for _, rev := range []int{from, to} {
		v, err := h.Values(ctx, release, rev, false)
		if err != nil {
			return "", err
		}
		f := filepath.Join(dir, "revision-"+strconv.Itoa(rev))
		if err := os.WriteFile(f, []byte(v), 0o600); err != nil {
			return "", err
		}
		files = append(files, f)
	}
	// git diff --no-index exits 1 when the files differ; its output is still the diff
	out, _ := git.Git(ctx, "diff", "--no-index", "--no-color", "--", files[0], files[1])
	return out, nil
}

type releaseArg struct {
	Release   string `json:"release" rig:"required"`
	Namespace string `json:"namespace"`
}

func Register(p *sdk.Plugin) {
	p.Source("helm.releases", func(c *sdk.Ctx, in struct {
		Namespace string `json:"namespace" desc:"default plugins.helm.namespace, else all"`
	}) (*sdk.Frame, error) {
		rs, err := open(c, in.Namespace).Releases(c)
		return frames.Of(rs), err
	}, sdk.Cache("15s"))

	p.Source("helm.history", func(c *sdk.Ctx, in releaseArg) (*sdk.Frame, error) {
		rs, err := open(c, in.Namespace).History(c, in.Release)
		return frames.Of(rs), err
	})

	p.Source("helm.values", func(c *sdk.Ctx, in struct {
		releaseArg
		Revision int  `json:"revision" desc:"default the current one"`
		All      bool `json:"all" desc:"computed values, chart defaults included"`
	}) (*sdk.Frame, error) {
		v, err := open(c, in.Namespace).Values(c, in.Release, in.Revision, in.All)
		return frames.Text(v), err
	})

	p.Source("helm.diff", func(c *sdk.Ctx, in struct {
		releaseArg
		From int `json:"from" desc:"default the revision before to"`
		To   int `json:"to" desc:"default the current revision"`
	}) (*sdk.Frame, error) {
		d, err := open(c, in.Namespace).Diff(c, in.Release, in.From, in.To)
		return frames.Text(d), err
	}, sdk.Describe("values diff between two revisions"))

	p.Action("helm.upgrade", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		releaseArg
		Chart   string            `json:"chart" rig:"required" desc:"a chart path, repo/chart or oci:// reference"`
		Version string            `json:"version"`
		Values  []string          `json:"values" desc:"values files"`
		Set     map[string]string `json:"set"`
		Install bool              `json:"install" desc:"install when the release does not exist"`
	}) (*sdk.Result, error) {
		h := open(c, in.Namespace)
		args := []string{"upgrade", in.Release, in.Chart, "--wait"}
		if in.Install {
			args = append(args, "--install")
		}
		if in.Version != "" {
			args = append(args, "--version", in.Version)
		}
		for _, f := range in.Values {
			args = append(args, "-f", f)
		}
		for k, v := range in.Set {
			args = append(args, "--set", k+"="+v)
		}
		cmd := sh.New("helm", h.args(args...)...)
		cmd.Dir = git.Dir()
		out, err := cmd.Output(c)
		forge.Lines(c, splitLines(string(out)))
		if err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "upgraded " + in.Release}, nil
	})

	p.Action("helm.rollback", sdk.EffectDanger, func(c *sdk.Ctx, in struct {
		releaseArg
		Revision int `json:"revision" desc:"default the previous revision"`
	}) (*sdk.Result, error) {
		args := []string{"rollback", in.Release, "--wait"}
		if in.Revision > 0 {
			args = append(args, strconv.Itoa(in.Revision))
		}
		if err := open(c, in.Namespace).run(c, nil, args...); err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "rolled back " + in.Release}, nil
	})
	p.Plan("helm.rollback", func(c *sdk.Ctx, in struct {
		releaseArg
		Revision int `json:"revision"`
	}) (*sdk.Result, error) {
		h := open(c, in.Namespace)
		d, err := h.Diff(c, in.Release, in.Revision, 0)
		if err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "values, rollback target (-) against current (+):\n" + d, Changes: []sdk.Change{{Op: sdk.OpUpdate, Target: in.Release, Detail: "rollback"}}}, nil
	})
}

func splitLines(s string) []string {
	var out []string
	for l := range strings.Lines(s) {
		out = append(out, strings.TrimRight(l, "\n"))
	}
	return out
}
