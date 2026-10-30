package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const instagramPhotoInfo = `{"id":"DerJwPnocL2","formats":[],"thumbnails":[
{"url":"https://scontent.cdninstagram.com/small.jpg","id":"0"},
{"url":"https://attacker.example/original.jpg","id":"1"},
{"url":"https://scontent-iev1-1.cdninstagram.com/original.jpg","id":"2"}]}`

const instagramCarouselInfo = `{"_type":"playlist","id":"Carousel01","entries":[
{"id":"child1","formats":[],"thumbnails":[{"url":"https://scontent.cdninstagram.com/photo.jpg"}]},
{"id":"child2","thumbnails":[{"url":"https://scontent.cdninstagram.com/cover.jpg"}],"formats":[
{"format_id":"dash-1","protocol":"http_dash_segments","url":"https://scontent.cdninstagram.com/dash.mp4","vcodec":"avc1","width":1080,"height":1920},
{"format_id":"101","protocol":"https","url":"https://scontent.cdninstagram.com/small.mp4","vcodec":"avc1","width":480,"height":854},
{"format_id":"102","protocol":"https","url":"https://instagram.fiev1-1.fna.fbcdn.net/large.mp4","vcodec":"avc1","width":720,"height":1280}]}]}`

func TestInstagramPostMediaPrefersOriginalPhotoAndProgressiveVideo(t *testing.T) {
	t.Parallel()

	items, err := instagramPostMedia(decodeInstagramInfo(t, instagramPhotoInfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Video || len(items[0].URLs) != 2 ||
		items[0].URLs[0] != "https://scontent-iev1-1.cdninstagram.com/original.jpg" {
		t.Fatalf("unexpected photo items: %#v", items)
	}

	items, err = instagramPostMedia(decodeInstagramInfo(t, instagramCarouselInfo))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Video || !items[1].Video {
		t.Fatalf("unexpected carousel items: %#v", items)
	}
	if len(items[1].URLs) != 2 || items[1].URLs[0] != "https://instagram.fiev1-1.fna.fbcdn.net/large.mp4" {
		t.Fatalf("unexpected video URLs: %#v", items[1].URLs)
	}

	if _, err := instagramPostMedia(decodeInstagramInfo(t, `{"id":"Empty0001","formats":[],"thumbnails":[]}`)); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("instagramPostMedia() error = %v, want ErrNoMedia", err)
	}
}

func TestDownloaderFallsBackToInstagramPhoto(t *testing.T) {
	photo := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x11}, 32)...)
	file := downloadInstagramWithFakeYTDLP(t, instagramPhotoInfo, map[string][]byte{
		"https://scontent-iev1-1.cdninstagram.com/original.jpg": photo,
	})
	defer file.Cleanup()

	if file.Path != "" || len(file.Album) != 1 || file.Album[0].Video ||
		filepath.Base(file.Album[0].Path) != "instagram-DerJwPnocL2-01.jpg" {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if body, err := os.ReadFile(file.Album[0].Path); err != nil || !bytes.Equal(body, photo) {
		t.Fatalf("downloaded photo = %q, %v", body, err)
	}
}

func TestDownloaderFallsBackToInstagramCarousel(t *testing.T) {
	photo := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x11}, 32)...)
	video := threadsTestMP4()
	file := downloadInstagramWithFakeYTDLP(t, instagramCarouselInfo, map[string][]byte{
		"https://scontent.cdninstagram.com/photo.jpg":       photo,
		"https://instagram.fiev1-1.fna.fbcdn.net/large.mp4": video,
	})
	defer file.Cleanup()

	if file.Path != "" || len(file.Album) != 2 {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if file.Album[0].Video || filepath.Base(file.Album[0].Path) != "instagram-Carousel01-01.jpg" {
		t.Errorf("unexpected photo item: %#v", file.Album[0])
	}
	if !file.Album[1].Video || filepath.Base(file.Album[1].Path) != "instagram-Carousel01-02.mp4" {
		t.Errorf("unexpected video item: %#v", file.Album[1])
	}
}

func downloadInstagramWithFakeYTDLP(t *testing.T, info string, media map[string][]byte) *DownloadedFile {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}

	workDir := t.TempDir()
	binary := writeHelper(t, workDir, `#!/bin/sh
for argument in "$@"; do
	if [ "$argument" = "--dump-single-json" ]; then
		cat <<'JSON'
`+info+`
JSON
		exit 0
	fi
done
printf '%s\n' 'ERROR: [Instagram] DerJwPnocL2: There is no video in this post' >&2
exit 1
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: binary, WorkDir: workDir, Format: "best", MaxFileSize: 4096,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	downloader.threadsHTTPClient = &http.Client{
		Transport: tikTokRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, ok := media[request.URL.String()]
			if !ok {
				return nil, fmt.Errorf("unexpected request %s", request.URL)
			}
			return tikTokHTTPResponse(request, http.StatusOK, body), nil
		}),
		CheckRedirect: downloader.threadsHTTPClient.CheckRedirect,
	}

	file, err := downloader.Download(context.Background(), "https://www.instagram.com/p/DerJwPnocL2/?exln=aDNyb3lvZDcyYjlz")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	return file
}

func decodeInstagramInfo(t *testing.T, value string) instagramPostInfo {
	t.Helper()
	info, err := decodeInstagramPostInfo([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestDownloaderDoesNotRetryRestrictedInstagramPost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	calls := filepath.Join(workDir, "calls")
	binary := writeHelper(t, workDir, `#!/bin/sh
printf 'call\n' >> '`+calls+`'
printf '%s\n' "ERROR: [Instagram] DfC8zcpk26z: This content isn't available to everyone: It can't be seen by certain audiences." >&2
exit 1
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: binary, WorkDir: workDir, Format: "best", MaxFileSize: 4096,
		SizeCheckGap: time.Millisecond, Attempts: 3, RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = downloader.Download(context.Background(), "https://www.instagram.com/reel/DfC8zcpk26z/")
	if !errors.Is(err, ErrRestricted) {
		t.Fatalf("Download() error = %v, want ErrRestricted", err)
	}
	if body, readErr := os.ReadFile(calls); readErr != nil || string(body) != "call\n" {
		t.Fatalf("yt-dlp calls = %q, %v; want exactly one", body, readErr)
	}
}
