# brsaver

A Telegram bot written in Go that detects TikTok, YouTube Shorts, Instagram, Facebook, and Spotify track links in group messages, downloads the media, replies with it as native Telegram video, photo, or audio, and immediately removes all temporary files.

## Features

- works in groups and supergroups through Telegram long polling;
- detects plain URLs and hidden `text_link` URLs;
- supports TikTok, YouTube Shorts, Instagram Reel/Post/TV, Facebook Reel/Watch/Video, and individual Spotify tracks;
- sends TikTok photo posts as native Telegram photos or albums;
- resolves Spotify track metadata, selects a close YouTube audio match by title, artist, and duration, and sends it as an MP3 with Telegram audio metadata;
- downloads multiple links concurrently through a bounded worker queue;
- limits processing time, the number of links, and file size (49 MiB by default);
- automatically compresses oversized videos to H.264/AAC using two-pass `ffmpeg` encoding;
- preserves forum group topics and replies to the original message;
- creates an isolated directory for every download and removes it after either success or failure;
- never passes URLs through a shell: `yt-dlp` is executed directly with an argument array;
- supports browser impersonation through `curl_cffi` for TikTok pages protected by TLS/JS challenges;
- falls back to TikTok's official embedded-player API when the regular TikTok extractor cannot read a public video.

## Quick start with Docker Compose

1. Create a bot through [@BotFather](https://t.me/BotFather) and copy its token.
2. In BotFather, run `/setprivacy`, select the bot, and choose **Disable**. Otherwise, Telegram will not send ordinary group messages to the bot.
3. Add the bot to the required group.
4. Create a local `.env` file:

   ```bash
   cp .env.example .env
   ```

   Set the actual `TELEGRAM_BOT_TOKEN`. To restrict the bot to specific groups, add their IDs to `ALLOWED_CHAT_IDS`.

5. Start the service:

   ```bash
   docker compose up --build -d
   docker compose logs -f brsaver
   ```

The container has a read-only root filesystem. Downloads are stored only in the temporary `/tmp` filesystem, which disappears with the container. The application also removes each job directory immediately after the media has been sent.

Events and errors are written to Docker logs. The `local` logging driver rotates the logs and retains up to five 10 MB files. To view them, run:

```bash
docker compose logs -f brsaver
```

Logs include the event type, provider, chat ID, and message ID, but never the bot token or full media URL.

## Local development

Local development requires Go 1.24+, a current nightly build of [`yt-dlp`](https://github.com/yt-dlp/yt-dlp) with `curl_cffi`, and `ffmpeg` in `PATH`. The nightly build is intentional because TikTok, Instagram, and other sites often change their extractor APIs before the next stable release.

```bash
cp .env.example .env
set -a
. ./.env
set +a
export TIKTOK_PHOTO_HELPER_PATH=./scripts/brsaver_tiktok_photos.py
go run ./cmd/brsaver
```

## Configuration

| Variable | Default | Description |
|---|---:|---|
| `TELEGRAM_BOT_TOKEN` | — | Required token issued by BotFather |
| `ALLOWED_CHAT_IDS` | all groups | Comma-separated list of allowed chat IDs |
| `WORKERS` | `2` | Number of concurrent download workers |
| `QUEUE_SIZE` | `32` | Maximum number of queued jobs |
| `MAX_LINKS_PER_MESSAGE` | `3` | Maximum number of URLs processed from one message |
| `MAX_FILE_SIZE_MB` | `49` | Final video/audio upload limit; the standard Bot API accepts files up to 50 MB |
| `MAX_DOWNLOAD_SIZE_MB` | `250` | Maximum original file size before compression |
| `DOWNLOAD_TIMEOUT` | `4m` | Timeout for one download operation |
| `DOWNLOAD_ATTEMPTS` | `3` | Number of attempts after a transient source error |
| `DOWNLOAD_RETRY_DELAY` | `2s` | Delay between download attempts |
| `COMPRESSION_TIMEOUT` | `12m` | Timeout for two-pass transcoding |
| `UPLOAD_TIMEOUT` | `3m` | Timeout for uploading media to Telegram |
| `COMPRESS_OVERSIZED` | `true` | Automatically compress videos that exceed the upload limit |
| `NOTIFY_ERRORS` | `true` | Notify the group when a download fails |
| `DOWNLOAD_DIR` | `/tmp/brsaver` | Root directory for temporary jobs |
| `YT_DLP_PATH` | `yt-dlp` | Path to the executable |
| `YT_DLP_FORMAT` | MP4/H.264 preferred | Custom `yt-dlp` format selector |
| `TIKTOK_PHOTO_HELPER_PATH` | `brsaver-tiktok-photos` | Path to the TikTok photo post helper |
| `FFMPEG_PATH` / `FFPROBE_PATH` | `ffmpeg` / `ffprobe` | Paths to the transcoding tools |
| `YT_DLP_COOKIES_FILE` | empty | Netscape cookies file for content you are authorized to access |
| `YT_DLP_YOUTUBE_COOKIES_FILE` | empty | Separate YouTube/Google cookies file used for YouTube Shorts and Spotify audio matching |
| `TELEGRAM_API_BASE_URL` | `https://api.telegram.org` | Bot API URL, including a local Bot API server if used |

Never commit the cookies file to Git. In Docker, mount it read-only and configure its path through an environment variable:

```yaml
services:
  brsaver:
    volumes:
      - ./cookies.txt:/run/secrets/cookies.txt:ro
      - ./youtube-cookies.txt:/run/secrets/youtube-cookies.txt:ro
    environment:
      YT_DLP_COOKIES_FILE: /run/secrets/cookies.txt
      YT_DLP_YOUTUBE_COOKIES_FILE: /run/secrets/youtube-cookies.txt
```

## Verification

```bash
go test ./...
go vet ./...
```

Only download and republish content when you have the appropriate rights. Private or platform-protected media may remain unavailable even when cookies are provided.

Spotify support currently accepts individual track links only; album and playlist links are intentionally ignored. Spotify provides the track metadata, while the audio is obtained from a closely matched public YouTube result. The bot rejects a result when its title, artist, or duration is not sufficiently close.
