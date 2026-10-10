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

	"brsaver/internal/telegram"
)

const (
	threadsPageSizeLimit    = 8 * 1024 * 1024
	maxThreadsAlbumItems    = 20
	threadsBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

	threadsMediaPhoto    = 1
	threadsMediaVideo    = 2
	threadsMediaCarousel = 8
)

var threadsJSONScriptPattern = regexp.MustCompile(`(?s)<script type="application/json"[^>]*>(.*?)</script>`)

type threadsPost struct {
	Code          string `json:"code"`
	MediaType     int    `json:"media_type"`
	ImageVersions struct {
		Candidates []threadsMediaVersion `json:"candidates"`
	} `json:"image_versions2"`
	VideoVersions []threadsMediaVersion `json:"video_versions"`
	CarouselMedia []threadsPost         `json:"carousel_media"`
}

type threadsMediaVersion struct {
	URL    string `json:"url"`
	Width  int64  `json:"width"`
	Height int64  `json:"height"`
}

type threadsMediaItem struct {
	URLs  []string
	Video bool
}

func newThreadsHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many Threads redirects")
			}
			if request.URL.Scheme != "https" || !isThreadsHTTPHost(request.URL.Hostname()) {
				return errors.New("Threads redirected to an unexpected host")
			}
			return nil
		},
	}
}

func isThreadsPostURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && threadsPostCode(parsed) != ""
}

func (d *Downloader) downloadThreadsPostOnce(ctx context.Context, rawURL string) (_ *DownloadedFile, resultErr error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid Threads URL")
	}
	code := threadsPostCode(parsed)
	if code == "" {
		return nil, errors.New("unsupported Threads URL")
	}
	// Only the post code comes from the message; the page is always requested
	// from Threads itself, which redirects the short link to the full post.
	document, err := d.fetchThreadsDocument(ctx, "https://www.threads.com/t/"+code)
	if err != nil {
		return nil, fmt.Errorf("request Threads post: %w", err)
	}
	items, err := parseThreadsPostHTML(document, code)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp(d.workDir, "job-")
	if err != nil {
		return nil, fmt.Errorf("create job directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("clean failed download: %w", cleanupErr)
			}
		}
	}()

	// A single video goes through the regular video path, so it can still be
	// compressed when it exceeds the Telegram upload limit.
	if len(items) == 1 && items[0].Video {
		path, _, err := d.downloadThreadsItem(ctx, items[0], dir, "threads-"+code, d.maxFileSize)
		if err != nil {
			return nil, err
		}
		keep = true
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
		path, size, err := d.downloadThreadsItem(ctx, item, dir, fmt.Sprintf("threads-%s-%02d", code, index+1), limit)
		if err != nil {
			return nil, err
		}
		totalSize += size
		album = append(album, telegram.AlbumItem{Path: path, Video: item.Video})
	}
	keep = true
	return &DownloadedFile{Album: album, dir: dir}, nil
}

func (d *Downloader) fetchThreadsDocument(ctx context.Context, pageURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	// Threads embeds the post data only into pages requested as a top-level
	// browser navigation; other requests receive an empty application shell.
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("Sec-Fetch-Dest", "document")
	request.Header.Set("Sec-Fetch-Mode", "navigate")
	request.Header.Set("Sec-Fetch-Site", "none")
	request.Header.Set("Sec-Fetch-User", "?1")
	request.Header.Set("Upgrade-Insecure-Requests", "1")
	request.Header.Set("User-Agent", threadsBrowserUserAgent)

	response, err := d.threadsHTTPClient.Do(request)
	if err != nil {
		return "", withoutRequestURL(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, threadsPageSizeLimit+1))
	if err != nil {
		return "", fmt.Errorf("read response: %w", withoutRequestURL(err))
	}
	if len(body) > threadsPageSizeLimit {
		return "", errors.New("response is too large")
	}
	return string(body), nil
}

func parseThreadsPostHTML(document, code string) ([]threadsMediaItem, error) {
	for _, match := range threadsJSONScriptPattern.FindAllStringSubmatch(document, -1) {
		var data any
		if err := json.Unmarshal([]byte(match[1]), &data); err != nil {
			continue
		}
		raw := findThreadsPost(data, code)
		if raw == nil {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("encode Threads post: %w", err)
		}
		var post threadsPost
		if err := json.Unmarshal(encoded, &post); err != nil {
			return nil, fmt.Errorf("decode Threads post: %w", err)
		}
		return threadsPostMedia(post)
	}
	return nil, errors.New("Threads page does not contain the requested post")
}

// findThreadsPost locates the post object by its code. The page also contains
// replies and related posts, so the code is the only reliable discriminator.
func findThreadsPost(data any, code string) map[string]any {
	stack := []any{data}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch value := node.(type) {
		case map[string]any:
			if value["code"] == code {
				if _, ok := value["media_type"]; ok {
					return value
				}
			}
			for _, child := range value {
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, value...)
		}
	}
	return nil
}

func threadsPostMedia(post threadsPost) ([]threadsMediaItem, error) {
	if post.MediaType != threadsMediaCarousel {
		item, ok := threadsSingleMedia(post)
		if !ok {
			return nil, ErrNoMedia
		}
		return []threadsMediaItem{item}, nil
	}

	if len(post.CarouselMedia) == 0 {
		return nil, ErrNoMedia
	}
	if len(post.CarouselMedia) > maxThreadsAlbumItems {
		return nil, errors.New("Threads carousel exceeds the item limit")
	}
	items := make([]threadsMediaItem, 0, len(post.CarouselMedia))
	for index, child := range post.CarouselMedia {
		item, ok := threadsSingleMedia(child)
		if !ok {
			return nil, fmt.Errorf("Threads carousel item %d has no trusted media URL", index+1)
		}
		items = append(items, item)
	}
	return items, nil
}

func threadsSingleMedia(post threadsPost) (threadsMediaItem, bool) {
	switch post.MediaType {
	case threadsMediaVideo:
		// Threads lists video versions from the best quality downwards.
		urls := make([]string, 0, len(post.VideoVersions))
		for _, version := range post.VideoVersions {
			urls = append(urls, version.URL)
		}
		urls = validThreadsMediaURLs(urls)
		return threadsMediaItem{URLs: urls, Video: true}, len(urls) > 0
	case threadsMediaPhoto:
		candidates := append([]threadsMediaVersion(nil), post.ImageVersions.Candidates...)
		sort.SliceStable(candidates, func(left, right int) bool {
			return candidates[left].Width*candidates[left].Height > candidates[right].Width*candidates[right].Height
		})
		urls := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			urls = append(urls, candidate.URL)
		}
		urls = validThreadsMediaURLs(urls)
		return threadsMediaItem{URLs: urls}, len(urls) > 0
	default:
		return threadsMediaItem{}, false
	}
}

func (d *Downloader) downloadThreadsItem(ctx context.Context, item threadsMediaItem, dir, name string, limit int64) (string, int64, error) {
	if limit <= 0 {
		return "", 0, ErrFileTooLarge
	}
	var lastErr error
	for _, mediaURL := range item.URLs {
		path, size, err := d.downloadThreadsURL(ctx, mediaURL, dir, name, item.Video, limit)
		if err == nil {
			return path, size, nil
		}
		if errors.Is(err, ErrFileTooLarge) || ctx.Err() != nil {
			return "", 0, err
		}
		lastErr = err
	}
	return "", 0, fmt.Errorf("all Threads media URLs failed: %w", lastErr)
}

func (d *Downloader) downloadThreadsURL(
	ctx context.Context,
	mediaURL, dir, name string,
	video bool,
	limit int64,
) (_ string, _ int64, resultErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return "", 0, errors.New("invalid Threads media URL")
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Referer", "https://www.threads.com/")
	request.Header.Set("User-Agent", threadsBrowserUserAgent)
	response, err := d.threadsHTTPClient.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("request Threads media: %w", withoutRequestURL(err))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", 0, fmt.Errorf("Threads media returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return "", 0, ErrFileTooLarge
	}

	temporaryPath := filepath.Join(dir, name+".part")
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create Threads media file: %w", err)
	}
	defer func() {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("close Threads media file: %w", closeErr)
			}
		}
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	written, err := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", 0, fmt.Errorf("download Threads media: %w", withoutRequestURL(err))
	}
	if written > limit {
		return "", 0, ErrFileTooLarge
	}
	if written < 12 {
		return "", 0, errors.New("Threads media response is empty or truncated")
	}
	if err := file.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync Threads media file: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, fmt.Errorf("close Threads media file: %w", err)
	}
	file = nil

	header := make([]byte, 512)
	readFile, err := os.Open(temporaryPath)
	if err != nil {
		return "", 0, fmt.Errorf("inspect Threads media file: %w", err)
	}
	read, readErr := io.ReadFull(readFile, header)
	closeErr := readFile.Close()
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", 0, fmt.Errorf("inspect Threads media file: %w", readErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close inspected Threads media file: %w", closeErr)
	}
	header = header[:read]

	extension := ""
	if video {
		if bytes.Equal(header[4:8], []byte("ftyp")) {
			extension = ".mp4"
		}
	} else {
		extension = map[string]string{
			"image/jpeg": ".jpg",
			"image/png":  ".png",
			"image/webp": ".webp",
		}[http.DetectContentType(header)]
	}
	if extension == "" {
		return "", 0, errors.New("Threads returned an unsupported media format")
	}
	path := filepath.Join(dir, name+extension)
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", 0, fmt.Errorf("finalize Threads media file: %w", err)
	}
	return path, written, nil
}

func validThreadsMediaURLs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || !isThreadsCDNHost(parsed.Hostname()) {
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

func isThreadsCDNHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return hostMatches(host, "fbcdn.net") || hostMatches(host, "cdninstagram.com")
}

func isThreadsHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return isThreadsHost(host) || isThreadsCDNHost(host)
}

// withoutRequestURL drops the URL from HTTP client errors, because signed
// media URLs must not end up in logs.
func withoutRequestURL(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err
	}
	return err
}
