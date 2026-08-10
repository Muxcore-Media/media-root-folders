# Changelog

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
