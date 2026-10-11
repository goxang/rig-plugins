package main

import (
	buildcommand "github.com/goxang/rig-plugins/adapters/builder/command"
	builddocker "github.com/goxang/rig-plugins/adapters/builder/docker"
	"github.com/goxang/rig-plugins/adapters/builder/golang"
	"github.com/goxang/rig-plugins/adapters/cache/redis"
	"github.com/goxang/rig-plugins/adapters/database/sql"
	"github.com/goxang/rig-plugins/adapters/debugger/delve"
	"github.com/goxang/rig-plugins/adapters/hosts/ssh"
	"github.com/goxang/rig-plugins/adapters/kv/consul"
	loadcommand "github.com/goxang/rig-plugins/adapters/loadgen/command"
	loadhttp "github.com/goxang/rig-plugins/adapters/loadgen/http"
	loadkv "github.com/goxang/rig-plugins/adapters/loadgen/kv"
	loadtool "github.com/goxang/rig-plugins/adapters/loadgen/tool"
	"github.com/goxang/rig-plugins/adapters/logs/loki"
	logsruntime "github.com/goxang/rig-plugins/adapters/logs/runtime"
	"github.com/goxang/rig-plugins/adapters/messaging/kafka"
	"github.com/goxang/rig-plugins/adapters/messaging/rabbitmq"
	"github.com/goxang/rig-plugins/adapters/metrics/prometheus"
	"github.com/goxang/rig-plugins/adapters/metrics/scrape"
	profcommand "github.com/goxang/rig-plugins/adapters/profiler/command"
	"github.com/goxang/rig-plugins/adapters/profiler/pprof"
	queryhttp "github.com/goxang/rig-plugins/adapters/query/http"
	rtdocker "github.com/goxang/rig-plugins/adapters/runtime/docker"
	"github.com/goxang/rig-plugins/adapters/runtime/kind"
	"github.com/goxang/rig-plugins/adapters/runtime/kubernetes"
	"github.com/goxang/rig-plugins/adapters/runtime/local"
	"github.com/goxang/rig-plugins/adapters/tracing/jaeger"
	"github.com/goxang/rig-plugins/adapters/tracing/tempo"
	"github.com/goxang/rig-plugins/adapters/tracing/zipkin"
	"github.com/goxang/rig/core"
)

// plugins are named by the component type rig.yaml uses; a name several kinds share is one plugin
// implementing each kind's contract. A probe is a nil value of the adapter's type: what it implements
// decides which optional capabilities the plugin provides.
var plugins = map[string][]served{
	"local":      {{core.KindRuntime, (*local.Runtime)(nil)}},
	"docker":     {{core.KindRuntime, (*rtdocker.Runtime)(nil)}, {core.KindBuilder, (*builddocker.Builder)(nil)}},
	"kubernetes": {{core.KindRuntime, (*kubernetes.Runtime)(nil)}},
	"kind":       {{core.KindRuntime, (*kind.Runtime)(nil)}},
	"go":         {{core.KindBuilder, (*golang.Builder)(nil)}},
	"command": {{core.KindBuilder, (*buildcommand.Builder)(nil)}, {core.KindProfiler, (*profcommand.Profiler)(nil)},
		{core.KindLoad, (*loadcommand.Gen)(nil)}},
	"prometheus": {{core.KindMetrics, (*prometheus.Metrics)(nil)}},
	"scrape":     {{core.KindMetrics, (*scrape.Metrics)(nil)}},
	"loki":       {{core.KindLogs, (*loki.Loki)(nil)}},
	"runtime":    {{core.KindLogs, (*logsruntime.Logs)(nil)}},
	"jaeger":     {{core.KindTracing, (*jaeger.Tracing)(nil)}},
	"tempo":      {{core.KindTracing, (*tempo.Tracing)(nil)}},
	"zipkin":     {{core.KindTracing, (*zipkin.Tracing)(nil)}},
	"pprof":      {{core.KindProfiler, (*pprof.Profiler)(nil)}},
	"delve":      {{core.KindDebugger, (*delve.Debugger)(nil)}},
	"sql":        {{core.KindDatabase, (*sql.DB)(nil)}},
	"redis":      {{core.KindCache, (*redis.Cache)(nil)}},
	"rabbitmq":   {{core.KindMessaging, (*rabbitmq.Rabbit)(nil)}},
	"kafka":      {{core.KindMessaging, (*kafka.Kafka)(nil)}},
	"consul":     {{core.KindKV, (*consul.KV)(nil)}},
	"http":       {{core.KindQuery, (*queryhttp.Query)(nil)}, {core.KindLoad, (*loadhttp.Gen)(nil)}},
	"kv":         {{core.KindLoad, (*loadkv.Gen)(nil)}},
	"k6":         {{core.KindLoad, (*loadtool.Gen)(nil)}},
	"vegeta":     {{core.KindLoad, (*loadtool.Gen)(nil)}},
	"ghz":        {{core.KindLoad, (*loadtool.Gen)(nil)}},
	"ssh":        {{core.KindHosts, (*ssh.SSH)(nil)}},
}
