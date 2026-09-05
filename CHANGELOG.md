# Changelog

All notable changes to this project are documented in this file.

The project uses calendar-based versions in the form `vYYYY.MM.DD`; an additional numeric component distinguishes multiple releases on the same day.

## [Unreleased]

### Added

- Added Octave Streaming as the primary title-search source, with direct track and album downloads in MP3 128/320 and lossless FLAC.
- Added strict Octave URL parsing, bounded API/media requests, expiring in-memory playback-token caching, and stable Telegram cache keys.
- Added persistent dynamic administrators, bans, owner-only role management, moderation audit, `/perf`, and redacted `/log [fresh] <url>` diagnostics.
- Added Octave remote-URL circuit breaking, playback-token refresh, validated HTTP Range resume, session cover reuse, and multipart Telegram upload progress.
- Added confidence-ranked search with alternate-version penalties and exact/similar/version labels.
- Added one-time `bootstrap.sh`, lean versioned-image deployment, Registry publishing, and explicit `rollback.sh`.
- Added `/id @username` lookup for Telegram users previously observed by the bot.
- Added a throttled post-download support message with a persistent per-user hide option.
- Added `/settings` with a per-user default format and quality that skips the format keyboard for tracks, search picks, and playlist ranges, with an "ask each time" option and a visible hint in the download status.
- Added `/history` listing the last ten delivered tracks with one-tap re-delivery from the Telegram `file_id` cache, an expired-cache hint instead of a new download, and a clear-history button.
- Added search by forwarded audio in private chats: the performer and title tags (or the file name) become the query and the audio duration guides ranking.

### Changed

- Replaced the generated project branding with the CC0 music gopher artwork.
- Kept YouTube search as an automatic fallback when Octave has no matching tracks or its API is unavailable.
- Added an Octave MP3 remote-URL fast path with local fallback, skipped redundant MP3 remuxing, and made `yt-dlp` request delay and fragment concurrency configurable.
- Added structured per-stage timing and throughput logs for source, conversion, Telegram upload, and cached delivery operations.
- Pipelined large album delivery so the next bounded batch downloads while the current batch is sent.
- Kept administrator commands out of Telegram's published command menu while preserving permission-checked manual access.

### Fixed

- Delete the upload-progress status after successful delivery instead of leaving a stale `100%` message behind.

## [v2026.08.14] - 2026-08-14

### Added

- English project homepage with a linked Russian version.
- Project logo, hero artwork, status badges, and focused documentation pages.
- MIT license and automated GitLab Release creation for version tags.

### Changed

- Migrated the primary repository, CI pipeline, badges, and automated releases from GitHub to GitLab.
- Increased the default download concurrency to seven jobs without a waiting download queue.
- Restored lowercase Russian and English user-facing locale text.
- Updated CI images and split Go quality checks from the Docker build.

## [v2026.08.13] - 2026-08-13

### Added

- Hardened Docker deployment with backups, health verification, and automatic image rollback.
- SQLite-backed caching, pending update persistence, metrics, health checks, and administration commands.
- Inline search, metadata-link resolution, playlist ranges, archive splitting, and shared request deduplication.

### Security

- Added read-only cookies handling through isolated temporary copies.
- Added safe SSH hardening that disables password authentication only after validating an installed public key.
- Tightened container permissions and protected deployment secrets and backups.

[v2026.08.14]: https://gitlab.com/d6xd/e6akl4k/-/compare/v2026.08.13...v2026.08.14
[v2026.08.13]: https://gitlab.com/d6xd/e6akl4k/-/tags/v2026.08.13
