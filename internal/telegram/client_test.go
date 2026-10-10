package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientGetUpdates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/getUpdates" {
			t.Errorf("path = %q", request.URL.Path)
		}
		var payload struct {
			Offset int64 `json:"offset"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload.Offset != 42 {
			t.Errorf("offset = %d, want 42", payload.Offset)
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":[{"update_id":42,"message":{"message_id":7,"chat":{"id":-1001,"type":"supergroup"},"text":"hello"}}]}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL)
	updates, err := client.GetUpdates(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Chat.ID != -1001 {
		t.Fatalf("unexpected updates: %#v", updates)
	}
}

func TestClientSendVideoAsMultipart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendVideo" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		if got := request.FormValue("chat_id"); got != "-1001" {
			t.Errorf("chat_id = %q", got)
		}
		if got := request.FormValue("message_thread_id"); got != "9" {
			t.Errorf("message_thread_id = %q", got)
		}
		file, _, err := request.FormFile("video")
		if err != nil {
			t.Errorf("video field: %v", err)
		} else {
			defer file.Close()
			body, _ := io.ReadAll(file)
			if string(body) != "video-data" {
				t.Errorf("video body = %q", body)
			}
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":8}}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, []byte("video-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server.URL)
	if err := client.SendVideo(context.Background(), -1001, 7, 9, path); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendAudioAsMultipart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendAudio" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		for name, want := range map[string]string{
			"chat_id": "-1001", "message_thread_id": "9", "title": "Track title",
			"performer": "Track artist", "duration": "208",
		} {
			if got := request.FormValue(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		file, _, err := request.FormFile("audio")
		if err != nil {
			t.Errorf("audio field: %v", err)
		} else {
			body, _ := io.ReadAll(file)
			_ = file.Close()
			if string(body) != "audio-data" {
				t.Errorf("audio body = %q", body)
			}
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":8}}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte("audio-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server.URL)
	if err := client.SendAudio(context.Background(), -1001, 7, 9, path, "Track title", "Track artist", 208); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendPhotosAsMediaGroup(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendMediaGroup" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		var media []struct {
			Type  string `json:"type"`
			Media string `json:"media"`
		}
		if err := json.Unmarshal([]byte(request.FormValue("media")), &media); err != nil {
			t.Errorf("decode media: %v", err)
		}
		if len(media) != 2 || media[0].Type != "photo" || media[0].Media != "attach://photo0" || media[1].Media != "attach://photo1" {
			t.Errorf("unexpected media payload: %#v", media)
		}
		for index, want := range []string{"photo-one", "photo-two"} {
			file, _, err := request.FormFile("photo" + strconv.Itoa(index))
			if err != nil {
				t.Errorf("photo%d field: %v", index, err)
				continue
			}
			body, _ := io.ReadAll(file)
			_ = file.Close()
			if string(body) != want {
				t.Errorf("photo%d body = %q, want %q", index, body, want)
			}
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":[{"message_id":8},{"message_id":9}]}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "one.jpg"), filepath.Join(dir, "two.jpg")}
	for index, body := range []string{"photo-one", "photo-two"} {
		if err := os.WriteFile(paths[index], []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client := testClient(t, server.URL)
	if err := client.SendAlbum(context.Background(), -1001, 7, 9, []AlbumItem{{Path: paths[0]}, {Path: paths[1]}}); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendsSinglePhoto(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendPhoto" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		file, _, err := request.FormFile("photo")
		if err != nil {
			t.Errorf("photo field: %v", err)
		} else {
			body, _ := io.ReadAll(file)
			_ = file.Close()
			if string(body) != "photo-data" {
				t.Errorf("photo body = %q", body)
			}
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":8}}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(path, []byte("photo-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server.URL)
	if err := client.SendAlbum(context.Background(), -1001, 7, 0, []AlbumItem{{Path: path}}); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendsMixedAlbumAsMediaGroup(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendMediaGroup" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		var media []struct {
			Type              string `json:"type"`
			Media             string `json:"media"`
			SupportsStreaming bool   `json:"supports_streaming"`
		}
		if err := json.Unmarshal([]byte(request.FormValue("media")), &media); err != nil {
			t.Errorf("decode media: %v", err)
		}
		if len(media) != 2 || media[0].Type != "photo" || media[0].Media != "attach://photo0" ||
			media[1].Type != "video" || media[1].Media != "attach://video1" || !media[1].SupportsStreaming {
			t.Errorf("unexpected media payload: %#v", media)
		}
		for field, want := range map[string]string{"photo0": "photo-data", "video1": "video-data"} {
			file, _, err := request.FormFile(field)
			if err != nil {
				t.Errorf("%s field: %v", field, err)
				continue
			}
			body, _ := io.ReadAll(file)
			_ = file.Close()
			if string(body) != want {
				t.Errorf("%s body = %q, want %q", field, body, want)
			}
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":[{"message_id":8},{"message_id":9}]}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	photo := filepath.Join(dir, "one.jpg")
	video := filepath.Join(dir, "two.mp4")
	if err := os.WriteFile(photo, []byte("photo-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("video-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server.URL)
	items := []AlbumItem{{Path: photo}, {Path: video, Video: true}}
	if err := client.SendAlbum(context.Background(), -1001, 7, 9, items); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendsSingleAlbumVideoAsVideo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot123:test-token/sendVideo" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		if _, _, err := request.FormFile("video"); err != nil {
			t.Errorf("video field: %v", err)
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":8}}`)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, []byte("video-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, server.URL)
	if err := client.SendAlbum(context.Background(), -1001, 7, 0, []AlbumItem{{Path: path, Video: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestClientDoesNotExposeTokenInNetworkErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := testClient(t, server.URL)
	server.Close()

	_, err := client.GetUpdates(context.Background(), 0)
	if err == nil {
		t.Fatal("GetUpdates() error = nil")
	}
	if strings.Contains(err.Error(), "123:test-token") {
		t.Fatalf("network error leaked bot token: %v", err)
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		Token: "123:test-token", APIBaseURL: baseURL,
		PollTimeout: time.Second, UploadTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
