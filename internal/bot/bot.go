package bot

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"brsaver/internal/media"
	"brsaver/internal/telegram"
)

const publicDownloadError = "Не вдалося завантажити це медіа. Можливо, воно приватне, недоступне або завелике."

type Config struct {
	Workers            int
	QueueSize          int
	MaxLinksPerMessage int
	DownloadTimeout    time.Duration
	UploadTimeout      time.Duration
	CompressionTimeout time.Duration
	NotifyErrors       bool
	AllowedChatIDs     map[int64]struct{}
}

type telegramClient interface {
	GetMe(context.Context) (telegram.User, error)
	DeleteWebhook(context.Context) error
	GetUpdates(context.Context, int64) ([]telegram.Update, error)
	SendText(context.Context, int64, int, int, string) error
	SendVideo(context.Context, int64, int, int, string) error
	SendPhotos(context.Context, int64, int, int, []string) error
}

type mediaDownloader interface {
	Download(context.Context, string) (*media.DownloadedFile, error)
}

type mediaTranscoder interface {
	Compress(context.Context, *media.DownloadedFile) (media.CompressionResult, error)
}

type Bot struct {
	telegram   telegramClient
	downloader mediaDownloader
	transcoder mediaTranscoder
	config     Config
	logger     *slog.Logger
}

type job struct {
	chatID    int64
	messageID int
	threadID  int
	link      media.Link
}

func New(client telegramClient, downloader mediaDownloader, transcoder mediaTranscoder, cfg Config, logger *slog.Logger) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{telegram: client, downloader: downloader, transcoder: transcoder, config: cfg, logger: logger}
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
	b.logger.Info("bot started", "username", user.Username, "workers", b.config.Workers)

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
	result := make([]job, 0, len(links))
	for _, link := range links {
		result = append(result, job{
			chatID: message.Chat.ID, messageID: message.MessageID,
			threadID: message.MessageThreadID, link: link,
		})
	}
	return result
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
	logger := b.logger.With("chat_id", item.chatID, "message_id", item.messageID, "provider", item.link.Provider)
	downloadCtx, cancelDownload := context.WithTimeout(ctx, b.config.DownloadTimeout)
	file, err := b.downloader.Download(downloadCtx, item.link.URL)
	cancelDownload()
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
	if len(file.Photos) > 0 {
		uploadCtx, cancelUpload := context.WithTimeout(ctx, b.config.UploadTimeout)
		err = b.telegram.SendPhotos(uploadCtx, item.chatID, item.messageID, item.threadID, file.Photos)
		cancelUpload()
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("photo album upload failed", "error", err, "photo_count", len(file.Photos))
				b.notifyError(ctx, item)
			}
			return
		}
		logger.Info("photo album sent", "photo_count", len(file.Photos))
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

func (b *Bot) notifyError(ctx context.Context, item job) {
	if !b.config.NotifyErrors || ctx.Err() != nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := b.telegram.SendText(notifyCtx, item.chatID, item.messageID, item.threadID, publicDownloadError); err != nil && !errors.Is(err, context.Canceled) {
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
