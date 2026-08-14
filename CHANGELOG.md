# Changelog

All notable changes to this project are documented in this file.

The project uses calendar-based versions in the form `vYYYY.MM.DD`; an additional numeric component distinguishes multiple releases on the same day.

## [v2026.08.14] - 2026-08-14

### Added

- English project homepage with a linked Russian version.
- Project logo, hero artwork, status badges, and focused documentation pages.
- MIT license and automated GitHub Release creation for version tags.

### Changed

- Increased the default download concurrency to seven jobs without a waiting download queue.
- Restored lowercase Russian and English user-facing locale text.
- Updated CI actions and split Go quality checks from the Docker build.

## [v2026.08.13] - 2026-08-13

### Added

- Hardened Docker deployment with backups, health verification, and automatic image rollback.
- SQLite-backed caching, pending update persistence, metrics, health checks, and administration commands.
- Inline search, metadata-link resolution, playlist ranges, archive splitting, and shared request deduplication.

### Security

- Added read-only cookies handling through isolated temporary copies.
- Added safe SSH hardening that disables password authentication only after validating an installed public key.
- Tightened container permissions and protected deployment secrets and backups.

[v2026.08.14]: https://github.com/d6xd/e6akl4k/compare/v2026.08.13...v2026.08.14
[v2026.08.13]: https://github.com/d6xd/e6akl4k/releases/tag/v2026.08.13
