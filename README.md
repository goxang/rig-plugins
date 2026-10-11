# rig-plugins

The official plugins for [rig](https://github.com/goxang/rig): runtimes, builders, databases,
brokers, metrics, logs, traces, profilers, debuggers, load generators, hosts, forges, trackers,
docs, infrastructure, registries and notifications, plus importers and packs.

```bash
go install github.com/goxang/rig-plugins/cmd/rig-std@latest
rig-std install     # writes a manifest per plugin to ~/.config/rig/plugins
rig-std list        # what each plugin serves
```

`rig-std` is one binary holding every Go plugin; rig starts `rig-std <name>` when a component's
type names that plugin. A name shared by several kinds (`docker`, `command`, `http`) is one plugin
implementing each kind's contract.

## Layout

- `cmd/rig-std/` the binary and its plugin table
- `adapters/<kind>/<type>/` the adapters, served through `contracts.Serve`
- `cmd/rig-std/manifests/` manifest keys beyond the code: config schema, secrets, detect, views
- `cmd/rig-index/` packs a release and writes the signed registry index
- `plugins/` declarative plugins and packs (`pack-std`, `pack-product`, ...), a directory each
- `internal/` helpers the adapters share (shell, HTTP, forges, frames, importers)

## Releasing

```bash
rig-index keygen    # once: the public key goes in rig's host.OfficialKey, the secret in RIG_SIGNING_KEY
rig-std install --relative dist/manifests
rig-index -version 1.0.0 -url https://github.com/goxang/rig-plugins/releases/download/v1.0.0
```

## Adding a plugin

Write the adapter against `github.com/goxang/rig/core`, register it with `plugin.Register`, and add
it to the table in `cmd/rig-std/plugins.go`. `go test ./cmd/rig-std` checks it serves its contract.
