package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const threadsTestPage = `<!DOCTYPE html><html><head>
<script type="application/json" data-sjs>{"broken":</script>
<script type="application/json" data-sjs>{"require":[["RelayPrefetchedStreamCache","next",[],[{"__bbox":{"result":{"data":{
"replies":[{"code":"ReplyCode01","media_type":2,"video_versions":[{"type":101,"url":"https://scontent.cdninstagram.com/reply.mp4"}]}],
"media":{"code":"PostCode123","media_type":8,"video_versions":null,"carousel_media":[
{"code":"ChildCode01","media_type":1,"video_versions":null,"image_versions2":{"candidates":[
{"width":320,"height":427,"url":"https://scontent.cdninstagram.com/small.jpg"},
{"width":1440,"height":1920,"url":"https://attacker.example/huge.jpg"},
{"width":1080,"height":1440,"url":"https://instagram.fiev1-1.fna.fbcdn.net/large.jpg"}]}},
{"code":"ChildCode02","media_type":2,"image_versions2":{"candidates":[{"width":720,"height":1280,"url":"https://scontent.cdninstagram.com/cover.jpg"}]},
"video_versions":[{"type":101,"url":"https://instagram.fiev1-1.fna.fbcdn.net/video.mp4"},{"type":102,"url":"https://instagram.fiev1-1.fna.fbcdn.net/video.mp4"}]}
]}}}}}]]]}</script>
</head><body></body></html>`

func TestParseThreadsPostHTMLSelectsRequestedCarousel(t *testing.T) {
	t.Parallel()

	items, err := parseThreadsPostHTML(threadsTestPage, "PostCode123")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2: %#v", len(items), items)
	}
	if items[0].Video || len(items[0].URLs) != 2 || items[0].URLs[0] != "https://instagram.fiev1-1.fna.fbcdn.net/large.jpg" {
		t.Fatalf("unexpected photo item: %#v", items[0])
	}
	if !items[1].Video || len(items[1].URLs) != 1 || items[1].URLs[0] != "https://instagram.fiev1-1.fna.fbcdn.net/video.mp4" {
		t.Fatalf("unexpected video item: %#v", items[1])
	}
}

func TestParseThreadsPostHTMLRejectsPostsWithoutMedia(t *testing.T) {
	t.Parallel()

	document := `<script type="application/json">{"data":{"media":{"code":"TextPost01","media_type":19,` +
		`"image_versions2":{"candidates":[]},"video_versions":null,"carousel_media":null}}}</script>`
	if _, err := parseThreadsPostHTML(document, "TextPost01"); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("parseThreadsPostHTML() error = %v, want ErrNoMedia", err)
	}
	if _, err := parseThreadsPostHTML(threadsTestPage, "MissingPost"); err == nil || errors.Is(err, ErrNoMedia) {
		t.Fatalf("parseThreadsPostHTML() error = %v, want missing post error", err)
	}
}

func TestDownloaderDownloadsThreadsVideoPost(t *testing.T) {
	t.Parallel()

	video := threadsTestMP4()
	document := `<script type="application/json">{"media":{"code":"VideoPost01","media_type":2,` +
		`"video_versions":[{"url":"https://attacker.example/video.mp4"},{"url":"https://instagram.fiev1-1.fna.fbcdn.net/video.mp4"}]}}</script>`
	downloader := newThreadsTestDownloader(t, 4096, func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://www.threads.com/t/VideoPost01":
			if request.Header.Get("Sec-Fetch-Mode") != "navigate" || !strings.Contains(request.Header.Get("Accept"), "text/html") {
				t.Errorf("Threads page requested without navigation headers: %v", request.Header)
			}
			return tikTokHTTPResponse(request, http.StatusOK, []byte(document)), nil
		case "https://instagram.fiev1-1.fna.fbcdn.net/video.mp4":
			return tikTokHTTPResponse(request, http.StatusOK, video), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
	})

	file, err := downloader.Download(context.Background(), "https://www.threads.net/@creator/post/VideoPost01?xmt=tracking")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Album) != 0 || filepath.Base(file.Path) != "threads-VideoPost01.mp4" {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if body, err := os.ReadFile(file.Path); err != nil || !bytes.Equal(body, video) {
		t.Fatalf("downloaded video = %q, %v", body, err)
	}
	jobDir := filepath.Dir(file.Path)
	if err := file.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("Threads job directory still exists after cleanup: %v", err)
	}
}

func TestDownloaderDownloadsThreadsCarouselAsAlbum(t *testing.T) {
	t.Parallel()

	photo := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x11}, 32)...)
	video := threadsTestMP4()
	downloader := newThreadsTestDownloader(t, 4096, func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://www.threads.com/t/PostCode123":
			return tikTokHTTPResponse(request, http.StatusOK, []byte(threadsTestPage)), nil
		case "https://instagram.fiev1-1.fna.fbcdn.net/large.jpg":
			return tikTokHTTPResponse(request, http.StatusOK, photo), nil
		case "https://instagram.fiev1-1.fna.fbcdn.net/video.mp4":
			return tikTokHTTPResponse(request, http.StatusOK, video), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
	})

	file, err := downloader.Download(context.Background(), "https://www.threads.com/@creator/post/PostCode123")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Cleanup()
	if file.Path != "" || len(file.Album) != 2 {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if file.Album[0].Video || filepath.Base(file.Album[0].Path) != "threads-PostCode123-01.jpg" {
		t.Errorf("unexpected photo item: %#v", file.Album[0])
	}
	if !file.Album[1].Video || filepath.Base(file.Album[1].Path) != "threads-PostCode123-02.mp4" {
		t.Errorf("unexpected video item: %#v", file.Album[1])
	}
}

func TestDownloaderLimitsThreadsAlbumVideosToUploadSize(t *testing.T) {
	t.Parallel()

	downloader := newThreadsTestDownloader(t, 4096, func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://www.threads.com/t/PostCode123":
			return tikTokHTTPResponse(request, http.StatusOK, []byte(threadsTestPage)), nil
		case "https://instagram.fiev1-1.fna.fbcdn.net/large.jpg":
			return tikTokHTTPResponse(request, http.StatusOK, append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 32)...)), nil
		case "https://instagram.fiev1-1.fna.fbcdn.net/video.mp4":
			return tikTokHTTPResponse(request, http.StatusOK, append(threadsTestMP4(), make([]byte, 128)...)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s", request.URL)
		}
	})
	downloader.maxUploadSize = 64

	_, err := downloader.Download(context.Background(), "https://www.threads.com/t/PostCode123")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Download() error = %v, want ErrFileTooLarge", err)
	}
	entries, err := os.ReadDir(downloader.workDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed Threads job left files behind: %v", entries)
	}
}

func TestDownloaderDoesNotRetryThreadsPostWithoutMedia(t *testing.T) {
	t.Parallel()

	requests := 0
	downloader := newThreadsTestDownloader(t, 4096, func(request *http.Request) (*http.Response, error) {
		requests++
		document := `<script type="application/json">{"media":{"code":"TextPost01","media_type":19}}</script>`
		return tikTokHTTPResponse(request, http.StatusOK, []byte(document)), nil
	})
	downloader.attempts = 3

	if _, err := downloader.Download(context.Background(), "https://www.threads.com/t/TextPost01"); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("Download() error = %v, want ErrNoMedia", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestThreadsHTTPClientRejectsForeignRedirects(t *testing.T) {
	t.Parallel()

	client := newThreadsHTTPClient()
	for target, allowed := range map[string]bool{
		"https://www.threads.com/@creator/post/PostCode123": true,
		"https://scontent.cdninstagram.com/photo.jpg":       true,
		"https://attacker.example/photo.jpg":                false,
		"http://www.threads.com/@creator/post/PostCode123":  false,
	} {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.CheckRedirect(request, nil); (err == nil) != allowed {
			t.Errorf("redirect to %s: error = %v, allowed = %v", target, err, allowed)
		}
	}
}

func newThreadsTestDownloader(t *testing.T, maxSize int64, roundTrip tikTokRoundTripFunc) *Downloader {
	t.Helper()
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: "yt-dlp-must-not-run", WorkDir: t.TempDir(), Format: "best",
		MaxFileSize: maxSize, SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	downloader.threadsHTTPClient = &http.Client{Transport: roundTrip, CheckRedirect: downloader.threadsHTTPClient.CheckRedirect}
	return downloader
}

func threadsTestMP4() []byte {
	return append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, bytes.Repeat([]byte{0x22}, 32)...)
}
