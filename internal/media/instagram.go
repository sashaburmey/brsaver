package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"brsaver/internal/telegram"
)

const (
	instagramMetadataSizeLimit = 8 * 1024 * 1024
	maxInstagramAlbumItems     = 20
)

var instagramShortcodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,64}$`)

type instagramPostInfo struct {
	ID string `json:"id"`
	instagramMediaInfo
	Entries []instagramMediaInfo `json:"entries"`
}

type instagramMediaInfo struct {
	Formats []struct {
		FormatID string `json:"format_id"`
		URL      string `json:"url"`
		Protocol string `json:"protocol"`
		VCodec   string `json:"vcodec"`
		Width    int64  `json:"width"`
		Height   int64  `json:"height"`
	} `json:"formats"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

func isInstagramURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	return hostMatches(strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")), "instagram.com")
}

// downloadInstagramPost handles posts yt-dlp cannot download as a video:
// photos and carousels. yt-dlp still resolves the post metadata, including
// the photo URLs, when it is told to ignore the missing video formats.
func (d *Downloader) downloadInstagramPost(ctx context.Context, rawURL, dir, cookiesFile string) (*DownloadedFile, error) {
	info, err := d.fetchInstagramPostInfo(ctx, rawURL, cookiesFile)
	if err != nil {
		return nil, err
	}
	items, err := instagramPostMedia(info)
	if err != nil {
		return nil, err
	}
	code := info.ID
	if !instagramShortcodePattern.MatchString(code) {
		code = "post"
	}

	if len(items) == 1 && items[0].Video {
		path, _, err := d.downloadThreadsItem(ctx, items[0], dir, "instagram-"+code, d.maxFileSize)
		if err != nil {
			return nil, err
		}
		return &DownloadedFile{Path: path, dir: dir}, nil
	}

	album := make([]telegram.AlbumItem, 0, len(items))
	var totalSize int64
	for index, item := range items {
		// Album items are uploaded as they are, without compression.
		limit := min(int64(maxTelegramPhotoSize), d.maxFileSize-totalSize)
		if item.Video {
			limit = min(d.maxUploadSize, d.maxFileSize-totalSize)
		}
		path, size, err := d.downloadThreadsItem(ctx, item, dir, fmt.Sprintf("instagram-%s-%02d", code, index+1), limit)
		if err != nil {
			return nil, err
		}
		totalSize += size
		album = append(album, telegram.AlbumItem{Path: path, Video: item.Video})
	}
	return &DownloadedFile{Album: album, dir: dir}, nil
}

func (d *Downloader) fetchInstagramPostInfo(ctx context.Context, rawURL, cookiesFile string) (instagramPostInfo, error) {
	args := []string{
		"--ignore-config",
		"--no-cache-dir",
		"--no-warnings",
		"--ignore-no-formats-error",
		"--dump-single-json",
		"--socket-timeout", "30",
	}
	if cookiesFile != "" {
		args = append(args, "--cookies", cookiesFile)
	}
	args = append(args, "--", rawURL)

	cmd := exec.CommandContext(ctx, d.binary, args...)
	var stdout cappedBuffer
	stdout.limit = instagramMetadataSizeLimit
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return instagramPostInfo{}, fmt.Errorf("Instagram metadata failed: %s", strings.ReplaceAll(detail, rawURL, "[URL]"))
	}
	if stdout.overflow {
		return instagramPostInfo{}, errors.New("Instagram metadata is too large")
	}
	return decodeInstagramPostInfo(stdout.buffer.Bytes())
}

func decodeInstagramPostInfo(data []byte) (instagramPostInfo, error) {
	var info instagramPostInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return instagramPostInfo{}, fmt.Errorf("decode Instagram metadata: %w", err)
	}
	return info, nil
}

func instagramPostMedia(info instagramPostInfo) ([]threadsMediaItem, error) {
	entries := info.Entries
	if len(entries) == 0 {
		entries = []instagramMediaInfo{info.instagramMediaInfo}
	}
	if len(entries) > maxInstagramAlbumItems {
		return nil, errors.New("Instagram carousel exceeds the item limit")
	}
	items := make([]threadsMediaItem, 0, len(entries))
	for index, entry := range entries {
		item, ok := instagramSingleMedia(entry)
		if !ok {
			if len(entries) == 1 {
				return nil, ErrNoMedia
			}
			return nil, fmt.Errorf("Instagram carousel item %d has no trusted media URL", index+1)
		}
		items = append(items, item)
	}
	return items, nil
}

func instagramSingleMedia(entry instagramMediaInfo) (threadsMediaItem, bool) {
	// Only progressive MP4 files can be fetched directly; DASH formats carry
	// separate audio and video streams.
	formats := append(entry.Formats[:0:0], entry.Formats...)
	sort.SliceStable(formats, func(left, right int) bool {
		return formats[left].Width*formats[left].Height > formats[right].Width*formats[right].Height
	})
	videos := make([]string, 0, len(formats))
	for _, format := range formats {
		if format.Protocol == "https" && format.VCodec != "none" && !strings.HasPrefix(format.FormatID, "dash-") {
			videos = append(videos, format.URL)
		}
	}
	if videos = validThreadsMediaURLs(videos); len(videos) > 0 {
		return threadsMediaItem{URLs: videos, Video: true}, true
	}

	// yt-dlp lists thumbnails from the worst to the best quality, and the
	// best one is the original photo.
	photos := make([]string, 0, len(entry.Thumbnails))
	for index := len(entry.Thumbnails) - 1; index >= 0; index-- {
		photos = append(photos, entry.Thumbnails[index].URL)
	}
	photos = validThreadsMediaURLs(photos)
	return threadsMediaItem{URLs: photos}, len(photos) > 0
}

// cappedBuffer keeps up to limit bytes and remembers whether more arrived.
type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(value) > remaining {
		b.overflow = true
		_, _ = b.buffer.Write(value[:max(remaining, 0)])
		return len(value), nil
	}
	_, _ = b.buffer.Write(value)
	return len(value), nil
}
