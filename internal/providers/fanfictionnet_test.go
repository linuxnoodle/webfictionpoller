package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func TestFanfictionNet_MatchURL(t *testing.T) {
	p := NewFanfictionNetProvider()
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.fanfiction.net/s/12345/1/Test-Story", true},
		{"https://fanfiction.net/s/12345", true},
		{"https://www.royalroad.com/fiction/12345/test", false},
	}
	for _, tt := range tests {
		if got := p.MatchURL(tt.url); got != tt.want {
			t.Errorf("MatchURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestFanfictionNet_ExtractStoryID(t *testing.T) {
	p := NewFanfictionNetProvider()
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.fanfiction.net/s/12345/1/Test-Story", "12345"},
		{"https://www.fanfiction.net/s/67890/", "67890"},
		{"https://www.fanfiction.net/s/11111", "11111"},
		{"https://example.com/not-ffn", ""},
	}
	for _, tt := range tests {
		got := p.extractStoryID(tt.url)
		if got != tt.want {
			t.Errorf("extractStoryID(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestFanfictionNet_NameAndAuth(t *testing.T) {
	p := NewFanfictionNetProvider()
	if p.Name() != "fanfictionnet" {
		t.Errorf("Name() = %q, want %q", p.Name(), "fanfictionnet")
	}
	if p.RequiresAuth() {
		t.Error("RequiresAuth() should be false")
	}
}

func TestFanfictionNet_FetchSeriesMetadata(t *testing.T) {
	storyHTML := `<!DOCTYPE html>
<html><body>
<div id="profile_top">
  <b>My Harry Potter Fanfic</b>
  <a href="/u/12345/AuthorName">AuthorName</a>
</div>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody struct {
			CMD string `json:"cmd"`
			URL string `json:"url"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		resp := map[string]interface{}{
			"status": "ok",
			"solution": map[string]string{
				"response": storyHTML,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := NewFanfictionNetProvider()
	p.proxyURL = server.URL

	series, err := p.FetchSeriesMetadata("https://www.fanfiction.net/s/12345/1/Test")
	if err != nil {
		t.Fatalf("FetchSeriesMetadata failed: %v", err)
	}

	if series.Title != "My Harry Potter Fanfic" {
		t.Errorf("Title = %q, want %q", series.Title, "My Harry Potter Fanfic")
	}
	if series.ProviderName != "fanfictionnet" {
		t.Errorf("ProviderName = %q, want %q", series.ProviderName, "fanfictionnet")
	}
}

func TestFanfictionNet_PollUpdates(t *testing.T) {
	storyHTML := `<!DOCTYPE html>
<html><body>
<select id="chap_select">
  <option value="1">Chapter 1: The Beginning</option>
  <option value="2">Chapter 2: The Middle</option>
  <option value="3">Chapter 3: The End</option>
</select>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status": "ok",
			"solution": map[string]string{
				"response": storyHTML,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := NewFanfictionNetProvider()
	p.proxyURL = server.URL

	series := models.Series{
		ID:        1,
		SourceURL: "https://www.fanfiction.net/s/12345/1/Test",
	}

	chapters, err := p.PollUpdates(series)
	if err != nil {
		t.Fatalf("PollUpdates failed: %v", err)
	}

	if len(chapters) != 3 {
		t.Fatalf("expected 3 chapters, got %d", len(chapters))
	}

	if !strings.Contains(chapters[0].URL, "12345/1") {
		t.Errorf("chapter URL = %q, should contain story ID and chapter number", chapters[0].URL)
	}
}

func TestFanfictionNet_SolveError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"status": "error",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := NewFanfictionNetProvider()
	p.proxyURL = server.URL

	_, err := p.solve("https://www.fanfiction.net/s/12345/1/Test")
	if err == nil {
		t.Error("expected error from flaresolverr error status")
	}
	if !strings.Contains(err.Error(), "flaresolverr status") {
		t.Errorf("error = %q, should mention flaresolverr status", err.Error())
	}
}
