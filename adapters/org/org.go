// Package org is unit/1: a project as an organisational unit (a team, a division, a company) that
// reports KPIs, people figures and items to the unit above, takes requests from it, and rolls up
// the units below it, which it reads over their `rig serve` with a token each.
package org

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig/sdk"
)

// Config is plugins.org in rig.yaml.
type Config struct {
	Unit        string   `json:"unit"`
	Title       string   `json:"title"`
	Layer       int      `json:"layer"`
	Parent      string   `json:"parent"`
	Owner       string   `json:"owner"`
	Description string   `json:"description"`
	KPIs        []Metric `json:"kpis"`
	People      []Metric `json:"people"`
	Items       []Feed   `json:"items"`
	// Children are the units below, each a rig serve this unit reads with a token.
	Children map[string]Child `json:"children"`
	// Escalate is the lowest severity of a child's item that shows here too (default high).
	Escalate string `json:"escalate"`
	// Cache is how long a child's answer is kept (default 60s); a child that is down shows its last one.
	Cache string `json:"cache"`
	// Drill lists the roles that may read a child's own figures through this unit; a local caller always may.
	Drill []string `json:"drill"`
	// Expose is what the units above may read or run here, each item to the roles in its to.
	Expose []Exposure `json:"expose"`
}

// Feed turns rows of a local source (incidents, risks in the tracker) into items.
type Feed struct {
	Kind      string            `json:"kind"`
	Source    string            `json:"source"`
	Args      map[string]any    `json:"args"`
	Transform any               `json:"transform"`
	Map       map[string]string `json:"map"`
	Severity  string            `json:"severity"`
}

type Child struct {
	URL         string `json:"url"`
	Token       string `json:"token"`
	Env         string `json:"env"`
	Fingerprint string `json:"fingerprint"`
	// Plugin is the name the child serves unit/1 under (default org).
	Plugin string `json:"plugin"`
}

type KPI struct {
	Unit    string    `json:"unit"`
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Value   float64   `json:"value"`
	Measure string    `json:"measure"`
	Target  string    `json:"target"`
	Status  string    `json:"status"`
	Updated time.Time `json:"updated"`
	Note    string    `json:"note"`
}

type Item struct {
	Unit     string    `json:"unit"`
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Title    string    `json:"title"`
	Severity string    `json:"severity"`
	Status   string    `json:"status"`
	Owner    string    `json:"owner"`
	Updated  time.Time `json:"updated"`
	Ref      string    `json:"ref"`
}

type Request struct {
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	By      string    `json:"by"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Status  string    `json:"status"`
	Answer  string    `json:"answer"`
	Due     string    `json:"due"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type Info struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Layer       int    `json:"layer"`
	Parent      string `json:"parent"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
}

type ChildRow struct {
	Name         string    `json:"name"`
	Title        string    `json:"title"`
	Layer        int       `json:"layer"`
	Reachable    bool      `json:"reachable"`
	OK           int       `json:"ok"`
	Warn         int       `json:"warn"`
	Err          int       `json:"err"`
	High         int       `json:"high"`
	OpenRequests int       `json:"open_requests"`
	Updated      time.Time `json:"updated"`
	Error        string    `json:"error"`
}

var severities = []string{"low", "medium", "high", "critical"}

func rank(severity string) int { return slices.Index(severities, strings.ToLower(severity)) }

func load(c *sdk.Ctx) (*Config, error) {
	var cfg Config
	b, _ := json.Marshal(c.Config().Config)
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("plugins.org: %w", err)
	}
	if cfg.Unit == "" {
		cfg.Unit = filepath.Base(c.Call.Project)
	}
	if cfg.Unit == "" || cfg.Unit == "." {
		return nil, sdk.Unavailable("this unit has no name", "set plugins.org.unit in rig.yaml")
	}
	if cfg.Escalate == "" {
		cfg.Escalate = "high"
	}
	return &cfg, nil
}

func (cfg *Config) info() Info {
	return Info{Name: cfg.Unit, Title: first(cfg.Title, cfg.Unit), Layer: cfg.Layer, Parent: cfg.Parent, Owner: cfg.Owner, Description: cfg.Description}
}

type childArg struct {
	Child string `json:"child" desc:"a child unit: its own figures instead of this unit's"`
}

type requestArgs struct {
	To    string `json:"to" desc:"the child unit to ask; empty records it here"`
	Title string `json:"title" rig:"required"`
	Body  string `json:"body"`
	Due   string `json:"due"`
	From  string `json:"from" desc:"the asking unit (a parent fills it)"`
	By    string `json:"by" desc:"the person behind the request"`
}

type answerArgs struct {
	ID     string `json:"id" rig:"required"`
	Status string `json:"status" rig:"required" desc:"accepted, answered, declined or done"`
	Answer string `json:"answer"`
}

type itemArgs struct {
	ID       string `json:"id" desc:"an existing item to update"`
	Kind     string `json:"kind" rig:"required" desc:"risk, blocker, decision, incident, highlight"`
	Title    string `json:"title" rig:"required"`
	Severity string `json:"severity" desc:"low, medium, high or critical"`
	Status   string `json:"status" desc:"open (default) or closed"`
	Owner    string `json:"owner"`
	Ref      string `json:"ref"`
}

// ponytail: mu orders read-modify-write within one plugin process; a CLI and a serve on the same project can still race, a state compare-and-set would close it
var mu sync.Mutex

func Register(p *sdk.Plugin) {
	p.Emits("unit.request", "unit.answered")
	p.Source("unit.info", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		cfg, err := load(c)
		if err != nil {
			return nil, err
		}
		return frames.Of([]Info{cfg.info()}), nil
	}, sdk.Describe("this unit: name, title, layer, parent, owner"))
	p.Source("unit.kpis", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		return metrics(c, a.Child, "unit.kpis", func(cfg *Config) []Metric { return cfg.KPIs })
	}, sdk.Describe("delivery KPIs with their targets and status (ok, warn, err)"))
	p.Source("unit.people", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		return metrics(c, a.Child, "unit.people", func(cfg *Config) []Metric { return cfg.People })
	}, sdk.Describe("people figures at unit level (headcount, open roles, load): never per person"))
	p.Source("unit.items", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		if a.Child != "" {
			return n.childFrame(a.Child, "unit.items", nil)
		}
		return frames.Of(n.items()), nil
	}, sdk.Describe("risks, blockers, decisions, incidents and highlights, with the children's escalated ones"))
	p.Source("unit.requests", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return frames.Of(n.requests()), nil
	}, sdk.Describe("requests this unit got from above and the ones it sent below, with their answers"))
	p.Source("unit.children", func(c *sdk.Ctx, _ sdk.Args) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return frames.Of(n.children()), nil
	}, sdk.Describe("the units below: reachable, KPI status counts, escalated items, open requests"))

	p.Source("unit.exposed", func(c *sdk.Ctx, a childArg) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		rows, err := n.exposed(a.Child)
		if err != nil {
			return nil, err
		}
		return frames.Of(rows), nil
	}, sdk.Describe("what this unit exposes to the caller; with child (a name or *) what the units below expose to this one"))
	p.Source("unit.show", func(c *sdk.Ctx, a sdk.Args) (*sdk.Frame, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		f, err := n.show(a)
		if err != nil {
			return nil, err
		}
		return convert[sdk.Frame](f), nil
	}, sdk.Describe("read an exposed source by name, with its declared inputs"), sdk.Arguments(exposeArgs))

	p.Action("unit.invoke", sdk.Write, func(c *sdk.Ctx, a sdk.Args) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.invoke(a)
	}, sdk.Describe("run an exposed action by name, with its declared inputs only"), sdk.Arguments(exposeArgs))
	p.Action("unit.request", sdk.Write, func(c *sdk.Ctx, a requestArgs) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.request(a)
	}, sdk.Describe("ask a child unit (to) for something, or record a request from above here"))
	p.Action("unit.answer", sdk.Write, func(c *sdk.Ctx, a answerArgs) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.answer(a)
	}, sdk.Describe("accept, answer, decline or close a request this unit got"))
	p.Action("unit.item", sdk.Write, func(c *sdk.Ctx, a itemArgs) (*sdk.Result, error) {
		n, err := open(c)
		if err != nil {
			return nil, err
		}
		return n.item(a)
	}, sdk.Describe("record or update a risk, blocker, decision, incident or highlight"))
}

func metrics(c *sdk.Ctx, child, source string, of func(*Config) []Metric) (*sdk.Frame, error) {
	n, err := open(c)
	if err != nil {
		return nil, err
	}
	if child != "" {
		return n.childFrame(child, source, nil)
	}
	ms := of(n.cfg)
	out := make([]KPI, len(ms))
	var wg sync.WaitGroup
	for i, m := range ms {
		wg.Go(func() { out[i] = n.eval(m, source) })
	}
	wg.Wait()
	return frames.Of(out), nil
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
