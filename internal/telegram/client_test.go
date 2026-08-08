package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
