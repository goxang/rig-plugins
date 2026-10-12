// Command rig-changed prints the plugins a change touches, so CI builds and releases only those:
//
//	rig-changed -base origin/main        # JSON: {"plugins":["gitlab","jira"],"go":true}
//
// A declarative plugin is its directory under plugins/. A Go plugin is a key of one of the tables in
// cmd/rig-std; it is touched when a package its table entry names, or one that package imports, changed.
// A change to rig-std itself or to go.mod touches every Go plugin, shown as "@go".
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const module = "github.com/goxang/rig-plugins"

type result struct {
	Plugins []string `json:"plugins"`
	Go      bool     `json:"go"`
}

func main() {
	base := flag.String("base", "origin/main", "git ref to compare HEAD with")
	flag.Parse()
	out, err := exec.Command("git", "diff", "--name-only", *base, "HEAD").Output()
	if err != nil {
		fail(err)
	}
	r, err := affected(strings.Fields(string(out)), ".")
	if err != nil {
		fail(err)
	}
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "rig-changed:", err)
	os.Exit(1)
}

// affected maps changed files (slash paths from the module root dir) to the plugins they touch.
func affected(files []string, dir string) (*result, error) {
	set := map[string]bool{}
	var pkgs []string
	for _, f := range files {
		switch parts := strings.Split(f, "/"); {
		case strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, ".md"):
		case parts[0] == "plugins" && len(parts) > 2:
			set[parts[1]] = true
		case f == "go.mod" || f == "go.sum":
			set["@go"] = true
		case strings.HasPrefix(f, "cmd/rig-std/manifests/"):
			set[strings.TrimSuffix(path.Base(f), ".yaml")] = true
		case strings.HasPrefix(f, "cmd/rig-std/"):
			set["@go"] = true
		case parts[0] == "adapters" || parts[0] == "internal":
			pkgs = append(pkgs, module+"/"+path.Dir(f))
		}
	}
	if len(pkgs) > 0 {
		deps, err := tables(dir)
		if err != nil {
			return nil, err
		}
		reach, err := dependents(dir, pkgs)
		if err != nil {
			return nil, err
		}
		for name, used := range deps {
			if slices.ContainsFunc(used, func(p string) bool { return reach[p] }) {
				set[name] = true
			}
		}
	}
	r := &result{Plugins: []string{}}
	for n := range set {
		r.Plugins = append(r.Plugins, n)
	}
	slices.Sort(r.Plugins)
	r.Go = slices.ContainsFunc(r.Plugins, func(n string) bool { return n == "@go" || isGo(dir, n) })
	return r, nil
}

// isGo is whether name is not a declarative plugin.
func isGo(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, "plugins", name))
	return err != nil
}

// tables reads every map in cmd/rig-std whose string keys are plugin names: for each name, the
// module packages its entries name (as in (*local.Runtime)(nil) or forge.RegisterSCM).
func tables(dir string) (map[string][]string, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(filepath.Join(dir, "cmd", "rig-std"))
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, "cmd", "rig-std", e.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		imports := map[string]string{}
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			if !strings.HasPrefix(ip, module+"/") {
				continue
			}
			name := path.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
			}
			imports[name] = ip
		}
		ast.Inspect(f, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			lit, ok := kv.Key.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, _ := strconv.Unquote(lit.Value)
			ast.Inspect(kv.Value, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && imports[id.Name] != "" {
						out[key] = append(out[key], imports[id.Name])
					}
				}
				return true
			})
			return true
		})
	}
	return out, nil
}

// dependents are the changed packages and every package of the module that imports one, directly or not.
func dependents(dir string, changed []string) (map[string]bool, error) {
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports", "./...")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "GOWORK=off")
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var pkgs []struct {
		ImportPath string
		Imports    []string
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var p struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
	reach := map[string]bool{}
	for _, c := range changed {
		reach[c] = true
	}
	for grew := true; grew; {
		grew = false
		for _, p := range pkgs {
			if !reach[p.ImportPath] && slices.ContainsFunc(p.Imports, func(i string) bool { return reach[i] }) {
				reach[p.ImportPath], grew = true, true
			}
		}
	}
	return reach, nil
}
