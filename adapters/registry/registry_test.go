package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/goxang/rig-plugins/internal/forge"
)

func TestDoomed(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	tags := []Tag{
		{Image: "app", Tag: "v1", Digest: "a", Created: day(1)},
		{Image: "app", Tag: "v2", Digest: "b", Created: day(2)},
		{Image: "app", Tag: "v3", Digest: "c", Created: day(3)},
		{Image: "app", Tag: "latest", Digest: "c", Created: day(3)},
		{Image: "app", Tag: "old-alias", Digest: "c", Created: day(0)},
		{Image: "db", Tag: "v1", Digest: "d", Created: day(1)},
	}
	got := Doomed(tags, 2, regexp.MustCompile(`^v`))
	if len(got) != 1 || got[0].Tag != "v1" || got[0].Image != "app" {
		t.Fatalf("doomed %+v", got)
	}
	// old-alias shares the newest digest, so it survives even when counted
	got = Doomed(tags, 1, nil)
	for _, d := range got {
		if d.Digest == "c" {
			t.Fatalf("deleted a kept digest: %+v", d)
		}
	}
	if len(got) != 2 {
		t.Fatalf("doomed %+v", got)
	}
}

func TestNexus(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(204)
		case r.URL.Query().Get("continuationToken") == "":
			w.Write([]byte(`{"items":[{"id":"c1","name":"app","version":"v1","assets":[{"lastModified":"2026-01-01T00:00:00Z","fileSize":10,"checksum":{"sha256":"aa"}}]}],"continuationToken":"next"}`))
		default:
			w.Write([]byte(`{"items":[{"id":"c2","name":"app","version":"v2","assets":[{"lastModified":"2026-01-02T00:00:00Z","fileSize":5}]}]}`))
		}
	}))
	defer srv.Close()
	n := nexus{&forge.Client{Base: srv.URL}}
	tags, err := n.Tags(context.Background(), "docker-hosted")
	if err != nil || len(tags) != 2 || tags[0].Digest != "sha256:aa" || tags[1].Size != 5 {
		t.Fatalf("tags %+v %v", tags, err)
	}
	doomed := Doomed(tags, 1, nil)
	if err := n.Delete(context.Background(), "docker-hosted", doomed[0]); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "/service/rest/v1/components/c1" {
		t.Fatalf("deleted %v", deleted)
	}
}
