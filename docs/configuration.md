# Configuration

The bot reads `.env` from its working directory. Existing process environment variables take precedence over values from the file.

Copy the example before a manual launch:

```bash
cp .env.example .env
chmod 600 .env
```

## Telegram and paths

| Variable | Default | Description |
| --- | --- | --- |
| `BOT_TOKEN` | required | Token issued by `@BotFather`. |
| `CACHE_CHAT_ID` | empty | Private cache channel ID used for inline audio and, by default, for user-facing error reports. |
| `INLINE_CACHE_CHAT_ID` | empty | Legacy alias for `CACHE_CHAT_ID`. |
| `ERROR_CHAT_ID` | `CACHE_CHAT_ID` | Optional separate chat for user-facing error reports. Without both variables error reporting is disabled. |
| `LASTFM_API_KEY` | empty | last.fm API key that enables `/lastfm`. Without it the command is hidden from the menu. Create one at https://www.last.fm/api/account/create. |
| `TELEGRAM_API_URL` | empty | Base URL of a local Telegram Bot API server, such as `http://telegram-bot-api:8081`. Empty means the cloud Bot API. See [Files over 50 MB](#files-over-50-mb). |
| `YANDEX_PROXY` | empty | `socks5h://`, `socks5://`, `http://`, or `https://` proxy for Yandex Music site and API requests only. See [Yandex Music outside the CIS](#yandex-music-outside-the-cis). |
| `ADMIN_IDS` | empty | Comma-separated immutable owner IDs. Owners manage dynamic admins; all admins can use moderation, `/perf`, and redacted `/log`. |
| `DOWNLOAD_DIR` | `downloads` | Temporary download directory. |
| `DATABASE_PATH` | `$XDG_DATA_HOME/musicbot.db` | SQLite database path. Falls back under `DOWNLOAD_DIR` when `XDG_DATA_HOME` is unset. |
| `YTDLP_COOKIES_FILE` | `cookies.txt` | Netscape-format cookies file used through isolated temporary copies. |
| `YTDLP_SLEEP_REQUESTS` | `0` | Delay in seconds between `yt-dlp` HTTP requests, from 0 to 60. |
| `YTDLP_CONCURRENT_FRAGMENTS` | `4` | Concurrent HLS/DASH fragments per `yt-dlp` process, from 1 to 16. |
| `YTDLP_YOUTUBE_CLIENTS` | `tv_simply` | Comma-separated YouTube player clients that the download without cookies alternates with the `yt-dlp` default clients, for example `tv_simply,web` or `default,-web`; `default` keeps only the `yt-dlp` choice. See [deployment](deployment.md#http-error-403-forbidden-with-valid-cookies). |
| `YTDLP_YOUTUBE_COOKIE_CLIENTS` | `mweb` | Player clients for the download with cookies after YouTube asks to sign in. |
| `YTDLP_POT_PROVIDER_URL` | empty | Base address of a bgutil PO token server for YouTube, such as `http://bgutil-pot:4416`; empty runs `yt-dlp` without PO tokens. See [YouTube PO tokens](#youtube-po-tokens). |

## Scheduling and limits

| Variable | Default | Allowed range or meaning |
| --- | --- | --- |
| `DOWNLOAD_WORKERS` | `7` | Concurrent heavy downloads, from 1 to 32. |
| `DOWNLOAD_QUEUE_SIZE` | `0` | Waiting download slots. Zero rejects excess work immediately. |
| `LOOKUP_WORKERS` | `2` | Concurrent preview and search jobs, from 1 to 32. |
| `LOOKUP_QUEUE_SIZE` | `40` | Waiting lookup jobs. |
| `ARCHIVE_WORKERS` | `1` | Concurrent ZIP jobs, from 1 to 8. |
| `ARCHIVE_QUEUE_SIZE` | `10` | Waiting ZIP jobs. |
| `UPDATE_WORKERS` | `32` | Concurrent Telegram update handlers, from 1 to 256. |
| `UPDATE_QUEUE_SIZE` | `256` | In-memory update buffer. |
| `RATE_LIMIT` | `12` | Private-chat requests allowed per `RATE_WINDOW`. |
| `INLINE_RATE_LIMIT` | `60` | Inline requests allowed per `RATE_WINDOW`. |
| `RATE_WINDOW` | `1m` | Go duration used for rate limiting. |
| `MAX_PLAYLIST_TRACKS` | `75` | Maximum tracks accepted in one request, from 1 to 1000. |
| `MAX_FILE_SIZE` | `52428800` | Maximum individual file size in bytes: up to 50 MiB on the cloud Bot API and up to 2000 MiB with `TELEGRAM_API_URL`. |

By default, seven downloads start immediately and there is no waiting download queue. An eighth simultaneous request receives a busy response. Lookup and archive jobs retain their own FIFO queues.

The zero request delay and four concurrent fragments favor download latency. Increase `YTDLP_SLEEP_REQUESTS` or reduce fragment concurrency if a source starts throttling the server IP.

## Storage and operations

| Variable | Default | Description |
| --- | --- | --- |
| `CACHE_TTL` | `4320h` | Lifetime of cached Telegram `file_id` entries. |
| `DROP_PENDING_UPDATES` | `false` | Drop Telegram updates accumulated while offline. Enable only for an intentional one-time reset. |
| `SHUTDOWN_TIMEOUT` | `30s` | Graceful shutdown deadline. |
| `HTTP_ADDR` | `127.0.0.1:8080` | Health and metrics listen address. Docker overrides it to `0.0.0.0:8080`. |
| `LOG_FORMAT` | text | Set to `json` for structured logs. |
| `DISK_WARNING_BYTES` | `536870912` | Free-space threshold that triggers an administrator warning. |
| `DISK_CHECK_INTERVAL` | `10m` | Disk monitoring interval. |
| `STATUS_MESSAGE` | `true` | Keep a pinned status message in the error chat and announce failures and recoveries there. See [Status message](#status-message). |
| `COOKIE_CHECK_INTERVAL` | `3h` | How often the status monitor checks the YouTube login with the configured cookies. `0` disables the scheduled check. |
| `ERROR_DIGEST_INTERVAL` | `24h` | Window of the silent digest of expected errors. `0` disables the digest. |

Completed tracks, user language, observed Telegram username-to-ID mappings, dismissed support notices, counters, pending Telegram updates, dynamic administrators, bans, audit records, bounded performance samples, and 90 days of download history are stored in SQLite. `/id @username` can resolve only users previously visible to the bot because the Bot API does not provide arbitrary username lookup. The post-download support notice is shown at most once per 24 hours until the user permanently hides it. IDs in `ADMIN_IDS` are immutable owners; only owners may use `/addadmin` and `/deladmin`. The old `inline-audio-cache.json` is imported automatically and renamed with a `.migrated` suffix.

Structured `media_stage` log records split latency into source probing/downloading, cover loading, transcoding, Telegram upload or remote fetch, and cached `file_id` delivery. Byte-carrying stages also report `size_bytes` and `bytes_per_second`. Secret-free samples are retained for 30 days with a 50,000-row cap and are available through `/perf [1h|24h|7d]`; `/log [fresh] <url>` returns a redacted trace of one real MP3 320 workflow.

## Error reports

Every error a user sees (link preview, search, download, playlist or ZIP delivery, cached re-send, `/history`, and inline downloads) is also posted to `ERROR_CHAT_ID` or, by default, to the cache channel. A post contains the stage, the user ID and last known username, the link with only track-identifying query parameters (`v`, `list`, `t`, `track`), the search query, the format, and the error text with signed-URL and cookie fragments removed.

Cancellations, a busy queue, rate limits, and the playlist size limit are not reported. Identical stage and error pairs are folded for ten minutes (per-video IDs in `yt-dlp` messages are ignored for this comparison), and the next post of the same error shows how many repeats were folded. Per-track failures of one playlist or batch are combined into a single post. Posts are sent by one background worker at most every three seconds from a queue of 64 distinct reports; overflow is dropped and counted as `error_reports_dropped`, delivered posts as `error_reports_sent`.

Every report has a short ID such as `r1a2b3c4`, which is also written to the `error_report id=…` log line, and shows the bot commit and `yt-dlp` version. Reports are kept in SQLite for 14 days.

Errors caused by the content itself (a deleted, private, members-only, or geo-blocked video, an unsupported link, nothing found, HTTP 404) are expected: they are not posted one by one but summarised in a silent digest once per `ERROR_DIGEST_INTERVAL`, grouped by stage and error with the number of distinct users. An error that mentions a 403, a bot check, a rate limit, or "try again later" is always real, as is any error with a line that matches no expected pattern. Real errors are posted silently; when the same error reaches three different users within 15 minutes, a loud 🚨 spike post goes out even inside the folding window, at most once an hour per error. Administrators, for example one repeating a reported request, do not count as affected users.

Buttons under a report work for administrators only, and their answers go to the administrator's private chat:

- «повторить» repeats a link preview, download, playlist, batch, cached re-send, or inline request with the reported format, and the result comes to the administrator.
- «починено → пользователю» asks for confirmation, then tells the user the error was fixed and repeats the request for them. The report is claimed first, so two administrators cannot notify a user twice; banned users are skipped, and the action is recorded in the audit as `report_fix_sent`.
- «для разработчика» sends a plain-text copy for an issue: report ID, time, version, stage, source, redacted link, query, format, and error, without the user ID or username.

## Status message

With `STATUS_MESSAGE=true` the bot posts a status message to the error chat, pins it silently, and checks every minute:

- Telegram API (the local server when `TELEGRAM_API_URL` is set) with `getMe`;
- SQLite;
- free disk space: yellow below `DISK_WARNING_BYTES`, red below a quarter of it;
- YouTube cookies: the result and age of the last login check (the same Watch Later check as the stale-cookie detector, scheduled every `COOKIE_CHECK_INTERVAL`) and the age of the cookies file;
- the Yandex relay, when `YANDEX_PROXY` is set, with a TCP connection to the proxy; the address is never shown;
- the YouTube PO token server, when `YTDLP_POT_PROVIDER_URL` is set, with a TCP connection to it;
- `yt-dlp`: the installed version and, once every six hours, the newest release on GitHub;
- error reports: yellow for an hour after the report queue dropped reports.

The message also shows real and expected errors of the last 24 hours, the last real error with its ID, the last spike, the download queue, the version, the uptime, and the update time. It is edited as soon as something changes and at least every ten minutes. When a component turns red, a loud 🔴 post goes out; a yellow component gets a silent 🟡 post and a recovery a silent 🟢 post with the duration of the problem (loud if the failure itself could not be posted). Component states and the message ID are kept in SQLite, so a restart neither repeats alerts nor posts a second message. If the message is deleted, the next edit posts and pins a new one.

The bot needs to post, edit, and pin messages in the error chat. In a channel that means the "post messages", "edit messages of others", and pin rights; if pinning fails, administrators get one private message about it.

## Files over 50 MB

The cloud Bot API accepts bot uploads up to 50 MiB, about 7 minutes of FLAC, 20 minutes of MP3 320, or 50 minutes of MP3 128. FLAC is written as 16-bit, since sources are lossy and 24 bits only make the file larger. Before a download the bot skips a track whose estimated size exceeds `MAX_FILE_SIZE`; for FLAC, OGG, M4A, and MP3 best the estimate may exceed the limit by 25%, because their real size varies with the material, and the downloaded file is checked again before upload. A track over the limit is not an error: the user gets buttons with the formats whose estimate fits the limit, or one notice with such buttons for all tracks of a playlist or batch that did not fit. These cases are not posted to the error chat; they are counted as `downloads_too_large` and `downloads_too_large_<format>` and shown in `/stats`.

To send files up to 2000 MiB, run a local Telegram Bot API server. `docker-compose.yml` has an optional `telegram-bot-api` service (the `aiogram/telegram-bot-api` image in `--local` mode) that listens on port 8081 of the project network only:

1. Create an application at https://my.telegram.org/apps. An existing application, for example one of a Telegram client, works too.
2. Write its `api_id` and `api_hash` to `telegram-bot-api.env` as `TELEGRAM_API_ID=...` and `TELEGRAM_API_HASH=...`, then `chmod 600 telegram-bot-api.env`. The file is ignored by git.
3. Add `telegram-bot-api` to `COMPOSE_PROFILES` in `.env` (profiles are comma-separated, for example `COMPOSE_PROFILES=yandex-relay,telegram-bot-api`), set `TELEGRAM_API_URL=http://telegram-bot-api:8081` and a larger `MAX_FILE_SIZE` such as `524288000` (500 MiB), then redeploy.

On its first start with `TELEGRAM_API_URL`, the bot logs out of the cloud Bot API, as the local server requires, and remembers that in SQLite, so later restarts do not log out again. After the logout the cloud Bot API refuses the bot for 10 minutes: to move back, remove `TELEGRAM_API_URL` and the larger `MAX_FILE_SIZE`, wait 10 minutes, and redeploy. Until the local server answers, the bot retries for a minute and then exits, so Docker restarts it. Cached `file_id` values stay valid on both servers. The server's `/var/lib/telegram-bot-api` volume holds a directory per bot token; do not share or list it.

## Yandex Music outside the CIS

Yandex Music answers HTTP 451 to servers outside the CIS: its links then get a "service refuses the server's country" message instead of a tracklist or a search. `YANDEX_PROXY` sends requests to `music.yandex.*` and `api.music.yandex.*` through a proxy with a CIS exit; other services and the Yandex artwork CDN are reached directly. The resolver timeout, redirect allowlist, and response limits stay the same.

`docker-compose.yml` has an optional `yandex-relay` service, an Xray client that exposes SOCKS on port 1080 of the project network only and forwards to a VLESS server in the CIS:

1. Copy `yandex-relay.example.json` to `yandex-relay.json` and fill in the `RELAY_*` placeholders, or replace the `relay` outbound with the one from your VLESS client config (keep the `relay` tag). The file is ignored by git.
2. `chown 10001:10001 yandex-relay.json && chmod 0600 yandex-relay.json`.
3. Append `COMPOSE_PROFILES=yandex-relay` and `YANDEX_PROXY=socks5h://yandex-relay:1080` to `.env`, then redeploy.

The relay routes only Yandex Music host names and drops every other destination, so it cannot be used as a general exit. Use `socks5h` so that the host name, not a local DNS answer, reaches the relay.

## YouTube PO tokens

YouTube answers media requests without a proof-of-origin (PO) token with `403` for more and more clients, and which clients still work without one changes from hour to hour (see [deployment](deployment.md#http-error-403-forbidden-with-valid-cookies)). `docker-compose.yml` has an optional `bgutil-pot` service, the [bgutil PO token server](https://github.com/Brainicism/bgutil-ytdlp-pot-provider), which listens on port 4416 of the project network only. The bot image ships the matching `yt-dlp` plugin in `/opt/yt-dlp-plugins`, outside the default plugin folders, so it loads only when the provider is set:

1. Add `bgutil-pot` to `COMPOSE_PROFILES` in `.env` (for example `COMPOSE_PROFILES=yandex-relay,telegram-bot-api,bgutil-pot`; a later line overrides an earlier one) and append `YTDLP_POT_PROVIDER_URL=http://bgutil-pot:4416`.
2. Redeploy. Every `yt-dlp` call then gets `--plugin-dirs /opt/yt-dlp-plugins --extractor-args youtubepot-bgutilhttp:base_url=http://bgutil-pot:4416`, and the status message gets a "токены YouTube (PO)" line ("YouTube PO tokens" in English).

The server sends YouTube's challenge to YouTube from its own container and never sees the cookies. A token takes a few seconds on the first request and is cached by the server afterwards. With tokens, on 2026-09-26 the anonymous `tv_simply`, `mweb`, and default clients and the signed-in `mweb` client downloaded fully, while the default signed-in clients still got `403`, so the default `YTDLP_YOUTUBE_CLIENTS` and `YTDLP_YOUTUBE_COOKIE_CLIENTS` stay right.

The plugin refuses a server of another version: bump the `bgutil-pot` image in `docker-compose.yml` and `BGUTIL_PLUGIN_VERSION` with `BGUTIL_PLUGIN_SHA256` in the `Dockerfile` together. Both are third-party code, pinned by digest and checksum; review a release before bumping it. To turn the provider off, remove `YTDLP_POT_PROVIDER_URL` and redeploy.

## Notices

Administrators post notices with `/msgall <text>` to every user the bot knows and `/msg <tg_id> <text>` to one user. Instead of text, either command can reply to any message (text, photo, video, or file), which is then copied as is. Formatting of the text is kept. `/msgall` first shows the notice exactly as users will get it and sends it only after the author confirms; confirmation expires after 15 minutes and is lost on restart, so a notice is never sent twice. Broadcasts run one at a time in the background at 20 messages a second, and the confirmation message shows progress and a final report of delivered, unreachable (blocked the bot or deleted the account), and failed recipients. The sender, banned users, and users who muted notices are skipped; `/msg` refuses a muted user and tells the administrator so. Both commands are recorded in the administrator audit.

Every notice carries a button that mutes further notices; users can also switch them with `/notify`, `/notify on`, and `/notify off`. The setting is stored in SQLite.

## Inline mode

1. Create a private Telegram channel.
2. Add the bot as an administrator with permission to post messages.
3. Set its numeric ID as `CACHE_CHAT_ID`.
4. Run `/setinline` in `@BotFather` and configure a placeholder such as `Find music`.
5. Run `/setinlinefeedback` and set it to `100`. Without inline feedback, Telegram does not tell the bot which result was sent, so a track that is not cached yet stays a loading message.
6. Restart the bot and type `@username song name` in any chat.

Inline results show the track title, artist, duration, and YouTube thumbnail. A track already in the cache channel is sent as audio right away; any other track is sent as a loading message with a cancel button, and the bot turns that message into the audio once the MP3 320 is downloaded and cached. A pasted YouTube, SoundCloud, or other supported link becomes one result, and a music-service track link is searched on YouTube by its title. Playlists and albums are left to the bot chat, and the button above the results says why the list is empty.
