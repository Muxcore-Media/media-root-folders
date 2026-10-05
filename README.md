# Root Folders

Managed library root-folder catalog for movies and TV path binding. Provides free-space / accessibility probes and admin UI picker support under capability `media.roots`.

Requires MuxCore **v0.4.0+**. No external contracts — exposes its own gRPC API (`RootFolderService`).

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `ROOTS_DB_PATH` | `/var/lib/media-root-folders/roots.db` | SQLite database path |
| `ROOTS_GRPC_ADDR` | `:9540` | gRPC listen address |
| `ROOTS_REQUIRE_ACCESSIBLE` | `true` | When true, reject Create/Update/Seed (unless `skip_validation`) when the path is missing or unreadable. Persisted when changed via SettingsProvider. |
| `ROOTS_ALLOWED_PREFIXES` | _(unset: refuse)_ | Comma-separated absolute prefixes every root path must resolve inside (symlinks resolved). Unset: `CreateRoot`/`UpdateRoot` path changes/`SeedFromPaths`/`ProbeRoot` are refused (fail-closed). Set to the library mounts, e.g. `/mnt/media,/srv/library`. |
| `ROOTS_BROWSE_PREFIXES` | _(unset)_ | Comma-separated absolute prefixes `BrowsePath` may list. Unset: falls back to `ROOTS_ALLOWED_PREFIXES`, then to the registered roots; never the whole filesystem. |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for module↔core gRPC (`true` for local dev) |

## Capability

`media.roots` — Library root-folder catalog

## Quick Start

```bash
go build -o media-root-folders ./cmd/module

export MUXCORE_INSECURE_DISABLE_TLS=true
./media-root-folders --muxcore-mesh-addr localhost:9090
```

## License

GPL-3.0
