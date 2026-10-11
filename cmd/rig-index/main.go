// Command rig-index packs a release of the official plugins and writes registry/index.json: one
// archive per Go plugin and platform (its manifest; bin/rig-std is a shared file, one per platform),
// one per declarative plugin, each with its sha256 and, when RIG_SIGNING_KEY is set, a minisign signature.
//
//	rig-std install --relative dist/manifests
//	rig-index -version 1.0.0 -manifests dist/manifests -plugins plugins -bins dist/bin \
//	  -out dist/release -url https://github.com/goxang/rig-plugins/releases/download/v1.0.0
//	rig-index keygen
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/goxang/rig/host"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		pub, secret, err := host.Keygen()
		if err != nil {
			fail(err)
		}
		fmt.Printf("public key (host.OfficialKey): %s\nRIG_SIGNING_KEY=%s\n", pub, secret)
		return
	}
	var r Release
	flag.StringVar(&r.Version, "version", "", "release version, such as 1.0.0")
	flag.StringVar(&r.Manifests, "manifests", "dist/manifests", "rig-std install --relative output")
	flag.StringVar(&r.Plugins, "plugins", "plugins", "declarative plugins and packs, a directory each")
	flag.StringVar(&r.Bins, "bins", "dist/bin", "rig-std_<os>_<arch> binaries")
	flag.StringVar(&r.Out, "out", "dist/release", "where archives go")
	flag.StringVar(&r.URL, "url", "", "where archives will be downloaded from")
	flag.StringVar(&r.Index, "index", "registry/index.json", "the index to update")
	flag.Parse()
	r.Key = os.Getenv("RIG_SIGNING_KEY")
	if r.Version == "" || r.URL == "" {
		fail(fmt.Errorf("-version and -url are required"))
	}
	if err := r.Run(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "rig-index:", err)
	os.Exit(1)
}

type Release struct {
	Version, Manifests, Plugins, Bins, Out, URL, Index, Key string
}

func (r *Release) Run() error {
	r.Version = strings.TrimPrefix(r.Version, "v")
	ix := &host.Index{Plugins: map[string]*host.IndexEntry{}}
	if b, err := os.ReadFile(r.Index); err == nil {
		if err := json.Unmarshal(b, ix); err != nil {
			return fmt.Errorf("%s: %w", r.Index, err)
		}
	}
	if err := os.MkdirAll(r.Out, 0o755); err != nil {
		return err
	}
	bins, _ := filepath.Glob(filepath.Join(r.Bins, "rig-std_*"))
	// one rig-std per platform, which every Go plugin's artifact lists as a shared file
	std := map[string]host.Artifact{}
	for _, bin := range bins {
		plat := strings.Replace(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(bin), "rig-std_"), ".exe"), "_", "/", 1)
		data, err := os.ReadFile(bin)
		if err != nil {
			return err
		}
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
		if _, err := zw.Write(data); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if std[plat], err = r.publish(fmt.Sprintf("rig-std_%s_%s.gz", r.Version, strings.ReplaceAll(plat, "/", "_")), gz.Bytes()); err != nil {
			return err
		}
	}
	packs := map[string][]string{}
	var entries []*host.Manifest
	for _, root := range []string{r.Manifests, r.Plugins} {
		dirs, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(root, d.Name())
			m, err := host.LoadManifest(dir)
			if err != nil {
				return err
			}
			m.Version = r.Version
			arts := map[string]host.Artifact{}
			if root == r.Manifests {
				if len(bins) == 0 {
					return fmt.Errorf("no rig-std_<os>_<arch> binaries in %s", r.Bins)
				}
				for plat, bin := range std {
					a, err := r.pack(m, dir, strings.ReplaceAll(plat, "/", "_"))
					if err != nil {
						return err
					}
					a.Files = map[string]host.Artifact{"bin/rig-std": bin}
					arts[plat] = a
				}
			} else if arts["any"], err = r.pack(m, dir, "any"); err != nil {
				return err
			}
			for dep := range m.Requires {
				packs[dep] = append(packs[dep], m.Name)
			}
			e := ix.Plugins[m.Name]
			if e == nil {
				e = &host.IndexEntry{Versions: map[string]*host.IndexVersion{}}
				ix.Plugins[m.Name] = e
			}
			e.Description, e.Publisher, e.Repo, e.Detect = m.Description, "goxang", "github.com/goxang/rig-plugins", m.Detect
			e.Provides = provides(m)
			e.Versions[r.Version] = &host.IndexVersion{Rig: m.Rig, Requires: m.Requires, Permissions: m.Permissions, Artifacts: arts}
			entries = append(entries, m)
		}
	}
	for _, m := range entries {
		ps := packs[m.Name]
		slices.Sort(ps)
		ix.Plugins[m.Name].Packs = ps
	}
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Index), 0o755); err != nil {
		return err
	}
	fmt.Printf("%d plugins, %d platforms, version %s\n", len(entries), len(bins), r.Version)
	return os.WriteFile(r.Index, append(b, '\n'), 0o644)
}

func provides(m *host.Manifest) map[string]any {
	p := map[string]any{}
	if len(m.Implements) > 0 {
		p["contracts"] = m.Implements
	}
	var ims []string
	for n := range m.Importers {
		ims = append(ims, n)
	}
	if len(ims) > 0 {
		slices.Sort(ims)
		p["importers"] = ims
	}
	if len(m.Schemes) > 0 {
		p["schemes"] = m.Schemes
	}
	return p
}

// pack writes <name>_<version>_<platform>.tar.gz: dir's files with the manifest at its release
// version, and returns its artifact.
func (r *Release) pack(m *host.Manifest, dir string, plat string) (host.Artifact, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	add := func(name string, mode int64, data []byte) error {
		// a fixed time keeps archives byte-for-byte reproducible
		h := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == host.ManifestFile {
			if data, err = setVersion(data, r.Version); err != nil {
				return err
			}
		}
		info, _ := d.Info()
		return add(filepath.ToSlash(rel), int64(info.Mode().Perm()), data)
	})
	if err != nil {
		return host.Artifact{}, err
	}
	if err := tw.Close(); err != nil {
		return host.Artifact{}, err
	}
	if err := zw.Close(); err != nil {
		return host.Artifact{}, err
	}
	return r.publish(fmt.Sprintf("%s_%s_%s.tar.gz", m.Name, r.Version, plat), buf.Bytes())
}

// publish writes file into the release with its signature when there is a key, and returns its artifact.
func (r *Release) publish(file string, data []byte) (host.Artifact, error) {
	if err := os.WriteFile(filepath.Join(r.Out, file), data, 0o644); err != nil {
		return host.Artifact{}, err
	}
	sum := sha256.Sum256(data)
	a := host.Artifact{URL: strings.TrimSuffix(r.URL, "/") + "/" + file, SHA256: hex.EncodeToString(sum[:])}
	if r.Key != "" {
		sig, err := host.Sign(r.Key, file, data)
		if err != nil {
			return host.Artifact{}, err
		}
		a.Sig = sig
		if err := os.WriteFile(filepath.Join(r.Out, file+".minisig"), []byte(sig), 0o644); err != nil {
			return host.Artifact{}, err
		}
	}
	return a, nil
}

// setVersion rewrites the manifest's version key, leaving the rest of the document as written.
func setVersion(manifest []byte, version string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(manifest, &doc); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "version" {
			root.Content[i+1].Value = version
			root.Content[i+1].Tag, root.Content[i+1].Style = "!!str", 0
			return yaml.Marshal(&doc)
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "version"}, &yaml.Node{Kind: yaml.ScalarNode, Value: version, Tag: "!!str"})
	return yaml.Marshal(&doc)
}
