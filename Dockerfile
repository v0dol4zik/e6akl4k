FROM golang:1.26.5-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go shazam_recognize.py ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/musicbot .

FROM debian:bookworm-slim

ARG TARGETARCH
ARG YTDLP_VERSION=2026.08.19
ARG DENO_VERSION=2.9.5
# The yt-dlp plugin for the bgutil PO token server; its version must match the bgutil-pot image
# in docker-compose.yml. It sits outside yt-dlp's default plugin folders and loads only with
# YTDLP_POT_PROVIDER_URL set.
ARG BGUTIL_PLUGIN_VERSION=2.0.0
ARG BGUTIL_PLUGIN_SHA256=bce874dfa25896c2798e0f4f8147b7b22e785479eb1e459ab232bf2506c95016
COPY requirements-shazam.txt /tmp/requirements-shazam.txt
# apt keeps one mirror address per run, and a stalled CDN node can hang it past its own timeouts,
# so every networked apt-get call gets a hard limit and a fresh run on retry; release downloads
# get the same connect, stall and retry limits as the package fetches.
RUN fetch() { \
        curl -fsSL --connect-timeout 20 --speed-limit 1024 --speed-time 30 \
            --retry 5 --retry-delay 2 --retry-all-errors "$@"; \
    } \
    && apt_get() { \
        for attempt in 1 2 3 4 5; do \
            timeout -k 10 180 apt-get -o Acquire::Retries=5 \
                -o Acquire::http::Timeout=30 \
                -o Acquire::https::Timeout=30 "$@" && return 0; \
            echo "apt-get $1: attempt $attempt failed" >&2; \
            sleep 5; \
        done; \
        return 1; \
    } \
    && apt_get update \
    && apt_get install -y --no-install-recommends ca-certificates curl \
    && apt-get --print-uris --yes --no-install-recommends install ffmpeg passwd python3 python3-mutagen python3-venv unzip \
       | sed -n "s/^'\\([^']*\\)' \\([^ ]*\\).*/\\1 \\2/p" \
       | xargs -r -n 2 -P 8 sh -c 'curl -fsSL --connect-timeout 20 --speed-limit 1024 --speed-time 30 --retry 5 --retry-delay 2 --retry-all-errors "$1" -o "/var/cache/apt/archives/$2"' sh \
    && apt_get install -y --no-install-recommends ffmpeg passwd python3 python3-mutagen python3-venv unzip \
    && python3 -m venv /opt/shazam \
    && /opt/shazam/bin/python -m pip install --no-cache-dir --timeout 30 --retries 5 -r /tmp/requirements-shazam.txt \
    && /opt/shazam/bin/python -c "from shazamio import Shazam, HTTPClient" \
    && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
    && case "$ARCH" in \
         amd64) DENO_ASSET=deno-x86_64-unknown-linux-gnu.zip ;; \
         arm64) DENO_ASSET=deno-aarch64-unknown-linux-gnu.zip ;; \
         *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;; \
       esac \
    && fetch "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp" -o /tmp/yt-dlp \
    && fetch "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/SHA2-256SUMS" -o /tmp/yt-dlp.sha256 \
    && (cd /tmp && grep ' yt-dlp$' yt-dlp.sha256 | sha256sum -c -) \
    && install -m 0755 /tmp/yt-dlp /usr/local/bin/yt-dlp \
    && fetch "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/${DENO_ASSET}" -o "/tmp/${DENO_ASSET}" \
    && fetch "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/${DENO_ASSET}.sha256sum" -o "/tmp/${DENO_ASSET}.sha256sum" \
    && (cd /tmp && sha256sum -c "${DENO_ASSET}.sha256sum") \
    && mv "/tmp/${DENO_ASSET}" /tmp/deno.zip \
    && unzip -q /tmp/deno.zip -d /usr/local/bin \
    && chmod 0755 /usr/local/bin/yt-dlp /usr/local/bin/deno \
    && fetch "https://github.com/Brainicism/bgutil-ytdlp-pot-provider/releases/download/${BGUTIL_PLUGIN_VERSION}/bgutil-ytdlp-pot-provider.zip" -o /tmp/bgutil-ytdlp-pot-provider.zip \
    && echo "${BGUTIL_PLUGIN_SHA256}  /tmp/bgutil-ytdlp-pot-provider.zip" | sha256sum -c - \
    && unzip -tq /tmp/bgutil-ytdlp-pot-provider.zip \
    && install -D -m 0644 /tmp/bgutil-ytdlp-pot-provider.zip /opt/yt-dlp-plugins/bgutil-ytdlp-pot-provider.zip \
    && yt-dlp --version \
    && python3 -c "import mutagen" \
    && deno --version \
    && rm -f /tmp/deno.zip /tmp/yt-dlp /tmp/yt-dlp.sha256 "/tmp/${DENO_ASSET}.sha256sum" /tmp/bgutil-ytdlp-pot-provider.zip /tmp/requirements-shazam.txt \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb \
    && useradd --uid 10001 --create-home --shell /usr/sbin/nologin musicbot \
    && mkdir -p /app/downloads /app/cache \
    && chown -R musicbot:musicbot /app

WORKDIR /app
ENV HOME=/home/musicbot \
    XDG_CACHE_HOME=/app/cache \
    XDG_DATA_HOME=/app/cache \
    HTTP_ADDR=0.0.0.0:8080 \
    TELEGRAM_FILE_DIR=/var/lib/telegram-bot-api \
    SHAZAM_PYTHON=/opt/shazam/bin/python \
    LOG_FORMAT=json
COPY --from=build /out/musicbot /usr/local/bin/musicbot
USER 10001:10001

HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
    CMD ["curl", "--fail", "--silent", "http://127.0.0.1:8080/healthz"]

CMD ["musicbot"]
