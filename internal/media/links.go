package media

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"brsaver/internal/telegram"
)

var plainURLPattern = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s<>"']+`)

type Link struct {
	URL      string
	Provider string
}

func ExtractLinks(text string, entities []telegram.MessageEntity) []Link {
	candidates := make([]string, 0, len(entities)+2)
	for _, entity := range entities {
		switch entity.Type {
		case "text_link":
			candidates = append(candidates, entity.URL)
		case "url":
			if value, ok := utf16Slice(text, entity.Offset, entity.Length); ok {
				candidates = append(candidates, value)
			}
		}
	}
	candidates = append(candidates, plainURLPattern.FindAllString(text, -1)...)

	seen := make(map[string]struct{}, len(candidates))
	result := make([]Link, 0, len(candidates))
	for _, candidate := range candidates {
		link, ok := supportedLink(candidate)
		if !ok {
			continue
		}
		if _, exists := seen[link.URL]; exists {
			continue
		}
		seen[link.URL] = struct{}{}
		result = append(result, link)
	}
	return result
}

func supportedLink(raw string) (Link, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,!?;:)]}>'\"…")
	if strings.HasPrefix(strings.ToLower(raw), "www.") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return Link{}, false
	}
	if parsed.Port() != "" {
		return Link{}, false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	path := strings.ToLower(parsed.EscapedPath())

	provider := ""
	switch {
	case hostMatches(host, "tiktok.com"):
		provider = "TikTok"
	case hostMatches(host, "youtube.com") && strings.HasPrefix(path, "/shorts/"):
		provider = "YouTube Shorts"
	case hostMatches(host, "instagram.com") && hasAnyPrefix(path, "/reel/", "/reels/", "/p/", "/tv/", "/share/reel/"):
		provider = "Instagram"
	case host == "fb.watch":
		provider = "Facebook"
	case hostMatches(host, "facebook.com") && (hasAnyPrefix(path, "/reel/", "/reels/", "/watch", "/share/r/", "/share/v/") || strings.Contains(path, "/videos/")):
		provider = "Facebook"
	case host == "open.spotify.com" && spotifyTrackID(parsed) != "":
		provider = "Spotify"
	case isThreadsHost(host) && threadsPostCode(parsed) != "":
		provider = "Threads"
	default:
		return Link{}, false
	}

	parsed.Fragment = ""
	if provider == "Spotify" {
		parsed.Scheme = "https"
		parsed.Host = "open.spotify.com"
		parsed.Path = "/track/" + spotifyTrackID(parsed)
		parsed.RawPath = ""
		parsed.RawQuery = ""
	}
	return Link{URL: parsed.String(), Provider: provider}, true
}

func spotifyTrackID(parsed *url.URL) string {
	if parsed == nil || strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".") != "open.spotify.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 3 && strings.HasPrefix(strings.ToLower(parts[0]), "intl-") {
		parts = parts[1:]
	}
	if len(parts) != 2 || strings.ToLower(parts[0]) != "track" || len(parts[1]) != 22 {
		return ""
	}
	for _, character := range parts[1] {
		if character < '0' || character > '9' {
			if character < 'A' || character > 'Z' {
				if character < 'a' || character > 'z' {
					return ""
				}
			}
		}
	}
	return parts[1]
}

// threadsPostCode accepts /@user/post/CODE and short /t/CODE post links.
func threadsPostCode(parsed *url.URL) string {
	if parsed == nil || !isThreadsHost(strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")) {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	code := ""
	switch {
	case len(parts) >= 3 && len(parts[0]) > 1 && strings.HasPrefix(parts[0], "@") && strings.ToLower(parts[1]) == "post":
		code = parts[2]
	case len(parts) >= 2 && strings.ToLower(parts[0]) == "t":
		code = parts[1]
	}
	if len(code) < 5 || len(code) > 64 {
		return ""
	}
	for _, character := range code {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && character != '_' && character != '-' {
			return ""
		}
	}
	return code
}

func isThreadsHost(host string) bool {
	return hostMatches(host, "threads.com") || hostMatches(host, "threads.net")
}

func hostMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func hasAnyPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func utf16Slice(value string, offset, length int) (string, bool) {
	if offset < 0 || length <= 0 {
		return "", false
	}
	units := utf16.Encode([]rune(value))
	end := offset + length
	if offset > len(units) || end > len(units) || end < offset {
		return "", false
	}
	return string(utf16.Decode(units[offset:end])), true
}
