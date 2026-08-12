package media

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSpotifyTrackHTML(t *testing.T) {
	t.Parallel()

	document := `<!doctype html><html><head>
		<meta content="Cut &amp; Run" property="og:title">
		<meta name='music:musician_description' content='Example Artist'>
		<meta content="208" name="music:duration">
	</head></html>`
	track, err := parseSpotifyTrackHTML(document)
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Cut & Run" || track.Performer != "Example Artist" || track.Duration != 208 {
		t.Fatalf("unexpected metadata: %#v", track)
	}
}

func TestParseSpotifyEmbedHTML(t *testing.T) {
	t.Parallel()

	document := `<html><body><script type="application/json" id="__NEXT_DATA__">{
		"props":{"pageProps":{"state":{"data":{"entity":{
			"type":"track","id":"7lQ8MOhq6IN2w8EYcFNSUk","name":"Without Me",
			"artists":[{"name":"Eminem"}],"duration":290320
		}}}}}}
	</script></body></html>`
	track, err := parseSpotifyEmbedHTML(document, "7lQ8MOhq6IN2w8EYcFNSUk")
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Without Me" || track.Performer != "Eminem" || track.Duration != 290 {
		t.Fatalf("unexpected metadata: %#v", track)
	}
}

func TestParseSpotifyEmbedHTMLRejectsUnexpectedTrack(t *testing.T) {
	t.Parallel()

	document := `<script id="__NEXT_DATA__" type="application/json">{
		"props":{"pageProps":{"state":{"data":{"entity":{
			"type":"track","id":"different-track-id","name":"Wrong Track",
			"artists":[{"name":"Wrong Artist"}],"duration":100000
		}}}}}}
	</script>`
	if _, err := parseSpotifyEmbedHTML(document, "7lQ8MOhq6IN2w8EYcFNSUk"); err == nil {
		t.Fatal("parseSpotifyEmbedHTML() error = nil")
	}
}

func TestParseSpotifyTrackHTMLUsesDescriptionFallback(t *testing.T) {
	t.Parallel()

	document := `<meta property="og:title" content="Track Name">
		<meta property="og:description" content="Track Artist · Track Name · Song · 2026">
		<meta name="music:duration" content="181">`
	track, err := parseSpotifyTrackHTML(document)
	if err != nil {
		t.Fatal(err)
	}
	if track.Performer != "Track Artist" {
		t.Fatalf("performer = %q", track.Performer)
	}
}

func TestParseSpotifyTrackHTMLRejectsIncompleteMetadata(t *testing.T) {
	t.Parallel()

	if _, err := parseSpotifyTrackHTML(`<meta property="og:title" content="Track Name">`); err == nil {
		t.Fatal("parseSpotifyTrackHTML() error = nil")
	}
}

func TestSelectSpotifyCandidatePrefersReliableOfficialAudio(t *testing.T) {
	t.Parallel()

	track := spotifyTrackMetadata{Title: "Cut To The Feeling", Performer: "Carly Rae Jepsen", Duration: 208}
	candidates := []youtubeCandidate{
		{ID: "OFFICIAL123", Title: "Cut To The Feeling", Channel: "CarlyRaeJepsenMusic", Duration: 208},
		{ID: "REMIX123456", Title: "Carly Rae Jepsen - Cut To The Feeling (Remix)", Channel: "Fan channel", Duration: 210},
		{ID: "ABCDEFGHIJK", Title: "Carly Rae Jepsen - Cut To The Feeling (Official Audio)", Channel: "Carly Rae Jepsen", Duration: 208},
		{ID: "LIVE1234567", Title: "Carly Rae Jepsen - Cut To The Feeling Live", Channel: "Concerts", Duration: 260},
	}

	candidate, score, err := selectSpotifyCandidate(track, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ID != "ABCDEFGHIJK" || score < spotifyMinimumMatchScore {
		t.Fatalf("selected %#v with score %d", candidate, score)
	}
}

func TestSpotifyCandidateAcceptsArtistInCompactChannelName(t *testing.T) {
	t.Parallel()

	track := spotifyTrackMetadata{Title: "Without Me", Performer: "Eminem", Duration: 290}
	candidate := youtubeCandidate{
		ID: "fsG39JgqTmo", Title: "Without Me", Channel: "EminemMusic", Duration: 290,
	}

	score, acceptable := spotifyCandidateScore(track, candidate)
	if !acceptable || score < spotifyMinimumMatchScore {
		t.Fatalf("candidate score = %d, acceptable = %t", score, acceptable)
	}
}

func TestSelectSpotifyCandidateRejectsUnreliableMatch(t *testing.T) {
	t.Parallel()

	track := spotifyTrackMetadata{Title: "Exact Song", Performer: "Original Artist", Duration: 200}
	_, _, err := selectSpotifyCandidate(track, []youtubeCandidate{{
		ID: "ABCDEFGHIJK", Title: "Different Song", Channel: "Different Artist", Duration: 200,
	}})
	if err == nil || !strings.Contains(err.Error(), ErrNoReliableAudioMatch.Error()) {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloaderDownloadsMatchedSpotifyTrackAndCleansUp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binary := filepath.Join(root, "fake-yt-dlp")
	cookiesFile := filepath.Join(root, "youtube-source-cookies.txt")
	const sourceCookies = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tsession\tsecret\n"
	if err := os.WriteFile(cookiesFile, []byte(sourceCookies), 0o400); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cookies=""
dump_json=""
previous=""
for argument in "$@"; do
	if [ "$previous" = "--cookies" ]; then cookies="$argument"; fi
	if [ "$argument" = "--dump-single-json" ]; then dump_json="yes"; fi
	previous="$argument"
done
[ -n "$cookies" ] || exit 20
[ "$cookies" != "` + cookiesFile + `" ] || exit 21
printf '# refreshed\n' >> "$cookies" || exit 22
if [ "$dump_json" = "yes" ]; then
	printf '%s\n' '{"entries":[{"id":"ABCDEFGHIJK","title":"Carly Rae Jepsen - Cut To The Feeling (Official Audio)","channel":"Carly Rae Jepsen","duration":208}]}'
	exit 0
fi
previous=""
directory=""
for argument in "$@"; do
	if [ "$previous" = "--paths" ]; then directory="$argument"; fi
	previous="$argument"
done
path="$directory/spotify-ABCDEFGHIJK.mp3"
printf 'fake-mp3-data' > "$path"
printf '%s\n' "$path"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	downloader, err := NewDownloader(DownloaderConfig{
		Binary: binary, WorkDir: root, Format: "best", MaxFileSize: 1024 * 1024,
		MaxAudioFileSize: 512 * 1024, YouTubeCookiesFile: cookiesFile,
		Attempts: 1, SizeCheckGap: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	downloader.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://open.spotify.com/embed/track/11dFghVXANMlKmJXsNCbNl" {
			t.Errorf("metadata URL = %q", request.URL)
		}
		body := `<script id="__NEXT_DATA__" type="application/json">{
			"props":{"pageProps":{"state":{"data":{"entity":{
				"type":"track","id":"11dFghVXANMlKmJXsNCbNl","title":"Cut To The Feeling",
				"artists":[{"name":"Carly Rae Jepsen"}],"duration":208000
			}}}}}}
		</script>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	file, err := downloader.Download(context.Background(), "https://open.spotify.com/track/11dFghVXANMlKmJXsNCbNl")
	if err != nil {
		t.Fatal(err)
	}
	if file.Audio == nil || file.Audio.Title != "Cut To The Feeling" || file.Audio.Performer != "Carly Rae Jepsen" {
		t.Fatalf("unexpected audio metadata: %#v", file.Audio)
	}
	if body, err := os.ReadFile(file.Path); err != nil || string(body) != "fake-mp3-data" {
		t.Fatalf("downloaded audio = %q, %v", body, err)
	}
	if body, err := os.ReadFile(cookiesFile); err != nil || string(body) != sourceCookies {
		t.Fatalf("source cookies changed: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(file.Path), "youtube-cookies.txt")); !os.IsNotExist(err) {
		t.Fatalf("temporary YouTube cookies still exist: %v", err)
	}
	jobDir := filepath.Dir(file.Path)
	if err := file.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Fatalf("temporary directory still exists: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
