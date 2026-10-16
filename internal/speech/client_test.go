package speech

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribeSendsOpenAICompatibleRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/openai/v1/audio/transcriptions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if err := request.ParseMultipartForm(1024 * 1024); err != nil {
			t.Errorf("parse multipart form: %v", err)
		}
		for name, want := range map[string]string{
			"model": "whisper-large-v3", "language": "uk", "response_format": "json", "prompt": "",
		} {
			if got := request.FormValue(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Errorf("file field: %v", err)
		} else {
			body, _ := io.ReadAll(file)
			_ = file.Close()
			if header.Filename != "file_7.ogg" || string(body) != "voice-data" {
				t.Errorf("file = %q with body %q", header.Filename, body)
			}
		}
		_, _ = io.WriteString(writer, `{"text":"  Привіт, як справи?\n"}`)
	}))
	defer server.Close()

	client := testClient(t, server.URL+"/openai/v1/")
	text, err := client.Transcribe(context.Background(), "file_7.oga", []byte("voice-data"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Привіт, як справи?" {
		t.Fatalf("text = %q", text)
	}
}

func TestTranscribeReportsAPIErrorWithoutKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"Rate limit reached"}}`)
	}))
	defer server.Close()

	_, err := testClient(t, server.URL).Transcribe(context.Background(), "voice.ogg", []byte("voice-data"))
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "Rate limit reached") {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaked API key: %v", err)
	}
}

func TestUploadName(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"file_1.oga": "file_1.ogg", "file_2.OPUS": "file_2.ogg", "file_3.mp4": "file_3.mp4", "": "audio.ogg",
	} {
		if got := uploadName(input); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", input, got, want)
		}
	}
}

func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(Config{
		APIBaseURL: baseURL, APIKey: "test-key", Model: "whisper-large-v3", Language: "uk", Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
