package media

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTikTokPlayerPhotoPostLive(t *testing.T) {
	photoURL := os.Getenv("BRSAVER_TIKTOK_PHOTO_LIVE_URL")
	if photoURL == "" {
		t.Skip("live TikTok photo test is disabled")
	}
	downloader := &Downloader{
		maxFileSize:      49 * 1024 * 1024,
		tikTokHTTPClient: newTikTokHTTPClient(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	photos, err := downloader.downloadTikTokPlayerPhotos(ctx, photoURL, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) == 0 {
		t.Fatal("TikTok player returned no photos")
	}
	for _, path := range photos {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("downloaded %s (%d bytes)", path, info.Size())
	}
}
