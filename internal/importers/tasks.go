// Package importers turns other tools' files into rig.yaml fragments: task runners into tasks, CI jobs
// into tasks, Procfiles into services, dotenv files into vars, Grafana dashboards into dashboards,
// Prometheus rules into alerts and OpenAPI specs into queries.
package importers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type task struct {
	Help  string   `json:"help,omitempty"`
	Group string   `json:"group,omitempty"`
	Steps []string `json:"steps"`
}

func tasks(ts map[string]task) map[string]any {
	if len(ts) == 0 {
		return map[string]any{}
	}
	return map[string]any{"tasks": ts}
}

// shell escapes rig's own ${...} expansion out of a command: a task step writes $$ for a shell $.
func shell(s string) string { return strings.ReplaceAll(s, "$", "$$") }

var (
	justRecipe = regexp.MustCompile(`^@?([A-Za-z_][\w-]*)\b[^:]*:([^=]|$)`)
	justSkip   = regexp.MustCompile(`^(set|alias|export|import|mod)\s|:=`)
)

// Just reads a justfile's recipes; a # comment right above one is its help. Private recipes (_x) are left out.
func Just(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]task{}
	help := ""
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
			help = strings.TrimSpace(strings.TrimLeft(line, "#"))
			continue
		case line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '[' || justSkip.MatchString(line):
		default:
			if m := justRecipe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "_") {
				out[m[1]] = task{Help: help, Group: "just", Steps: []string{"just " + m[1] + ` "$$@"`}}
			}
		}
		help = ""
	}
	return tasks(out), sc.Err()
}

var makeTarget = regexp.MustCompile(`^([A-Za-z0-9][\w. -]*?)\s*:([^=].*|)$`)

// Make reads a Makefile's explicit targets; `target: ## help` (the self-documenting idiom) or a # comment above is its help.
func Make(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]task{}
	help := ""
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "#") {
			help = strings.TrimSpace(strings.TrimLeft(line, "#"))
			continue
		}
		if m := makeTarget.FindStringSubmatch(line); m != nil && !strings.Contains(m[1], "%") {
			if _, h, ok := strings.Cut(m[2], "##"); ok {
				help = strings.TrimSpace(h)
			}
			for _, t := range strings.Fields(m[1]) {
				if t != "" && !strings.HasPrefix(t, ".") {
					out[t] = task{Help: help, Group: "make", Steps: []string{"make " + t}}
				}
			}
		}
		help = ""
	}
	return tasks(out), nil
}

// NPM reads package.json scripts, run with the package manager its lockfile names; pre and post hooks run with their script.
func NPM(path, dir string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pkg struct{ Scripts map[string]string }
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, err
	}
	pkgDir := filepath.Dir(path)
	run := "npm run"
	for lock, pm := range map[string]string{"pnpm-lock.yaml": "pnpm run", "yarn.lock": "yarn run", "bun.lockb": "bun run", "bun.lock": "bun run"} {
		if _, err := os.Stat(filepath.Join(pkgDir, lock)); err == nil {
			run = pm
		}
	}
	cd := ""
	if rel, err := filepath.Rel(dir, pkgDir); err == nil && rel != "." {
		cd = "cd " + rel + " && "
	}
	out := map[string]task{}
	for name, cmd := range pkg.Scripts {
		if base, ok := strings.CutPrefix(name, "pre"); ok && pkg.Scripts[base] != "" {
			continue
		}
		if base, ok := strings.CutPrefix(name, "post"); ok && pkg.Scripts[base] != "" {
			continue
		}
		out[name] = task{Help: cmd, Group: "npm", Steps: []string{cd + run + " " + name + ` -- "$$@"`}}
	}
	return tasks(out), nil
}

// Procfile turns each process into a local service running its command; $PORT is left to the shell.
func Procfile(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	svcs := map[string]any{}
	for line := range strings.Lines(string(raw)) {
		name, cmd, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.HasPrefix(name, "#") {
			continue
		}
		svcs[strings.TrimSpace(name)] = map[string]any{"run": map[string]any{"command": []string{"sh", "-c", shell(strings.TrimSpace(cmd))}}}
	}
	return map[string]any{"services": svcs}, nil
}

// Dotenv makes a .env file's values rig vars, so ${NAME} works anywhere in rig.yaml.
func Dotenv(path, _ string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		vars[strings.TrimSpace(k)] = shell(v)
	}
	return map[string]any{"vars": vars}, nil
}

// yamlFiles lists path itself, or the YAML and JSON files directly in it.
func yamlFiles(path string, exts ...string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && slices.Contains(exts, filepath.Ext(e.Name())) {
			out = append(out, filepath.Join(path, e.Name()))
		}
	}
	return out, nil
}
