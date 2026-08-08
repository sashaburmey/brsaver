package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrCannotCompress = errors.New("media cannot be compressed below the Telegram upload limit")

type TranscoderConfig struct {
	FFmpegPath        string
	FFprobePath       string
	MaxFileSize       int64
	CompressOversized bool
}

type Transcoder struct {
	ffmpegPath        string
	ffprobePath       string
	maxFileSize       int64
	compressOversized bool
}

type CompressionResult struct {
	Compressed   bool
	OriginalSize int64
	FinalSize    int64
}

func NewTranscoder(cfg TranscoderConfig) (*Transcoder, error) {
	if cfg.FFmpegPath == "" || cfg.FFprobePath == "" {
		return nil, errors.New("ffmpeg and ffprobe paths are required")
	}
	if cfg.MaxFileSize <= 0 {
		return nil, errors.New("maximum output file size must be positive")
	}
	return &Transcoder{
		ffmpegPath: cfg.FFmpegPath, ffprobePath: cfg.FFprobePath,
		maxFileSize: cfg.MaxFileSize, compressOversized: cfg.CompressOversized,
	}, nil
}

func (t *Transcoder) Compress(ctx context.Context, file *DownloadedFile) (CompressionResult, error) {
	if file == nil || file.Path == "" {
		return CompressionResult{}, errors.New("downloaded file is required")
	}
	info, err := os.Stat(file.Path)
	if err != nil {
		return CompressionResult{}, fmt.Errorf("inspect media before compression: %w", err)
	}
	result := CompressionResult{OriginalSize: info.Size(), FinalSize: info.Size()}
	if info.Size() <= t.maxFileSize {
		return result, nil
	}
	if !t.compressOversized {
		return result, ErrFileTooLarge
	}

	duration, err := t.probeDuration(ctx, file.Path)
	if err != nil {
		return result, err
	}
	videoBitrate, audioBitrate, err := targetBitrates(duration, t.maxFileSize)
	if err != nil {
		return result, err
	}

	dir := filepath.Dir(file.Path)
	output := filepath.Join(dir, "compressed.mp4")
	passlog := filepath.Join(dir, "ffmpeg-pass")
	defer removePassLogs(passlog)

	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Remove(output); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("replace compressed media: %w", err)
		}
		removePassLogs(passlog)
		if err := t.transcode(ctx, file.Path, output, passlog, videoBitrate, audioBitrate); err != nil {
			return result, err
		}
		compressedInfo, err := os.Stat(output)
		if err != nil {
			return result, fmt.Errorf("inspect compressed media: %w", err)
		}
		result.FinalSize = compressedInfo.Size()
		if result.FinalSize > 0 && result.FinalSize <= t.maxFileSize {
			result.Compressed = true
			file.Path = output
			return result, nil
		}
		videoBitrate = videoBitrate * 75 / 100
	}
	return result, ErrCannotCompress
}

func (t *Transcoder) probeDuration(ctx context.Context, path string) (float64, error) {
	cmd := exec.CommandContext(ctx, t.ffprobePath,
		"-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path,
	)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe failed: %s", commandError(err, stderr.String()))
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil || duration <= 0 {
		return 0, errors.New("ffprobe returned an invalid media duration")
	}
	return duration, nil
}

func (t *Transcoder) transcode(ctx context.Context, input, output, passlog string, videoBitrate, audioBitrate int64) error {
	videoRate := strconv.FormatInt(videoBitrate, 10)
	audioRate := strconv.FormatInt(audioBitrate, 10)
	common := []string{
		"-y", "-i", input, "-map", "0:v:0",
		"-vf", "scale=min(720\\,iw):-2:force_original_aspect_ratio=decrease",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p",
		"-b:v", videoRate, "-passlogfile", passlog,
	}

	firstPass := append(append([]string{}, common...), "-pass", "1", "-an", "-f", "mp4", os.DevNull)
	if err := runFFmpeg(ctx, t.ffmpegPath, firstPass); err != nil {
		return fmt.Errorf("ffmpeg first pass failed: %w", err)
	}
	secondPass := append(append([]string{}, common...),
		"-pass", "2", "-map", "0:a:0?", "-c:a", "aac", "-b:a", audioRate,
		"-movflags", "+faststart", output,
	)
	if err := runFFmpeg(ctx, t.ffmpegPath, secondPass); err != nil {
		return fmt.Errorf("ffmpeg second pass failed: %w", err)
	}
	return nil
}

func runFFmpeg(ctx context.Context, binary string, args []string) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr limitedBuffer
	cmd.Stdout = &limitedBuffer{}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(commandError(err, stderr.String()))
	}
	return nil
}

func commandError(err error, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail != "" {
		return detail
	}
	return err.Error()
}

func targetBitrates(duration float64, maxFileSize int64) (int64, int64, error) {
	if duration <= 0 || maxFileSize <= 0 {
		return 0, 0, errors.New("duration and maximum file size must be positive")
	}
	// The 88% budget leaves room for the MP4 container and bitrate variance.
	total := int64(float64(maxFileSize) * 8 * 0.88 / duration)
	audio := int64(96_000)
	if total < 500_000 {
		audio = 48_000
	}
	video := total - audio
	if video < 80_000 {
		return 0, 0, ErrCannotCompress
	}
	if video > 6_000_000 {
		video = 6_000_000
	}
	return video, audio, nil
}

func removePassLogs(prefix string) {
	paths, _ := filepath.Glob(prefix + "*")
	for _, path := range paths {
		_ = os.Remove(path)
	}
}
