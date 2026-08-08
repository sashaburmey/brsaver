#!/usr/bin/env python3

import json
import os
import re
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

from curl_cffi import requests
from yt_dlp import YoutubeDL
from yt_dlp.extractor.tiktok import TikTokIE
from yt_dlp.utils import DownloadError


PHOTO_PATH = re.compile(r"^/@[^/]+/photo/[0-9]+$")
TRUSTED_CDN_SUFFIXES = (
    ".tiktokcdn.com",
    ".tiktokcdn-eu.com",
    ".tiktokcdn-us.com",
)
CONTENT_TYPES = {
    "image/jpeg": ".jpg",
    "image/jpg": ".jpg",
    "image/png": ".png",
    "image/webp": ".webp",
}


class HelperError(Exception):
    pass


class QuietLogger:
    def debug(self, message):
        pass

    def info(self, message):
        pass

    def warning(self, message):
        pass

    def error(self, message):
        pass


def normalize_photo_url(raw_url):
    parsed = urlsplit(raw_url)
    if parsed.scheme != "https" or parsed.hostname != "www.tiktok.com":
        raise HelperError("unsupported TikTok photo URL")
    if not PHOTO_PATH.fullmatch(parsed.path):
        raise HelperError("invalid TikTok photo path")
    video_path = parsed.path.replace("/photo/", "/video/", 1)
    return urlunsplit((parsed.scheme, parsed.netloc, video_path, parsed.query, ""))


def trusted_image_url(raw_url):
    try:
        parsed = urlsplit(raw_url)
    except ValueError:
        return False
    host = (parsed.hostname or "").lower()
    return parsed.scheme == "https" and any(host.endswith(suffix) for suffix in TRUSTED_CDN_SUFFIXES)


def extract_image_urls(video_url, cookies_file):
    image_post = None
    original_parser = TikTokIE._parse_aweme_video_web

    def capture_image_post(extractor, aweme_detail, webpage_url, video_id, extract_flat=False):
        nonlocal image_post
        image_post = aweme_detail.get("imagePost")
        return original_parser(extractor, aweme_detail, webpage_url, video_id, extract_flat)

    TikTokIE._parse_aweme_video_web = capture_image_post
    options = {
        "cachedir": False,
        "logger": QuietLogger(),
        "noplaylist": True,
        "quiet": True,
        "socket_timeout": 30,
    }
    if cookies_file:
        options["cookiefile"] = cookies_file
    try:
        with YoutubeDL(options) as downloader:
            for attempt in range(3):
                image_post = None
                try:
                    downloader.extract_info(video_url, download=False)
                    break
                except DownloadError:
                    if attempt == 2:
                        raise
                    time.sleep(2)
    finally:
        TikTokIE._parse_aweme_video_web = original_parser

    images = image_post.get("images") if isinstance(image_post, dict) else None
    if not isinstance(images, list) or not images:
        raise HelperError("TikTok post does not contain downloadable photos")

    urls = []
    seen = set()
    for image in images:
        image_url = image.get("imageURL") if isinstance(image, dict) else None
        candidates = image_url.get("urlList") if isinstance(image_url, dict) else None
        if not isinstance(candidates, list):
            continue
        selected = next((value for value in candidates if isinstance(value, str) and trusted_image_url(value)), None)
        if selected and selected not in seen:
            seen.add(selected)
            urls.append(selected)
    if not urls:
        raise HelperError("TikTok post did not provide trusted photo URLs")
    return urls


def download_images(urls, video_url, output_dir, max_images, max_file_size, max_total_size):
    if len(urls) > max_images:
        raise HelperError("TikTok photo count exceeds the configured limit")

    session = requests.Session(impersonate="chrome")
    files = []
    total_size = 0
    for index, image_url in enumerate(urls, start=1):
        response = session.get(
            image_url,
            headers={"Referer": video_url},
            stream=True,
            timeout=30,
        )
        try:
            if response.status_code < 200 or response.status_code >= 300:
                raise HelperError("TikTok CDN rejected a photo download")
            if not trusted_image_url(response.url):
                raise HelperError("TikTok CDN redirected a photo to an untrusted host")

            content_type = response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
            extension = CONTENT_TYPES.get(content_type)
            if not extension:
                raise HelperError("TikTok returned an unsupported photo format")
            content_length = response.headers.get("content-length")
            if content_length and int(content_length) > max_file_size:
                raise HelperError("TikTok photo exceeds the Telegram size limit")

            filename = f"photo-{index:02d}{extension}"
            path = output_dir / filename
            file_size = 0
            try:
                with path.open("xb") as target:
                    os.chmod(path, 0o600)
                    for chunk in response.iter_content(chunk_size=64 * 1024):
                        if not chunk:
                            continue
                        file_size += len(chunk)
                        total_size += len(chunk)
                        if file_size > max_file_size:
                            raise HelperError("TikTok photo exceeds the Telegram size limit")
                        if total_size > max_total_size:
                            raise HelperError("TikTok photo post exceeds the total download limit")
                        target.write(chunk)
            except Exception:
                path.unlink(missing_ok=True)
                raise
            if file_size == 0:
                path.unlink(missing_ok=True)
                raise HelperError("TikTok returned an empty photo")
            files.append(filename)
        finally:
            response.close()
    return files


def positive_int(raw_value, name):
    try:
        value = int(raw_value)
    except ValueError as error:
        raise HelperError(f"{name} must be an integer") from error
    if value <= 0:
        raise HelperError(f"{name} must be positive")
    return value


def main():
    if len(sys.argv) != 7:
        raise HelperError("invalid helper arguments")
    photo_url, raw_output_dir, raw_max_images, raw_max_file_size, raw_max_total_size, cookies_file = sys.argv[1:]
    output_dir = Path(raw_output_dir).resolve(strict=True)
    if not output_dir.is_dir():
        raise HelperError("output path is not a directory")

    max_images = positive_int(raw_max_images, "maximum image count")
    max_file_size = positive_int(raw_max_file_size, "maximum image size")
    max_total_size = positive_int(raw_max_total_size, "maximum total size")
    video_url = normalize_photo_url(photo_url)
    urls = extract_image_urls(video_url, cookies_file)
    files = download_images(urls, video_url, output_dir, max_images, max_file_size, max_total_size)
    print(json.dumps({"files": files}, separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except HelperError as error:
        print(f"TikTok photo error: {error}", file=sys.stderr)
        sys.exit(1)
    except Exception as error:
        print(f"TikTok photo extraction failed ({type(error).__name__})", file=sys.stderr)
        sys.exit(1)
