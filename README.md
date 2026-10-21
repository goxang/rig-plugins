# rig-plugins

The official plugins for [rig](https://github.com/goxang/rig): runtimes, builders, databases,
brokers, metrics, logs, traces, profilers, debuggers, load generators, hosts, forges, trackers,
docs, infrastructure, registries and notifications, plus importers and packs.

You do not install these by hand: the first `rig` in a project installs the ones its `rig.yaml` names,
as prebuilt binaries from the [releases](https://github.com/goxang/rig-plugins/releases), no Go needed.
By hand, `rig plugin add --user pack-std` (the everyday set) or `rig plugin add <name>`.

Building from source (Go 1.26+): `go install github.com/goxang/rig-plugins/cmd/rig-std@latest && rig-std install`.

`rig-std` is one binary holding every Go plugin; rig starts `rig-std <name>` when a component's
type names that plugin. A name shared by several kinds (`docker`, `command`, `http`) is one plugin
implementing each kind's contract.

[`docs/org.md`](docs/org.md): a company as a tree of projects, with a plugin per management layer.

## Layout

- `cmd/rig-std/` the binary and its plugin table
- `adapters/<kind>/<type>/` the adapters, served through `contracts.Serve`
- `cmd/rig-std/manifests/` manifest keys beyond the code: config schema, secrets, detect, views
- `cmd/rig-index/` packs a release and writes the signed registry index; `cmd/rig-changed/` finds the plugins a change touches
- `plugins/` declarative plugins and packs (`pack-std`, `pack-product`, ...), a directory each
- `internal/` helpers the adapters share (shell, HTTP, forges, frames, importers)

## Releasing

Automatic on every push to `main` (`.github/workflows/release.yml`). `cmd/rig-changed` maps the
changed files to the plugins they touch (a plugin directory, its manifest, or a package it imports);
only those are built, packed, signed with `RIG_SIGNING_KEY` and added to `registry/index.json`, as
the next version (`feat:` minor, `!:` major, else patch). A change that touches no plugin releases
nothing; a Go plugin change builds `rig-std` once per platform. Pull requests dry-run the same pack.

```bash
rig-index keygen    # once: the public key goes in rig's host.OfficialKey, the secret in RIG_SIGNING_KEY
go run ./cmd/rig-changed -base origin/main    # what a change touches
rig-std install --relative dist/manifests
rig-index -version 1.2.0 -url https://github.com/goxang/rig-plugins/releases/download/v1.2.0 -only jira,arcade
```

## Adding a plugin

Write the adapter against `github.com/goxang/rig/core`, register it with `plugin.Register`, and add
it to the table in `cmd/rig-std/plugins.go`. `go test ./cmd/rig-std` checks it serves its contract.
