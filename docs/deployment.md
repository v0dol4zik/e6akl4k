# Deployment

## One-time bootstrap

The supported deployment target is a Debian or Ubuntu server with root or `sudo` access.

```bash
chmod +x bootstrap.sh deploy.sh rollback.sh
./bootstrap.sh
```

The bootstrap script:

- installs Docker Engine and Docker Compose when needed;
- asks for `BOT_TOKEN` and an optional cookies file;
- creates the persistent `downloads/` and `cache/` directories;
- creates `.env`, cookies, and persistent application directories;
- validates an installed SSH public key before disabling password authentication.

If the deployment user already has a non-empty `~/.ssh/authorized_keys`, the script validates the effective `sshd` configuration and disables password authentication. Without a preinstalled key, SSH hardening is skipped to avoid locking the user out.

For a non-interactive bootstrap, provide the token and optional cookies path:

```bash
BOT_TOKEN='123456:...' COOKIES_FILE='/tmp/cookies.txt' ./bootstrap.sh
```

Run the bootstrap only for initial host provisioning. Regular deployments never modify `.env` or cookies.

## Updating an installation

If the server contains a Git checkout:

```bash
git pull --ff-only origin main
./deploy.sh
```

If files are uploaded through SFTP, upload the updated tracked files without overwriting `.env`, `cookies.txt`, `cache/`, `downloads/`, or `backups/`, then run:

```bash
cd /path/to/e6akl4k
./deploy.sh
```

Without an argument, `deploy.sh` builds a uniquely tagged local image. To deploy a CI image, pass its immutable Git SHA tag:

```bash
./deploy.sh registry.gitlab.com/group/project:0123456789abcdef
```

The script validates Compose, free space, and SQLite, creates a timestamped database backup, deploys the exact image, and waits for a healthy container. Failure restores the previous image automatically. A manual rollback is available through `./rollback.sh`.

### Switching to a local Telegram Bot API server

Images older than `TELEGRAM_API_URL` support refuse to start with `MAX_FILE_SIZE` above 50 MiB and would talk to the cloud Bot API, which refuses the bot for 10 minutes after the switch. Switch in two deployments, so that an automatic rollback never lands on such an image:

1. Deploy the new code with the unchanged `.env` and check that the bot is healthy.
2. Create `telegram-bot-api.env` (see [Files over 50 MB](configuration.md#files-over-50-mb)), back up `.env`, add `telegram-bot-api` to `COMPOSE_PROFILES`, `TELEGRAM_API_URL=http://telegram-bot-api:8081`, and the larger `MAX_FILE_SIZE`, then run `./deploy.sh "$(cat .deploy/current-image)"`. The bot log should show the logout from the cloud and `Telegram Bot API: локальный сервер http://telegram-bot-api:8081`.

To move back, restore the previous `.env`, redeploy the current image 10 minutes after the switch at the earliest, and stop the server with `docker compose stop telegram-bot-api`. Roll back to an image without local server support only after that.

GitLab CI publishes `$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA` for the default branch and tags. Authenticate the server with `docker login registry.gitlab.com` before its first Registry deployment.

## YouTube cookies

YouTube may respond with `Sign in to confirm you're not a bot` on a VPS. Export cookies from a signed-in account in Netscape format and deploy them with:

```bash
sudo install -o 10001 -g 10001 -m 0600 "$HOME/cookies.txt" ./cookies.txt
./deploy.sh
```

The source file is stored with `0600` permissions and mounted read-only. Every `yt-dlp` operation uses an isolated temporary copy, so concurrent processes cannot corrupt the original file.

If the bot check returns, replace the mounted file explicitly and run the deployment again. `deploy.sh` itself never overwrites it.

### `HTTP Error 403: Forbidden` with valid cookies

Without a PO token provider, YouTube binds a signed-in cookie session to SABR-only streaming or to PO-token-bound media URLs. The metadata probe still succeeds, but every media request returns `403` whichever client `yt-dlp` picks, while the same download without cookies works. So YouTube downloads start without cookies, and only the tracks that failed are downloaded again, so a playlist never starts over. A server IP that YouTube has flagged also gets `HTTP Error 429` and "Sign in to confirm you're not a bot" on the web clients without cookies, and which clients still work changes from hour to hour. On 2026-09-26 (`yt-dlp` 2026.08.19), while the IP was flagged, the anonymous `tv_simply` client and the signed-in `mweb` client downloaded fully, while `android_vr` and the default signed-in `tv_downgraded` got `403` on the media URL. A few hours later `tv_simply` offered no formats without a GVS PO token, and the anonymous default clients worked again. So the download without cookies alternates between `YTDLP_YOUTUBE_CLIENTS` (`tv_simply`) and the `yt-dlp` default clients, and starts with the set that got the last download through; the choice resets to `YTDLP_YOUTUBE_CLIENTS` on restart. A `403`, a bot check, no audio format, or "The page needs to be reloaded" from the first set gets one more attempt without cookies with the other set, logged as `youtube_forbidden_retry` with the `clients` it used. After that, the cookies and `YTDLP_YOUTUBE_COOKIE_CLIENTS` (`mweb`) are used only when YouTube asks to sign in (a bot check, an age gate, or a members-only or private video), logged as `youtube_signin_retry`. Probes and searches still send the cookies first; one that fails with `403` is retried once without cookies, logged as `cookie_forbidden_retry` and counted in `youtube_cookie_retries`.

YouTube also sends an occasional `403` to cookies that still work, so a suspicion alone never alerts. When three retries happen within ten minutes, or a download fails with a bot check or rotated cookies, the bot first opens the account's Watch Later playlist (`:ytwatchlater`) with an isolated copy of the cookies; YouTube serves it only to a signed-in session. Administrators get an alert with a check button only when this login check fails. A working login is trusted for 30 minutes, a network error or timeout is logged without an alert, and results are counted in `youtube_cookie_login_ok`, `youtube_cookie_login_failed`, and `youtube_cookie_login_unknown`. Alerts are sent at most once every six hours.

The status message runs the same login check on a schedule, every `COOKIE_CHECK_INTERVAL` (three hours by default), and shows its result and age together with the age of the cookies file. The first check after a start runs at once when the last one is older than the interval.

To confirm the diagnosis manually, run the same download inside the container with and without `--cookies`:

```bash
sudo docker exec e6akl4k-music_bot-1 sh -c 'cp /app/cookies.txt /tmp/c.txt && yt-dlp --ignore-config --cookies /tmp/c.txt -f bestaudio -o /tmp/t.%(ext)s <url>; rm -f /tmp/c.txt /tmp/t.*'
sudo docker exec e6akl4k-music_bot-1 sh -c 'yt-dlp --ignore-config -f bestaudio -o /tmp/t.%(ext)s <url>; rm -f /tmp/t.*'
```

If only the first command fails with `403` while the Watch Later check passes, the cookies are fine and the bot already works around it; fresh cookies help only when the login check fails.

When YouTube changes which clients work, compare them with the same command and `--extractor-args youtube:player_client=<client>` (with and without `--cookies`), then set the clients that download fully in `.env` without rebuilding the image. `--test` is not enough: it fetches only the first 10 KiB, which often succeeds where the full download gets `403`. The longer-term fix is a PO token provider: the optional `bgutil-pot` service runs third-party code next to the bot, so it is off by default; see [YouTube PO tokens](configuration.md#youtube-po-tokens). With `YTDLP_POT_PROVIDER_URL` set, add `--plugin-dirs /opt/yt-dlp-plugins --extractor-args youtubepot-bgutilhttp:base_url=http://bgutil-pot:4416 -v` to the commands above; the verbose log then lists `bgutil:http` among the PO token providers.

## Operations

```bash
sudo docker compose ps
sudo docker compose logs -f
sudo docker compose restart
sudo docker compose down
```

The HTTP server listens on `0.0.0.0:8080` inside the container, but Compose does not publish it externally:

```text
GET /healthz   SQLite, yt-dlp, cookies (`yt_dlp_cookies`: ok/suspect), disk, and queue state as JSON
GET /metrics   Prometheus metrics
```

Use an authenticated reverse proxy or a local-only port binding before exposing either endpoint.

### Status message

With `STATUS_MESSAGE=true` (the default) the bot keeps a pinned status message in the error chat (`ERROR_CHAT_ID`, or the cache channel): Telegram API, SQLite, disk, cookies, the Yandex relay, the `yt-dlp` version, error counts, and the queue. Make the bot an administrator of that chat with the rights to post, edit, and pin messages. If it cannot pin, the message is still posted and edited, and administrators get one private message about the missing right. The message ID is stored in SQLite, so deployments edit the same message; after it is deleted, the next edit (on the next change, or within ten minutes) posts and pins a new one.

### Prometheus scrape

`/metrics` exports persistent counters (`musicbot_counter_total{name}`), per-stage latency quantiles and success ratios for the last hour (`musicbot_stage_seconds{stage,source,quantile}`, `musicbot_stage_ok_ratio{stage,source}`), and the Telegram cache hit ratio (`musicbot_cache_hit_ratio`). SQLite aggregates are cached for 30 seconds, so a 15-30 second scrape interval adds no database load:

```yaml
scrape_configs:
  - job_name: musicbot
    scrape_interval: 30s
    metrics_path: /metrics
    static_configs:
      - targets: ["127.0.0.1:8080"]
```

## Updating external tools

The Dockerfile pins `yt-dlp` and Deno through build arguments and verifies their published SHA-256 checksums. To update them deliberately, change `YTDLP_VERSION` or `DENO_VERSION`, then run:

```bash
sudo docker compose build --no-cache --pull
sudo docker compose up -d
```

## Manual launch

Requirements: Go 1.26+, a recent `yt-dlp`, `ffmpeg`, and Deno available in `PATH`.

```bash
cp .env.example .env
# Set BOT_TOKEN, then protect the file.
chmod 600 .env
go build -o musicbot .
./musicbot
```

The bot reads `.env` from the working directory; process environment variables take precedence. By default, `cookies.txt` is loaded from the same directory. Set a different path with `YTDLP_COOKIES_FILE`.
