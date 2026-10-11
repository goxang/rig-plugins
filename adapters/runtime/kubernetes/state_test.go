package kubernetes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStateFallsBackToRig(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *rig-state*) echo '{\"data\":{\"tag\":\"v1\"}}';; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := &Runtime{Opt: Options{StateConfigMap: "rig-state"}}
	m, err := r.LoadState(context.Background())
	if err != nil || m["tag"] != "v1" {
		t.Fatalf("state %v, %v", m, err)
	}
	r.Opt.StateConfigMap = "mine"
	if m, _ := r.LoadState(context.Background()); len(m) != 0 {
		t.Fatalf("a named configmap fell back: %v", m)
	}
}
