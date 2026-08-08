package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxFileSize     = 49 * 1024 * 1024
	defaultMaxDownloadSize = 250 * 1024 * 1024
	defaultFormat          = "bestvideo[height<=1080][ext=mp4][vcodec^=avc1]+bestaudio[ext=m4a]/best[height<=1080][ext=mp4][vcodec^=avc1]/best[height<=1080][ext=mp4][vcodec^=h264]/best[height<=1080][ext=mp4]/best[height<=1080]/best"
)

type Config struct {
	TelegramToken      string
	TelegramAPIBaseURL string
	YTDLPPath          string
	YTDLPFormat        string
	FFmpegPath         string
	FFprobePath        string
	CookiesFile        string
	DownloadDir        string
	MaxFileSize        int64
	MaxDownloadSize    int64
	Workers            int
	QueueSize          int
	MaxLinksPerMessage int
	PollTimeout        time.Duration
	DownloadTimeout    time.Duration
	DownloadRetryDelay time.Duration
	DownloadAttempts   int
	UploadTimeout      time.Duration
	CompressionTimeout time.Duration
	SizeCheckInterval  time.Duration
	NotifyErrors       bool
	CompressOversized  bool
	AllowedChatIDs     map[int64]struct{}
}

func Load() (Config, error) {
	cfg := Config{
		TelegramToken:      strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		TelegramAPIBaseURL: envOr("TELEGRAM_API_BASE_URL", "https://api.telegram.org"),
		YTDLPPath:          envOr("YT_DLP_PATH", "yt-dlp"),
		YTDLPFormat:        envOr("YT_DLP_FORMAT", defaultFormat),
		FFmpegPath:         envOr("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:        envOr("FFPROBE_PATH", "ffprobe"),
		CookiesFile:        strings.TrimSpace(os.Getenv("YT_DLP_COOKIES_FILE")),
		DownloadDir:        envOr("DOWNLOAD_DIR", filepath.Join(os.TempDir(), "brsaver")),
	}

	if cfg.TelegramToken == "" {
		return Config{}, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	baseURL, err := url.Parse(cfg.TelegramAPIBaseURL)
	if err != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.Host == "" {
		return Config{}, fmt.Errorf("TELEGRAM_API_BASE_URL must be an absolute HTTP(S) URL")
	}
	cfg.TelegramAPIBaseURL = strings.TrimRight(cfg.TelegramAPIBaseURL, "/")

	if cfg.MaxFileSize, err = envBytes("MAX_FILE_SIZE_MB", defaultMaxFileSize); err != nil {
		return Config{}, err
	}
	if cfg.MaxDownloadSize, err = envBytes("MAX_DOWNLOAD_SIZE_MB", defaultMaxDownloadSize); err != nil {
		return Config{}, err
	}
	if cfg.MaxDownloadSize < cfg.MaxFileSize {
		return Config{}, fmt.Errorf("MAX_DOWNLOAD_SIZE_MB cannot be smaller than MAX_FILE_SIZE_MB")
	}
	if cfg.Workers, err = envInt("WORKERS", 2, 1, 16); err != nil {
		return Config{}, err
	}
	if cfg.QueueSize, err = envInt("QUEUE_SIZE", 32, 1, 1000); err != nil {
		return Config{}, err
	}
	if cfg.MaxLinksPerMessage, err = envInt("MAX_LINKS_PER_MESSAGE", 3, 1, 10); err != nil {
		return Config{}, err
	}
	if cfg.PollTimeout, err = envDuration("POLL_TIMEOUT", 50*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.DownloadTimeout, err = envDuration("DOWNLOAD_TIMEOUT", 4*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.DownloadRetryDelay, err = envDuration("DOWNLOAD_RETRY_DELAY", 2*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.DownloadAttempts, err = envInt("DOWNLOAD_ATTEMPTS", 3, 1, 10); err != nil {
		return Config{}, err
	}
	if cfg.UploadTimeout, err = envDuration("UPLOAD_TIMEOUT", 3*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.CompressionTimeout, err = envDuration("COMPRESSION_TIMEOUT", 12*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.SizeCheckInterval, err = envDuration("SIZE_CHECK_INTERVAL", 500*time.Millisecond); err != nil {
		return Config{}, err
	}
	if cfg.NotifyErrors, err = envBool("NOTIFY_ERRORS", true); err != nil {
		return Config{}, err
	}
	if cfg.CompressOversized, err = envBool("COMPRESS_OVERSIZED", true); err != nil {
		return Config{}, err
	}
	if cfg.AllowedChatIDs, err = envChatIDs("ALLOWED_CHAT_IDS"); err != nil {
		return Config{}, err
	}

	if cfg.PollTimeout < time.Second || cfg.PollTimeout > 60*time.Second {
		return Config{}, fmt.Errorf("POLL_TIMEOUT must be between 1s and 60s")
	}
	if cfg.SizeCheckInterval < 100*time.Millisecond || cfg.SizeCheckInterval > 10*time.Second {
		return Config{}, fmt.Errorf("SIZE_CHECK_INTERVAL must be between 100ms and 10s")
	}
	if cfg.YTDLPPath == "" || cfg.YTDLPFormat == "" || cfg.FFmpegPath == "" || cfg.FFprobePath == "" || cfg.DownloadDir == "" {
		return Config{}, fmt.Errorf("media tool paths, YT_DLP_FORMAT and DOWNLOAD_DIR cannot be empty")
	}

	return cfg, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback, minValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minValue, maxValue)
	}
	return value, nil
}

func envBytes(name string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	megabytes, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || megabytes < 1 || megabytes > 2000 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 2000", name)
	}
	return megabytes * 1024 * 1024, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration such as 30s or 4m", name)
	}
	return value, nil
}

func envBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return value, nil
}

func envChatIDs(name string) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return result, nil
	}
	for _, part := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("%s must contain comma-separated non-zero chat IDs", name)
		}
		result[id] = struct{}{}
	}
	return result, nil
}
