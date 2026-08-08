package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrFileTooLarge = errors.New("downloaded media exceeds the configured size limit")

type DownloaderConfig struct {
	Binary       string
	WorkDir      string
	CookiesFile  string
	Format       string
	MaxFileSize  int64
	SizeCheckGap time.Duration
	Attempts     int
	RetryDelay   time.Duration
}

type Downloader struct {
	binary       string
	workDir      string
	cookiesFile  string
	format       string
	maxFileSize  int64
	sizeCheckGap time.Duration
	attempts     int
	retryDelay   time.Duration
}

type DownloadedFile struct {
	Path string
	dir  string
	once sync.Once
	err  error
}

func NewDownloader(cfg DownloaderConfig) (*Downloader, error) {
	if cfg.Binary == "" || cfg.WorkDir == "" || cfg.Format == "" {
		return nil, errors.New("downloader binary, work directory and format are required")
	}
	if cfg.MaxFileSize <= 0 {
		return nil, errors.New("maximum file size must be positive")
	}
	if cfg.SizeCheckGap <= 0 {
		cfg.SizeCheckGap = 500 * time.Millisecond
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 3
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 2 * time.Second
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}
	return &Downloader{
		binary:       cfg.Binary,
		workDir:      cfg.WorkDir,
		cookiesFile:  cfg.CookiesFile,
		format:       cfg.Format,
		maxFileSize:  cfg.MaxFileSize,
		sizeCheckGap: cfg.SizeCheckGap,
		attempts:     cfg.Attempts,
		retryDelay:   cfg.RetryDelay,
	}, nil
}

func (d *Downloader) Download(ctx context.Context, rawURL string) (*DownloadedFile, error) {
	var lastErr error
	for attempt := 1; attempt <= d.attempts; attempt++ {
		file, err := d.downloadOnce(ctx, rawURL)
		if err == nil {
			return file, nil
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, ErrFileTooLarge) || attempt == d.attempts {
			break
		}

		timer := time.NewTimer(d.retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("download failed after %d attempts: %w", d.attempts, lastErr)
}

func (d *Downloader) downloadOnce(ctx context.Context, rawURL string) (_ *DownloadedFile, resultErr error) {
	dir, err := os.MkdirTemp(d.workDir, "job-")
	if err != nil {
		return nil, fmt.Errorf("create job directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("clean failed download: %w", cleanupErr)
			}
		}
	}()

	args := []string{
		"--ignore-config",
		"--no-cache-dir",
		"--no-playlist",
		"--no-progress",
		"--no-warnings",
		"--restrict-filenames",
		"--max-filesize", strconv.FormatInt(d.maxFileSize, 10),
		"--format", d.format,
		"--merge-output-format", "mp4",
		"--remux-video", "mp4",
		"--print", "after_move:filepath",
		"--paths", dir,
		"--output", "%(extractor)s-%(id)s.%(ext)s",
		"--socket-timeout", "30",
	}
	if d.cookiesFile != "" {
		args = append(args, "--cookies", d.cookiesFile)
	}
	args = append(args, "--", rawURL)

	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, d.binary, args...)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start yt-dlp: %w", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ticker := time.NewTicker(d.sizeCheckGap)
	defer ticker.Stop()

	tooLarge := false
	commandDone := false
	for !commandDone {
		select {
		case err = <-wait:
			commandDone = true
		case <-ticker.C:
			var size int64
			size, resultErr = directorySize(dir)
			if resultErr != nil {
				cancel()
				err = <-wait
				return nil, resultErr
			}
			// Merging separate video and audio streams temporarily keeps the
			// sources and destination together. Three times the final-file limit
			// leaves room for that while still bounding an unknown-size HLS job.
			if size > d.maxFileSize*3 {
				tooLarge = true
				cancel()
				err = <-wait
				commandDone = true
			}
		case <-ctx.Done():
			cancel()
			err = <-wait
			commandDone = true
		}
	}
	if tooLarge {
		return nil, ErrFileTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		detail = strings.ReplaceAll(detail, rawURL, "[URL]")
		return nil, fmt.Errorf("yt-dlp failed: %s", detail)
	}

	path, err := findOutputPath(dir, stdout.String())
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect downloaded media: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("downloaded media is not a regular file")
	}
	if info.Size() <= 0 {
		return nil, errors.New("downloaded media is empty")
	}
	if info.Size() > d.maxFileSize {
		return nil, ErrFileTooLarge
	}

	keep = true
	return &DownloadedFile{Path: path, dir: dir}, nil
}

func (f *DownloadedFile) Cleanup() error {
	if f == nil || f.dir == "" {
		return nil
	}
	f.once.Do(func() { f.err = os.RemoveAll(f.dir) })
	return f.err
}

func findOutputPath(dir, output string) (string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		candidate := strings.TrimSpace(lines[index])
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		if isWithin(candidate, dir) {
			if info, err := os.Lstat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
	}

	var largest string
	var largestSize int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && !strings.HasSuffix(path, ".part") && !strings.HasSuffix(path, ".ytdl") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Size() > largestSize {
				largest, largestSize = path, info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan download directory: %w", err)
	}
	if largest == "" {
		return "", errors.New("yt-dlp did not produce a media file")
	}
	return largest, nil
}

func isWithin(path, dir string) bool {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteDir, absolutePath)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func directorySize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	const limit = 16 * 1024
	remaining := limit - b.buffer.Len()
	if remaining > 0 {
		_, _ = b.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

var _ io.Writer = (*limitedBuffer)(nil)
