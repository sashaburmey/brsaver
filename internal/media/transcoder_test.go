package media

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTranscoderLeavesSmallFileUntouched(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "small.mp4")
	if err := os.WriteFile(path, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	transcoder, err := NewTranscoder(TranscoderConfig{
		FFmpegPath: "not-needed", FFprobePath: "not-needed", MaxFileSize: 100,
		CompressOversized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := &DownloadedFile{Path: path, dir: dir}
	result, err := transcoder.Compress(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compressed || result.OriginalSize != 5 || result.FinalSize != 5 || file.Path != path {
		t.Fatalf("unexpected result: %#v, path %q", result, file.Path)
	}
}

func TestTranscoderUsesTwoPassOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helpers are POSIX shell scripts")
	}
	t.Parallel()

	dir := t.TempDir()
	input := filepath.Join(dir, "large.mp4")
	if err := os.WriteFile(input, make([]byte, 4*1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '60.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
output=""
for argument in "$@"; do output="$argument"; done
if [ "$output" != "/dev/null" ]; then printf 'compressed-video' > "$output"; fi
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	transcoder, err := NewTranscoder(TranscoderConfig{
		FFmpegPath: ffmpeg, FFprobePath: probe, MaxFileSize: 2 * 1024 * 1024,
		CompressOversized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	file := &DownloadedFile{Path: input, dir: dir}
	result, err := transcoder.Compress(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compressed || result.OriginalSize != 4*1024*1024 || result.FinalSize <= 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if file.Path != filepath.Join(dir, "compressed.mp4") {
		t.Fatalf("output path = %q", file.Path)
	}
}

func TestTargetBitratesRejectsImpossibleDuration(t *testing.T) {
	t.Parallel()

	if _, _, err := targetBitrates(24*60*60, 1024); err == nil {
		t.Fatal("targetBitrates() error = nil, want impossible-size error")
	}
}
