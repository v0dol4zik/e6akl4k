# Changelog

All notable changes to this project are documented in this file.

The project uses calendar-based versions in the form `vYYYY.MM.DD`; an additional numeric component distinguishes multiple releases on the same day.

## [Unreleased]

### Added

- Added persistent dynamic administrators, bans, owner-only role management, moderation audit, `/perf`, and redacted `/log [fresh] <url>` diagnostics.
- Added confidence-ranked search with alternate-version penalties and exact/similar/version labels.
- Added one-time `bootstrap.sh`, lean versioned-image deployment, Registry publishing, and explicit `rollback.sh`.
- Added `/id @username` lookup for Telegram users previously observed by the bot.
- Added a throttled post-download support message with a persistent per-user hide option.
- Added `/settings` with a per-user default format and quality that skips the format keyboard for tracks, search picks, and playlist ranges, with an "ask each time" option and a visible hint in the download status.
- Added `/history` listing the last ten delivered tracks with one-tap re-delivery from the Telegram `file_id` cache, an expired-cache hint instead of a new download, and a clear-history button.
- Added search by forwarded audio in private chats: the performer and title tags (or the file name) become the query and the audio duration guides ranking.
- Added Prometheus series for every persistent counter, per-stage P50/P95 latency and success ratios, and the cache hit ratio, with SQLite aggregates cached for 30 seconds between scrapes.
- Added a stale YouTube cookies detector that treats three HTTP 403 failures within ten minutes as a cookie problem, persists a six-hour administrator alert cooldown, adds a "check" button that runs a fresh download trace, and exposes `yt_dlp_cookies` in `/healthz`.
- Added forwarding of every user-facing error to the cache channel (or a separate `ERROR_CHAT_ID`): stage, user, redacted link or search query, format and error text, with ten-minute folding of identical errors, one combined post per playlist or batch delivery, a three-second posting interval, and `error_reports_sent` / `error_reports_dropped` counters.
- Added last.fm integration behind `LASTFM_API_KEY`: `/lastfm name` (or a last.fm/user link) links a profile after checking it, and the menu shows the last 10, 25, or 50 scrobbles with the track playing now, loved tracks, and the top of the week or month. Any listed track is searched on YouTube with one tap, and a list can be exported as `.txt`. The API key is redacted from every error, a missing or private profile and the API rate limit are explained to the user, and `lastfm_links` and `lastfm_lists` counters are recorded.
- Added tracklist and cover export, an idea by @yamusic_export_bot: `/export` and `/cover` commands (with a link or as a reply) plus "tracklist as text" and "cover" buttons under every preview. Tracks become "Artist - Title" lines with upload tags such as "(Official Video)" removed; a single track is sent as a copyable line and a playlist, album, chosen range, or batch as a `.txt` file of up to 1000 lines. Deezer and Yandex Music playlists and albums are read through their public APIs, other sources through a flat `yt-dlp` probe. Covers are sent as files so Telegram does not recompress them, prefer square album art, request the largest Deezer (1800×1800) and Yandex (original) rendition with a fallback to the API size, crop letterboxed YouTube "Topic" thumbnails to the square losslessly as PNG, and are fetched only over HTTPS from the Deezer and Yandex artwork CDNs. `exports` and `covers` counters are recorded.
- Added administrator notices: `/msgall` broadcasts to every user after a preview and confirmation, `/msg <tg_id>` messages one user, and either command can copy a replied-to message with its media. Broadcasts run one at a time in the background at 20 messages a second with live progress and a final report of delivered, unreachable, and failed recipients, skip banned users, and are recorded in the audit. Every notice has a mute button, and `/notify` (`on` / `off`) switches notices per user.
- Added batch downloads for several track links in one message (up to five): one shared format, sequential downloads inside a single slot, cached tracks re-sent by `file_id`, per-link failures reported without aborting the batch, and a ZIP-or-individual delivery choice for every batch of two or more links.

### Changed

- Removed the Octave Streaming integration completely: private title search, inline search, and every download now go through YouTube via `yt-dlp`. The Octave API client, remote-URL fast path, circuit breaker, `search_octave` / `search_youtube_fallback` counters, and the `musicbot_octave_*` metrics are gone, and `music.octavestreaming.com` links are no longer recognized.
- Replaced the generated project branding with the CC0 music gopher artwork.
- Made `yt-dlp` request delay and fragment concurrency configurable, and skipped redundant MP3 remuxing.
- Added structured per-stage timing and throughput logs for source, conversion, Telegram upload, and cached delivery operations.
- Pipelined large album delivery so the next bounded batch downloads while the current batch is sent.
- Kept administrator commands out of Telegram's published command menu while preserving permission-checked manual access.

### Fixed

- Administrator usage hints such as `/ban <tg_id>` are HTML-escaped, so Telegram no longer rejects them as malformed markup.
- YouTube downloads that fail with `HTTP Error 403` while cookies are attached are retried once without cookies (YouTube binds some cookie sessions to SABR-only streaming); the retry is logged, counted as `youtube_cookie_retries`, and three retries within ten minutes send administrators a "degraded cookies" alert.
- Batch downloads now release their download slot before Telegram delivery, so cached re-sends, archive waits and uploads no longer block other users.
- Clearing `/history` removes only the entries the list can show; failed and legacy rows stay for administrator statistics.
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
