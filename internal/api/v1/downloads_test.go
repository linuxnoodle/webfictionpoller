package v1_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// TestDownloadStatusFallback verifies that /downloads/comics/{id}/status
// returns a sensible "complete" or "failed" status when no in-memory job
// exists yet (e.g. cold restart), based on the chapter's stored `downloaded`
// flag.
func TestDownloadStatusFallback(t *testing.T) {
	srv, tokens, db, _, _ := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)

	// Issue a token directly via the store.
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	// No chapter exists; expect a "failed" fallback (chapter lookup succeeds
	// but Downloaded is false → state=failed per the handler's contract).
	resp := do(t, mux, "GET", "/api/v1/downloads/comics/9999/status", plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%s", resp.Code, resp.Body.String())
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "failed" {
		t.Errorf("expected failed for missing chapter, got %q", status.State)
	}
}

// TestCBZReturns404ForUncachedChapter verifies the CBZ endpoint surfaces a
// clear "no pages cached" error rather than an empty zip when the chapter
// hasn't been downloaded.
func TestCBZReturns404ForUncachedChapter(t *testing.T) {
	srv, tokens, db, store, _, _ := setupServerWithStore(t)
	mux := newAuthenticatedMux(t, srv, db)
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	// Insert a chapter but no pages.
	sid, _ := store.AddComicSeries(models.ComicSeries{
		SourceID:     "x",
		Title:        "x",
		SourceURL:    "https://mangadex.org/title/x",
		ProviderName: "mangadex",
	})
	store.UpsertComicChapters(sid, []models.ComicChapter{{SourceID: "c1", Title: "c1", ChapterNum: "1"}})
	chapters, _ := store.GetComicChapters(sid)
	chapterID := chapters[0].ID

	resp := do(t, mux, "GET", "/api/v1/downloads/comics/"+itoa(chapterID)+"/cbz", plaintext, nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for uncached chapter, got %d, body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "no_pages_cached") {
		t.Errorf("expected no_pages_cached error code, got %q", resp.Body.String())
	}
}

// TestPollStatusAndMetrics verifies the poll-status and metrics endpoints
// surface real data (or sensible "no pool" errors) when the pool is wired.
func TestPollStatusAndMetrics(t *testing.T) {
	srv, tokens, db, _, _ := setupServer(t)
	// Pool is wired in main.go; in tests it's nil. Endpoints should return 503.
	mux := newAuthenticatedMux(t, srv, db)
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	resp := do(t, mux, "GET", "/api/v1/poll/status", plaintext, nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("poll/status without pool: %d (want 503)", resp.Code)
	}

	resp = do(t, mux, "GET", "/api/v1/metrics/providers", plaintext, nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Errorf("metrics without pool: %d (want 503)", resp.Code)
	}
}

// TestProvidersEndpointReturnsRegistry verifies /providers returns the
// built-in catalog when the registry has been populated by provider init().
func TestProvidersEndpointReturnsRegistry(t *testing.T) {
	srv, tokens, db, _, _ := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	// Provider init() only fires when the providers package is imported.
	// This test imports it via the test binary's dep chain (handlers → providers).
	resp := do(t, mux, "GET", "/api/v1/providers", plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("providers: %d", resp.Code)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "royalroad") && !strings.Contains(string(body), "mangadex") {
		t.Errorf("expected provider catalog, got %s", string(body))
	}
}

// itoa is a tiny helper to avoid pulling strconv into every test.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// guard against time import being flagged unused if we strip wait helpers later.
var _ = time.Second
