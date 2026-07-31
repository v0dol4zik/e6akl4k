FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/musicbot .

FROM debian:bookworm-slim

ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg passwd unzip \
    && ARCH="${TARGETARCH:-$(dpkg --print-architecture)}" \
    && case "$ARCH" in \
         amd64) YTDLP_ASSET=yt-dlp_linux; DENO_ASSET=deno-x86_64-unknown-linux-gnu.zip ;; \
         arm64) YTDLP_ASSET=yt-dlp_linux_aarch64; DENO_ASSET=deno-aarch64-unknown-linux-gnu.zip ;; \
         *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/latest/download/${YTDLP_ASSET}" -o /usr/local/bin/yt-dlp \
    && curl -fsSL "https://github.com/denoland/deno/releases/latest/download/${DENO_ASSET}" -o /tmp/deno.zip \
    && unzip -q /tmp/deno.zip -d /usr/local/bin \
    && chmod 0755 /usr/local/bin/yt-dlp /usr/local/bin/deno \
    && yt-dlp --version \
    && deno --version \
    && rm -f /tmp/deno.zip \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --shell /usr/sbin/nologin musicbot \
    && mkdir -p /app/downloads /app/cache \
    && chown -R musicbot:musicbot /app

WORKDIR /app
ENV HOME=/home/musicbot \
    XDG_CACHE_HOME=/app/cache
COPY --from=build /out/musicbot /usr/local/bin/musicbot
USER 10001:10001

CMD ["musicbot"]
