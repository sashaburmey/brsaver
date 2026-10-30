package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

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
	application := New(client, photoMediaDownloader{}, rejectingTranscoder{}, nil, Config{
		DownloadTimeout: time.Second, UploadTimeout: time.Second, CompressionTimeout: time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15, threadID: 9,
		link: media.Link{URL: "https://vt.tiktok.com/example/", Provider: "TikTok"},
	})

	if len(client.album) != 2 || client.chatID != -1001 || client.messageID != 15 || client.threadID != 9 {
		t.Fatalf("unexpected photo upload: %#v", client)
	}
	// The download finishes at once, so the status may already show photos.
	if len(client.actions) == 0 {
		t.Fatal("no chat action was sent")
	}
	for _, action := range client.actions {
		if action != chatActionUploadVideo && action != chatActionUploadPhoto {
			t.Fatalf("unexpected chat action %q", action)
		}
	}
}

func TestHandleEndsChatStatusBeforeErrorReply(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, failingMediaDownloader{}, rejectingTranscoder{}, nil, Config{
		DownloadTimeout: time.Second, UploadTimeout: time.Second, CompressionTimeout: time.Second, NotifyErrors: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15,
		link: media.Link{URL: "https://www.instagram.com/reel/Example01/", Provider: "Instagram"},
	})

	if len(client.events) == 0 || client.events[len(client.events)-1] != "text:"+publicDownloadError {
		t.Fatalf("events = %v, want the error reply last", client.events)
	}
	if client.events[0] != "action:"+chatActionUploadVideo {
		t.Fatalf("events = %v, want the upload status first", client.events)
	}
}

func TestHandleSendsDownloadedAudioWithoutTranscoding(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, audioMediaDownloader{}, rejectingTranscoder{}, nil, Config{
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
	if len(client.actions) == 0 || client.actions[0] != chatActionUploadDocument {
		t.Fatalf("chat actions = %v, want %q first", client.actions, chatActionUploadDocument)
	}
}

func TestHandleSkipsPostsWithoutMediaSilently(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, noMediaDownloader{}, rejectingTranscoder{}, nil, Config{
		DownloadTimeout: time.Second, UploadTimeout: time.Second, CompressionTimeout: time.Second, NotifyErrors: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15,
		link: media.Link{URL: "https://www.threads.com/t/TextPost01", Provider: "Threads"},
	})

	if len(client.texts) != 0 || len(client.album) != 0 {
		t.Fatalf("unexpected reply to a post without media: %#v", client)
	}
}

func TestJobsForUpdateQueuesVoiceOnlyWithTranscriber(t *testing.T) {
	t.Parallel()

	update := telegram.Update{Message: &telegram.Message{
		MessageID: 15, MessageThreadID: 9,
		Chat:  telegram.Chat{ID: -1001, Type: "supergroup"},
		Voice: &telegram.Voice{FileID: "voice-id", Duration: 12, FileSize: 4096},
	}}

	application := &Bot{config: Config{MaxLinksPerMessage: 3}}
	if jobs := application.jobsForUpdate(update); len(jobs) != 0 {
		t.Fatalf("got voice jobs without a transcriber: %#v", jobs)
	}

	application.transcriber = &fakeTranscriber{}
	jobs := application.jobsForUpdate(update)
	if len(jobs) != 1 || jobs[0].voice == nil || jobs[0].voice.fileID != "voice-id" ||
		jobs[0].voice.kind != "voice" || jobs[0].threadID != 9 {
		t.Fatalf("unexpected voice jobs: %#v", jobs)
	}

	update.Message.Voice = nil
	update.Message.VideoNote = &telegram.VideoNote{FileID: "note-id", Duration: 30}
	jobs = application.jobsForUpdate(update)
	if len(jobs) != 1 || jobs[0].voice == nil || jobs[0].voice.kind != "video_note" {
		t.Fatalf("unexpected video note jobs: %#v", jobs)
	}
}

func TestHandleRepliesWithTranscript(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{file: []byte("voice-data"), fileName: "file_7.oga"}
	transcriber := &fakeTranscriber{text: "Привіт, як справи?"}
	application := New(client, photoMediaDownloader{}, rejectingTranscoder{}, transcriber, Config{
		TranscriptionTimeout: time.Second, MaxVoiceDuration: time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15, threadID: 9,
		voice: &voiceMessage{kind: "voice", fileID: "voice-id", duration: 12},
	})

	if client.fileID != "voice-id" || transcriber.filename != "file_7.oga" || string(transcriber.audio) != "voice-data" {
		t.Fatalf("unexpected transcription input: client=%#v transcriber=%#v", client, transcriber)
	}
	if len(client.texts) != 1 || client.texts[0] != "Привіт, як справи?" {
		t.Fatalf("unexpected replies: %#v", client.texts)
	}
}

func TestHandleSkipsTooLongVoice(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{}
	application := New(client, photoMediaDownloader{}, rejectingTranscoder{}, &fakeTranscriber{}, Config{
		TranscriptionTimeout: time.Second, MaxVoiceDuration: time.Minute, NotifyErrors: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15,
		voice: &voiceMessage{kind: "voice", fileID: "voice-id", duration: 61},
	})

	if client.fileID != "" || len(client.texts) != 0 {
		t.Fatalf("too long voice was processed: %#v", client)
	}
}

func TestHandleReportsTranscriptionFailure(t *testing.T) {
	t.Parallel()

	client := &photoTelegramClient{file: []byte("voice-data"), fileName: "file_7.oga"}
	application := New(client, photoMediaDownloader{}, rejectingTranscoder{}, &fakeTranscriber{err: errors.New("rate limit")}, Config{
		TranscriptionTimeout: time.Second, NotifyErrors: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	application.handle(context.Background(), job{
		chatID: -1001, messageID: 15,
		voice: &voiceMessage{kind: "voice", fileID: "voice-id", duration: 5},
	})

	if len(client.texts) != 1 || client.texts[0] != publicTranscriptionError {
		t.Fatalf("unexpected replies: %#v", client.texts)
	}
	if len(client.events) != 2 || client.events[0] != "action:"+chatActionTyping {
		t.Fatalf("events = %v, want one typing status before the reply", client.events)
	}
}

func TestSplitTextKeepsWordsAndLimit(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("слово ", 30)
	parts := splitText(strings.TrimSpace(text), 20)
	if strings.Join(parts, " ") != strings.TrimSpace(text) {
		t.Fatalf("parts do not rebuild the text: %#v", parts)
	}
	for _, part := range parts {
		if utf8.RuneCountInString(part) > 20 || strings.HasPrefix(part, " ") || strings.HasSuffix(part, " ") {
			t.Fatalf("bad part %q in %#v", part, parts)
		}
	}
	if parts := splitText(strings.Repeat("я", 45), 20); len(parts) != 3 || parts[2] != "яяяяя" {
		t.Fatalf("unexpected split of text without spaces: %#v", parts)
	}
}

type photoTelegramClient struct {
	mu                          sync.Mutex
	actions                     []string
	events                      []string
	texts                       []string
	fileID                      string
	file                        []byte
	fileName                    string
	chatID, messageID, threadID int64
	album                       []telegram.AlbumItem
	audioPath                   string
	audioTitle                  string
	audioPerformer              string
	audioDuration               int
}

func (c *photoTelegramClient) GetMe(context.Context) (telegram.User, error) {
	return telegram.User{}, nil
}

func (c *photoTelegramClient) DeleteWebhook(context.Context) error { return nil }

func (c *photoTelegramClient) SendChatAction(_ context.Context, _ int64, _ int, action string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actions = append(c.actions, action)
	c.events = append(c.events, "action:"+action)
	return nil
}

func (c *photoTelegramClient) GetUpdates(context.Context, int64) ([]telegram.Update, error) {
	return nil, nil
}

func (c *photoTelegramClient) SendText(_ context.Context, _ int64, _, _ int, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = append(c.texts, text)
	c.events = append(c.events, "text:"+text)
	return nil
}

func (c *photoTelegramClient) DownloadFile(_ context.Context, fileID string, _ int64) ([]byte, string, error) {
	c.fileID = fileID
	return c.file, c.fileName, nil
}

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

func (c *photoTelegramClient) SendAlbum(_ context.Context, chatID int64, messageID, threadID int, items []telegram.AlbumItem) error {
	c.chatID = chatID
	c.messageID = int64(messageID)
	c.threadID = int64(threadID)
	c.album = append([]telegram.AlbumItem(nil), items...)
	return nil
}

type photoMediaDownloader struct{}

func (photoMediaDownloader) Download(context.Context, string) (*media.DownloadedFile, error) {
	return &media.DownloadedFile{Album: []telegram.AlbumItem{{Path: "one.jpg"}, {Path: "two.jpg"}}}, nil
}

type noMediaDownloader struct{}

type failingMediaDownloader struct{}

func (failingMediaDownloader) Download(context.Context, string) (*media.DownloadedFile, error) {
	return nil, errors.New("source unavailable")
}

func (noMediaDownloader) Download(context.Context, string) (*media.DownloadedFile, error) {
	return nil, fmt.Errorf("download failed after 1 attempts: %w", media.ErrNoMedia)
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

type fakeTranscriber struct {
	text     string
	err      error
	filename string
	audio    []byte
}

func (t *fakeTranscriber) Transcribe(_ context.Context, filename string, audio []byte) (string, error) {
	t.filename = filename
	t.audio = audio
	return t.text, t.err
}

type rejectingTranscoder struct{}

func (rejectingTranscoder) Compress(context.Context, *media.DownloadedFile) (media.CompressionResult, error) {
	return media.CompressionResult{}, errors.New("unexpected transcode")
}
