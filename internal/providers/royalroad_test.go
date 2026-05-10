package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func TestRoyalRoad_MatchURL(t *testing.T) {
	p := NewRoyalRoadProvider()
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.royalroad.com/fiction/12345/test-fiction", true},
		{"https://royalroad.com/fiction/123", true},
		{"https://www.fanfiction.net/s/12345/1/", false},
		{"https://forums.spacebattles.com/threads/test.12345/", false},
	}
	for _, tt := range tests {
		if got := p.MatchURL(tt.url); got != tt.want {
			t.Errorf("MatchURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestRoyalRoad_BuildRSSURL(t *testing.T) {
	p := NewRoyalRoadProvider()
	tests := []struct {
		url  string
		want string
	}{
		{
			"https://www.royalroad.com/fiction/12345/test-fiction",
			"https://www.royalroad.com/syndication/12345",
		},
		{
			"https://www.royalroad.com/fiction/67890",
			"https://www.royalroad.com/syndication/67890",
		},
		{
			"https://www.example.com/not-royalroad",
			"",
		},
	}
	for _, tt := range tests {
		got := p.buildRSSURL(tt.url)
		if got != tt.want {
			t.Errorf("buildRSSURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestRoyalRoad_PollScrape(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
<div id="chapters">
	<div class="chapter-row">
		<a href="/fiction/12345/chapter/1/first">First Chapter</a>
		<time datetime="2024-01-15T10:00:00Z"></time>
	</div>
	<div class="chapter-row">
		<a href="/fiction/12345/chapter/2/second">Second Chapter</a>
		<time datetime="2024-01-16T10:00:00Z"></time>
	</div>
</div>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent header not set")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer server.Close()

	p := NewRoyalRoadProvider()
	p.client = server.Client()

	series := models.Series{
		ID:        1,
		SourceURL: server.URL + "/fiction/12345/test",
	}

	chapters, err := p.pollScrape(series)
	if err != nil {
		t.Fatalf("pollScrape failed: %v", err)
	}

	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}

	if chapters[0].Title != "First Chapter" {
		t.Errorf("chapter[0].Title = %q, want %q", chapters[0].Title, "First Chapter")
	}

	if !strings.HasPrefix(chapters[0].URL, "https://www.royalroad.com") {
		t.Errorf("chapter[0].URL = %q, expected absolute URL", chapters[0].URL)
	}
}

func TestRoyalRoad_FetchSeriesMetadata(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
<div class="fic-title"><h1>Test Fiction Title</h1></div>
<div class="fic-header"><span class="author"><a href="/profile/1">Author Name</a></span></div>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer server.Close()

	p := NewRoyalRoadProvider()
	p.client = server.Client()

	series, err := p.FetchSeriesMetadata(server.URL + "/fiction/12345/test")
	if err != nil {
		t.Fatalf("FetchSeriesMetadata failed: %v", err)
	}

	if series.Title != "Test Fiction Title" {
		t.Errorf("Title = %q, want %q", series.Title, "Test Fiction Title")
	}
	if series.Author != "Author Name" {
		t.Errorf("Author = %q, want %q", series.Author, "Author Name")
	}
	if series.ProviderName != "royalroad" {
		t.Errorf("ProviderName = %q, want %q", series.ProviderName, "royalroad")
	}
}

func TestRoyalRoad_NameAndAuth(t *testing.T) {
	p := NewRoyalRoadProvider()
	if p.Name() != "royalroad" {
		t.Errorf("Name() = %q, want %q", p.Name(), "royalroad")
	}
	if p.RequiresAuth() {
		t.Error("RequiresAuth() should be false")
	}
}
