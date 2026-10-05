# Changelog

## [0.1.12] - 2026-10-05


### Security
- New operator allow-list `ROOTS_ALLOWED_PREFIXES` (comma-separated absolute prefixes, sdk/go/module v0.6.6 `pathguard`): `CreateRoot`, `UpdateRoot` (path change), `SeedFromPaths` and `ProbeRoot` only accept paths that resolve inside it (symlinks resolved, `..` refused, `/data/movies2` is not inside `/data/movies`). Unset means fail-closed: these RPCs return `FailedPrecondition`, so `/`, `/etc` etc. can no longer become roots (NFR-SEC-008 / RULE-VAL-1).
- `BrowsePath` no longer defaults to the whole filesystem: it is confined (symlink-aware) to `ROOTS_BROWSE_PREFIXES`, else `ROOTS_ALLOWED_PREFIXES`, else the registered roots, and refuses when none exist. An empty path starts at the single browse root or lists the roots; `parent` is never above a root and symlinked directories are not offered.
- Registered roots that resolve outside `ROOTS_ALLOWED_PREFIXES` (legacy rows) are logged at start-up and reported `accessible=false`, so `PickRoot` never hands them out; they remain listed for deletion.

### Changed
- Deploy prerequisite: set `ROOTS_ALLOWED_PREFIXES` to the library mounts (run-host / compose / NixOS env) before upgrading, or root creation and probing are refused.

## [0.1.11] - 2026-10-05


### Security
- gRPC server and peer dials use mesh TLS (meshtls, sdk/go/module v0.6.5) unless the dev insecure flag is set (ADR-0016/0017).

## [0.1.10] - 2026-10-05

### Changed
- Built on core v0.6.14 / sdk/go/module v0.6.4: unregisters on shutdown and re-registers after core restarts (ADR-0022).

## [0.1.9] - 2026-10-05


### Changed
- Reported version comes from muxcore.json (ADR-0021); built on core v0.6.12 / sdk/go/module v0.6.3 (mesh enrollment, ADR-0017).

## [0.1.7] - 2026-10-05

### Changed
- CI runs on GitHub-hosted runners from the umbrella template; retired-origin workflows removed.
- Dependencies resolve from published GitHub tags (no filesystem `replace`); requires core v0.6.0.

## [0.1.6] — 2026-08-10

### Added

- Advertise `settings` capability so admin-ui discovers SettingsProvider without ListAll probing.

## [0.1.5] — 2026-08-10

### Added
- SettingsProvider mesh (`RegisterSettings`) for `require_accessible` create/update validation.

### Changed
- Pin `core/sdk/go/module` to **v0.5.2**.


## [0.1.4] — 2026-08-10

### Fixed
- Sync Info()/muxcore.json version to **0.1.4**.


## [0.1.3] — 2026-08-10

### Fixed
- Module `Info().Version` aligned to **0.1.3** (was 0.1.0).

## v0.1.1 (2026-08-09)

- MVP host integration: library root-folder catalog for movies/TV path binding
- Free-space / accessibility probes; admin UI picker under `media.roots`

## v0.1.0

- Initial release (`RootFolderService`, SQLite catalog)
