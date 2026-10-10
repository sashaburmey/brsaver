package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloaderFallsBackToOfficialTikTokPlayer(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, `#!/bin/sh
printf '%s\n' 'ERROR: [TikTok] 7670437694874373408: Unexpected response from webpage request' >&2
exit 1
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	mediaBody := append([]byte{0, 0, 0, 24}, []byte("ftypisom-valid-mp4")...)
	requestCount := 0
	downloader.tikTokHTTPClient = &http.Client{Transport: tikTokRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		switch request.URL.Hostname() {
		case "www.tiktok.com":
			if request.URL.Path != "/player/api/v1/items" || request.URL.Query().Get("item_ids") != "7670437694874373408" {
				t.Errorf("unexpected TikTok player request: %s", request.URL)
			}
			if request.Header.Get("User-Agent") == "" || request.Header.Get("Referer") == "" {
				t.Error("TikTok player request is missing browser headers")
			}
			body := `{"status_code":0,"items":[{"id_str":"7670437694874373408","video_info":{"profiles":[` +
				`{"codec_type":"h265","bitrate":3000,"play_addr":{"data_size":20,"height":1920,"width":1080,"url_list":["https://v16-webapp-prime.tiktok.com/h265"]}},` +
				`{"codec_type":"h264","bitrate":2000,"play_addr":{"data_size":24,"height":1024,"width":576,"url_list":["https://v19-webapp-prime.tiktok.com/video.mp4"]}}` +
				`]}}]}`
			return tikTokHTTPResponse(request, http.StatusOK, []byte(body)), nil
		case "v19-webapp-prime.tiktok.com":
			if request.Header.Get("Referer") != "https://www.tiktok.com/player/v1/7670437694874373408" {
				t.Errorf("media Referer = %q", request.Header.Get("Referer"))
			}
			return tikTokHTTPResponse(request, http.StatusOK, mediaBody), nil
		default:
			return nil, fmt.Errorf("unexpected host %q", request.URL.Hostname())
		}
	})}

	media, err := downloader.Download(context.Background(), "https://vt.tiktok.com/ZS4vQwFmQ/")
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want 2", requestCount)
	}
	if filepath.Ext(media.Path) != ".mp4" {
		t.Fatalf("media path = %q", media.Path)
	}
	if body, err := os.ReadFile(media.Path); err != nil || !bytes.Equal(body, mediaBody) {
		t.Fatalf("downloaded media = %q, %v", body, err)
	}
	jobDir := filepath.Dir(media.Path)
	if err := media.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("fallback job directory still exists: %v", err)
	}
}

func TestDownloaderFallsBackToOfficialTikTokPlayerPhotos(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, `#!/bin/sh
printf '%s\n' 'ERROR: Unsupported URL: https://www.tiktok.com/@creator/photo/7672651988764495122?_r=1' >&2
exit 1
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, Format: "best", MaxFileSize: 4096,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	firstPhoto := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x11}, 32)...)
	secondPhoto := append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{0x22}, 32)...)
	requestCount := 0
	downloader.tikTokHTTPClient = &http.Client{Transport: tikTokRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		switch request.URL.Hostname() {
		case "www.tiktok.com":
			if request.URL.Query().Get("item_ids") != "7672651988764495122" {
				t.Errorf("unexpected TikTok photo player request: %s", request.URL)
			}
			body := `{"status_code":0,"items":[{"id_str":"7672651988764495122","image_post_info":{"images":[` +
				`{"display_image":{"url_list":["https://attacker.example/first.jpg","https://p16-common-sign.tiktokcdn.com/first.jpg"]}},` +
				`{"display_image":{"url_list":["https://p19-common-sign.tiktokcdn.com/second.jpg"]}}` +
				`]}}]}`
			return tikTokHTTPResponse(request, http.StatusOK, []byte(body)), nil
		case "p16-common-sign.tiktokcdn.com":
			if request.Header.Get("Referer") != "https://www.tiktok.com/player/v1/7672651988764495122" {
				t.Errorf("photo Referer = %q", request.Header.Get("Referer"))
			}
			return tikTokHTTPResponse(request, http.StatusOK, firstPhoto), nil
		case "p19-common-sign.tiktokcdn.com":
			return tikTokHTTPResponse(request, http.StatusOK, secondPhoto), nil
		default:
			return nil, fmt.Errorf("unexpected host %q", request.URL.Hostname())
		}
	})}

	media, err := downloader.Download(context.Background(), "https://vt.tiktok.com/example/")
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 3 {
		t.Fatalf("request count = %d, want 3", requestCount)
	}
	if len(media.Album) != 2 {
		t.Fatalf("photo count = %d, want 2", len(media.Album))
	}
	for index, want := range [][]byte{firstPhoto, secondPhoto} {
		if filepath.Ext(media.Album[index].Path) != ".jpg" || media.Album[index].Video {
			t.Errorf("photo item = %#v", media.Album[index])
		}
		if body, readErr := os.ReadFile(media.Album[index].Path); readErr != nil || !bytes.Equal(body, want) {
			t.Errorf("downloaded photo %d = %q, %v", index+1, body, readErr)
		}
	}
	jobDir := filepath.Dir(media.Album[0].Path)
	if err := media.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("fallback job directory still exists: %v", err)
	}
}

func TestTikTokPlayerRejectsUntrustedMediaHost(t *testing.T) {
	t.Parallel()

	workDir := t.TempDir()
	helper := writeHelper(t, workDir, `#!/bin/sh
printf '%s\n' 'ERROR: [TikTok] 7670437694874373408: Unexpected response from webpage request' >&2
exit 1
`)
	downloader, err := NewDownloader(DownloaderConfig{
		Binary: helper, WorkDir: workDir, Format: "best", MaxFileSize: 1024,
		SizeCheckGap: time.Millisecond, Attempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestCount := 0
	downloader.tikTokHTTPClient = &http.Client{Transport: tikTokRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		body := `{"status_code":0,"items":[{"id_str":"7670437694874373408","video_info":{"profiles":[` +
			`{"codec_type":"h264","play_addr":{"data_size":24,"height":1024,"width":576,"url_list":["https://attacker.example/video.mp4"]}}` +
			`]}}]}`
		return tikTokHTTPResponse(request, http.StatusOK, []byte(body)), nil
	})}

	_, err = downloader.Download(context.Background(), "https://vt.tiktok.com/ZS4vQwFmQ/")
	if err == nil || !strings.Contains(err.Error(), "compatible H.264") {
		t.Fatalf("Download() error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, untrusted URL was requested", requestCount)
	}
}

func TestTikTokPlayerHonorsDeclaredSizeLimit(t *testing.T) {
	t.Parallel()

	downloader := &Downloader{maxFileSize: 10, tikTokHTTPClient: &http.Client{Transport: tikTokRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"status_code":0,"items":[{"id_str":"7670437694874373408","video_info":{"profiles":[` +
			`{"codec_type":"h264","play_addr":{"data_size":11,"height":1024,"width":576,"url_list":["https://v19-webapp-prime.tiktok.com/video.mp4"]}}` +
			`]}}]}`
		return tikTokHTTPResponse(request, http.StatusOK, []byte(body)), nil
	})}}

	_, err := downloader.fetchTikTokPlayerProfiles(context.Background(), "7670437694874373408")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("fetchTikTokPlayerProfiles() error = %v", err)
	}
}

func TestTikTokVideoIDFromURL(t *testing.T) {
	t.Parallel()

	if got := tikTokVideoIDFromURL("https://www.tiktok.com/@creator/video/7670437694874373408?x=1"); got != "7670437694874373408" {
		t.Fatalf("tikTokVideoIDFromURL() = %q", got)
	}
	for _, rawURL := range []string{
		"https://attacker.example/@creator/video/7670437694874373408",
		"http://www.tiktok.com/@creator/video/7670437694874373408",
		"https://www.tiktok.com/@creator/video/not-a-number",
	} {
		if got := tikTokVideoIDFromURL(rawURL); got != "" {
			t.Errorf("tikTokVideoIDFromURL(%q) = %q", rawURL, got)
		}
	}
}

func TestTikTokPhotoIDFromURL(t *testing.T) {
	t.Parallel()

	if got := tikTokPhotoIDFromURL("https://www.tiktok.com/@creator/photo/7672651988764495122?_r=1"); got != "7672651988764495122" {
		t.Fatalf("tikTokPhotoIDFromURL() = %q", got)
	}
	for _, rawURL := range []string{
		"https://attacker.example/@creator/photo/7672651988764495122",
		"http://www.tiktok.com/@creator/photo/7672651988764495122",
		"https://www.tiktok.com/@creator/video/7672651988764495122",
	} {
		if got := tikTokPhotoIDFromURL(rawURL); got != "" {
			t.Errorf("tikTokPhotoIDFromURL(%q) = %q", rawURL, got)
		}
	}
}

type tikTokRoundTripFunc func(*http.Request) (*http.Response, error)

func (function tikTokRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func tikTokHTTPResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}
