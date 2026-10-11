package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goxang/rig-plugins/adapters/loadgen"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/spec"
	"gopkg.in/yaml.v3"
)

func newGen() *Gen { return &Gen{lat: loadgen.NewReservoir(16)} }

func TestReaders(t *testing.T) {
	g := newGen()
	vegetaRead(strings.NewReader(`{"code":200,"latency":2000000,"error":""}
{"code":500,"latency":1000000,"error":"500 Internal Server Error"}
{"code":0,"latency":0,"error":"dial tcp: refused"}
`), g)
	if g.sent.Load() != 3 || g.failed.Load() != 2 || g.lat.Percentiles().P99 != 2*time.Millisecond {
		t.Errorf("vegeta: sent %d failed %d %v", g.sent.Load(), g.failed.Load(), g.lat.Percentiles())
	}

	g = newGen()
	k6Read(strings.NewReader(`{"type":"Metric","metric":"http_reqs","data":{}}
{"type":"Point","metric":"http_reqs","data":{"value":1}}
{"type":"Point","metric":"http_req_failed","data":{"value":1}}
{"type":"Point","metric":"http_req_duration","data":{"value":12.5}}
{"type":"Point","metric":"http_reqs","data":{"value":1}}
{"type":"Point","metric":"http_req_failed","data":{"value":0}}
`), g)
	if g.sent.Load() != 2 || g.failed.Load() != 1 || g.lat.Percentiles().P50 != 12500*time.Microsecond {
		t.Errorf("k6: sent %d failed %d %v", g.sent.Load(), g.failed.Load(), g.lat.Percentiles())
	}

	g = newGen()
	ghzRead(strings.NewReader(`{"count":2,"details":[{"latency":3000000,"status":"OK"},{"latency":1000000,"status":"Unavailable"}]}`), g)
	if g.sent.Load() != 2 || g.failed.Load() != 1 {
		t.Errorf("ghz: sent %d failed %d", g.sent.Load(), g.failed.Load())
	}
}

type env struct{ core.Env }

func (env) Resolve(_ context.Context, a string) (string, error) { return a, nil }
func (env) Project() *spec.Project                              { return &spec.Project{Dir: "."} }

func TestVegeta(t *testing.T) {
	if _, err := exec.LookPath("vegeta"); err != nil {
		t.Skip("vegeta not on PATH")
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	var c spec.Component
	if err := yaml.Unmarshal([]byte("type: vegeta\nrate: 50\ntarget: "+srv.URL), &c); err != nil {
		t.Fatal(err)
	}
	v, err := New("vegeta", env{}, &c)
	if err != nil {
		t.Fatal(err)
	}
	g := v.(*Gen)
	ctx := context.Background()
	if err := g.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	time.Sleep(1500 * time.Millisecond)
	_ = g.SetRate(ctx, 100)
	time.Sleep(1500 * time.Millisecond)
	st, _ := g.Status(ctx)
	if st.Sent < 50 || st.Failed != 0 || st.Latency.P50 == 0 || hits.Load() < st.Sent {
		t.Errorf("status %+v, hits %d", st, hits.Load())
	}
}
