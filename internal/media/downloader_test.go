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
