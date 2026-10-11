package terraform

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/goxang/rig/sdk"
)

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"main.tf", "infra/prod/main.tf", "infra/modules/db/main.tf", "infra/prod/.terraform/x.tf", "app/main.go"} {
		p := filepath.Join(root, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, nil, 0o644)
	}
	if got := Discover(root); !slices.Equal(got, []string{".", "infra/prod"}) {
		t.Fatalf("stacks %v", got)
	}
}

func TestChanges(t *testing.T) {
	got, err := Changes([]byte(`{"resource_changes":[
		{"address":"aws_s3_bucket.a","change":{"actions":["create"]}},
		{"address":"aws_db.b","change":{"actions":["delete","create"]}},
		{"address":"aws_iam.c","change":{"actions":["no-op"]}},
		{"address":"aws_sg.d","change":{"actions":["delete"]}}]}`))
	want := []sdk.Change{
		{Op: sdk.OpCreate, Target: "aws_s3_bucket.a"},
		{Op: sdk.OpUpdate, Target: "aws_db.b", Detail: "replace"},
		{Op: sdk.OpDelete, Target: "aws_sg.d"},
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("changes %+v %v", got, err)
	}
}
