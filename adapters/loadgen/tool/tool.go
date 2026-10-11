// Package tool drives an external load tool (k6, vegeta, ghz) as a rig load generator: the tool runs
// as a child process at the current rate and its per-request results stream back into the counters.
package tool

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goxang/rig-plugins/adapters/loadgen"
	"github.com/goxang/rig-plugins/internal/sh"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	for name, t := range tools {
		plugin.Register(core.KindLoad, name, t.desc, func(env core.Env, c *spec.Component) (any, error) { return New(name, env, c) })
	}
}

type Options struct {
	Target  string            `yaml:"target"`
	Method  string            `yaml:"method"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
	Rate    float64           `yaml:"rate"`
	// k6
	Script string `yaml:"script"`
	VUs    int    `yaml:"vus"`
	// ghz
	Proto    string   `yaml:"proto"`
	Call     string   `yaml:"call"`
	Data     string   `yaml:"data"`
	Insecure bool     `yaml:"insecure"`
	Args     []string `yaml:"args"`
}

type tool struct {
	desc string
	// argv builds the command for one run at rate rps; a run of a tool that cannot change its rate
	// live is restarted when the rate changes.
	argv func(o *Options, url string, rps float64, tmp string) ([]string, error)
	read func(r io.Reader, g *Gen)
	// rounds tools exit on their own after a short run, and are simply run again.
	rounds bool
}

var tools = map[string]tool{
	"vegeta": {desc: "vegeta at a constant rate (vegeta on PATH)", argv: vegetaArgv, read: vegetaRead},
	"k6":     {desc: "k6 script capped at the rate (k6 on PATH)", argv: k6Argv, read: k6Read},
	"ghz":    {desc: "ghz gRPC load in 5s rounds (ghz on PATH)", argv: ghzArgv, read: ghzRead, rounds: true},
}

type Gen struct {
	name string
	t    tool
	opt  Options
	env  core.Env
	rate loadgen.Rate

	mu      sync.Mutex
	cancel  context.CancelFunc
	restart context.CancelFunc
	running bool
	lastErr string

	sent, failed atomic.Int64
	lat          *loadgen.Reservoir
}

func New(name string, env core.Env, c *spec.Component) (any, error) {
	g := &Gen{name: name, t: tools[name], env: env, lat: loadgen.NewReservoir(4096)}
	if err := c.Decode(&g.opt); err != nil {
		return nil, err
	}
	if g.opt.Target == "" && g.opt.Script == "" {
		return nil, fmt.Errorf("%s load generator needs target", name)
	}
	if name == "k6" && g.opt.Script == "" {
		return nil, fmt.Errorf("k6 load generator needs script")
	}
	if name == "ghz" && g.opt.Call == "" {
		return nil, fmt.Errorf("ghz load generator needs call")
	}
	g.rate.Set(g.opt.Rate)
	return g, nil
}

func (g *Gen) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return nil
	}
	if _, err := exec.LookPath(g.name); err != nil {
		return fmt.Errorf("%s not found on PATH", g.name)
	}
	url := ""
	if g.opt.Target != "" {
		u, err := g.env.Resolve(ctx, g.opt.Target)
		if err != nil {
			return err
		}
		url = u
	}
	runCtx, cancel := context.WithCancel(context.Background())
	g.cancel, g.running, g.lastErr = cancel, true, ""
	go g.loop(runCtx, url)
	return nil
}

func (g *Gen) Stop(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
	g.running = false
	return nil
}

func (g *Gen) Close() error { return g.Stop(context.Background()) }

func (g *Gen) SetRate(_ context.Context, rps float64) error {
	if g.rate.Get() == rps {
		return nil
	}
	g.rate.Set(rps)
	g.mu.Lock()
	if g.restart != nil && !g.t.rounds {
		g.restart()
	}
	g.mu.Unlock()
	return nil
}

func (g *Gen) Status(context.Context) (core.LoadStatus, error) {
	g.mu.Lock()
	running, lastErr := g.running, g.lastErr
	g.mu.Unlock()
	st := core.LoadStatus{Running: running, Rate: g.rate.Get(), Sent: g.sent.Load(), Failed: g.failed.Load()}
	st.Latency = g.lat.Percentiles()
	st.Extra = map[string]string{"tool": g.name, "target": g.opt.Target}
	if lastErr != "" {
		st.Extra["error"] = lastErr
	}
	return st, nil
}

func (g *Gen) loop(ctx context.Context, url string) {
	tmp, err := os.MkdirTemp("", "rig-"+g.name)
	if err != nil {
		g.fail(err)
		return
	}
	defer os.RemoveAll(tmp)
	for ctx.Err() == nil {
		rps := g.rate.Get()
		if rps <= 0 {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		start := time.Now()
		if err := g.run(ctx, url, rps, tmp); err != nil && ctx.Err() == nil {
			g.fail(err)
		}
		if d := time.Since(start); d < time.Second && ctx.Err() == nil {
			time.Sleep(time.Second - d) // a tool that dies at once must not spin
		}
	}
}

func (g *Gen) fail(err error) {
	g.mu.Lock()
	g.lastErr = err.Error()
	g.mu.Unlock()
}

func (g *Gen) run(ctx context.Context, url string, rps float64, tmp string) error {
	argv, err := g.t.argv(&g.opt, url, rps, tmp)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g.mu.Lock()
	g.restart = cancel
	g.mu.Unlock()

	cmd := exec.CommandContext(runCtx, argv[0], append(argv[1:], g.opt.Args...)...)
	cmd.Dir = g.env.Project().Dir
	sh.OwnGroup(cmd)
	cmd.Cancel = func() error { sh.KillGroup(cmd); return nil }
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	g.t.read(out, g)
	if err := cmd.Wait(); err != nil && runCtx.Err() == nil {
		return fmt.Errorf("%s: %v %s", g.name, err, lastLine(stderr.String()))
	}
	return nil
}

func (g *Gen) record(lat time.Duration, failed bool) {
	g.sent.Add(1)
	if failed {
		g.failed.Add(1)
	}
	if lat > 0 {
		g.lat.Add(lat)
	}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndexByte(s, '\n')+1:]
}

func lines(r io.Reader, f func([]byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		f(sc.Bytes())
	}
}

func vegetaArgv(o *Options, url string, rps float64, tmp string) ([]string, error) {
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	target := cmp.Or(o.Method, "GET") + " " + url + "\n"
	for k, v := range o.Headers {
		target += k + ": " + v + "\n"
	}
	if o.Body != "" {
		body := filepath.Join(tmp, "body")
		if err := os.WriteFile(body, []byte(o.Body), 0o600); err != nil {
			return nil, err
		}
		target += "@" + body + "\n"
	}
	targets := filepath.Join(tmp, "targets")
	if err := os.WriteFile(targets, []byte(target), 0o600); err != nil {
		return nil, err
	}
	// ponytail: a shell pipe, vegeta attack writes gob and only encode turns it into lines
	script := fmt.Sprintf("vegeta attack -rate=%g/1s -duration=0 -targets=%s \"$@\" | vegeta encode --to json", rps, shq(targets))
	return []string{"sh", "-c", script, "vegeta"}, nil
}

func vegetaRead(r io.Reader, g *Gen) {
	lines(r, func(b []byte) {
		var res struct {
			Code    int    `json:"code"`
			Latency int64  `json:"latency"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(b, &res) == nil {
			g.record(time.Duration(res.Latency), res.Error != "" || res.Code == 0 || res.Code >= 400)
		}
	})
}

// ponytail: k6 is a closed model, --rps only caps it; enough vus must exist to reach the rate
func k6Argv(o *Options, url string, rps float64, _ string) ([]string, error) {
	argv := []string{"k6", "run", "--quiet", "--no-summary", "--out", "json=-", "--rps", fmt.Sprint(int(rps + .5)),
		"--vus", fmt.Sprint(cmp.Or(o.VUs, 50)), "--duration", "24h"}
	if url != "" {
		argv = append(argv, "-e", "TARGET="+url)
	}
	return append(argv, o.Script), nil
}

func k6Read(r io.Reader, g *Gen) {
	lines(r, func(b []byte) {
		var p struct {
			Type   string `json:"type"`
			Metric string `json:"metric"`
			Data   struct {
				Value float64 `json:"value"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &p) != nil || p.Type != "Point" {
			return
		}
		switch p.Metric {
		case "http_reqs":
			g.sent.Add(1)
		case "http_req_failed":
			if p.Data.Value != 0 {
				g.failed.Add(1)
			}
		case "http_req_duration":
			g.lat.Add(time.Duration(p.Data.Value * float64(time.Millisecond)))
		}
	})
}

func ghzArgv(o *Options, url string, rps float64, _ string) ([]string, error) {
	argv := []string{"ghz", "--call", o.Call, "--rps", fmt.Sprint(int(rps + .5)), "-z", "5s", "-O", "json"}
	if o.Proto != "" {
		argv = append(argv, "--proto", o.Proto)
	}
	if o.Data != "" {
		argv = append(argv, "-d", o.Data)
	}
	if o.Insecure {
		argv = append(argv, "--insecure")
	}
	if len(o.Headers) > 0 {
		md, _ := json.Marshal(o.Headers)
		argv = append(argv, "-m", string(md))
	}
	return append(argv, strings.TrimPrefix(strings.TrimPrefix(url, "grpc://"), "http://")), nil
}

func ghzRead(r io.Reader, g *Gen) {
	var rep struct {
		Details []struct {
			Latency int64  `json:"latency"`
			Status  string `json:"status"`
		} `json:"details"`
	}
	if json.NewDecoder(r).Decode(&rep) != nil {
		return
	}
	for _, d := range rep.Details {
		g.record(time.Duration(d.Latency), d.Status != "OK")
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
