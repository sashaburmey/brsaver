package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const maxResponseSize = 1024 * 1024

type Config struct {
	APIBaseURL string
	APIKey     string
	Model      string
	// Language is an ISO-639-1 code such as "uk"; empty enables auto-detection.
	Language string
	Prompt   string
	Timeout  time.Duration
}

// Client talks to an OpenAI-compatible /audio/transcriptions endpoint, which
// Groq, OpenAI, and self-hosted Whisper servers expose.
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	language   string
	prompt     string
	httpClient *http.Client
}

type errorResponse struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("transcription API key is required")
	}
	if strings.TrimSpace(cfg.APIBaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("transcription API base URL and model are required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	return &Client{
		endpoint:   strings.TrimRight(cfg.APIBaseURL, "/") + "/audio/transcriptions",
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		language:   cfg.Language,
		prompt:     cfg.Prompt,
		httpClient: &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Transcribe returns the recognized text, or an empty string when the audio
// contains no recognizable speech.
func (c *Client) Transcribe(ctx context.Context, filename string, audio []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"model":           c.model,
		"response_format": "json",
		"temperature":     "0",
		"language":        c.language,
		"prompt":          c.prompt,
	}
	for name, value := range fields {
		if value == "" {
			continue
		}
		if err := writer.WriteField(name, value); err != nil {
			return "", err
		}
	}
	part, err := writer.CreateFormFile("file", uploadName(filename))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("create transcription request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.httpClient.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return "", fmt.Errorf("call transcription API: %w", err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}
	if len(data) > maxResponseSize {
		return "", errors.New("transcription response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError errorResponse
		if json.Unmarshal(data, &apiError) == nil && apiError.Error.Message != "" {
			return "", fmt.Errorf("transcription API error %d: %s", response.StatusCode, apiError.Error.Message)
		}
		return "", fmt.Errorf("unexpected transcription HTTP status %d", response.StatusCode)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("decode transcription response: %w", err)
	}
	return strings.TrimSpace(result.Text), nil
}

// uploadName maps Telegram's .oga voice extension to .ogg because
// transcription APIs detect the format by extension and reject .oga.
func uploadName(filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".oga", ".opus":
		return strings.TrimSuffix(filename, filepath.Ext(filename)) + ".ogg"
	case "":
		return "audio.ogg"
	}
	return filename
}
