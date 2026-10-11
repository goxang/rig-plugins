package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/goxang/rig/host"
)

// TestReleaseInstalls packs a release, signed, and installs a pack from it through rig's installer,
// which checks every checksum and signature on the way.
func TestReleaseInstalls(t *testing.T) {
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	pub, secret, err := host.Keygen()
	if err != nil {
		t.Fatal(err)
	}
	old := host.OfficialKey
	host.OfficialKey = pub
	defer func() { host.OfficialKey = old }()

	tmp := t.TempDir()
	r := Release{Version: "v1.0.0", Manifests: filepath.Join(tmp, "manifests"), Plugins: "../../plugins", Bins: filepath.Join(tmp, "bin"),
		Out: filepath.Join(tmp, "out"), URL: "https://example.invalid/v1.0.0", Index: filepath.Join(tmp, "index.json"), Key: secret}
	for _, n := range []string{"git", "gitlab"} {
		_ = os.MkdirAll(filepath.Join(r.Manifests, n), 0o755)
		m := "name: " + n + "\nversion: dev\npublisher: goxang\nserver: {command: [bin/rig-std, " + n + "]}\n"
		if err := os.WriteFile(filepath.Join(r.Manifests, n, host.ManifestFile), []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(r.Bins, 0o755)
	for _, plat := range []string{runtime.GOOS + "_" + runtime.GOARCH, "darwin_arm64"} {
		_ = os.WriteFile(filepath.Join(r.Bins, "rig-std_"+plat), []byte("#!/bin/sh\n"), 0o755)
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}

	// a mirror serves <name>/<version>/<file> and files/<shared file>
	mirror := filepath.Join(tmp, "mirror")
	_ = os.MkdirAll(mirror, 0o755)
	b, _ := os.ReadFile(r.Index)
	_ = os.WriteFile(filepath.Join(mirror, "index.json"), b, 0o644)
	_ = os.MkdirAll(filepath.Join(mirror, "files"), 0o755)
	shared, _ := filepath.Glob(filepath.Join(r.Out, "rig-std_*.gz"))
	for _, f := range shared {
		data, _ := os.ReadFile(f)
		_ = os.WriteFile(filepath.Join(mirror, "files", filepath.Base(f)), data, 0o644)
	}
	files, _ := filepath.Glob(filepath.Join(r.Out, "*.tar.gz"))
	for _, f := range files {
		var name string
		for _, n := range []string{"git", "gitlab", "pack-gitlab"} {
			if m, _ := filepath.Match(n+"_1.0.0_*", filepath.Base(f)); m {
				name = n
			}
		}
		if name == "" {
			continue
		}
		_ = os.MkdirAll(filepath.Join(mirror, name, "1.0.0"), 0o755)
		data, _ := os.ReadFile(f)
		_ = os.WriteFile(filepath.Join(mirror, name, "1.0.0", filepath.Base(f)), data, 0o644)
	}

	proj := t.TempDir()
	in := &host.Installer{Dir: proj, Registry: &host.Registry{Mirror: mirror}, Yes: true}
	if err := in.Add(context.Background(), []string{"pack/gitlab"}); err != nil {
		t.Fatal(err)
	}
	lock, _ := host.ReadLock(proj)
	for _, n := range []string{"git", "gitlab", "pack-gitlab"} {
		if l := lock.Plugins[n]; l == nil || l.Version != "1.0.0" {
			t.Fatalf("%s locked as %+v", n, l)
		}
	}
	ms, _ := host.Discover(proj, nil)
	for _, m := range ms {
		if m.Name == "gitlab" {
			if _, err := os.Stat(filepath.Join(m.Dir, "bin", "rig-std")); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("gitlab not discovered")
}

func TestPluginsLoadClean(t *testing.T) {
	dirs, _ := filepath.Glob("../../plugins/*")
	for _, d := range dirs {
		m, err := host.LoadManifest(d)
		if err != nil || len(m.Warnings) > 0 {
			t.Errorf("%s: %v %v", d, err, m.Warnings)
		}
	}
}
