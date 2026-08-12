package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	spotifyPageSizeLimit     = 3 * 1024 * 1024
	spotifySearchResultLimit = 8
	spotifyMinimumMatchScore = 72
)

var (
	metaTagPattern         = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	htmlAttrPattern        = regexp.MustCompile(`(?is)([a-zA-Z_:.-]+)\s*=\s*("[^"]*"|'[^']*')`)
	htmlTitlePattern       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	spotifyNextDataPattern = regexp.MustCompile(`(?is)<script[^>]*\bid=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	youtubeIDPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	ignoredMatchTerms      = map[string]struct{}{
		"a": {}, "an": {}, "the": {}, "and": {}, "feat": {}, "featuring": {}, "ft": {},
	}
)

type AudioInfo struct {
	Title      string
	Performer  string
	Duration   int
	MatchScore int
}

type spotifyTrackMetadata struct {
	Title     string
	Performer string
	Duration  int
}

type youtubeCandidate struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	WebpageURL string  `json:"webpage_url"`
	Title      string  `json:"title"`
	Channel    string  `json:"channel"`
	Uploader   string  `json:"uploader"`
	Duration   float64 `json:"duration"`
}

func newSpotifyHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many Spotify redirects")
			}
			if request.URL.Scheme != "https" || strings.ToLower(request.URL.Hostname()) != "open.spotify.com" {
				return errors.New("Spotify redirected to an unexpected host")
			}
			return nil
		},
	}
}

func isSpotifyTrackURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && spotifyTrackID(parsed) != ""
}

func (d *Downloader) downloadSpotifyTrackOnce(ctx context.Context, rawURL string) (_ *DownloadedFile, resultErr error) {
	dir, err := os.MkdirTemp(d.workDir, "spotify-")
	if err != nil {
		return nil, fmt.Errorf("create Spotify job directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("clean failed Spotify download: %w", cleanupErr)
			}
		}
	}()
	youTubeCookiesFile, err := copyCookieFile(d.youTubeCookiesFile, dir, "youtube-cookies.txt")
	if err != nil {
		return nil, err
	}
	if youTubeCookiesFile != "" {
		defer func(path string) {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				keep = false
				if resultErr == nil {
					resultErr = fmt.Errorf("remove temporary YouTube cookies: %w", removeErr)
				}
			}
		}(youTubeCookiesFile)
	}

	metadata, err := d.fetchSpotifyTrackMetadata(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	candidates, err := d.searchYouTubeCandidates(ctx, metadata, youTubeCookiesFile)
	if err != nil {
		return nil, err
	}
	candidate, score, err := selectSpotifyCandidate(metadata, candidates)
	if err != nil {
		return nil, err
	}

	if !youtubeIDPattern.MatchString(candidate.ID) {
		return nil, errors.New("matched YouTube result has an invalid ID")
	}
	videoURL := "https://www.youtube.com/watch?v=" + candidate.ID

	args := []string{
		"--ignore-config",
		"--no-cache-dir",
		"--no-playlist",
		"--no-progress",
		"--no-warnings",
		"--max-filesize", strconv.FormatInt(d.maxAudioFileSize, 10),
		"--format", "bestaudio[ext=m4a]/bestaudio/best",
		"--extract-audio",
		"--audio-format", "mp3",
		"--audio-quality", "0",
		"--print", "after_move:filepath",
		"--paths", dir,
		"--output", "spotify-%(id)s.%(ext)s",
		"--socket-timeout", "30",
	}
	if youTubeCookiesFile != "" {
		args = append(args, "--cookies", youTubeCookiesFile)
	}
	args = append(args, "--", videoURL)
	stdout, stderr, err := d.runSpotifyDownloadCommand(ctx, dir, args)
	if err != nil {
		if errors.Is(err, ErrFileTooLarge) || ctx.Err() != nil {
			return nil, err
		}
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = err.Error()
		}
		detail = strings.ReplaceAll(detail, videoURL, "[URL]")
		return nil, fmt.Errorf("Spotify audio download failed: %s", detail)
	}

	path, err := findOutputPath(dir, stdout)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Spotify audio: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, errors.New("downloaded Spotify audio is not a non-empty regular file")
	}
	if info.Size() > d.maxAudioFileSize {
		return nil, ErrFileTooLarge
	}
	if strings.ToLower(filepath.Ext(path)) != ".mp3" {
		return nil, errors.New("Spotify audio was not converted to MP3")
	}

	keep = true
	return &DownloadedFile{
		Path: path,
		Audio: &AudioInfo{
			Title: metadata.Title, Performer: metadata.Performer,
			Duration: metadata.Duration, MatchScore: score,
		},
		dir: dir,
	}, nil
}

func (d *Downloader) fetchSpotifyTrackMetadata(ctx context.Context, rawURL string) (spotifyTrackMetadata, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return spotifyTrackMetadata{}, errors.New("invalid Spotify track URL")
	}
	trackID := spotifyTrackID(parsed)
	if trackID == "" {
		return spotifyTrackMetadata{}, errors.New("unsupported Spotify URL")
	}

	embedURL := "https://open.spotify.com/embed/track/" + trackID
	embedDocument, embedErr := d.fetchSpotifyDocument(ctx, embedURL)
	if embedErr == nil {
		if metadata, err := parseSpotifyEmbedHTML(embedDocument, trackID); err == nil {
			return metadata, nil
		} else {
			embedErr = err
		}
	}

	pageURL := "https://open.spotify.com/track/" + trackID
	document, pageErr := d.fetchSpotifyDocument(ctx, pageURL)
	if pageErr != nil {
		return spotifyTrackMetadata{}, fmt.Errorf("request Spotify metadata: embed: %v; page: %w", embedErr, pageErr)
	}
	metadata, pageErr := parseSpotifyTrackHTML(document)
	if pageErr != nil {
		return spotifyTrackMetadata{}, fmt.Errorf("parse Spotify metadata: embed: %v; page: %w", embedErr, pageErr)
	}
	return metadata, nil
}

func (d *Downloader) fetchSpotifyDocument(ctx context.Context, pageURL string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/136 Safari/537.36")

	response, err := d.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, spotifyPageSizeLimit+1))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if len(body) > spotifyPageSizeLimit {
		return "", errors.New("response is too large")
	}
	return string(body), nil
}

func parseSpotifyEmbedHTML(document, expectedTrackID string) (spotifyTrackMetadata, error) {
	match := spotifyNextDataPattern.FindStringSubmatch(document)
	if len(match) != 2 {
		return spotifyTrackMetadata{}, errors.New("Spotify embed did not contain __NEXT_DATA__")
	}
	var nextData struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity struct {
							Type     string `json:"type"`
							ID       string `json:"id"`
							Name     string `json:"name"`
							Title    string `json:"title"`
							Duration int64  `json:"duration"`
							Artists  []struct {
								Name string `json:"name"`
							} `json:"artists"`
						} `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(match[1]), &nextData); err != nil {
		return spotifyTrackMetadata{}, fmt.Errorf("decode Spotify embed data: %w", err)
	}
	entity := nextData.Props.PageProps.State.Data.Entity
	if entity.Type != "track" || entity.ID != expectedTrackID {
		return spotifyTrackMetadata{}, errors.New("Spotify embed returned an unexpected entity")
	}
	title := strings.TrimSpace(entity.Title)
	if title == "" {
		title = strings.TrimSpace(entity.Name)
	}
	artistNames := make([]string, 0, len(entity.Artists))
	for _, artist := range entity.Artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			artistNames = append(artistNames, name)
		}
	}
	track := spotifyTrackMetadata{
		Title: title, Performer: strings.Join(artistNames, ", "),
		Duration: int((entity.Duration + 500) / 1000),
	}
	if track.Title == "" || track.Performer == "" || track.Duration <= 0 {
		return spotifyTrackMetadata{}, errors.New("Spotify embed contained incomplete track metadata")
	}
	if len([]rune(track.Title)) > 300 || len([]rune(track.Performer)) > 300 || track.Duration > 6*60*60 {
		return spotifyTrackMetadata{}, errors.New("Spotify embed metadata is outside expected limits")
	}
	return track, nil
}

func parseSpotifyTrackHTML(document string) (spotifyTrackMetadata, error) {
	metadataValues := make(map[string]string)
	for _, tag := range metaTagPattern.FindAllString(document, -1) {
		attributes := make(map[string]string)
		for _, match := range htmlAttrPattern.FindAllStringSubmatch(tag, -1) {
			value := strings.Trim(match[2], `"'`)
			attributes[strings.ToLower(match[1])] = html.UnescapeString(value)
		}
		key := strings.ToLower(attributes["property"])
		if key == "" {
			key = strings.ToLower(attributes["name"])
		}
		if key != "" && attributes["content"] != "" {
			metadataValues[key] = strings.TrimSpace(attributes["content"])
		}
	}

	track := spotifyTrackMetadata{
		Title:     metadataValues["og:title"],
		Performer: metadataValues["music:musician_description"],
	}
	if duration, err := strconv.Atoi(metadataValues["music:duration"]); err == nil {
		track.Duration = duration
	}
	if track.Performer == "" {
		parts := strings.Split(metadataValues["og:description"], " · ")
		if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[1]), strings.TrimSpace(track.Title)) {
			track.Performer = strings.TrimSpace(parts[0])
		}
	}
	if track.Performer == "" {
		if match := htmlTitlePattern.FindStringSubmatch(document); len(match) == 2 {
			pageTitle := html.UnescapeString(strings.TrimSpace(match[1]))
			prefix := track.Title + " - song and lyrics by "
			if strings.HasPrefix(strings.ToLower(pageTitle), strings.ToLower(prefix)) {
				track.Performer = strings.TrimSuffix(strings.TrimSpace(pageTitle[len(prefix):]), " | Spotify")
			}
		}
	}
	track.Title = strings.TrimSpace(track.Title)
	track.Performer = strings.TrimSpace(track.Performer)
	if track.Title == "" || track.Performer == "" || track.Duration <= 0 {
		return spotifyTrackMetadata{}, errors.New("Spotify page did not contain complete track metadata")
	}
	if len([]rune(track.Title)) > 300 || len([]rune(track.Performer)) > 300 || track.Duration > 6*60*60 {
		return spotifyTrackMetadata{}, errors.New("Spotify track metadata is outside expected limits")
	}
	return track, nil
}

func (d *Downloader) searchYouTubeCandidates(ctx context.Context, track spotifyTrackMetadata, cookiesFile string) ([]youtubeCandidate, error) {
	query := track.Performer + " - " + track.Title + " official audio"
	args := []string{
		"--ignore-config",
		"--no-cache-dir",
		"--no-warnings",
		"--flat-playlist",
		"--playlist-end", strconv.Itoa(spotifySearchResultLimit),
		"--dump-single-json",
		"--socket-timeout", "30",
	}
	if cookiesFile != "" {
		args = append(args, "--cookies", cookiesFile)
	}
	args = append(args, "--", "ytsearch"+strconv.Itoa(spotifySearchResultLimit)+":"+query)
	cmd := exec.CommandContext(ctx, d.binary, args...)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("YouTube search failed: %s", detail)
	}

	var result struct {
		Entries []youtubeCandidate `json:"entries"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode YouTube search results: %w", err)
	}
	if len(result.Entries) == 0 || len(result.Entries) > spotifySearchResultLimit {
		return nil, errors.New("YouTube search returned an invalid candidate count")
	}
	return result.Entries, nil
}

func selectSpotifyCandidate(track spotifyTrackMetadata, candidates []youtubeCandidate) (youtubeCandidate, int, error) {
	bestScore := -1
	var best youtubeCandidate
	for _, candidate := range candidates {
		score, acceptable := spotifyCandidateScore(track, candidate)
		if acceptable && score > bestScore {
			best, bestScore = candidate, score
		}
	}
	if bestScore < spotifyMinimumMatchScore {
		return youtubeCandidate{}, 0, ErrNoReliableAudioMatch
	}
	return best, bestScore, nil
}

func spotifyCandidateScore(track spotifyTrackMetadata, candidate youtubeCandidate) (int, bool) {
	titleTerms := significantTerms(track.Title)
	artistTerms := significantTerms(track.Performer)
	if len(titleTerms) == 0 || len(artistTerms) == 0 || strings.TrimSpace(candidate.Title) == "" {
		return 0, false
	}

	candidateTitleTerms := termSet(candidate.Title)
	candidateAllTerms := termSet(candidate.Title + " " + candidate.Channel + " " + candidate.Uploader)
	titleCoverage := termCoverage(titleTerms, candidateTitleTerms)
	artistCoverage := termCoverageLoose(artistTerms, candidateAllTerms, normalizeMatchText(candidate.Title+" "+candidate.Channel+" "+candidate.Uploader))
	if titleCoverage < 0.75 || artistCoverage < 0.5 {
		return 0, false
	}

	score := int(titleCoverage*50 + artistCoverage*25)
	targetNormalized := normalizeMatchText(track.Title)
	candidateNormalized := normalizeMatchText(candidate.Title)
	if targetNormalized != "" && strings.Contains(candidateNormalized, targetNormalized) {
		score += 8
	}

	duration := int(candidate.Duration + 0.5)
	if duration <= 0 {
		return 0, false
	}
	difference := absInt(duration - track.Duration)
	switch {
	case difference <= 4:
		score += 22
	case difference <= 10:
		score += 16
	case difference <= 20:
		score += 7
	case difference <= 30:
		score -= 15
	default:
		return 0, false
	}

	targetTerms := termSet(track.Title)
	for _, suspicious := range []string{"live", "remix", "cover", "karaoke", "instrumental", "nightcore", "slowed", "sped"} {
		if _, present := candidateTitleTerms[suspicious]; present {
			if _, expected := targetTerms[suspicious]; !expected {
				score -= 28
			}
		}
	}
	if strings.Contains(candidateNormalized, "official audio") || strings.Contains(normalizeMatchText(candidate.Channel), "topic") {
		score += 5
	}
	return score, score >= spotifyMinimumMatchScore
}

func significantTerms(value string) []string {
	seen := make(map[string]struct{})
	terms := make([]string, 0)
	for _, term := range strings.Fields(normalizeMatchText(value)) {
		if _, ignored := ignoredMatchTerms[term]; ignored {
			continue
		}
		if _, exists := seen[term]; exists {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

func termSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, term := range strings.Fields(normalizeMatchText(value)) {
		result[term] = struct{}{}
	}
	return result
}

func termCoverage(required []string, available map[string]struct{}) float64 {
	if len(required) == 0 {
		return 0
	}
	matched := 0
	for _, term := range required {
		if _, exists := available[term]; exists {
			matched++
		}
	}
	return float64(matched) / float64(len(required))
}

func termCoverageLoose(required []string, available map[string]struct{}, normalized string) float64 {
	if len(required) == 0 {
		return 0
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	matched := 0
	for _, term := range required {
		if _, exists := available[term]; exists || (len([]rune(term)) >= 4 && strings.Contains(compact, term)) {
			matched++
		}
	}
	return float64(matched) / float64(len(required))
}

func normalizeMatchText(value string) string {
	var builder strings.Builder
	space := true
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			builder.WriteRune(character)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func (d *Downloader) runSpotifyDownloadCommand(ctx context.Context, dir string, args []string) (string, string, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, d.binary, args...)
	var stdout bytes.Buffer
	var stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", stderr.String(), fmt.Errorf("start Spotify audio download: %w", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ticker := time.NewTicker(d.sizeCheckGap)
	defer ticker.Stop()
	for {
		select {
		case err := <-wait:
			return stdout.String(), stderr.String(), err
		case <-ticker.C:
			size, err := directorySize(dir)
			if err != nil {
				cancel()
				<-wait
				return stdout.String(), stderr.String(), err
			}
			if size > d.maxAudioFileSize*2 {
				cancel()
				<-wait
				return stdout.String(), stderr.String(), ErrFileTooLarge
			}
		case <-ctx.Done():
			cancel()
			<-wait
			return stdout.String(), stderr.String(), ctx.Err()
		}
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
