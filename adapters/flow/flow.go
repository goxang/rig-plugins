// Package flow is a live diagram of what moves through a system: edges between its parts with rate,
// errors and latency, read as PromQL from the project's metrics component, or simulated for a demo.
package flow

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

type Config struct {
	// Simulate makes the numbers up (every row says so), for a demo without metrics.
	Simulate bool `json:"simulate"`
	// Source is the metrics component to query (default the first).
	Source string `json:"source"`
	Nodes  []Node `json:"nodes"`
	Edges  []Edge `json:"edges"`
	Types  []Type `json:"types"`
	// Warn and Err are the error percentages an edge turns warn and err at (default 1 and 5).
	Warn float64 `json:"warn"`
	Err  float64 `json:"err"`
}

type Node struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// Edge's rate (per second), errors (per second) and latency (ms) are PromQL; Sim is the rate a simulation centres on.
type Edge struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Label   string  `json:"label"`
	Rate    string  `json:"rate"`
	Errors  string  `json:"errors"`
	Latency string  `json:"latency"`
	Sim     float64 `json:"sim"`
}

// Type is a transaction type for the summary: count today, errors today and p95 latency (ms) as PromQL.
type Type struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Count   string  `json:"count"`
	Errors  string  `json:"errors"`
	Latency string  `json:"latency"`
	Share   float64 `json:"share"`
}

// Row is an edge (from, to) or a node (node) of flow.graph.
type Row struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Node      string  `json:"node"`
	Label     string  `json:"label"`
	Value     float64 `json:"value" unit:"/s"`
	Errors    float64 `json:"errors" unit:"%"`
	Latency   float64 `json:"latency" unit:"ms"`
	Status    string  `json:"status"`
	Simulated bool    `json:"simulated"`
}

type Summary struct {
	Type      string  `json:"type"`
	Count     float64 `json:"count"`
	Success   float64 `json:"success" unit:"%"`
	P95       float64 `json:"p95" unit:"ms"`
	Status    string  `json:"status"`
	Simulated bool    `json:"simulated"`
}

func load(c *sdk.Ctx) (*Config, error) {
	var cfg Config
	b, _ := json.Marshal(c.Config().Config)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("plugins.flow: %w", err)
	}
	if len(cfg.Edges) == 0 {
		return nil, sdk.Unavailable("flow has no edges", "list plugins.flow.edges in rig.yaml: {from, to, rate, errors, latency}")
	}
	if cfg.Warn == 0 {
		cfg.Warn = 1
	}
	if cfg.Err == 0 {
		cfg.Err = 5
	}
	return &cfg, nil
}

func Register(p *sdk.Plugin) {
	p.Source("flow.graph", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		cfg, err := load(c)
		if err != nil {
			return nil, err
		}
		read := cfg.simulated(time.Now())
		if !cfg.Simulate {
			read = promql(c, cfg.Source)
		}
		return frames.Of(cfg.graph(read)), nil
	}, sdk.Describe("the flow as a graph: edges (from, to) with rate, error % and latency, and nodes with their status"))
	p.Source("flow.summary", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		cfg, err := load(c)
		if err != nil {
			return nil, err
		}
		read := cfg.simulated(time.Now())
		if !cfg.Simulate {
			read = promql(c, cfg.Source)
		}
		return frames.Of(cfg.summary(read)), nil
	}, sdk.Describe("per type: count today, success % and p95 latency"))
}

// reader answers one query (or, simulating, one named figure) with a number; ok is false when it cannot.
type reader func(kind, key, query string) (float64, bool)

func promql(c *sdk.Ctx, source string) reader {
	return func(_, _, q string) (float64, bool) {
		if q == "" {
			return 0, false
		}
		f, err := c.Query("metrics.query", sdk.Args{"query": q, "instant": true, "source": source})
		if err != nil {
			return 0, false
		}
		rows := orgkit.Rows(f)
		sum := 0.0
		for _, r := range rows {
			v, _ := strconv.ParseFloat(fmt.Sprint(r["value"]), 64)
			sum += v
		}
		return sum, len(rows) > 0
	}
}

func (cfg *Config) graph(read reader) []Row {
	rows := make([]Row, len(cfg.Edges))
	var wg sync.WaitGroup
	for i, e := range cfg.Edges {
		wg.Go(func() {
			key := e.From + ">" + e.To
			rate, ok := read("rate", key, e.Rate)
			errs, _ := read("errors", key, e.Errors)
			lat, _ := read("latency", key, e.Latency)
			r := Row{From: e.From, To: e.To, Label: e.Label, Value: round(rate), Latency: round(lat), Simulated: cfg.Simulate}
			if rate > 0 {
				r.Errors = round(100 * errs / rate)
			}
			r.Status = cfg.status(r.Errors, ok)
			rows[i] = r
		})
	}
	wg.Wait()
	names := []string{}
	for _, e := range cfg.Edges {
		for _, n := range []string{e.From, e.To} {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	for _, name := range names {
		n := Row{Node: name, Label: name, Status: "ok", Simulated: cfg.Simulate}
		if i := slices.IndexFunc(cfg.Nodes, func(x Node) bool { return x.Name == name }); i >= 0 && cfg.Nodes[i].Label != "" {
			n.Label = cfg.Nodes[i].Label
		}
		in, out := 0.0, 0.0
		for _, e := range rows[:len(cfg.Edges)] {
			if e.To == name {
				in += e.Value
				n.Status = worst(n.Status, e.Status)
			}
			if e.From == name {
				out += e.Value
			}
		}
		n.Value = round(max(in, out))
		rows = append(rows, n)
	}
	return rows
}

func (cfg *Config) summary(read reader) []Summary {
	out := make([]Summary, len(cfg.Types))
	for i, t := range cfg.Types {
		count, ok := read("count", t.Name, t.Count)
		errs, _ := read("errors", t.Name, t.Errors)
		p95, _ := read("latency", t.Name, t.Latency)
		s := Summary{Type: first(t.Label, t.Name), Count: math.Round(count), P95: round(p95), Success: 100, Simulated: cfg.Simulate}
		if count > 0 {
			s.Success = round(100 - 100*errs/count)
		}
		s.Status = cfg.status(100-s.Success, ok)
		out[i] = s
	}
	return out
}

func (cfg *Config) status(errPct float64, ok bool) string {
	switch {
	case !ok:
		return "unknown"
	case errPct >= cfg.Err:
		return "err"
	case errPct >= cfg.Warn:
		return "warn"
	}
	return "ok"
}

var order = []string{"ok", "unknown", "warn", "err"}

func worst(a, b string) string {
	if slices.Index(order, b) > slices.Index(order, a) {
		return b
	}
	return a
}

// simulated makes figures that move smoothly with the time of day and differ per edge or type,
// with a short error burst now and then.
func (cfg *Config) simulated(now time.Time) reader {
	day := float64(now.Hour()*3600+now.Minute()*60+now.Second()) / 86400
	busy := 0.35 + 0.65*math.Pow(math.Sin(math.Pi*day), 2)
	return func(kind, key, _ string) (float64, bool) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(key))
		seed := float64(h.Sum32()%1000) / 1000
		wave := 1 + 0.08*math.Sin(float64(now.Unix())/17+seed*40)
		burst := 0.0
		if int(now.Unix()/60+int64(seed*97))%23 == 0 {
			burst = 0.06
		}
		base := 40.0
		if i := slices.IndexFunc(cfg.Edges, func(e Edge) bool { return e.From+">"+e.To == key }); i >= 0 && cfg.Edges[i].Sim > 0 {
			base = cfg.Edges[i].Sim
		}
		share := 1.0 / float64(max(len(cfg.Types), 1))
		if i := slices.IndexFunc(cfg.Types, func(t Type) bool { return t.Name == key }); i >= 0 && cfg.Types[i].Share > 0 {
			share = cfg.Types[i].Share
		}
		rate := base * busy * wave
		switch kind {
		case "rate":
			return rate, true
		case "errors":
			if slices.ContainsFunc(cfg.Types, func(t Type) bool { return t.Name == key }) {
				total, _ := cfg.simulated(now)("count", key, "")
				return total * (0.004 + 0.01*seed + burst), true
			}
			return rate * (0.002 + 0.008*seed + burst), true
		case "latency":
			return (60 + 140*seed) * (0.9 + 0.3*busy) * wave, true
		case "count":
			total := 0.0
			for _, e := range cfg.Edges {
				if !slices.ContainsFunc(cfg.Edges, func(x Edge) bool { return x.To == e.From }) {
					total += first64(e.Sim, base)
				}
			}
			return total * share * 86400 * day * 0.7, true
		}
		return 0, false
	}
}

func first64(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}

func round(v float64) float64 { return math.Round(v*10) / 10 }

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
