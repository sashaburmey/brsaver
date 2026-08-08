package media

import (
	"testing"
	"unicode/utf16"

	"brsaver/internal/telegram"
)

func TestExtractLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		entities []telegram.MessageEntity
		wantURL  string
		provider string
	}{
		{
			name:     "TikTok plain URL",
			text:     "Глянь https://vm.tiktok.com/ZM123/.",
			wantURL:  "https://vm.tiktok.com/ZM123/",
			provider: "TikTok",
		},
		{
			name:     "YouTube Shorts URL",
			text:     "https://www.youtube.com/shorts/abc123?si=test",
			wantURL:  "https://www.youtube.com/shorts/abc123?si=test",
			provider: "YouTube Shorts",
		},
		{
			name: "Instagram text link",
			text: "🔥 ось відео",
			entities: []telegram.MessageEntity{{
				Type: "text_link", Offset: 3, Length: 10, URL: "https://instagram.com/reel/ABC/",
			}},
			wantURL:  "https://instagram.com/reel/ABC/",
			provider: "Instagram",
		},
		{
			name:     "Facebook reel",
			text:     "www.facebook.com/share/r/AbCd/",
			wantURL:  "https://www.facebook.com/share/r/AbCd/",
			provider: "Facebook",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			links := ExtractLinks(test.text, test.entities)
			if len(links) != 1 {
				t.Fatalf("got %d links, want 1: %#v", len(links), links)
			}
			if links[0].URL != test.wantURL || links[0].Provider != test.provider {
				t.Fatalf("got %#v, want URL %q from %q", links[0], test.wantURL, test.provider)
			}
		})
	}
}

func TestExtractLinksUsesTelegramUTF16Offsets(t *testing.T) {
	t.Parallel()

	prefix := "🔥 дивись: "
	link := "https://www.instagram.com/reel/ABC123/"
	text := prefix + link
	entities := []telegram.MessageEntity{{
		Type:   "url",
		Offset: len(utf16.Encode([]rune(prefix))),
		Length: len(utf16.Encode([]rune(link))),
	}}

	got := ExtractLinks(text, entities)
	if len(got) != 1 || got[0].URL != link {
		t.Fatalf("got %#v, want %q", got, link)
	}
}

func TestExtractLinksRejectsUnsupportedOrUnsafeURLs(t *testing.T) {
	t.Parallel()

	text := "https://youtube.com/watch?v=regular https://evil.example/reel/1 https://instagram.com:8080/reel/1"
	if got := ExtractLinks(text, nil); len(got) != 0 {
		t.Fatalf("got unsupported links: %#v", got)
	}
}

func TestExtractLinksDeduplicatesEntitiesAndPlainText(t *testing.T) {
	t.Parallel()

	link := "https://www.youtube.com/shorts/abc"
	entities := []telegram.MessageEntity{{Type: "url", Offset: 0, Length: len(link)}}
	got := ExtractLinks(link, entities)
	if len(got) != 1 {
		t.Fatalf("got %d links, want 1", len(got))
	}
}
