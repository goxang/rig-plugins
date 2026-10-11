package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/goxang/rig/protocol"
	"github.com/goxang/rig/protocol/rpc"
	"github.com/goxang/rig/protocol/transcript"
)

var record = flag.Bool("record", false, "record testdata/<plugin>.jsonl")

// imports are the deterministic importer calls each importer plugin's transcript holds.
var imports = map[string][]protocol.ImportParams{
	"compose":     {{Importer: "compose", Files: []string{"compose.yaml"}, Dir: "."}},
	"godev":       {{Importer: "godev", Files: []string{".godev.yaml"}, Dir: "."}},
	"rig":         {{Importer: "rig", Files: []string{"rig.yaml"}, Dir: "."}},
	"kubernetes":  {{Importer: "kubernetes", Files: []string{"k8s"}, Dir: "."}},
	"just":        {{Importer: "just", Files: []string{"justfile"}, Dir: "."}},
	"make":        {{Importer: "make", Files: []string{"Makefile"}, Dir: "."}},
	"npm-scripts": {{Importer: "npm-scripts", Files: []string{"package.json"}, Dir: "."}},
	"procfile":    {{Importer: "procfile", Files: []string{"Procfile"}, Dir: "."}},
	"dotenv":      {{Importer: "dotenv", Files: []string{".env"}, Dir: "."}},
	"openapi":     {{Importer: "openapi", Files: []string{"openapi.yaml"}, Dir: "."}},
	"grafana":     {{Importer: "grafana", Files: []string{"grafana"}, Dir: "."}},
	"prometheus":  {{Importer: "prometheus-rules", Files: []string{"rules.yml"}, Dir: "."}},
	"gitlab":      {{Importer: "gitlab-ci", Files: []string{".gitlab-ci.yml"}, Dir: "."}},
	"github":      {{Importer: "github-actions", Files: []string{".github/workflows"}, Dir: "."}},
}

// TestTranscriptsReplay plays every plugin's golden session (what it provides, then its imports)
// back to rig-std: a change in what a plugin says on the wire fails here.
func TestTranscriptsReplay(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "rig-std")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	project, _ := filepath.Abs("testdata/project")
	cmd := func(name string) *exec.Cmd {
		c := exec.Command(bin, name)
		c.Dir = project
		return c
	}
	for _, name := range names() {
		golden := filepath.Join("testdata", name+".jsonl")
		if *record {
			var buf bytes.Buffer
			p, err := rpc.Start(cmd(name), rpc.Options{Tap: transcript.NewRecorder(&buf).Tap})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = p.Call(ctx, protocol.MethodInitialize, protocol.InitializeParams{Host: protocol.HostInfo{Name: "rig", Version: "1.0.0"}, Protocols: []string{protocol.Version}}, nil)
			for _, ip := range imports[name] {
				_ = p.Call(ctx, protocol.MethodImport, ip, nil)
			}
			cancel()
			if err := p.Close(5 * time.Second); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(golden)
			if err != nil {
				t.Fatalf("%v (go test -run Transcripts -record writes it)", err)
			}
			defer f.Close()
			lines, err := transcript.Parse(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := transcript.Replay(context.Background(), cmd(name), lines, 5*time.Second); err != nil {
				t.Fatal(err)
			}
		})
	}
}
