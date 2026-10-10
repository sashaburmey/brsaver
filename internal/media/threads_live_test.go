package media

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestThreadsPostLive(t *testing.T) {
	postURL := os.Getenv("BRSAVER_THREADS_LIVE_URL")
	if postURL == "" {
		t.Skip("live Threads test is disabled")
	}
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: "yt-dlp-must-not-run", WorkDir: t.TempDir(), Format: "best",
		MaxFileSize: 250 * 1024 * 1024, MaxUploadSize: 49 * 1024 * 1024, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	file, err := downloader.Download(ctx, postURL)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Cleanup()

	paths := []string{file.Path}
	if file.Path == "" {
		paths = paths[:0]
		for _, item := range file.Album {
			paths = append(paths, item.Path)
		}
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("downloaded %s (%d bytes)", path, info.Size())
	}
}
