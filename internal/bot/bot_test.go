package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"brsaver/internal/media"
	"brsaver/internal/telegram"
)

func TestJobsForUpdateFiltersChatsAndLimitsLinks(t *testing.T) {
	t.Parallel()

	application := &Bot{config: Config{
		MaxLinksPerMessage: 1,
		AllowedChatIDs:     map[int64]struct{}{-1001: {}},
	}}
	update := telegram.Update{Message: &telegram.Message{
		MessageID: 15,
		Chat:      telegram.Chat{ID: -1001, Type: "supergroup"},
		Text:      "https://vm.tiktok.com/one/ https://youtube.com/shorts/two",
	}}

	jobs := application.jobsForUpdate(update)
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].chatID != -1001 || jobs[0].messageID != 15 || jobs[0].link.Provider != "TikTok" {
		t.Fatalf("unexpected job: %#v", jobs[0])
	}

	update.Message.Chat.ID = -1002
	if jobs := application.jobsForUpdate(update); len(jobs) != 0 {
		t.Fatalf("got jobs from disallowed chat: %#v", jobs)
	}
	update.Message.Chat = telegram.Chat{ID: -1001, Type: "private"}
	if jobs := application.jobsForUpdate(update); len(jobs) != 0 {
		t.Fatalf("got jobs from private chat: %#v", jobs)
	}
}

func TestHandleSendsDownloadedPhotosWithoutTranscoding(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, photoMediaDownloader{}, rejectingTranscoder{}, Config{
		DownloadTimeout: time.Second, UploadTimeout: time.Second, CompressionTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15, threadID: 9,
		link: media.Link{URL: "https://vt.tiktok.com/example/", Provider: "TikTok"},
	})

	if len(client.photos) != 2 || client.chatID != -1001 || client.messageID != 15 || client.threadID != 9 {
		t.Fatalf("unexpected photo upload: %#v", client)
	}
}

func TestHandleSendsDownloadedAudioWithoutTranscoding(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, audioMediaDownloader{}, rejectingTranscoder{}, Config{
		DownloadTimeout: time.Second, UploadTimeout: time.Second, CompressionTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15, threadID: 9,
		link: media.Link{URL: "https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl", Provider: "Spotify"},
	})

	if client.audioPath != "track.mp3" || client.audioTitle != "Cut to the Feeling" ||
		client.audioPerformer != "Carly Rae Jepsen" || client.audioDuration != 208 {
		t.Fatalf("unexpected audio upload: %#v", client)
	}
}

type photoTelegramClient struct {
	chatID, messageID, threadID int64
	photos                      []string
	audioPath                   string
	audioTitle                  string
	audioPerformer              string
	audioDuration               int
}

func (c *photoTelegramClient) GetMe(context.Context) (telegram.User, error) {
	return telegram.User{}, nil
}

func (c *photoTelegramClient) DeleteWebhook(context.Context) error { return nil }

func (c *photoTelegramClient) GetUpdates(context.Context, int64) ([]telegram.Update, error) {
	return nil, nil
}

func (c *photoTelegramClient) SendText(context.Context, int64, int, int, string) error { return nil }

func (c *photoTelegramClient) SendVideo(context.Context, int64, int, int, string) error {
	return errors.New("unexpected video upload")
}

func (c *photoTelegramClient) SendAudio(_ context.Context, _ int64, _, _ int, path, title, performer string, duration int) error {
	c.audioPath = path
	c.audioTitle = title
	c.audioPerformer = performer
	c.audioDuration = duration
	return nil
}

func (c *photoTelegramClient) SendPhotos(_ context.Context, chatID int64, messageID, threadID int, paths []string) error {
	c.chatID = chatID
	c.messageID = int64(messageID)
	c.threadID = int64(threadID)
	c.photos = append([]string(nil), paths...)
	return nil
}

type photoMediaDownloader struct{}

func (photoMediaDownloader) Download(context.Context, string) (*media.DownloadedFile, error) {
	return &media.DownloadedFile{Photos: []string{"one.jpg", "two.jpg"}}, nil
}

type audioMediaDownloader struct{}

func (audioMediaDownloader) Download(context.Context, string) (*media.DownloadedFile, error) {
	return &media.DownloadedFile{
		Path: "track.mp3",
		Audio: &media.AudioInfo{
			Title: "Cut to the Feeling", Performer: "Carly Rae Jepsen", Duration: 208, MatchScore: 105,
		},
	}, nil
}

type rejectingTranscoder struct{}

func (rejectingTranscoder) Compress(context.Context, *media.DownloadedFile) (media.CompressionResult, error) {
	return media.CompressionResult{}, errors.New("unexpected transcode")
}
