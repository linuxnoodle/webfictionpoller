package providers

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

func TestXenForo_MatchURL(t *testing.T) {
	tests := []struct {
		provider Provider
		url      string
		want     bool
	}{
		{NewSpaceBattlesProvider(), "https://forums.spacebattles.com/threads/test.12345/", true},
		{NewSpaceBattlesProvider(), "https://forum.questionablequesting.com/threads/test.12345/", false},
		{NewQuestionableQuestingProvider(), "https://forum.questionablequesting.com/threads/test.12345/", true},
		{NewQuestionableQuestingProvider(), "https://forums.spacebattles.com/threads/test.12345/", false},
		{NewSufficientVelocityProvider(), "https://forums.sufficientvelocity.com/threads/test.67890/", true},
		{NewSufficientVelocityProvider(), "https://forums.spacebattles.com/threads/test.12345/", false},
	}

	for _, tt := range tests {
		if got := tt.provider.MatchURL(tt.url); got != tt.want {
			t.Errorf("MatchURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestXenForo_RequiresAuth(t *testing.T) {
	sb := NewSpaceBattlesProvider()
	if sb.RequiresAuth() {
		t.Error("SpaceBattles should not require auth")
	}

	qq := NewQuestionableQuestingProvider()
	if !qq.RequiresAuth() {
		t.Error("QuestionableQuesting should require auth")
	}

	sv := NewSufficientVelocityProvider()
	if sv.RequiresAuth() {
		t.Error("SufficientVelocity should not require auth")
	}
}

func TestXenForo_NormalizeThreadURL(t *testing.T) {
	p := NewSpaceBattlesProvider()
	tests := []struct {
		input string
		want  string
	}{
		{
			"https://forums.spacebattles.com/threads/test.12345/unread",
			"https://forums.spacebattles.com/threads/test.12345",
		},
		{
			"https://forums.spacebattles.com/threads/test.12345/page-3",
			"https://forums.spacebattles.com/threads/test.12345",
		},
		{
			"https://forums.spacebattles.com/threads/test.12345/",
			"https://forums.spacebattles.com/threads/test.12345",
		},
		{
			"https://forums.spacebattles.com/threads/test.12345",
			"https://forums.spacebattles.com/threads/test.12345",
		},
		{
			"https://forums.spacebattles.com/threads/test.12345/threadmarks.rss?threadmark_category=1",
			"https://forums.spacebattles.com/threads/test.12345",
		},
	}

	for _, tt := range tests {
		got := p.normalizeThreadURL(tt.input)
		if got != tt.want {
			t.Errorf("normalizeThreadURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestXenForo_ExtractTitleFromURL(t *testing.T) {
	p := NewSpaceBattlesProvider()
	tests := []struct {
		url  string
		want string
	}{
		{"https://forums.spacebattles.com/threads/my-awesome-story.12345/", "my awesome story"},
		{"https://forums.spacebattles.com/threads/simple.99999/", "simple"},
		{"https://example.com/not-a-thread", "https://example.com/not-a-thread"},
	}

	for _, tt := range tests {
		got := p.extractTitleFromURL(tt.url)
		if got != tt.want {
			t.Errorf("extractTitleFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestXenForo_BuildThreadmarksRSSURL(t *testing.T) {
	p := NewSpaceBattlesProvider()
	tests := []struct {
		input string
		want  string
	}{
		{
			"https://forums.spacebattles.com/threads/test.12345",
			"https://forums.spacebattles.com/threads/test.12345/threadmarks.rss",
		},
		{
			"https://forums.spacebattles.com/threads/test.12345/",
			"https://forums.spacebattles.com/threads/test.12345/threadmarks.rss",
		},
	}

	for _, tt := range tests {
		got := p.buildThreadmarksRSSURL(tt.input)
		if got != tt.want {
			t.Errorf("buildThreadmarksRSSURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestXenForo_SetCookies(t *testing.T) {
	p := NewQuestionableQuestingProvider()
	err := p.SetCookies("xf_session=abc123; other_cookie=value")
	if err != nil {
		t.Fatalf("SetCookies failed: %v", err)
	}

	err = p.SetCookies("")
	if err != nil {
		t.Fatalf("SetCookies with empty string failed: %v", err)
	}
}

func TestXenForo_FetchSeriesMetadata(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
<div class="p-title-value"><h1>My Awesome Thread</h1></div>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	}))
	defer server.Close()

	p := NewSpaceBattlesProvider()
	p.client = server.Client()

	series, err := p.FetchSeriesMetadata(server.URL + "/threads/test.12345/")
	if err != nil {
		t.Fatalf("FetchSeriesMetadata failed: %v", err)
	}

	if series.Title != "My Awesome Thread" {
		t.Errorf("Title = %q, want %q", series.Title, "My Awesome Thread")
	}
	if series.ProviderName != "spacebattles" {
		t.Errorf("ProviderName = %q, want %q", series.ProviderName, "spacebattles")
	}
	if !strings.HasSuffix(series.SourceURL, "/threads/test.12345") {
		t.Errorf("SourceURL = %q, should be normalized", series.SourceURL)
	}
}

func TestXenForo_PollUpdates(t *testing.T) {
	rssXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Thread</title>
    <item>
      <title>Chapter 1</title>
      <link>https://forums.spacebattles.com/threads/test.12345/post-1</link>
      <pubDate>Mon, 15 Jan 2024 10:00:00 +0000</pubDate>
    </item>
    <item>
      <title>Chapter 2</title>
      <link>https://forums.spacebattles.com/threads/test.12345/post-2</link>
      <pubDate>Tue, 16 Jan 2024 10:00:00 +0000</pubDate>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "threadmarks.rss") {
			t.Errorf("expected request to threadmarks.rss, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(rssXML))
	}))
	defer server.Close()

	p := NewSpaceBattlesProvider()
	p.client = server.Client()

	series := models.Series{
		ID:        1,
		SourceURL: server.URL + "/threads/test.12345",
	}

	chapters, err := p.PollUpdates(series)
	if err != nil {
		t.Fatalf("PollUpdates failed: %v", err)
	}

	if len(chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(chapters))
	}
	if chapters[0].Title != "Chapter 1" {
		t.Errorf("chapters[0].Title = %q, want %q", chapters[0].Title, "Chapter 1")
	}
}

func TestXenForo_PollUpdates_ReloginOnAuthFailure(t *testing.T) {
	var loginCalls int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body><input name="_xfToken" value="testtoken"></body></html>`)
		case r.URL.Path == "/login/login":
			atomic.AddInt32(&loginCalls, 1)
			http.SetCookie(w, &http.Cookie{Name: "xf_user", Value: "abc123", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><body>OK</body></html>`)
		case strings.HasSuffix(r.URL.Path, "/threadmarks.rss"):
			if atomic.LoadInt32(&loginCalls) == 0 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>T</title>
<item><title>Chapter 1</title><link>`+server.URL+`/threads/test.1/post-1</link>
<pubDate>Mon, 15 Jan 2024 10:00:00 +0000</pubDate></item>
</channel></rss>`)
		case strings.HasSuffix(r.URL.Path, "/threadmarks"):
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := NewQuestionableQuestingProvider()
	p.baseURL = server.URL
	p.client = server.Client()
	p.client.Jar, _ = cookiejar.New(nil)

	p.SetCredentialSource(func() (string, string, bool) {
		return "user", "pass", true
	})

	series := models.Series{
		ID:           1,
		SourceURL:    server.URL + "/threads/test.1",
		ProviderName: "questionablequesting",
	}

	chapters, err := p.PollUpdates(series)
	if err != nil {
		t.Fatalf("PollUpdates should have self-healed via re-login, got: %v", err)
	}
	if len(chapters) != 1 {
		t.Fatalf("expected 1 chapter after re-login, got %d", len(chapters))
	}
	if chapters[0].Title != "Chapter 1" {
		t.Errorf("chapter title = %q, want %q", chapters[0].Title, "Chapter 1")
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("expected exactly 1 login call, got %d", got)
	}
}

func TestXenForo_PollUpdates_AuthFailure_NoCredentialSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	p := NewQuestionableQuestingProvider()
	p.baseURL = server.URL
	p.client = server.Client()
	p.client.Jar, _ = cookiejar.New(nil)

	_, err := p.PollUpdates(models.Series{ID: 1, SourceURL: server.URL + "/threads/test.1"})
	if err == nil {
		t.Fatal("expected errAuthRequired, got nil")
	}
	if !errors.Is(err, errAuthRequired) {
		t.Errorf("expected errAuthRequired, got: %v", err)
	}
}

func TestXenForo_ReloginCooldown(t *testing.T) {
	var loginCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/":
			io.WriteString(w, `<html><body><input name="_xfToken" value="t"></body></html>`)
		case r.URL.Path == "/login/login":
			atomic.AddInt32(&loginCalls, 1)
			http.SetCookie(w, &http.Cookie{Name: "xf_user", Value: "x", Path: "/"})
			io.WriteString(w, `<html>OK</html>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := NewQuestionableQuestingProvider()
	p.baseURL = server.URL
	p.client = server.Client()
	p.client.Jar, _ = cookiejar.New(nil)
	p.SetCredentialSource(func() (string, string, bool) { return "u", "p", true })

	if !p.tryRelogin() {
		t.Error("first tryRelogin should succeed")
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("expected 1 login call after first tryRelogin, got %d", got)
	}

	if !p.tryRelogin() {
		t.Error("second tryRelogin (within cooldown) should report success because last login was OK")
	}
	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("login should not have been called again within cooldown, got %d calls", got)
	}

	p.lastLoginAttempt = p.lastLoginAttempt.Add(-reloginCooldown - 1)
	if !p.tryRelogin() {
		t.Error("third tryRelogin (after cooldown) should succeed")
	}
	if got := atomic.LoadInt32(&loginCalls); got != 2 {
		t.Errorf("expected 2 login calls after cooldown expired, got %d", got)
	}
}

func TestXenForo_ReloginFailed_PropagatesAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/":
			io.WriteString(w, `<html><body><input name="_xfToken" value="t"></body></html>`)
		case r.URL.Path == "/login/login":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()

	p := NewQuestionableQuestingProvider()
	p.baseURL = server.URL
	p.client = server.Client()
	p.client.Jar, _ = cookiejar.New(nil)
	p.SetCredentialSource(func() (string, string, bool) { return "u", "p", true })

	_, err := p.PollUpdates(models.Series{ID: 1, SourceURL: server.URL + "/threads/test.1"})
	if err == nil {
		t.Fatal("expected error when re-login fails")
	}
	if !errors.Is(err, errAuthRequired) {
		t.Errorf("expected errAuthRequired when re-login fails, got: %v", err)
	}
}
