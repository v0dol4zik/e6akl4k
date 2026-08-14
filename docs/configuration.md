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
| `CACHE_CHAT_ID` | empty | Private cache channel ID used for inline audio. |
| `INLINE_CACHE_CHAT_ID` | empty | Legacy alias for `CACHE_CHAT_ID`. |
| `INLINE_PLACEHOLDER_FILE_ID` | generated | Existing silent MP3 `file_id` for inline placeholders. |
| `ADMIN_IDS` | empty | Comma-separated Telegram user IDs allowed to use `/stats` and `/status`. |
| `DOWNLOAD_DIR` | `downloads` | Temporary download directory. |
| `DATABASE_PATH` | `$XDG_DATA_HOME/musicbot.db` | SQLite database path. Falls back under `DOWNLOAD_DIR` when `XDG_DATA_HOME` is unset. |
| `YTDLP_COOKIES_FILE` | `cookies.txt` | Netscape-format cookies file used through isolated temporary copies. |

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

Completed tracks, user language, counters, pending Telegram updates, and 90 days of history are stored in SQLite. The old `inline-audio-cache.json` is imported automatically and renamed with a `.migrated` suffix.

## Inline mode

1. Create a private Telegram channel.
2. Add the bot as an administrator with permission to post messages.
3. Set its numeric ID as `CACHE_CHAT_ID`.
4. Run `/setinline` in `@BotFather` and configure a placeholder such as `Find music`.
5. Run `/setinlinefeedback` and set it to `100`.
6. Restart the bot and type `@username song name` in any chat.

On its first launch, the bot creates a one-second silent MP3 and uploads it to the cache channel. Set `INLINE_PLACEHOLDER_FILE_ID` only when you want to provide this `file_id` explicitly.
