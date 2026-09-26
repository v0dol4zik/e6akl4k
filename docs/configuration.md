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
| `YANDEX_PROXY` | empty | `socks5h://`, `socks5://`, `http://`, or `https://` proxy for Yandex Music site and API requests only. See [Yandex Music outside the CIS](#yandex-music-outside-the-cis). |
| `INLINE_PLACEHOLDER_FILE_ID` | generated | Existing silent MP3 `file_id` for inline placeholders. |
| `ADMIN_IDS` | empty | Comma-separated immutable owner IDs. Owners manage dynamic admins; all admins can use moderation, `/perf`, and redacted `/log`. |
| `DOWNLOAD_DIR` | `downloads` | Temporary download directory. |
| `DATABASE_PATH` | `$XDG_DATA_HOME/musicbot.db` | SQLite database path. Falls back under `DOWNLOAD_DIR` when `XDG_DATA_HOME` is unset. |
| `YTDLP_COOKIES_FILE` | `cookies.txt` | Netscape-format cookies file used through isolated temporary copies. |
| `YTDLP_SLEEP_REQUESTS` | `0` | Delay in seconds between `yt-dlp` HTTP requests, from 0 to 60. |
| `YTDLP_CONCURRENT_FRAGMENTS` | `4` | Concurrent HLS/DASH fragments per `yt-dlp` process, from 1 to 16. |

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
| `MAX_FILE_SIZE` | `52428800` | Maximum individual file size in bytes, capped at Telegram's 50 MiB bot limit. |

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

Completed tracks, user language, observed Telegram username-to-ID mappings, dismissed support notices, counters, pending Telegram updates, dynamic administrators, bans, audit records, bounded performance samples, and 90 days of download history are stored in SQLite. `/id @username` can resolve only users previously visible to the bot because the Bot API does not provide arbitrary username lookup. The post-download support notice is shown at most once per 24 hours until the user permanently hides it. IDs in `ADMIN_IDS` are immutable owners; only owners may use `/addadmin` and `/deladmin`. The old `inline-audio-cache.json` is imported automatically and renamed with a `.migrated` suffix.

Structured `media_stage` log records split latency into source probing/downloading, cover loading, transcoding, Telegram upload or remote fetch, and cached `file_id` delivery. Byte-carrying stages also report `size_bytes` and `bytes_per_second`. Secret-free samples are retained for 30 days with a 50,000-row cap and are available through `/perf [1h|24h|7d]`; `/log [fresh] <url>` returns a redacted trace of one real MP3 320 workflow.

## Error reports

Every error a user sees (link preview, search, download, playlist or ZIP delivery, cached re-send, `/history`, and inline downloads) is also posted to `ERROR_CHAT_ID` or, by default, to the cache channel. A post contains the stage, the user ID and last known username, the link with only track-identifying query parameters (`v`, `list`, `t`, `track`), the search query, the format, and the error text with signed-URL and cookie fragments removed.

Cancellations, a busy queue, rate limits, and the playlist size limit are not reported. Identical stage and error pairs are folded for ten minutes (per-video IDs in `yt-dlp` messages are ignored for this comparison), and the next post of the same error shows how many repeats were folded. Per-track failures of one playlist or batch are combined into a single post. Posts are sent by one background worker at most every three seconds from a queue of 64 distinct reports; overflow is dropped and counted as `error_reports_dropped`, delivered posts as `error_reports_sent`.

## Yandex Music outside the CIS

Yandex Music answers HTTP 451 to servers outside the CIS: its links then get a "service refuses the server's country" message instead of a tracklist or a search. `YANDEX_PROXY` sends requests to `music.yandex.*` and `api.music.yandex.*` through a proxy with a CIS exit; other services and the Yandex artwork CDN are reached directly. The resolver timeout, redirect allowlist, and response limits stay the same.

`docker-compose.yml` has an optional `yandex-relay` service, an Xray client that exposes SOCKS on port 1080 of the project network only and forwards to a VLESS server in the CIS:

1. Copy `yandex-relay.example.json` to `yandex-relay.json` and fill in the `RELAY_*` placeholders, or replace the `relay` outbound with the one from your VLESS client config (keep the `relay` tag). The file is ignored by git.
2. `chown 10001:10001 yandex-relay.json && chmod 0600 yandex-relay.json`.
3. Append `COMPOSE_PROFILES=yandex-relay` and `YANDEX_PROXY=socks5h://yandex-relay:1080` to `.env`, then redeploy.

The relay routes only Yandex Music host names and drops every other destination, so it cannot be used as a general exit. Use `socks5h` so that the host name, not a local DNS answer, reaches the relay.

## Notices

Administrators post notices with `/msgall <text>` to every user the bot knows and `/msg <tg_id> <text>` to one user. Instead of text, either command can reply to any message (text, photo, video, or file), which is then copied as is. Formatting of the text is kept. `/msgall` first shows the notice exactly as users will get it and sends it only after the author confirms; confirmation expires after 15 minutes and is lost on restart, so a notice is never sent twice. Broadcasts run one at a time in the background at 20 messages a second, and the confirmation message shows progress and a final report of delivered, unreachable (blocked the bot or deleted the account), and failed recipients. The sender, banned users, and users who muted notices are skipped; `/msg` refuses a muted user and tells the administrator so. Both commands are recorded in the administrator audit.

Every notice carries a button that mutes further notices; users can also switch them with `/notify`, `/notify on`, and `/notify off`. The setting is stored in SQLite.

## Inline mode

1. Create a private Telegram channel.
2. Add the bot as an administrator with permission to post messages.
3. Set its numeric ID as `CACHE_CHAT_ID`.
4. Run `/setinline` in `@BotFather` and configure a placeholder such as `Find music`.
5. Run `/setinlinefeedback` and set it to `100`.
6. Restart the bot and type `@username song name` in any chat.

On its first launch, the bot creates a one-second silent MP3 and uploads it to the cache channel. Set `INLINE_PLACEHOLDER_FILE_ID` only when you want to provide this `file_id` explicitly.
