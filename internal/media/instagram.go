package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"brsaver/internal/telegram"
)

const maxInstagramAlbumItems = 20

// instagramEntryTemplate makes yt-dlp print every post item before its
// download. Photos have no video formats, so yt-dlp fails on them after this
// line, but the photo URLs are already known by then.
const instagramEntryTemplate = "video:%(.{id,format_id,thumbnails})j"

var instagramIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,64}$`)

type instagramEntry struct {
	ID         string `json:"id"`
	FormatID   string `json:"format_id"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
	path string
}

func isInstagramURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	return hostMatches(strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")), "instagram.com")
}

// parseInstagramOutput pairs every printed post item with the video file
// yt-dlp printed right after it, if any.
func parseInstagramOutput(output, dir string) []instagramEntry {
	var entries []instagramEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			var entry instagramEntry
			if json.Unmarshal([]byte(line), &entry) == nil {
				entries = append(entries, entry)
			}
			continue
		}
		if line == "" || len(entries) == 0 || entries[len(entries)-1].path != "" {
			continue
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(dir, line)
		}
		if info, err := os.Lstat(line); err == nil && info.Mode().IsRegular() && isWithin(line, dir) {
			entries[len(entries)-1].path = line
		}
	}
	return entries
}

// instagramMedia turns the items of one post into a single video or an album.
// Videos were already downloaded by yt-dlp; photos are fetched from the CDN.
func (d *Downloader) instagramMedia(ctx context.Context, entries []instagramEntry, dir string) (*DownloadedFile, error) {
	if len(entries) > maxInstagramAlbumItems {
		return nil, errors.New("Instagram carousel exceeds the item limit")
	}
	// A single video keeps the regular path, so it can still be compressed.
	if len(entries) == 1 && entries[0].path != "" {
		if _, err := checkDownloadedSize(entries[0].path, d.maxFileSize); err != nil {
			return nil, err
		}
		return &DownloadedFile{Path: entries[0].path, dir: dir}, nil
	}

	album := make([]telegram.AlbumItem, 0, len(entries))
	var totalSize int64
	for index, entry := range entries {
		if entry.path != "" {
			// Album items are uploaded as they are, without compression.
			size, err := checkDownloadedSize(entry.path, min(d.maxUploadSize, d.maxFileSize-totalSize))
			if err != nil {
				return nil, err
			}
			totalSize += size
			album = append(album, telegram.AlbumItem{Path: entry.path, Video: true})
			continue
		}
		if entry.FormatID != "" {
			return nil, fmt.Errorf("Instagram video item %d was not downloaded", index+1)
		}

		// yt-dlp lists thumbnails from the worst to the best quality, and the
		// best one is the original photo.
		urls := make([]string, 0, len(entry.Thumbnails))
		for thumbnail := len(entry.Thumbnails) - 1; thumbnail >= 0; thumbnail-- {
			urls = append(urls, entry.Thumbnails[thumbnail].URL)
		}
		urls = validThreadsMediaURLs(urls)
		if len(urls) == 0 {
			if len(entries) == 1 {
				return nil, ErrNoMedia
			}
			return nil, fmt.Errorf("Instagram carousel item %d has no trusted media URL", index+1)
		}
		id := entry.ID
		if !instagramIDPattern.MatchString(id) {
			id = "photo"
		}
		limit := min(int64(maxTelegramPhotoSize), d.maxFileSize-totalSize)
		path, size, err := d.downloadThreadsItem(ctx, threadsMediaItem{URLs: urls}, dir, fmt.Sprintf("instagram-%02d-%s", index+1, id), limit)
		if err != nil {
			return nil, err
		}
		totalSize += size
		album = append(album, telegram.AlbumItem{Path: path})
	}
	return &DownloadedFile{Album: album, dir: dir}, nil
}

func checkDownloadedSize(path string, limit int64) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("inspect downloaded media: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("downloaded media is not a regular file")
	}
	if info.Size() <= 0 {
		return 0, errors.New("downloaded media is empty")
	}
	if info.Size() > limit {
		return 0, ErrFileTooLarge
	}
	return info.Size(), nil
}
