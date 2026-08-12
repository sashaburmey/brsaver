package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	tikTokPlayerAPIURL    = "https://www.tiktok.com/player/api/v1/items"
	tikTokPlayerBodyLimit = 2 * 1024 * 1024
)

var tikTokVideoIDInErrorPattern = regexp.MustCompile(`(?m)\[TikTok\]\s+([0-9]{15,22}):`)

type tikTokPlayerResponse struct {
	StatusCode int                `json:"status_code"`
	Items      []tikTokPlayerItem `json:"items"`
}

type tikTokPlayerItem struct {
	ID        string `json:"id_str"`
	VideoInfo struct {
		Profiles []struct {
			CodecType string `json:"codec_type"`
			Bitrate   int64  `json:"bitrate"`
			PlayAddr  struct {
				DataSize int64    `json:"data_size"`
				Height   int64    `json:"height"`
				Width    int64    `json:"width"`
				URLs     []string `json:"url_list"`
			} `json:"play_addr"`
		} `json:"profiles"`
	} `json:"video_info"`
	ImagePostInfo struct {
		Images []struct {
			DisplayImage struct {
				URLs []string `json:"url_list"`
			} `json:"display_image"`
		} `json:"images"`
	} `json:"image_post_info"`
}

type tikTokPlayerProfile struct {
	URLs    []string
	Pixels  int64
	Bitrate int64
}

func newTikTokHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many TikTok redirects")
			}
			if request.URL.Scheme != "https" || !isTikTokHTTPHost(request.URL.Hostname()) {
				return errors.New("TikTok redirected to an unexpected host")
			}
			return nil
		},
	}
}

func (d *Downloader) downloadTikTokPlayerVideo(ctx context.Context, rawURL, ytDLPError, dir string) (string, error) {
	videoID, err := d.resolveTikTokVideoID(ctx, rawURL, ytDLPError)
	if err != nil {
		return "", err
	}
	profiles, err := d.fetchTikTokPlayerProfiles(ctx, videoID)
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, "tiktok-player-"+videoID+".mp4")
	var lastErr error
	for _, profile := range profiles {
		for _, mediaURL := range profile.URLs {
			if err := d.downloadTikTokPlayerURL(ctx, mediaURL, videoID, path); err == nil {
				return path, nil
			} else {
				if errors.Is(err, ErrFileTooLarge) || ctx.Err() != nil {
					return "", err
				}
				lastErr = err
			}
		}
	}
	return "", fmt.Errorf("all TikTok player media URLs failed: %w", lastErr)
}

func (d *Downloader) downloadTikTokPlayerPhotos(ctx context.Context, photoURL, dir string) ([]string, error) {
	itemID := tikTokPhotoIDFromURL(photoURL)
	if itemID == "" {
		return nil, errors.New("could not determine the TikTok photo post ID")
	}
	item, err := d.fetchTikTokPlayerItem(ctx, itemID)
	if err != nil {
		return nil, err
	}
	if len(item.ImagePostInfo.Images) == 0 {
		return nil, errors.New("TikTok player did not return photos")
	}
	if len(item.ImagePostInfo.Images) > maxTikTokPhotos {
		return nil, errors.New("TikTok photo count exceeds the configured limit")
	}

	paths := make([]string, 0, len(item.ImagePostInfo.Images))
	var totalSize int64
	for index, image := range item.ImagePostInfo.Images {
		urls := validTikTokMediaURLs(image.DisplayImage.URLs)
		if len(urls) == 0 {
			return nil, errors.New("TikTok player did not return a trusted photo URL")
		}

		var (
			path     string
			fileSize int64
			lastErr  error
		)
		for _, imageURL := range urls {
			path, fileSize, lastErr = d.downloadTikTokPlayerPhotoURL(
				ctx, imageURL, itemID, dir, index+1, d.maxFileSize-totalSize,
			)
			if lastErr == nil {
				break
			}
			if errors.Is(lastErr, ErrFileTooLarge) || ctx.Err() != nil {
				return nil, lastErr
			}
		}
		if lastErr != nil {
			return nil, fmt.Errorf("all TikTok player URLs for photo %d failed: %w", index+1, lastErr)
		}
		totalSize += fileSize
		paths = append(paths, path)
	}
	return paths, nil
}

func (d *Downloader) resolveTikTokVideoID(ctx context.Context, rawURL, ytDLPError string) (string, error) {
	if videoID := tikTokVideoIDFromURL(rawURL); videoID != "" {
		return videoID, nil
	}
	if match := tikTokVideoIDInErrorPattern.FindStringSubmatch(ytDLPError); len(match) == 2 {
		return match[1], nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", errors.New("invalid TikTok URL")
	}
	request.Header.Set("User-Agent", tikTokBrowserUserAgent)
	response, err := d.tikTokHTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve TikTok URL: %w", err)
	}
	response.Body.Close()
	if videoID := tikTokVideoIDFromURL(response.Request.URL.String()); videoID != "" {
		return videoID, nil
	}
	return "", errors.New("could not determine the TikTok video ID")
}

func (d *Downloader) fetchTikTokPlayerProfiles(ctx context.Context, videoID string) ([]tikTokPlayerProfile, error) {
	item, err := d.fetchTikTokPlayerItem(ctx, videoID)
	if err != nil {
		return nil, err
	}

	profiles := make([]tikTokPlayerProfile, 0, len(item.VideoInfo.Profiles))
	oversizedH264 := false
	for _, profile := range item.VideoInfo.Profiles {
		if !strings.EqualFold(profile.CodecType, "h264") {
			continue
		}
		if profile.PlayAddr.DataSize > d.maxFileSize {
			oversizedH264 = true
			continue
		}
		urls := validTikTokMediaURLs(profile.PlayAddr.URLs)
		if len(urls) == 0 {
			continue
		}
		profiles = append(profiles, tikTokPlayerProfile{
			URLs: urls, Pixels: profile.PlayAddr.Width * profile.PlayAddr.Height, Bitrate: profile.Bitrate,
		})
	}
	if len(profiles) == 0 {
		if oversizedH264 {
			return nil, ErrFileTooLarge
		}
		return nil, errors.New("TikTok player did not return a compatible H.264 video")
	}
	sort.SliceStable(profiles, func(left, right int) bool {
		if profiles[left].Pixels != profiles[right].Pixels {
			return profiles[left].Pixels > profiles[right].Pixels
		}
		return profiles[left].Bitrate > profiles[right].Bitrate
	})
	return profiles, nil
}

func (d *Downloader) fetchTikTokPlayerItem(ctx context.Context, itemID string) (tikTokPlayerItem, error) {
	endpoint, err := url.Parse(tikTokPlayerAPIURL)
	if err != nil {
		return tikTokPlayerItem{}, errors.New("invalid TikTok player API URL")
	}
	query := endpoint.Query()
	query.Set("item_ids", itemID)
	query.Set("language", "en")
	query.Set("aid", "1459")
	query.Set("data_source", "web_core")
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return tikTokPlayerItem{}, fmt.Errorf("create TikTok player request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Referer", "https://www.tiktok.com/player/v1/"+itemID)
	request.Header.Set("User-Agent", tikTokBrowserUserAgent)
	response, err := d.tikTokHTTPClient.Do(request)
	if err != nil {
		return tikTokPlayerItem{}, fmt.Errorf("request TikTok player API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tikTokPlayerItem{}, fmt.Errorf("TikTok player API returned HTTP %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, tikTokPlayerBodyLimit+1))
	if err != nil {
		return tikTokPlayerItem{}, fmt.Errorf("read TikTok player response: %w", err)
	}
	if len(body) > tikTokPlayerBodyLimit {
		return tikTokPlayerItem{}, errors.New("TikTok player response is too large")
	}
	var result tikTokPlayerResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return tikTokPlayerItem{}, fmt.Errorf("decode TikTok player response: %w", err)
	}
	if result.StatusCode != 0 || len(result.Items) != 1 || result.Items[0].ID != itemID {
		return tikTokPlayerItem{}, errors.New("TikTok player returned an unexpected item")
	}
	return result.Items[0], nil
}

func (d *Downloader) downloadTikTokPlayerURL(ctx context.Context, mediaURL, videoID, path string) (resultErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return errors.New("invalid TikTok media URL")
	}
	request.Header.Set("Accept", "video/mp4,video/*;q=0.9,*/*;q=0.5")
	request.Header.Set("Referer", "https://www.tiktok.com/player/v1/"+videoID)
	request.Header.Set("User-Agent", tikTokBrowserUserAgent)
	response, err := d.tikTokHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("request TikTok media: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("TikTok media returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > d.maxFileSize {
		return ErrFileTooLarge
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create TikTok media file: %w", err)
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("close TikTok media file: %w", closeErr)
			}
		}
		if resultErr != nil {
			_ = os.Remove(path)
		}
	}()

	written, err := io.Copy(file, io.LimitReader(response.Body, d.maxFileSize+1))
	if err != nil {
		return fmt.Errorf("download TikTok media: %w", err)
	}
	if written > d.maxFileSize {
		return ErrFileTooLarge
	}
	if written < 12 {
		return errors.New("TikTok media response is empty or truncated")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync TikTok media file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close TikTok media file: %w", err)
	}
	file = nil
	header := make([]byte, 12)
	readFile, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect TikTok media file: %w", err)
	}
	_, readErr := io.ReadFull(readFile, header)
	closeErr := readFile.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(header[4:8], []byte("ftyp")) {
		return errors.New("TikTok player did not return a valid MP4 file")
	}
	return nil
}

func (d *Downloader) downloadTikTokPlayerPhotoURL(
	ctx context.Context,
	mediaURL, itemID, dir string,
	index int,
	remainingTotal int64,
) (_ string, _ int64, resultErr error) {
	if remainingTotal <= 0 {
		return "", 0, ErrFileTooLarge
	}
	limit := min(int64(maxTelegramPhotoSize), remainingTotal)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", 0, errors.New("invalid TikTok photo URL")
	}
	request.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/*;q=0.8")
	request.Header.Set("Referer", "https://www.tiktok.com/player/v1/"+itemID)
	request.Header.Set("User-Agent", tikTokBrowserUserAgent)
	response, err := d.tikTokHTTPClient.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("request TikTok photo: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", 0, fmt.Errorf("TikTok photo returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return "", 0, ErrFileTooLarge
	}

	temporaryPath := filepath.Join(dir, fmt.Sprintf("tiktok-player-%s-%02d.part", itemID, index))
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create TikTok photo file: %w", err)
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("close TikTok photo file: %w", closeErr)
			}
		}
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	written, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", 0, fmt.Errorf("download TikTok photo: %w", err)
	}
	if written > limit {
		return "", 0, ErrFileTooLarge
	}
	if written == 0 {
		return "", 0, errors.New("TikTok returned an empty photo")
	}
	if err := file.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync TikTok photo file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close TikTok photo file: %w", err)
	}
	file = nil

	header := make([]byte, 512)
	readFile, err := os.Open(temporaryPath)
	if err != nil {
		return "", 0, fmt.Errorf("inspect TikTok photo file: %w", err)
	}
	read, readErr := readFile.Read(header)
	closeErr := readFile.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", 0, fmt.Errorf("inspect TikTok photo file: %w", readErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close inspected TikTok photo file: %w", closeErr)
	}
	extension := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	}[http.DetectContentType(header[:read])]
	if extension == "" {
		return "", 0, errors.New("TikTok returned an unsupported photo format")
	}
	path := strings.TrimSuffix(temporaryPath, ".part") + extension
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", 0, fmt.Errorf("finalize TikTok photo file: %w", err)
	}
	return path, written, nil
}

func tikTokVideoIDFromURL(rawURL string) string {
	return tikTokItemIDFromURL(rawURL, "video")
}

func tikTokPhotoIDFromURL(rawURL string) string {
	return tikTokItemIDFromURL(rawURL, "photo")
}

func tikTokItemIDFromURL(rawURL, itemType string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !hostMatches(strings.ToLower(parsed.Hostname()), "tiktok.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if strings.EqualFold(parts[index], itemType) && validTikTokVideoID(parts[index+1]) {
			return parts[index+1]
		}
	}
	return ""
}

func validTikTokVideoID(value string) bool {
	return len(value) >= 15 && len(value) <= 22 && strings.Trim(value, "0123456789") == ""
}

func validTikTokMediaURLs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || !isTikTokHTTPHost(parsed.Hostname()) {
			continue
		}
		if _, exists := seen[parsed.String()]; exists {
			continue
		}
		seen[parsed.String()] = struct{}{}
		result = append(result, parsed.String())
	}
	return result
}

func isTikTokHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return hostMatches(host, "tiktok.com") || hostMatches(host, "tiktokcdn.com") || hostMatches(host, "tiktokv.com")
}

const tikTokBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140 Safari/537.36"
