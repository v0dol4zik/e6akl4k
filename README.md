# e6akl4k music downloader bot

A Telegram bot written in Go for finding and downloading music with `yt-dlp`.

## Table of contents

- [Русская версия](README.ru.md)
- [Features](#features)
- [Project structure](#project-structure)
- [Inline mode and cache channel](#inline-mode-and-cache-channel)
- [Concurrency and caching](#concurrency-and-caching)
- [Quick deployment](#quick-deployment)
- [YouTube cookies](#youtube-cookies)
- [Manual launch](#manual-launch)
- [Observability and administration](#observability-and-administration)
- [Limitations](#limitations)
- [Verification](#verification)

## Features

- YouTube / YouTube Music, SoundCloud, Bandcamp, VK, Mixcloud, Audiomack, and other sources supported by `yt-dlp`.
- MP3 128/320/VBR, FLAC, M4A, and OGG Vorbis with metadata and cover art.
- Title search directly in a private chat and inline search with `@bot track name` from any chat.
- A preview showing the title, artist, duration, track count, and estimated file size.
- Spotify, Apple Music, Deezer, Tidal, and Yandex Music are treated as metadata links: the bot suggests matching YouTube versions and asks the user to confirm the match.
- Entire playlists, the first 10/25/75 tracks, or ranges of ten tracks.
- MP3/M4A files are sent as audio albums of up to ten files; FLAC/OGG files are sent as documents. ZIP archives are automatically split into parts of approximately 45 MB.
- Russian and English interfaces, progress updates, ETA, and download cancellation.
- A persistent SQLite cache of Telegram `file_id` values: repeated requests do not start `yt-dlp` or `ffmpeg` again.
- Identical concurrent requests are coalesced into a single download.
- Up to seven concurrent downloads with no waiting queue, separate FIFO queues for lookups and archiving, rate limiting, and one heavy task per user.
- Incoming Telegram updates are stored in SQLite until processed and restored after a restart.
- Health checks, Prometheus metrics, JSON logs, disk monitoring, and the administrative `/stats` and `/status` commands.

Music services such as Spotify are not used as audio sources. The bot reads their public metadata, searches for possible matches on YouTube, and lets the user choose. An approximate match is never presented as the original recording without confirmation.

## Project structure

```text
runtime.go          startup, polling, and graceful shutdown
config.go           typed environment configuration
main.go             Telegram handlers and file delivery
workflows.go        previews, search, ranges, and shared caching
resolver.go         safe oEmbed/Open Graph link resolution
store.go            SQLite languages, cache, counters, and history
scheduler.go        queues, rate limiting, and deduplication
app_state.go        one-time actions and active jobs
bot_ui.go           keyboards, URL validation, and formatting
inline.go           inline state and placeholder handling
inline_handlers.go  inline search, download, and audio replacement
downloader.go       yt-dlp, metadata, ranges, and files
archive.go          ZIP archives
health.go           /healthz, /metrics, and disk monitoring
locales.go          Russian and English copy
deploy.sh           initial deployment for Debian/Ubuntu
Dockerfile          multi-stage Go + yt-dlp + ffmpeg + Deno image
docker-compose.yml  persistent, restricted container configuration
```

`yt-dlp`, `ffmpeg`, and Deno remain external tools. The Telegram flow, scheduling, persistent state, caching, and archive handling are implemented in Go.

## Inline mode and cache channel

1. Create a private Telegram channel.
2. Add the bot as an administrator with permission to post messages.
3. Save the numeric channel ID:

```env
CACHE_CHAT_ID=-1001234567890
```

The legacy `INLINE_CACHE_CHAT_ID` name is also supported.

4. In `@BotFather`, run `/setinline`, select the bot, and enter a placeholder such as `Find music`.
5. Run `/setinlinefeedback` and set it to `100` so the bot receives every selected result.
6. Restart the bot and type `@username song name`.

On its first launch, the bot creates a one-second silent MP3 and uploads it to the cache channel. The resulting `file_id` can be set explicitly with `INLINE_PLACEHOLDER_FILE_ID`.

Completed tracks are stored in SQLite (`$XDG_DATA_HOME/musicbot.db`, or `/app/cache/musicbot.db` in Docker). The old `inline-audio-cache.json` is imported automatically and renamed with a `.migrated` suffix. Inline mode downloads MP3 at 320 kbps; playlists are handled in a private chat.

The cache channel lets the bot obtain a `file_id` before responding to the first user. Without it, the shared cache still stores the `file_id` received after the first regular delivery.

## Concurrency and caching

Up to seven downloads run concurrently by default. They do not have a waiting queue: an eighth simultaneous request receives a "bot is busy" response. Two fast lookup jobs and one ZIP archive job still have separate FIFO queues. New links and searches are limited to 12 requests per minute.

```env
DATABASE_PATH=/app/cache/musicbot.db
CACHE_TTL=4320h
DOWNLOAD_WORKERS=7
DOWNLOAD_QUEUE_SIZE=0
LOOKUP_WORKERS=2
LOOKUP_QUEUE_SIZE=40
ARCHIVE_WORKERS=1
ARCHIVE_QUEUE_SIZE=10
UPDATE_WORKERS=32
UPDATE_QUEUE_SIZE=256
RATE_LIMIT=12
INLINE_RATE_LIMIT=60
RATE_WINDOW=1m
MAX_PLAYLIST_TRACKS=75
DROP_PENDING_UPDATES=false
```

Every `yt-dlp` operation, including downloads, previews, and searches, uses an isolated temporary copy of `cookies.txt`. The source file is mounted read-only in the container, so parallel processes cannot corrupt it.

## Quick deployment

On a new Debian or Ubuntu server:

```bash
chmod +x deploy.sh
./deploy.sh
```

The script installs Docker and Compose, asks for `BOT_TOKEN` and an optional cookies path, creates the required directories, builds the container, and waits for a successful health check.

If the user running the deployment already has a non-empty `~/.ssh/authorized_keys`, the script validates the effective `sshd` configuration and disables password authentication. Without a preinstalled SSH key, this step is safely skipped to avoid locking the user out.

For an automated deployment, create `.env` through a secrets manager before running the script:

```bash
COOKIES_FILE='/tmp/cookies.txt' ./deploy.sh
```

Subsequent runs preserve the token. `cookies.txt` is replaced only when `COOKIES_FILE` is explicitly provided. Before an update, the script backs up SQLite, `.env`, and cookies to `backups/`; if the health check fails, it automatically starts the previous image.

```bash
sudo docker compose logs -f
sudo docker compose restart
sudo docker compose down
```

The Dockerfile pins `yt-dlp` and Deno through build arguments, verifies their published SHA-256 checksums, and runs `--version` as a smoke test. To update them deliberately, change `YTDLP_VERSION` or `DENO_VERSION`, then run:

```bash
sudo docker compose build --no-cache --pull
sudo docker compose up -d
```

## YouTube cookies

On a VPS, YouTube often responds with `Sign in to confirm you're not a bot`. Export cookies from a signed-in account in Netscape format:

```bash
COOKIES_FILE="$HOME/cookies.txt" ./deploy.sh
```

The file is mounted read-only, and `yt-dlp` operates on isolated temporary copies. Its permissions are set to `0600`. If the bot check returns, export a fresh file and run the deployment again.

## Manual launch

Requirements: Go 1.26+, a recent `yt-dlp`, `ffmpeg`, and Deno available in `PATH`.

```bash
cp .env.example .env
# set BOT_TOKEN and run chmod 600 .env
go build -o musicbot .
./musicbot
```

The bot reads `.env` from the working directory; process environment variables take precedence. By default, `cookies.txt` is loaded from the same directory. Set a different path with `YTDLP_COOKIES_FILE`. The complete configuration reference is available in `.env.example`.

## Observability and administration

The HTTP server listens on `127.0.0.1:8080` when running natively and on `0.0.0.0:8080` inside the container:

```text
GET /healthz   SQLite, yt-dlp, disk, and queue state as JSON
GET /metrics   Prometheus metrics
```

Compose does not publish the port externally. For an external Prometheus instance, add an authenticated reverse proxy or a local port binding.

```env
ADMIN_IDS=123456789,987654321
DISK_WARNING_BYTES=536870912
DISK_CHECK_INTERVAL=10m
LOG_FORMAT=json
```

Administrators can use `/stats` and `/status`. A low-disk warning is sent once and becomes eligible again only after disk space recovers and drops below the threshold another time. Repeated YouTube bot-check or cookie errors are counted separately and reported to administrators no more than once per hour.

## Limitations

- The official Telegram Bot API accepts bot uploads up to 50 MB. Telegram's music player supports MP3 and M4A; FLAC and OGG are sent as documents.
- The duration threshold depends on the selected format and rejects tracks that are certain to exceed the size limit before downloading them.
- ZIP archives are split automatically, but every individual file must still fit within Telegram's limit.
- A single request cannot exceed the configurable playlist limit. When a large playlist is delivered as individual files, the bot processes it in batches and cleans the disk after every batch.
- Language, cache, statistics, and 90 days of download history are stored in SQLite. One-time buttons and temporary files are intentionally not restored after a restart.
- A Telegram `file_id` belongs to a specific bot and cannot be transferred when the bot token changes.

## Verification

```bash
go test ./...
go test -race ./...
go vet ./...
docker compose config --quiet
```

CI also builds the binary and Docker image. Tests cover SQLite, cache TTL, queues, rate limiting, deduplication, playlist ranges, ZIP splitting, link resolution, and repeat delivery through Telegram `file_id` values.
