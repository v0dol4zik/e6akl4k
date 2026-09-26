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
- Added a stale YouTube cookies detector that treats a bot check, rotated cookies, or three HTTP 403 failures within ten minutes as a suspected cookie problem and confirms it with a login check before alerting administrators: the account's Watch Later playlist, which YouTube serves only to a signed-in session, is opened with an isolated copy of the cookies. The alert goes out only when that check fails; a working login is trusted for 30 minutes, and a network error or timeout is only logged. Results are counted as `youtube_cookie_login_ok`, `youtube_cookie_login_failed`, and `youtube_cookie_login_unknown`, alerts keep a persisted six-hour cooldown and a "check" button that runs a fresh download trace, and `/healthz` exposes `yt_dlp_cookies`.
- Added forwarding of every user-facing error to the cache channel (or a separate `ERROR_CHAT_ID`): stage, user, redacted link or search query, format and error text, with ten-minute folding of identical errors, one combined post per playlist or batch delivery, a three-second posting interval, and `error_reports_sent` / `error_reports_dropped` counters.
- Added last.fm integration behind `LASTFM_API_KEY`: `/lastfm name` (or a last.fm/user link) links a profile after checking it, and the menu shows the last 10, 25, or 50 scrobbles with the track playing now, loved tracks, and the top of the week or month. Any listed track is searched on YouTube with one tap, and a list can be exported as `.txt`. The API key is redacted from every error, a missing or private profile and the API rate limit are explained to the user, and `lastfm_links` and `lastfm_lists` counters are recorded.
- Added tracklist and cover export, an idea by @yamusic_export_bot: `/export` and `/cover` commands (with a link or as a reply) plus "tracklist as text" and "cover" buttons under every preview. Tracks become "Artist - Title" lines with upload tags such as "(Official Video)" removed; a single track is sent as a copyable line and a playlist, album, chosen range, or batch as a `.txt` file of up to 1000 lines. Deezer and Yandex Music playlists and albums are read through their public APIs, other sources through a flat `yt-dlp` probe. Covers are sent as files so Telegram does not recompress them, prefer square album art, request the largest Deezer (1800×1800) and Yandex (original) rendition with a fallback to the API size, crop letterboxed YouTube "Topic" thumbnails to the square losslessly as PNG, and are fetched only over HTTPS from the Deezer and Yandex artwork CDNs. `exports` and `covers` counters are recorded.
- Added administrator notices: `/msgall` broadcasts to every user after a preview and confirmation, `/msg <tg_id>` messages one user, and either command can copy a replied-to message with its media. Broadcasts run one at a time in the background at 20 messages a second with live progress and a final report of delivered, unreachable, and failed recipients, skip banned users, and are recorded in the audit. Every notice has a mute button, and `/notify` (`on` / `off`) switches notices per user.
- Added `YANDEX_PROXY` for Yandex Music links from servers outside the CIS: only `music.yandex.*` and `api.music.yandex.*` requests go through the proxy, with an optional `yandex-relay` Xray sidecar (Compose profile, SOCKS on the project network only, Yandex Music hosts only) and `yandex-relay.example.json`.
- Added batch downloads for several track links in one message (up to five): one shared format, sequential downloads inside a single slot, cached tracks re-sent by `file_id`, per-link failures reported without aborting the batch, and a ZIP-or-individual delivery choice for every batch of two or more links.
- Added an optional local Telegram Bot API server for files up to 2000 MiB: `TELEGRAM_API_URL` points the bot at it, `MAX_FILE_SIZE` may then go up to 2000 MiB, and the `telegram-bot-api` Compose profile runs `aiogram/telegram-bot-api` in `--local` mode on the project network only, with `api_id` and `api_hash` in a separate ignored `telegram-bot-api.env`. The bot logs out of the cloud Bot API once and remembers it in SQLite, waits up to a minute for the local server, splits media groups by size, and explains the 10-minute cloud lockout if it is started on the cloud right after the switch.

### Changed

- Removed the Octave Streaming integration completely: private title search, inline search, and every download now go through YouTube via `yt-dlp`. The Octave API client, remote-URL fast path, circuit breaker, `search_octave` / `search_youtube_fallback` counters, and the `musicbot_octave_*` metrics are gone, and `music.octavestreaming.com` links are no longer recognized.
- Replaced the generated project branding with the CC0 music gopher artwork.
- Made `yt-dlp` request delay and fragment concurrency configurable, and skipped redundant MP3 remuxing.
- Added structured per-stage timing and throughput logs for source, conversion, Telegram upload, and cached delivery operations.
- Pipelined large album delivery so the next bounded batch downloads while the current batch is sent.
- Kept administrator commands out of Telegram's published command menu while preserving permission-checked manual access.
- A track over the Telegram file limit is no longer an error: instead of «это дольше 3:37: файл не влезет в лимит Telegram» the user gets buttons with the lighter formats that fit, or one notice for all such tracks of a playlist or batch, and these cases are counted as `downloads_too_large` (also per format and in `/stats`) instead of being posted to the error chat. FLAC is written as 16-bit, about 7.2 MiB a minute instead of 12.7, and the pre-download check lets FLAC, OGG, M4A, and MP3 best estimates exceed the limit by 25% because the real file size is checked before upload.

### Fixed

- Playlist, album, and artist links of Spotify, Apple Music, Deezer, Tidal, and Yandex Music are no longer searched on YouTube as a single track by their title. Deezer and Yandex Music collections show their name and track count with "tracklist as text" and "cover" buttons (the shown list is exported without a new request), other collections get a hint to send single tracks, and Deezer `link.deezer.com` share links are expanded first.
- Yandex Music links from a geo-blocked server (HTTP 451) now explain that the service refuses the server's country, in the preview, `/export`, and `/cover`, and are reported to the operator chat, instead of searching the home page title "Яндекс Музыка — собираем музыку для вас" and offering a random video. Yandex tracks are named through the track API before the page, and any link that opens a service home page is explained rather than searched.
- Previews and downloads of long YouTube playlists no longer fail with `context deadline exceeded`: the tracklist is read with a flat `yt-dlp` probe instead of resolving every video first.
- A failed link preview replaces the "analyzing" status instead of leaving it behind.
- FLAC and OGG downloads no longer fail with `module mutagen was not found`: the image installs `python3-mutagen`, which `yt-dlp` needs to embed covers into these formats, and the build checks that it imports.
- Administrator usage hints such as `/ban <tg_id>` are HTML-escaped, so Telegram no longer rejects them as malformed markup.
- The Docker image build no longer hangs on a stalled Debian package download: each parallel fetch has connect and stall timeouts and retries on any error, including connection resets, every networked `apt-get` run, including the index update, is capped at three minutes and retried up to five times, and the `yt-dlp` and Deno release downloads use the same connect, stall, and retry limits.
- YouTube downloads that fail with `HTTP Error 403` while cookies are attached are retried once without cookies (YouTube binds some cookie sessions to SABR-only streaming); the retry is logged and counted as `youtube_cookie_retries`, and three retries within ten minutes send administrators a "degraded cookies" alert only if a login check with the same cookies fails, since YouTube also answers working cookies with an occasional 403.
- Batch downloads now release their download slot before Telegram delivery, so cached re-sends, archive waits and uploads no longer block other users.
- Clearing `/history` removes only the entries the list can show; failed and legacy rows stay for administrator statistics.
- Delete the upload-progress status after successful delivery instead of leaving a stale `100%` message behind.
- Inline results show real track titles, artists, durations, and YouTube thumbnails instead of the "Downloading…" placeholder audio. A track that is not cached yet is sent as a loading text message with a cancel button, which the bot edits into the audio once it is cached; cached tracks are still sent as audio right away. The silent placeholder MP3 and `INLINE_PLACEHOLDER_FILE_ID` are gone.
- Inline mode no longer loses messages or overwrites a delivered track: failures, cancellations, the Telegram size limit, a bot restart, and expired results are written into the sent message instead of an edit that Telegram rejected, and a cancel tapped as the upload finishes can no longer replace the audio with "cancelled".
- Inline queries no longer wait behind the same user's running download or earlier keystrokes, so typing a query cancels the previous search instead of queueing it. Empty, short, rate-limited, and failed queries, playlists, blocked services, and links to a service home page get an explanatory button above the results instead of an empty list.
- Pasted links in inline mode are read like in a private chat: a YouTube video is named through oEmbed without a slow `yt-dlp` probe, other supported links are probed, and Spotify, Apple Music, Deezer, Tidal, and Yandex Music track links are searched on YouTube by their title instead of being offered as the raw URL.
- A search that finds nothing is retried without its last word, up to twice, so a trailing typo such as "subtronics — sploinky dub vshj" still finds the track. A search that still finds nothing answers "nothing found" in place of the "searching" status and is no longer posted to the error chat.
- Spotify links without the `open.` prefix (`spotify.com/track/...`) are moved to `open.spotify.com` before reading their title. Music-service track links skip the `yt-dlp` probe, which has no extractor for these services and failed with TLS handshake timeouts while downloading their pages.
- YouTube downloads start without cookies, because YouTube now answers a signed-in session's media requests with `HTTP Error 403: Forbidden` while anonymous downloads work. Only the tracks that failed are downloaded again, so a playlist never starts over: an anonymous `403` gets one fresh attempt, and the cookies are attached only when YouTube asks to sign in. A download no longer fails after both the cookie attempt and its single retry got a `403`.

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
