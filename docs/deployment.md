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

GitLab CI publishes `$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA` for the default branch and tags. Authenticate the server with `docker login registry.gitlab.com` before its first Registry deployment.

## YouTube cookies

YouTube may respond with `Sign in to confirm you're not a bot` on a VPS. Export cookies from a signed-in account in Netscape format and deploy them with:

```bash
sudo install -o 10001 -g 10001 -m 0600 "$HOME/cookies.txt" ./cookies.txt
./deploy.sh
```

The source file is stored with `0600` permissions and mounted read-only. Every `yt-dlp` operation uses an isolated temporary copy, so concurrent processes cannot corrupt the original file.

If the bot check returns, replace the mounted file explicitly and run the deployment again. `deploy.sh` itself never overwrites it.

## Operations

```bash
sudo docker compose ps
sudo docker compose logs -f
sudo docker compose restart
sudo docker compose down
```

The HTTP server listens on `0.0.0.0:8080` inside the container, but Compose does not publish it externally:

```text
GET /healthz   SQLite, yt-dlp, disk, and queue state as JSON
GET /metrics   Prometheus metrics
```

Use an authenticated reverse proxy or a local-only port binding before exposing either endpoint.

### Prometheus scrape

`/metrics` exports persistent counters (`musicbot_counter_total{name}`), per-stage latency quantiles and success ratios for the last hour (`musicbot_stage_seconds{stage,source,quantile}`, `musicbot_stage_ok_ratio{stage,source}`), the Telegram cache hit ratio (`musicbot_cache_hit_ratio`), the Octave remote-URL fast-path share (`musicbot_octave_fast_path_ratio`), and the Octave circuit breaker state (`musicbot_octave_circuit_state`: 0 closed, 1 open, 2 half-open). SQLite aggregates are cached for 30 seconds, so a 15-30 second scrape interval adds no database load:

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
