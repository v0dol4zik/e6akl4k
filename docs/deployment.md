# Deployment

## Automated deployment

The supported deployment target is a Debian or Ubuntu server with root or `sudo` access.

```bash
chmod +x deploy.sh
./deploy.sh
```

The script:

- installs Docker Engine and Docker Compose when needed;
- asks for `BOT_TOKEN` and an optional cookies file;
- creates the persistent `downloads/` and `cache/` directories;
- validates the Compose configuration;
- backs up SQLite, `.env`, and cookies before an update;
- builds and starts the container;
- waits for the health check and restores the previous image if startup fails.

If the deployment user already has a non-empty `~/.ssh/authorized_keys`, the script validates the effective `sshd` configuration and disables password authentication. Without a preinstalled key, SSH hardening is skipped to avoid locking the user out.

For non-interactive deployment, create `.env` through a secrets manager and optionally provide a cookies path:

```bash
COOKIES_FILE='/tmp/cookies.txt' ./deploy.sh
```

Subsequent runs preserve the existing token. `cookies.txt` is replaced only when `COOKIES_FILE` is explicitly provided.

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

The script creates a timestamped backup before rebuilding. If the new container fails its health check, the previous Docker image is started automatically.

## YouTube cookies

YouTube may respond with `Sign in to confirm you're not a bot` on a VPS. Export cookies from a signed-in account in Netscape format and deploy them with:

```bash
COOKIES_FILE="$HOME/cookies.txt" ./deploy.sh
```

The source file is stored with `0600` permissions and mounted read-only. Every `yt-dlp` operation uses an isolated temporary copy, so concurrent processes cannot corrupt the original file.

If the bot check returns, export fresh cookies and run the deployment again.

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
