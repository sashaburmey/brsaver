package media

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDownloaderCreatesAndCleansJobDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, `#!/bin/sh
dir=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--paths" ]; then dir="$argument"; fi
  previous="$argument"
done
printf 'video-data' > "$dir/test.mp4"
printf '%s\n' "$dir/test.mp4"
`)
	downloader := newTestDownloader(t, helper, workDir, 1024)

	file, err := downloader.Download(context.Background(), "https://example.test/video")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	jobDir := filepath.Dir(file.Path)
	if _, err := os.Stat(file.Path); err != nil {
		t.Fatalf("downloaded file does not exist: %v", err)
	}
	if err := file.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("job directory still exists after cleanup: %v", err)
	}
	if err := file.Cleanup(); err != nil {
		t.Fatalf("second Cleanup() error = %v", err)
	}
}

func TestDownloaderCleansDirectoryAfterCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, "#!/bin/sh\nexit 7\n")
	downloader := newTestDownloader(t, helper, workDir, 1024)

	if _, err := downloader.Download(context.Background(), "https://example.test/video"); err == nil {
		t.Fatal("Download() error = nil, want command failure")
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "job-") {
			t.Fatalf("failed job directory was not removed: %s", entry.Name())
		}
	}
}

func TestDownloaderRetriesTransientCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, `#!/bin/sh
state="$(dirname "$0")/attempts"
attempt=0
if [ -f "$state" ]; then attempt=$(cat "$state"); fi
attempt=$((attempt + 1))
printf '%s' "$attempt" > "$state"
if [ "$attempt" -lt 2 ]; then exit 7; fi
dir=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--paths" ]; then dir="$argument"; fi
  previous="$argument"
done
printf 'video-data' > "$dir/test.mp4"
printf '%s\n' "$dir/test.mp4"
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 3, RetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	file, err := downloader.Download(context.Background(), "https://example.test/video")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	defer file.Cleanup()
	attempts, err := os.ReadFile(filepath.Join(workDir, "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(attempts) != "2" {
		t.Fatalf("attempt count = %q, want 2", attempts)
	}
}

func TestDownloaderUsesWritablePrivateCookieCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	cookiesFile := filepath.Join(workDir, "source-cookies.txt")
	const originalCookies = "# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tTRUE\t0\tsession\tsecret\n"
	if err := os.WriteFile(cookiesFile, []byte(originalCookies), 0o400); err != nil {
		t.Fatal(err)
	}
	helper := writeHelper(t, workDir, `#!/bin/sh
dir=""
cookies=""
extractor_args=""
impersonate=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--paths" ]; then dir="$argument"; fi
  if [ "$previous" = "--cookies" ]; then cookies="$argument"; fi
  if [ "$previous" = "--extractor-args" ]; then extractor_args="$argument"; fi
  if [ "$previous" = "--impersonate" ]; then impersonate="$argument"; fi
  previous="$argument"
done
[ -n "$cookies" ] || exit 10
[ "$cookies" != "`+cookiesFile+`" ] || exit 11
[ "$extractor_args" = "tiktok:device_id=7301234567890123456" ] || exit 12
[ "$impersonate" = "chrome" ] || exit 14
printf '# refreshed\n' >> "$cookies" || exit 13
printf 'video-data' > "$dir/test.mp4"
printf '%s\n' "$dir/test.mp4"
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, CookiesFile: cookiesFile,
		TikTokDeviceID: "7301234567890123456", Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	media, err := downloader.Download(context.Background(), "https://vt.tiktok.com/example/")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	defer media.Cleanup()
	if body, err := os.ReadFile(cookiesFile); err != nil || string(body) != originalCookies {
		t.Fatalf("source cookies changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(media.Path), "cookies.txt")); !os.IsNotExist(err) {
		t.Fatalf("temporary cookies still exist after download: %v", err)
	}
}

func TestDownloaderUsesSeparateYouTubeCookies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	generalCookies := filepath.Join(workDir, "general-source-cookies.txt")
	youTubeCookies := filepath.Join(workDir, "youtube-source-cookies.txt")
	if err := os.WriteFile(generalCookies, []byte("general-cookie-data"), 0o400); err != nil {
		t.Fatal(err)
	}
	const originalYouTubeCookies = "youtube-cookie-data"
	if err := os.WriteFile(youTubeCookies, []byte(originalYouTubeCookies), 0o400); err != nil {
		t.Fatal(err)
	}
	helper := writeHelper(t, workDir, `#!/bin/sh
dir=""
cookies=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--paths" ]; then dir="$argument"; fi
  if [ "$previous" = "--cookies" ]; then cookies="$argument"; fi
  previous="$argument"
done
[ -n "$cookies" ] || exit 10
[ "$cookies" != "`+youTubeCookies+`" ] || exit 11
[ "$(cat "$cookies")" = "youtube-cookie-data" ] || exit 12
printf '# refreshed\n' >> "$cookies" || exit 13
printf 'video-data' > "$dir/test.mp4"
printf '%s\n' "$dir/test.mp4"
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, CookiesFile: generalCookies,
		YouTubeCookiesFile: youTubeCookies, Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	media, err := downloader.Download(context.Background(), "https://www.youtube.com/shorts/2EG9B92agKY")
	if err != nil {
		t.Fatal(err)
	}
	defer media.Cleanup()
	if body, err := os.ReadFile(youTubeCookies); err != nil || string(body) != originalYouTubeCookies {
		t.Fatalf("source YouTube cookies changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(media.Path), "youtube-cookies.txt")); !os.IsNotExist(err) {
		t.Fatalf("temporary YouTube cookies still exist: %v", err)
	}
}

func TestIsTikTokURL(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"https://www.tiktok.com/@creator/video/123": true,
		"https://vt.tiktok.com/example/":            true,
		"https://TIKTOK.COM./example/":              true,
		"http://www.tiktok.com/example/":            false,
		"https://tiktok.com.example.test/example/":  false,
		"https://example.test/tiktok.com/example/":  false,
	}
	for rawURL, want := range tests {
		if got := isTikTokURL(rawURL); got != want {
			t.Errorf("isTikTokURL(%q) = %t, want %t", rawURL, got, want)
		}
	}
}

func TestIsYouTubeURL(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"https://www.youtube.com/shorts/abc": true,
		"https://youtu.be/abc":               true,
		"http://www.youtube.com/shorts/abc":  false,
		"https://youtube.com.example/abc":    false,
	}
	for rawURL, want := range tests {
		if got := isYouTubeURL(rawURL); got != want {
			t.Errorf("isYouTubeURL(%q) = %t, want %t", rawURL, got, want)
		}
	}
}

func TestDownloaderNeverReturnsTemporaryCookiesAsMedia(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	cookiesFile := filepath.Join(workDir, "source-cookies.txt")
	if err := os.WriteFile(cookiesFile, []byte("cookie-data"), 0o400); err != nil {
		t.Fatal(err)
	}
	helper := writeHelper(t, workDir, "#!/bin/sh\nexit 0\n")
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, CookiesFile: cookiesFile,
		Format: "best", MaxFileSize: 1024, SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	if media, err := downloader.Download(context.Background(), "https://example.test/video"); err == nil {
		_ = media.Cleanup()
		t.Fatal("Download() error = nil, want missing media error")
	}
}

func TestDownloaderFallsBackToTikTokPhotoHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	t.Parallel()

	workDir := t.TempDir()
	downloaderBinary := writeHelper(t, workDir, `#!/bin/sh
printf '%s\n' 'ERROR: Unsupported URL: https://www.tiktok.com/@creator/photo/123456?_r=1' >&2
exit 1
`)
	photoHelper := filepath.Join(workDir, "fake-photo-helper")
	if err := os.WriteFile(photoHelper, []byte(`#!/bin/sh
printf 'photo-data' > "$2/photo-01.jpg"
printf '%s\n' '{"files":["photo-01.jpg"]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: downloaderBinary, WorkDir: workDir, Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 1, PhotoHelper: photoHelper,
	})
	if err != nil {
		t.Fatal(err)
	}

	media, err := downloader.Download(context.Background(), "https://vt.tiktok.com/example/")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if media.Path != "" || len(media.Album) != 1 || media.Album[0].Video {
		t.Fatalf("unexpected downloaded media: %#v", media)
	}
	if body, err := os.ReadFile(media.Album[0].Path); err != nil || string(body) != "photo-data" {
		t.Fatalf("downloaded photo = %q, %v", body, err)
	}
	jobDir := filepath.Dir(media.Album[0].Path)
	if err := media.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("photo job directory still exists after cleanup: %v", err)
	}
}

func TestExtractTikTokPhotoURL(t *testing.T) {
	t.Parallel()

	detail := "ERROR: Unsupported URL: https://www.tiktok.com/@creator/photo/123456?_r=1&_t=test"
	got := extractTikTokPhotoURL(detail)
	want := "https://www.tiktok.com/@creator/photo/123456?_r=1&_t=test"
	if got != want {
		t.Fatalf("extractTikTokPhotoURL() = %q, want %q", got, want)
	}
}

func TestFindOutputPathStaysInsideJobDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	inside := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(dir), "outside.mp4")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	got, err := findOutputPath(dir, outside)
	if err != nil {
		t.Fatal(err)
	}
	if got != inside {
		t.Fatalf("got %q, want in-directory file %q", got, inside)
	}
}

func writeHelper(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-yt-dlp")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestDownloader(t *testing.T, binary, workDir string, maxSize int64) *Downloader {
	t.Helper()
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: binary, WorkDir: workDir, Format: "best",
		MaxFileSize: maxSize, SizeCheckGap: 10 * time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return downloader
}
