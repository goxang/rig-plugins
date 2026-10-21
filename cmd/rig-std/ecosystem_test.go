package main

import (
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/host"
	"github.com/goxang/rig/kernel/view"
	"github.com/goxang/rig/protocol"
)

// TestOverlays checks each overlay names a plugin, decodes as a manifest, and that its views parse
// and bind only to that plugin's own sources or the ones it declares it uses.
func TestOverlays(t *testing.T) {
	files, _ := fs.Glob(overlays, "manifests/*.yaml")
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(f, "manifests/"), ".yaml")
		if !strings.Contains(" "+strings.Join(names(), " ")+" ", " "+name+" ") {
			t.Errorf("%s: no plugin %s", f, name)
		}
		raw, _ := overlays.ReadFile(f)
		var m host.Manifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for vn, node := range m.Views {
			var v view.View
			if err := node.Decode(&v); err != nil {
				t.Fatalf("%s view %s: %v", f, vn, err)
			}
			view.Walk(&v.Root, func(n *protocol.Node) {
				if n.Bind != nil && !strings.HasPrefix(n.Bind.Source, name+".") && !slices.ContainsFunc(m.Permissions.Uses, func(u string) bool {
					ok, _ := path.Match(u, n.Bind.Source)
					return ok
				}) {
					t.Errorf("%s view %s: source %s is not %s's", f, vn, n.Bind.Source, name)
				}
			})
		}
	}
}
