package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// tools are the helpers declarative plugins run as `rig-std tool <name>`: they print ndjson.
var tools = map[string]func(args []string) error{
	"usage":      toolUsage,
	"plugintree": toolPluginTree,
}

type proc struct {
	pid, ppid int
	mb, cpu   float64
	up, cmd   string
}

func psTable() (map[int]*proc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,pcpu=,etime=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	procs := map[int]*proc{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		p := &proc{up: f[4], cmd: strings.Join(f[5:], " ")}
		p.pid, _ = strconv.Atoi(f[0])
		p.ppid, _ = strconv.Atoi(f[1])
		rss, _ := strconv.ParseFloat(f[2], 64)
		p.mb = rss / 1024
		p.cpu, _ = strconv.ParseFloat(f[3], 64)
		procs[p.pid] = p
	}
	return procs, nil
}

func (p *proc) exe() string {
	f := strings.Fields(p.cmd)
	if len(f) == 0 {
		return ""
	}
	return filepath.Base(f[0])
}

// rigPID walks up from this process to the rig that started it.
func rigPID(procs map[int]*proc) int {
	pid := os.Getppid()
	for procs[pid] != nil {
		if procs[pid].exe() == "rig" {
			return pid
		}
		pid = procs[pid].ppid
	}
	return os.Getppid()
}

func childrenOf(procs map[int]*proc) map[int][]int {
	kids := map[int][]int{}
	for _, p := range procs {
		kids[p.ppid] = append(kids[p.ppid], p.pid)
	}
	return kids
}

func subtree(kids map[int][]int, pid int) []int {
	var out []int
	for stack := []int{pid}; len(stack) > 0; {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, x)
		stack = append(stack, kids[x]...)
	}
	return out
}

func emit(v any) {
	raw, _ := json.Marshal(v)
	fmt.Println(string(raw))
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

var (
	stdOwner    = regexp.MustCompile(`rig-std\s+([a-z0-9-]+)`)
	pluginOwner = regexp.MustCompile(`/plugins/([a-z0-9-]+)/`)
)

func toolUsage([]string) error {
	procs, err := psTable()
	if err != nil {
		return err
	}
	rig := rigPID(procs)
	kids := childrenOf(procs)
	type group struct {
		name    string
		n       int
		cpu, mb float64
		up      string
	}
	groups := map[string]*group{}
	var order []string
	for _, top := range kids[rig] {
		p := procs[top]
		name := "other"
		if m := stdOwner.FindStringSubmatch(p.cmd); m != nil {
			name = m[1]
		} else if m := pluginOwner.FindStringSubmatch(p.cmd); m != nil {
			name = m[1]
		} else {
			name = "task"
		}
		for _, x := range subtree(kids, top) {
			g := groups[name]
			if g == nil {
				g = &group{name: name, up: procs[x].up}
				groups[name] = g
				order = append(order, name)
			}
			g.n++
			g.cpu += procs[x].cpu
			g.mb += procs[x].mb
		}
	}
	rows := []*group{}
	if me := procs[rig]; me != nil {
		rows = append(rows, &group{"rig", 1, me.cpu, me.mb, me.up})
	}
	list := make([]*group, 0, len(order))
	for _, n := range order {
		list = append(list, groups[n])
	}
	slices.SortStableFunc(list, func(a, b *group) int {
		switch {
		case a.mb > b.mb:
			return -1
		case a.mb < b.mb:
			return 1
		}
		return 0
	})
	rows = append(rows, list...)
	total := &group{name: "total"}
	for _, r := range rows {
		total.n += r.n
		total.cpu += r.cpu
		total.mb += r.mb
	}
	for _, r := range append(rows, total) {
		emit(struct {
			Name  string  `json:"name"`
			Procs int     `json:"procs"`
			CPU   float64 `json:"cpu %"`
			MB    float64 `json:"memory MB"`
			Up    string  `json:"up"`
		}{r.name, r.n, round1(r.cpu), round1(r.mb), r.up})
	}
	return nil
}

type pluginInfo struct {
	name, version, kind, state, dir string
	requires                        []string
	talks, use                      string
	pids                            []int
	mb, cpu                         float64
}

var (
	reRequires = regexp.MustCompile(`(?m)^requires:[ \t]*(\{.*\})?[ \t]*\n((?:[ \t]+\S.*\n?)*)`)
	reName     = regexp.MustCompile(`([a-z][a-z0-9-]*)\s*:`)
	reTrail    = regexp.MustCompile(`\s+\(.*\)$`)
)

func listPlugins(rigExe string) []*pluginInfo {
	args := []string{}
	if f := os.Getenv("RIG_FILE"); f != "" {
		args = append(args, "-f", f)
	}
	if e := os.Getenv("RIG_ENV"); e != "" {
		args = append(args, "-e", e)
	}
	cmd := exec.Command(rigExe, append(args, "plugin", "ls")...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, _ := cmd.Output()
	var rows []*pluginInfo
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[min(1, len(lines)):] {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		rest := strings.Join(f[4:], " ")
		rows = append(rows, &pluginInfo{name: f[0], version: f[1], kind: f[2], state: f[3], dir: reTrail.ReplaceAllString(rest, "")})
	}
	return rows
}

func readManifest(p *pluginInfo) {
	raw, err := os.ReadFile(filepath.Join(p.dir, "rig-plugin.yaml"))
	if err != nil {
		p.talks = "?"
		return
	}
	text := string(raw)
	has := func(pattern string) bool { return regexp.MustCompile(pattern).MatchString(text) }
	if m := reRequires.FindStringSubmatch(text); m != nil {
		body := m[1]
		if body == "" {
			body = m[2]
		}
		for _, r := range reName.FindAllStringSubmatch(body, -1) {
			p.requires = append(p.requires, r[1])
		}
	}
	switch {
	case has(`(?m)^server:`):
		p.talks = "JSON-RPC over stdio"
	case has(`(?m)^mcp:`):
		p.talks = "MCP over stdio"
	case has(`(?m)^\s+run:`):
		p.talks = "scripts (run:)"
	case len(p.requires) > 0 && !has(`(?m)^(views|sources|actions):`):
		p.talks = "pack (only requires)"
	default:
		p.talks = "yaml only"
	}
}

func toolPluginTree(args []string) error {
	mode := "tree"
	if len(args) > 0 {
		mode = args[0]
	}
	procs, err := psTable()
	if err != nil {
		return err
	}
	jp := rigPID(procs)
	rigExe, err := exec.LookPath("rig")
	if err != nil {
		rigExe = "rig"
		if p := procs[jp]; p != nil {
			rigExe = strings.Fields(p.cmd)[0]
		}
	}
	list := listPlugins(rigExe)
	by := map[string]*pluginInfo{}
	for _, p := range list {
		by[p.name] = p
		readManifest(p)
	}
	kids := childrenOf(procs)
	for _, c := range kids[jp] {
		cmd := procs[c].cmd
		for _, p := range list {
			if regexp.MustCompile(`rig-std\s+`+regexp.QuoteMeta(p.name)+`(\s|$)`).MatchString(cmd) || p.dir != "" && strings.Contains(cmd, p.dir) {
				p.pids = append(p.pids, subtree(kids, c)...)
			}
		}
	}
	for _, p := range list {
		slices.Sort(p.pids)
		p.pids = slices.Compact(p.pids)
		for _, q := range p.pids {
			p.mb += procs[q].mb
			p.cpu += procs[q].cpu
		}
		switch {
		case p.state != "on":
			p.use = "off"
		case len(p.pids) > 0:
			p.use = "running"
		default:
			p.use = "idle"
		}
	}
	if mode == "edges" {
		for _, p := range list {
			if p.state == "on" {
				emit(map[string]string{"from": "rig", "to": p.name, "label": p.talks})
			}
			for _, r := range p.requires {
				emit(map[string]string{"from": p.name, "to": r, "label": "requires"})
			}
		}
		return nil
	}

	type row struct {
		ID      string `json:"id"`
		Parent  string `json:"parent"`
		Name    string `json:"name"`
		Use     string `json:"use"`
		Talks   string `json:"talks"`
		Kind    string `json:"kind"`
		PID     string `json:"pid"`
		CPU     any    `json:"cpu"`
		Mem     string `json:"mem"`
		Version string `json:"version"`
		Dir     string `json:"dir"`
	}
	required := map[string]bool{}
	for _, p := range list {
		for _, r := range p.requires {
			required[r] = true
		}
	}
	var rows []row
	rig := procs[jp]
	if rig == nil {
		rig = &proc{pid: jp}
	}
	own := 0.0
	for _, q := range subtree(kids, jp) {
		if procs[q] != nil {
			own += procs[q].mb
		}
	}
	rows = append(rows, row{ID: "rig", Name: "rig", Use: "running", Talks: "the host", Kind: "host", PID: strconv.Itoa(jp),
		CPU: round1(rig.cpu), Mem: fmt.Sprintf("%.0f MB (+children %.0f)", rig.mb, own-rig.mb)})
	var add func(name, parent string, seen []string)
	add = func(name, parent string, seen []string) {
		rid := parent + "/" + name
		p := by[name]
		if p == nil {
			rows = append(rows, row{ID: rid, Parent: parent, Name: name, Use: "missing", Talks: "?", CPU: ""})
			return
		}
		r := row{ID: rid, Parent: parent, Name: name, Use: p.use, Talks: p.talks, Kind: p.kind, CPU: "", Version: p.version, Dir: p.dir}
		if len(p.pids) > 0 {
			var ids []string
			for _, q := range p.pids[:min(3, len(p.pids))] {
				ids = append(ids, strconv.Itoa(q))
			}
			r.PID, r.CPU, r.Mem = strings.Join(ids, ","), round1(p.cpu), fmt.Sprintf("%.0f MB", p.mb)
		}
		rows = append(rows, r)
		for _, q := range p.requires {
			if !slices.Contains(seen, q) {
				add(q, rid, append(slices.Clone(seen), name))
			}
		}
	}
	for _, p := range list {
		if !required[p.name] {
			add(p.name, "rig", []string{p.name})
		}
	}
	for _, r := range rows {
		emit(r)
	}
	return nil
}
