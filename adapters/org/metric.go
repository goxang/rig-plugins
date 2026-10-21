package org

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/kernel/frames"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// Metric is one KPI or people figure: a fixed value, a reduction over a local source's rows, or a
// roll-up of the same figure across the children.
type Metric struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Measure string   `json:"measure"`
	Note    string   `json:"note"`
	Value   *float64 `json:"value"`
	// Source and Args name a local source (gitlab.scm.mrs, a metrics component with query:, ...);
	// Transform is a view transform (filter, group, ...); Since keeps rows whose Time field is newer.
	Source    string         `json:"source"`
	Args      map[string]any `json:"args"`
	Transform any            `json:"transform"`
	Time      string         `json:"time"`
	Since     string         `json:"since"`
	// From: children rolls up the children's figure named KPI (default this one's name).
	From string `json:"from"`
	KPI  string `json:"kpi"`
	// Reduce is count, sum, avg, min, max or last (default count over a source, sum over children).
	Reduce string  `json:"reduce"`
	Field  string  `json:"field"`
	Scale  float64 `json:"scale"`
	// OK and Warn are conditions such as ">= 99.5"; a value meeting neither is err.
	OK   string `json:"ok"`
	Warn string `json:"warn"`
}

func (n *node) eval(m Metric, source string) KPI {
	k := KPI{Unit: n.cfg.Unit, Name: m.Name, Title: first(m.Title, m.Name), Measure: m.Measure, Target: m.OK, Updated: n.now, Note: m.Note}
	v, err := n.value(m, source)
	if err != nil {
		k.Status, k.Note = "unknown", err.Error()
		return k
	}
	if m.Scale != 0 {
		v *= m.Scale
	}
	k.Value = v
	k.Status, err = m.status(v)
	if err != nil {
		k.Status, k.Note = "unknown", err.Error()
	}
	return k
}

func (n *node) value(m Metric, source string) (float64, error) {
	switch {
	case m.Value != nil:
		return *m.Value, nil
	case m.From == "children":
		want := first(m.KPI, m.Name)
		var vals []float64
		var missing []string
		for _, name := range n.childNames() {
			fr, err := n.child(name, source, nil)
			if err != nil {
				missing = append(missing, name)
				continue
			}
			for _, r := range frames.Rows(fr) {
				if r["name"] == want && r["status"] != "unknown" {
					if x, ok := r["value"].(float64); ok {
						vals = append(vals, x)
					}
				}
			}
		}
		if len(vals) == 0 {
			return 0, fmt.Errorf("no child reports %s%s", want, unreachable(missing))
		}
		return reduce(first(m.Reduce, "sum"), vals, len(vals))
	case m.Source != "":
		fr, err := n.query(m.Source, m.Args)
		if err != nil {
			return 0, err
		}
		if fr, err = transform(fr, m.Transform); err != nil {
			return 0, err
		}
		rows := frames.Rows(fr)
		if m.Since != "" {
			d, err := duration(m.Since)
			if err != nil {
				return 0, err
			}
			after := n.now.Add(-d)
			rows = slices.DeleteFunc(rows, func(r map[string]any) bool { return unix(r[first(m.Time, "updated")]).Before(after) })
		}
		var vals []float64
		if m.Field != "" {
			for _, r := range rows {
				if x, ok := number(r[m.Field]); ok {
					vals = append(vals, x)
				}
			}
		}
		return reduce(first(m.Reduce, map[bool]string{true: "last", false: "count"}[m.Field != ""]), vals, len(rows))
	}
	return 0, fmt.Errorf("%s has no value, source or from", m.Name)
}

func unreachable(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return " (unreachable: " + strings.Join(names, ", ") + ")"
}

func transform(f *protocol.Frame, ops any) (*protocol.Frame, error) {
	if ops == nil {
		return f, nil
	}
	return view.Apply(f, ops, nil)
}

func reduce(fn string, vals []float64, rows int) (float64, error) {
	if fn == "count" {
		return float64(rows), nil
	}
	if len(vals) == 0 {
		return 0, fmt.Errorf("no values to %s", fn)
	}
	switch fn {
	case "last":
		return vals[len(vals)-1], nil
	case "min":
		return slices.Min(vals), nil
	case "max":
		return slices.Max(vals), nil
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	switch fn {
	case "sum":
		return sum, nil
	case "avg":
		return sum / float64(len(vals)), nil
	}
	return 0, fmt.Errorf("reduce is count, sum, avg, min, max or last, not %q", fn)
}

func (m Metric) status(v float64) (string, error) {
	if m.OK == "" {
		return "", nil
	}
	for _, c := range []struct{ cond, status string }{{m.OK, "ok"}, {m.Warn, "warn"}} {
		if c.cond == "" {
			continue
		}
		ok, err := meets(c.cond, v)
		if err != nil {
			return "", err
		}
		if ok {
			return c.status, nil
		}
	}
	return "err", nil
}

// meets checks v against a condition such as ">= 99.5" or "< 2".
func meets(cond string, v float64) (bool, error) {
	cond = strings.TrimSpace(cond)
	for _, op := range []string{">=", "<=", "==", "!=", ">", "<"} {
		rest, ok := strings.CutPrefix(cond, op)
		if !ok {
			continue
		}
		x, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			return false, fmt.Errorf("condition %q: %w", cond, err)
		}
		switch op {
		case ">=":
			return v >= x, nil
		case "<=":
			return v <= x, nil
		case "==":
			return v == x, nil
		case "!=":
			return v != x, nil
		case ">":
			return v > x, nil
		}
		return v < x, nil
	}
	return false, fmt.Errorf("condition %q starts with one of >= <= == != > <", cond)
}

// duration also takes days: 7d, 30d.
func duration(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(d, 64)
		return time.Duration(n * 24 * float64(time.Hour)), err
	}
	return time.ParseDuration(s)
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case bool:
		return map[bool]float64{true: 1}[x], true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}
