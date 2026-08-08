FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/brsaver ./cmd/brsaver

FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    DOWNLOAD_DIR=/tmp/brsaver

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg && \
    rm -rf /var/lib/apt/lists/* && \
    pip install --no-cache-dir --upgrade --pre "yt-dlp[default,curl-cffi]" && \
    groupadd --system --gid 10001 brsaver && \
    useradd --system --uid 10001 --gid brsaver --home-dir /home/brsaver --create-home --shell /usr/sbin/nologin brsaver && \
    mkdir -p /tmp/brsaver && \
    chown brsaver:brsaver /tmp/brsaver

COPY --from=builder /out/brsaver /usr/local/bin/brsaver
COPY --chmod=0755 scripts/brsaver_tiktok_photos.py /usr/local/bin/brsaver-tiktok-photos

USER brsaver
ENTRYPOINT ["/usr/local/bin/brsaver"]
