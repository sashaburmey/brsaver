package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"brsaver/internal/bot"
	"brsaver/internal/config"
	"brsaver/internal/media"
	"brsaver/internal/telegram"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:         cfg.TelegramToken,
		APIBaseURL:    cfg.TelegramAPIBaseURL,
		PollTimeout:   cfg.PollTimeout,
		UploadTimeout: cfg.UploadTimeout,
	})
	if err != nil {
		logger.Error("cannot initialize Telegram client", "error", err)
		os.Exit(1)
	}

	downloader, err := media.NewDownloader(media.DownloaderConfig{
		Binary:             cfg.YTDLPPath,
		WorkDir:            cfg.DownloadDir,
		CookiesFile:        cfg.CookiesFile,
		YouTubeCookiesFile: cfg.YouTubeCookiesFile,
		TikTokDeviceID:     cfg.TikTokDeviceID,
		Format:             cfg.YTDLPFormat,
		MaxFileSize:        cfg.MaxDownloadSize,
		MaxAudioFileSize:   cfg.MaxFileSize,
		SizeCheckGap:       cfg.SizeCheckInterval,
		Attempts:           cfg.DownloadAttempts,
		RetryDelay:         cfg.DownloadRetryDelay,
		PhotoHelper:        cfg.TikTokPhotoHelper,
	})
	if err != nil {
		logger.Error("cannot initialize media downloader", "error", err)
		os.Exit(1)
	}
	transcoder, err := media.NewTranscoder(media.TranscoderConfig{
		FFmpegPath:        cfg.FFmpegPath,
		FFprobePath:       cfg.FFprobePath,
		MaxFileSize:       cfg.MaxFileSize,
		CompressOversized: cfg.CompressOversized,
	})
	if err != nil {
		logger.Error("cannot initialize media transcoder", "error", err)
		os.Exit(1)
	}

	application := bot.New(client, downloader, transcoder, bot.Config{
		Workers:            cfg.Workers,
		QueueSize:          cfg.QueueSize,
		MaxLinksPerMessage: cfg.MaxLinksPerMessage,
		DownloadTimeout:    cfg.DownloadTimeout,
		UploadTimeout:      cfg.UploadTimeout,
		CompressionTimeout: cfg.CompressionTimeout,
		NotifyErrors:       cfg.NotifyErrors,
		AllowedChatIDs:     cfg.AllowedChatIDs,
	}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
		logger.Error("bot stopped with an error", "error", err)
		os.Exit(1)
	}
	logger.Info("bot stopped")
}
