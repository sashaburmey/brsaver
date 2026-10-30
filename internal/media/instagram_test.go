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
	"strings"
	"testing"
	"time"
)

const (
	instagramPhotoLine = `{"id": "DerJwPnocL2", "thumbnails": [` +
		`{"url": "https://scontent.cdninstagram.com/small.jpg"}, ` +
		`{"url": "https://attacker.example/original.jpg"}, ` +
		`{"url": "https://scontent-iev1-1.cdninstagram.com/original.jpg"}]}`
	instagramNoFormats = `printf '%s\n' 'ERROR: [Instagram] DerJwPnocL2: No video formats found!' >&2`
)

func TestDownloaderSendsInstagramPhotoFromSingleYTDLPCall(t *testing.T) {
	photo := instagramTestJPEG()
	result := downloadInstagramWithFakeYTDLP(t, `
printf '%s\n' '`+instagramPhotoLine+`'
`+instagramNoFormats+`
exit 1
`, map[string][]byte{"https://scontent-iev1-1.cdninstagram.com/original.jpg": photo})
	if result.err != nil {
		t.Fatalf("Download() error = %v", result.err)
	}
	file := result.file
	defer file.Cleanup()

	if file.Path != "" || len(file.Album) != 1 || file.Album[0].Video ||
		filepath.Base(file.Album[0].Path) != "instagram-01-DerJwPnocL2.jpg" {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if body, err := os.ReadFile(file.Album[0].Path); err != nil || !bytes.Equal(body, photo) {
		t.Fatalf("downloaded photo = %q, %v", body, err)
	}
	if result.calls != 1 {
		t.Fatalf("yt-dlp ran %d times, want 1", result.calls)
	}
}

func TestDownloaderSendsMixedInstagramCarouselInOrder(t *testing.T) {
	result := downloadInstagramWithFakeYTDLP(t, `
printf '%s\n' '{"id": "PhotoItem01", "thumbnails": [{"url": "https://scontent.cdninstagram.com/photo.jpg"}]}'
`+instagramNoFormats+`
printf '%s\n' '{"id": "VideoItem02", "format_id": "101", "thumbnails": []}'
printf 'video' > "$dir/Instagram-VideoItem02.mp4"
printf '%s\n' "$dir/Instagram-VideoItem02.mp4"
exit 1
`, map[string][]byte{"https://scontent.cdninstagram.com/photo.jpg": instagramTestJPEG()})
	if result.err != nil {
		t.Fatalf("Download() error = %v", result.err)
	}
	file := result.file
	defer file.Cleanup()

	if file.Path != "" || len(file.Album) != 2 {
		t.Fatalf("unexpected downloaded media: %#v", file)
	}
	if file.Album[0].Video || filepath.Base(file.Album[0].Path) != "instagram-01-PhotoItem01.jpg" {
		t.Errorf("unexpected photo item: %#v", file.Album[0])
	}
	if !file.Album[1].Video || filepath.Base(file.Album[1].Path) != "Instagram-VideoItem02.mp4" {
		t.Errorf("unexpected video item: %#v", file.Album[1])
	}
}

func TestDownloaderSendsEveryVideoOfInstagramCarousel(t *testing.T) {
	result := downloadInstagramWithFakeYTDLP(t, `
for id in VideoItem01 VideoItem02 VideoItem03; do
	printf '{"id": "%s", "format_id": "101"}\n' "$id"
	printf 'video' > "$dir/Instagram-$id.mp4"
	printf '%s\n' "$dir/Instagram-$id.mp4"
done
`, nil)
	if result.err != nil {
		t.Fatalf("Download() error = %v", result.err)
	}
	defer result.file.Cleanup()

	if len(result.file.Album) != 3 {
		t.Fatalf("got %d album items, want 3: %#v", len(result.file.Album), result.file)
	}
	for index, item := range result.file.Album {
		if want := fmt.Sprintf("Instagram-VideoItem0%d.mp4", index+1); !item.Video || filepath.Base(item.Path) != want {
			t.Errorf("album item %d = %#v, want video %s", index, item, want)
		}
	}
}

func TestDownloaderKeepsSingleInstagramVideoCompressible(t *testing.T) {
	result := downloadInstagramWithFakeYTDLP(t, `
printf '%s\n' '{"id": "ReelCode01", "format_id": "dash-1+dash-2"}'
printf 'video' > "$dir/Instagram-ReelCode01.mp4"
printf '%s\n' "$dir/Instagram-ReelCode01.mp4"
`, nil)
	if result.err != nil {
		t.Fatalf("Download() error = %v", result.err)
	}
	defer result.file.Cleanup()

	if len(result.file.Album) != 0 || filepath.Base(result.file.Path) != "Instagram-ReelCode01.mp4" {
		t.Fatalf("unexpected downloaded media: %#v", result.file)
	}
}

func TestDownloaderRejectsInstagramCarouselWithMissingVideo(t *testing.T) {
	result := downloadInstagramWithFakeYTDLP(t, `
printf '%s\n' '{"id": "PhotoItem01", "thumbnails": [{"url": "https://scontent.cdninstagram.com/photo.jpg"}]}'
printf '%s\n' '{"id": "VideoItem02", "format_id": "101"}'
printf '%s\n' 'ERROR: [Instagram] VideoItem02: HTTP Error 503' >&2
exit 1
`, map[string][]byte{"https://scontent.cdninstagram.com/photo.jpg": instagramTestJPEG()})
	if result.err == nil || !strings.Contains(result.err.Error(), "HTTP Error 503") {
		t.Fatalf("Download() error = %v, want the yt-dlp failure", result.err)
	}
}

func TestDownloaderDoesNotRetryRestrictedInstagramPost(t *testing.T) {
	result := downloadInstagramWithFakeYTDLP(t, `
printf '%s\n' "ERROR: [Instagram] DfC8zcpk26z: This content isn't available to everyone: It can't be seen by certain audiences." >&2
exit 1
`, nil)
	if !errors.Is(result.err, ErrRestricted) {
		t.Fatalf("Download() error = %v, want ErrRestricted", result.err)
	}
	if result.calls != 1 {
		t.Fatalf("yt-dlp ran %d times, want 1", result.calls)
	}
}

type instagramTestResult struct {
	file  *DownloadedFile
	err   error
	calls int
}

// downloadInstagramWithFakeYTDLP runs a fake yt-dlp whose body can use $dir,
// the job directory passed through --paths.
func downloadInstagramWithFakeYTDLP(t *testing.T, body string, media map[string][]byte) instagramTestResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	calls := filepath.Join(workDir, "calls")
	binary := writeHelper(t, workDir, `#!/bin/sh
printf 'call\n' >> '`+calls+`'
dir=""
while [ "$#" -gt 0 ]; do
	if [ "$1" = "--paths" ]; then dir="$2"; fi
	shift
done
`+body)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: binary, WorkDir: workDir, Format: "best", MaxFileSize: 4096,
		SizeCheckGap: time.Millisecond, Attempts: 3, RetryDelay: time.Millisecond,
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
	callLog, _ := os.ReadFile(calls)
	return instagramTestResult{file: file, err: err, calls: strings.Count(string(callLog), "call\n")}
}

func instagramTestJPEG() []byte {
	return append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x11}, 32)...)
}
