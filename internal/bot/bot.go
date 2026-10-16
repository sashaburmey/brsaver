package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"brsaver/internal/media"
	"brsaver/internal/telegram"
)

const (
	publicDownloadError      = "Не вдалося завантажити це медіа. Можливо, воно приватне, недоступне або завелике."
	publicTranscriptionError = "Не вдалося розшифрувати це голосове повідомлення."
)

const (
	// The Bot API only serves files up to 20 MB through getFile.
	maxVoiceFileSize = 20 * 1024 * 1024
	// Telegram allows 4096 characters per message; the margin covers characters
	// that count as two UTF-16 code units.
	maxTextLength = 4000
)

type Config struct {
	Workers            int
	QueueSize          int
	MaxLinksPerMessage int
	DownloadTimeout    time.Duration
	UploadTimeout      time.Duration
	CompressionTimeout time.Duration
	// TranscriptionTimeout covers downloading a voice message and transcribing it.
	TranscriptionTimeout time.Duration
	MaxVoiceDuration     time.Duration
	NotifyErrors         bool
	AllowedChatIDs       map[int64]struct{}
}

type telegramClient interface {
	GetMe(context.Context) (telegram.User, error)
	DeleteWebhook(context.Context) error
	GetUpdates(context.Context, int64) ([]telegram.Update, error)
	SendText(context.Context, int64, int, int, string) error
	SendVideo(context.Context, int64, int, int, string) error
	SendAudio(context.Context, int64, int, int, string, string, string, int) error
	SendAlbum(context.Context, int64, int, int, []telegram.AlbumItem) error
	DownloadFile(context.Context, string, int64) ([]byte, string, error)
}

type mediaDownloader interface {
	Download(context.Context, string) (*media.DownloadedFile, error)
}

type mediaTranscoder interface {
	Compress(context.Context, *media.DownloadedFile) (media.CompressionResult, error)
}

// Transcriber converts speech to text. A nil Transcriber disables voice
// message transcription.
type Transcriber interface {
	Transcribe(ctx context.Context, filename string, audio []byte) (string, error)
}

type Bot struct {
	telegram    telegramClient
	downloader  mediaDownloader
	transcoder  mediaTranscoder
	transcriber Transcriber
	config      Config
	logger      *slog.Logger
}

type job struct {
	chatID    int64
	messageID int
	threadID  int
	link      media.Link
	voice     *voiceMessage
}

type voiceMessage struct {
	kind     string
	fileID   string
	duration int
	fileSize int64
}

func New(client telegramClient, downloader mediaDownloader, transcoder mediaTranscoder, transcriber Transcriber, cfg Config, logger *slog.Logger) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{
		telegram: client, downloader: downloader, transcoder: transcoder, transcriber: transcriber,
		config: cfg, logger: logger,
	}
}

func (b *Bot) Run(ctx context.Context) error {
	startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := b.telegram.DeleteWebhook(startupCtx); err != nil {
		return err
	}
	user, err := b.telegram.GetMe(startupCtx)
	if err != nil {
		return err
	}
	b.logger.Info("bot started", "username", user.Username, "workers", b.config.Workers, "transcription", b.transcriber != nil)

	jobs := make(chan job, b.config.QueueSize)
	var workers sync.WaitGroup
	for workerID := 1; workerID <= b.config.Workers; workerID++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			b.worker(ctx, jobs)
		}()
	}
	defer func() {
		close(jobs)
		workers.Wait()
	}()

	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		updates, err := b.telegram.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.logger.Error("cannot receive Telegram updates", "error", err, "retry_in", backoff)
			if !waitFor(ctx, backoff) {
				break
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			for _, item := range b.jobsForUpdate(update) {
				select {
				case jobs <- item:
				case <-ctx.Done():
					return nil
				}
			}
		}
	}
	return nil
}

func (b *Bot) jobsForUpdate(update telegram.Update) []job {
	message := update.Message
	if message == nil || (message.Chat.Type != "group" && message.Chat.Type != "supergroup") {
		return nil
	}
	if message.From != nil && message.From.IsBot {
		return nil
	}
	if len(b.config.AllowedChatIDs) > 0 {
		if _, allowed := b.config.AllowedChatIDs[message.Chat.ID]; !allowed {
			return nil
		}
	}

	links := media.ExtractLinks(message.Text, message.Entities)
	links = appendUnique(links, media.ExtractLinks(message.Caption, message.CaptionEntities)...)
	if len(links) > b.config.MaxLinksPerMessage {
		links = links[:b.config.MaxLinksPerMessage]
	}
	result := make([]job, 0, len(links)+1)
	if voice := voiceFromMessage(message); voice != nil && b.transcriber != nil {
		result = append(result, job{
			chatID: message.Chat.ID, messageID: message.MessageID,
			threadID: message.MessageThreadID, voice: voice,
		})
	}
	for _, link := range links {
		result = append(result, job{
			chatID: message.Chat.ID, messageID: message.MessageID,
			threadID: message.MessageThreadID, link: link,
		})
	}
	return result
}

func voiceFromMessage(message *telegram.Message) *voiceMessage {
	switch {
	case message.Voice != nil:
		return &voiceMessage{
			kind: "voice", fileID: message.Voice.FileID,
			duration: message.Voice.Duration, fileSize: message.Voice.FileSize,
		}
	case message.VideoNote != nil:
		return &voiceMessage{
			kind: "video_note", fileID: message.VideoNote.FileID,
			duration: message.VideoNote.Duration, fileSize: message.VideoNote.FileSize,
		}
	}
	return nil
}

func (b *Bot) worker(ctx context.Context, jobs <-chan job) {
	for item := range jobs {
		if ctx.Err() != nil {
			return
		}
		b.handle(ctx, item)
	}
}

func (b *Bot) handle(ctx context.Context, item job) {
	if item.voice != nil {
		b.transcribe(ctx, item)
		return
	}
	logger := b.logger.With("chat_id", item.chatID, "message_id", item.messageID, "provider", item.link.Provider)
	downloadCtx, cancelDownload := context.WithTimeout(ctx, b.config.DownloadTimeout)
	file, err := b.downloader.Download(downloadCtx, item.link.URL)
	cancelDownload()
	if errors.Is(err, media.ErrNoMedia) {
		// Text-only posts are shared often, so they are skipped silently.
		logger.Info("post has no media")
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("media download failed", "error", err)
			b.notifyError(ctx, item)
		}
		return
	}
	defer func() {
		if cleanupErr := file.Cleanup(); cleanupErr != nil {
			logger.Error("cannot remove temporary media", "error", cleanupErr)
		}
	}()
	if len(file.Album) > 0 {
		uploadCtx, cancelUpload := context.WithTimeout(ctx, b.config.UploadTimeout)
		err = b.telegram.SendAlbum(uploadCtx, item.chatID, item.messageID, item.threadID, file.Album)
		cancelUpload()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("album upload failed", "error", err, "item_count", len(file.Album))
				b.notifyError(ctx, item)
			}
			return
		}
		logger.Info("album sent", "item_count", len(file.Album))
		return
	}
	if file.Audio != nil {
		uploadCtx, cancelUpload := context.WithTimeout(ctx, b.config.UploadTimeout)
		err = b.telegram.SendAudio(
			uploadCtx, item.chatID, item.messageID, item.threadID, file.Path,
			file.Audio.Title, file.Audio.Performer, file.Audio.Duration,
		)
		cancelUpload()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("audio upload failed", "error", err)
				b.notifyError(ctx, item)
			}
			return
		}
		logger.Info("audio sent", "match_score", file.Audio.MatchScore)
		return
	}

	compressionCtx, cancelCompression := context.WithTimeout(ctx, b.config.CompressionTimeout)
	compression, err := b.transcoder.Compress(compressionCtx, file)
	cancelCompression()
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("media compression failed", "error", err, "original_size", compression.OriginalSize)
			b.notifyError(ctx, item)
		}
		return
	}
	if compression.Compressed {
		logger.Info("media compressed", "original_size", compression.OriginalSize, "final_size", compression.FinalSize)
	}

	uploadCtx, cancelUpload := context.WithTimeout(ctx, b.config.UploadTimeout)
	err = b.telegram.SendVideo(uploadCtx, item.chatID, item.messageID, item.threadID, file.Path)
	cancelUpload()
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("media upload failed", "error", err)
			b.notifyError(ctx, item)
		}
		return
	}
	logger.Info("media sent")
}

func (b *Bot) transcribe(ctx context.Context, item job) {
	voice := item.voice
	logger := b.logger.With("chat_id", item.chatID, "message_id", item.messageID, "kind", voice.kind, "duration", voice.duration)
	if limit := b.config.MaxVoiceDuration; limit > 0 && time.Duration(voice.duration)*time.Second > limit {
		logger.Info("voice message is too long to transcribe")
		return
	}
	if voice.fileSize > maxVoiceFileSize {
		logger.Info("voice message is too large to transcribe", "file_size", voice.fileSize)
		return
	}

	transcriptionCtx, cancel := context.WithTimeout(ctx, b.config.TranscriptionTimeout)
	audio, filename, err := b.telegram.DownloadFile(transcriptionCtx, voice.fileID, maxVoiceFileSize)
	var text string
	if err == nil {
		text, err = b.transcriber.Transcribe(transcriptionCtx, filename, audio)
	}
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("voice transcription failed", "error", err)
			b.notifyError(ctx, item)
		}
		return
	}
	if text == "" {
		logger.Info("voice message has no recognizable speech")
		return
	}

	for _, part := range splitText(text, maxTextLength) {
		sendCtx, cancelSend := context.WithTimeout(ctx, 15*time.Second)
		err = b.telegram.SendText(sendCtx, item.chatID, item.messageID, item.threadID, part)
		cancelSend()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("transcript upload failed", "error", err)
			}
			return
		}
	}
	logger.Info("voice transcribed", "characters", utf8.RuneCountInString(text))
}

// splitText cuts text into parts of at most limit runes, preferring to break
// at whitespace so that words stay intact.
func splitText(text string, limit int) []string {
	var parts []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := limit
		for index := limit; index > limit/2; index-- {
			if unicode.IsSpace(runes[index]) {
				cut = index
				break
			}
		}
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

func (b *Bot) notifyError(ctx context.Context, item job) {
	if !b.config.NotifyErrors || ctx.Err() != nil {
		return
	}
	message := publicDownloadError
	if item.voice != nil {
		message = publicTranscriptionError
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := b.telegram.SendText(notifyCtx, item.chatID, item.messageID, item.threadID, message); err != nil && !errors.Is(err, context.Canceled) {
		b.logger.Error("cannot notify chat about failure", "error", err, "chat_id", item.chatID)
	}
}

func appendUnique(current []media.Link, additional ...media.Link) []media.Link {
	seen := make(map[string]struct{}, len(current)+len(additional))
	for _, link := range current {
		seen[link.URL] = struct{}{}
	}
	for _, link := range additional {
		if _, exists := seen[link.URL]; exists {
			continue
		}
		seen[link.URL] = struct{}{}
		current = append(current, link)
	}
	return current
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
