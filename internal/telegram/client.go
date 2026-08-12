package telegram

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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxAPIResponseSize = 2 * 1024 * 1024

const maxMediaGroupSize = 10

type ClientConfig struct {
	Token         string
	APIBaseURL    string
	PollTimeout   time.Duration
	UploadTimeout time.Duration
}

type Client struct {
	endpoint      string
	requestClient *http.Client
	uploadClient  *http.Client
	pollSeconds   int
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

type APIError struct {
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram API error %d: %s", e.Code, e.Description)
}

func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("Telegram token is required")
	}
	if !strings.Contains(cfg.Token, ":") || strings.ContainsAny(cfg.Token, "/?# \t\r\n") {
		return nil, errors.New("Telegram token has an invalid format")
	}
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		return nil, errors.New("Telegram API base URL is required")
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 50 * time.Second
	}
	if cfg.UploadTimeout <= 0 {
		cfg.UploadTimeout = 3 * time.Minute
	}
	return &Client{
		endpoint:      strings.TrimRight(cfg.APIBaseURL, "/") + "/bot" + cfg.Token + "/",
		requestClient: &http.Client{Timeout: cfg.PollTimeout + 15*time.Second},
		uploadClient:  &http.Client{Timeout: cfg.UploadTimeout},
		pollSeconds:   int(cfg.PollTimeout / time.Second),
	}, nil
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.doJSON(ctx, "getMe", struct{}{}, &user)
	return user, err
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.doJSON(ctx, "deleteWebhook", map[string]bool{"drop_pending_updates": false}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	payload := struct {
		Offset         int64    `json:"offset"`
		Limit          int      `json:"limit"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{
		Offset: offset, Limit: 100, Timeout: c.pollSeconds, AllowedUpdates: []string{"message"},
	}
	var updates []Update
	err := c.doJSON(ctx, "getUpdates", payload, &updates)
	return updates, err
}

func (c *Client) SendText(ctx context.Context, chatID int64, messageID, threadID int, text string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
		"reply_parameters": map[string]any{
			"message_id":                  messageID,
			"allow_sending_without_reply": true,
		},
	}
	if threadID != 0 {
		payload["message_thread_id"] = threadID
	}
	return c.doJSON(ctx, "sendMessage", payload, nil)
}

func (c *Client) SendVideo(ctx context.Context, chatID int64, messageID, threadID int, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open video for upload: %w", err)
	}
	defer file.Close()

	return c.doMultipart(ctx, "sendVideo", func(writer *multipart.Writer) error {
		return writeVideoForm(writer, file, chatID, messageID, threadID, filepath.Base(path))
	})
}

func (c *Client) SendAudio(ctx context.Context, chatID int64, messageID, threadID int, path, title, performer string, duration int) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open audio for upload: %w", err)
	}
	defer file.Close()

	return c.doMultipart(ctx, "sendAudio", func(writer *multipart.Writer) error {
		return writeAudioForm(writer, file, chatID, messageID, threadID, filepath.Base(path), title, performer, duration)
	})
}

func (c *Client) SendPhotos(ctx context.Context, chatID int64, messageID, threadID int, paths []string) error {
	if len(paths) == 0 {
		return errors.New("at least one photo is required")
	}
	for start := 0; start < len(paths); start += maxMediaGroupSize {
		end := min(start+maxMediaGroupSize, len(paths))
		if err := c.sendPhotoBatch(ctx, chatID, messageID, threadID, paths[start:end]); err != nil {
			return fmt.Errorf("send photo batch %d: %w", start/maxMediaGroupSize+1, err)
		}
	}
	return nil
}

func (c *Client) sendPhotoBatch(ctx context.Context, chatID int64, messageID, threadID int, paths []string) error {
	files := make([]*os.File, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			for _, opened := range files {
				_ = opened.Close()
			}
			return fmt.Errorf("open photo for upload: %w", err)
		}
		files = append(files, file)
	}
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()

	method := "sendMediaGroup"
	if len(files) == 1 {
		method = "sendPhoto"
	}
	return c.doMultipart(ctx, method, func(writer *multipart.Writer) error {
		return writePhotoForm(writer, files, chatID, messageID, threadID)
	})
}

func writeVideoForm(writer *multipart.Writer, file *os.File, chatID int64, messageID, threadID int, filename string) error {
	if err := writeUploadFields(writer, chatID, messageID, threadID); err != nil {
		return err
	}
	if err := writer.WriteField("supports_streaming", "true"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("video", filename)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	return err
}

func writeAudioForm(writer *multipart.Writer, file *os.File, chatID int64, messageID, threadID int, filename, title, performer string, duration int) error {
	if err := writeUploadFields(writer, chatID, messageID, threadID); err != nil {
		return err
	}
	fields := map[string]string{
		"title":     truncateRunes(strings.TrimSpace(title), 64),
		"performer": truncateRunes(strings.TrimSpace(performer), 64),
	}
	if duration > 0 {
		fields["duration"] = strconv.Itoa(duration)
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return err
			}
		}
	}
	part, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	return err
}

func truncateRunes(value string, limit int) string {
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit])
}

func writePhotoForm(writer *multipart.Writer, files []*os.File, chatID int64, messageID, threadID int) error {
	if err := writeUploadFields(writer, chatID, messageID, threadID); err != nil {
		return err
	}
	if len(files) == 1 {
		part, err := writer.CreateFormFile("photo", filepath.Base(files[0].Name()))
		if err != nil {
			return err
		}
		_, err = io.Copy(part, files[0])
		return err
	}

	media := make([]map[string]string, 0, len(files))
	for index := range files {
		media = append(media, map[string]string{
			"type":  "photo",
			"media": "attach://photo" + strconv.Itoa(index),
		})
	}
	encodedMedia, err := json.Marshal(media)
	if err != nil {
		return err
	}
	if err := writer.WriteField("media", string(encodedMedia)); err != nil {
		return err
	}
	for index, file := range files {
		part, err := writer.CreateFormFile("photo"+strconv.Itoa(index), filepath.Base(file.Name()))
		if err != nil {
			return err
		}
		if _, err := io.Copy(part, file); err != nil {
			return err
		}
	}
	return nil
}

func writeUploadFields(writer *multipart.Writer, chatID int64, messageID, threadID int) error {
	reply, err := json.Marshal(map[string]any{
		"message_id":                  messageID,
		"allow_sending_without_reply": true,
	})
	if err != nil {
		return err
	}
	fields := map[string]string{
		"chat_id":          strconv.FormatInt(chatID, 10),
		"reply_parameters": string(reply),
	}
	if threadID != 0 {
		fields["message_thread_id"] = strconv.Itoa(threadID)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) doMultipart(ctx context.Context, method string, build func(*multipart.Writer) error) error {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	writeResult := make(chan error, 1)
	go func() {
		writeErr := build(multipartWriter)
		if closeErr := multipartWriter.Close(); writeErr == nil {
			writeErr = closeErr
		}
		_ = writer.CloseWithError(writeErr)
		writeResult <- writeErr
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return fmt.Errorf("create %s upload request: %w", method, err)
	}
	req.Header.Set("Content-Type", contentType)
	response, err := c.uploadClient.Do(req)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeResult
		return safeHTTPError("upload media with "+method, err)
	}
	writeErr := <-writeResult
	if writeErr != nil {
		_ = response.Body.Close()
		return fmt.Errorf("build %s upload: %w", method, writeErr)
	}
	defer response.Body.Close()
	return decodeResponse(response, nil)
}

func (c *Client) doJSON(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.requestClient.Do(req)
	if err != nil {
		return safeHTTPError("call Telegram "+method, err)
	}
	defer response.Body.Close()
	return decodeResponse(response, result)
}

// safeHTTPError removes the request URL because it contains the bot token.
func safeHTTPError(operation string, err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return fmt.Errorf("%s: %w", operation, urlError.Err)
	}
	return fmt.Errorf("%s: request failed", operation)
}

func decodeResponse(response *http.Response, result any) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseSize+1))
	if err != nil {
		return fmt.Errorf("read Telegram response: %w", err)
	}
	if len(body) > maxAPIResponseSize {
		return errors.New("Telegram response is too large")
	}
	var envelope apiResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Telegram response (HTTP %d): %w", response.StatusCode, err)
	}
	if !envelope.OK {
		return &APIError{Code: envelope.ErrorCode, Description: envelope.Description}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("unexpected Telegram HTTP status %d", response.StatusCode)
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram result: %w", err)
		}
	}
	return nil
}
