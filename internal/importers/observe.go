package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type grafanaPanel struct {
	Type    string             `json:"type"`
	Title   string             `json:"title"`
	Desc    string             `json:"description"`
	GridPos struct{ W, H int } `json:"gridPos"`
	Targets []struct {
		Expr   string `json:"expr"`
		Legend string `json:"legendFormat"`
		Hide   bool   `json:"hide"`
	} `json:"targets"`
	FieldConfig struct {
		Defaults struct {
			Unit       string   `json:"unit"`
			Min        *float64 `json:"min"`
			Max        *float64 `json:"max"`
			Thresholds struct {
				Steps []struct {
					Value *float64 `json:"value"`
				} `json:"steps"`
			} `json:"thresholds"`
			Custom struct {
				Stacking struct {
					Mode string `json:"mode"`
				} `json:"stacking"`
			} `json:"custom"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
	Panels []grafanaPanel `json:"panels"`
}

var grafanaKinds = map[string]string{"timeseries": "line", "graph": "line", "stat": "stat", "singlestat": "stat",
	"gauge": "gauge", "bargauge": "bar", "barchart": "bar", "table": "table"}

var grafanaUnits = map[string]string{"reqps": "/s", "ops": "/s", "rps": "/s", "cps": "/s", "wps": "/s", "iops": "/s",
	"ns": "ns", "µs": "µs", "ms": "ms", "s": "s", "dtdurations": "s", "dtdurationms": "ms",
	"bytes": "bytes", "decbytes": "bytes", "bits": "bits", "Bps": "bytes/s", "binBps": "bytes/s", "bps": "bits/s", "binbps": "bits/s",
	"percent": "%", "percentunit": "ratio"}

// Grafana turns dashboard JSON exports (a file or a folder of them) into dashboards: Prometheus panels
// with their legends, units, thresholds and rows, and query, custom and interval variables.
// Panels without a PromQL target (text, logs, other datasources) are left out.
func Grafana(path, _ string) (map[string]any, error) {
	files, err := yamlFiles(path, ".json")
	if err != nil {
		return nil, err
	}
	dashboards := map[string]any{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var d struct {
			Title      string         `json:"title"`
			Panels     []grafanaPanel `json:"panels"`
			Dashboard  *json.RawMessage
			Templating struct {
				List []struct {
					Name    string `json:"name"`
					Type    string `json:"type"`
					Query   any    `json:"query"`
					Multi   bool   `json:"multi"`
					All     bool   `json:"includeAll"`
					Current struct {
						Value any `json:"value"`
					} `json:"current"`
				} `json:"list"`
			} `json:"templating"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if d.Dashboard != nil {
			if err := json.Unmarshal(*d.Dashboard, &d); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
		}
		vars := map[string]any{}
		for _, v := range d.Templating.List {
			q := fmt.Sprint(v.Query)
			if m, ok := v.Query.(map[string]any); ok {
				q = fmt.Sprint(m["query"])
			}
			dv := map[string]any{}
			switch v.Type {
			case "query":
				m := labelValues.FindStringSubmatch(q)
				if m == nil {
					continue
				}
				dv["query"], dv["label"] = or(m[1], "up"), m[2]
			case "custom", "interval":
				var vals []string
				for _, s := range strings.Split(q, ",") {
					if s = strings.TrimSpace(s); s != "" {
						vals = append(vals, s)
					}
				}
				dv["values"] = vals
			default:
				continue
			}
			if s, ok := v.Current.Value.(string); ok && s != "" && s != "$__all" {
				dv["default"] = s
			}
			if v.Multi {
				dv["multi"] = true
			}
			if v.All {
				dv["all"] = true
			}
			vars[v.Name] = dv
		}
		var panels []map[string]any
		var walk func([]grafanaPanel)
		walk = func(ps []grafanaPanel) {
			for _, p := range ps {
				if p.Type == "row" {
					panels = append(panels, map[string]any{"row": p.Title})
					walk(p.Panels)
					continue
				}
				if panel := grafanaToPanel(p); panel != nil {
					panels = append(panels, panel)
				}
			}
		}
		walk(d.Panels)
		if len(panels) == 0 {
			continue
		}
		name := slug(or(d.Title, strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))))
		dash := map[string]any{"help": d.Title, "panels": panels}
		if len(vars) > 0 {
			dash["vars"] = vars
		}
		dashboards[name] = dash
	}
	return map[string]any{"dashboards": dashboards}, nil
}

// the selector may hold commas ({job="$job", nodename="$nodename"}): the label is after the last one
var labelValues = regexp.MustCompile(`label_values\(\s*(?:(.*\S)\s*,)?\s*(\w+)\s*\)\s*$`)

func grafanaToPanel(p grafanaPanel) map[string]any {
	var qs []map[string]any
	for _, t := range p.Targets {
		// __auto is Grafana's "label set" legend: rig's default
		if t.Expr != "" && !t.Hide {
			qs = append(qs, map[string]any{"query": t.Expr, "legend": strings.TrimPrefix(t.Legend, "__auto")})
		}
	}
	if len(qs) == 0 {
		return nil
	}
	out := map[string]any{"title": p.Title}
	if len(qs) == 1 {
		out["query"] = qs[0]["query"]
		if l := qs[0]["legend"]; l != "" {
			out["legend"] = l
		}
	} else {
		out["queries"] = qs
	}
	if k := grafanaKinds[p.Type]; k != "" && k != "line" {
		out["kind"] = k
	}
	dflt := p.FieldConfig.Defaults
	if u := grafanaUnits[dflt.Unit]; u != "" {
		out["unit"] = u
	}
	if p.Desc != "" {
		out["help"] = p.Desc
	}
	if p.GridPos.W > 0 {
		out["width"] = p.GridPos.W
	}
	if dflt.Min != nil {
		out["min"] = *dflt.Min
	}
	if dflt.Max != nil {
		out["max"] = *dflt.Max
	}
	// Grafana's steps start at a base (null) value: the next two are warn and crit.
	var th []float64
	for _, s := range dflt.Thresholds.Steps {
		if s.Value != nil {
			th = append(th, *s.Value)
		}
	}
	if len(th) > 0 {
		out["warn"] = th[0]
	}
	if len(th) > 1 {
		out["crit"] = th[1]
	}
	if m := dflt.Custom.Stacking.Mode; m != "" && m != "none" {
		out["stack"] = true
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

var threshold = regexp.MustCompile(`^(?s)\s*(.+?)\s*(>=|<=|>|<)\s*(-?[0-9.eE+]+)\s*$`)

// PrometheusRules turns alerting rules into alerts on the metrics component ${METRICS} (default
// prometheus): `expr > n` keeps
// its threshold (warn, or crit for severity critical); any other expression alerts while it returns
// series, as count(expr) >= 1. Recording rules are left out.
func PrometheusRules(path, _ string) (map[string]any, error) {
	files, err := yamlFiles(path, ".yml", ".yaml")
	if err != nil {
		return nil, err
	}
	var alerts []map[string]any
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Groups []struct {
				Rules []struct {
					Alert  string            `yaml:"alert"`
					Expr   string            `yaml:"expr"`
					Labels map[string]string `yaml:"labels"`
				} `yaml:"rules"`
			} `yaml:"groups"`
			Spec *struct {
				Groups yaml.Node `yaml:"groups"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if doc.Spec != nil {
			// a PrometheusRule custom resource keeps the same groups under spec
			if err := doc.Spec.Groups.Decode(&doc.Groups); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
		}
		for _, g := range doc.Groups {
			for _, r := range g.Rules {
				if r.Alert == "" {
					continue
				}
				a := map[string]any{"name": r.Alert, "source": "${METRICS:-prometheus}"}
				level := "warn"
				if r.Labels["severity"] == "critical" {
					level = "crit"
				}
				if m := threshold.FindStringSubmatch(r.Expr); m != nil && !strings.ContainsAny(m[1], "<>") {
					n, err := strconv.ParseFloat(m[3], 64)
					if err != nil {
						continue
					}
					a["query"], a[level] = m[1], n
					if strings.HasPrefix(m[2], "<") {
						a["below"] = true
					}
				} else {
					a["query"], a[level] = "count("+strings.TrimSpace(r.Expr)+")", 1
				}
				alerts = append(alerts, a)
			}
		}
	}
	return map[string]any{"alerts": alerts}, nil
}

// OpenAPI turns an OpenAPI 3 or Swagger 2 spec into an http query component named after the API and
// a query per GET operation; path parameters become {{params}} given on each run. The address is
// ${<API>_URL}, defaulting to the spec's first server.
func OpenAPI(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Info struct {
			Title string `yaml:"title"`
		} `yaml:"info"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
		Host     string `yaml:"host"`
		BasePath string `yaml:"basePath"`
		Paths    map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			Summary     string `yaml:"summary"`
			Tags        []string
		} `yaml:"paths"`
	}
	// YAML is a superset of JSON, so one decoder reads both.
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	name := slug(or(doc.Info.Title, "api"))
	base := ""
	if len(doc.Servers) > 0 {
		base = doc.Servers[0].URL
	} else if doc.Host != "" {
		base = "http://" + doc.Host + doc.BasePath
	}
	comp := map[string]any{"type": "http", "kind": "query", "addr": "${" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_URL:-" + base + "}"}
	if base == "" {
		comp["addr"] = "${" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_URL}"
	}
	queries := map[string]any{}
	for p, ops := range doc.Paths {
		op, ok := ops["get"]
		if !ok {
			continue
		}
		qname := slug(or(op.OperationID, "get "+p))
		q := pathParam.ReplaceAllStringFunc(p, func(m string) string { return "{{" + m[1:len(m)-1] + "}}" })
		e := map[string]any{"source": name, "query": "GET " + q, "group": name}
		if len(op.Tags) > 0 {
			e["group"] = name + "/" + op.Tags[0]
		}
		if op.Summary != "" {
			e["help"] = op.Summary
		}
		queries[qname] = e
	}
	return map[string]any{"components": map[string]any{name: comp}, "queries": queries}, nil
}

var pathParam = regexp.MustCompile(`\{[^}/]+\}`)
