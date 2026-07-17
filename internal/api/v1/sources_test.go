package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

// TestSourceEndpoints exercises the full v1 multi-source API surface against
// a freshly seeded series.
func TestSourceEndpoints(t *testing.T) {
	srv, tokens, db, store, _, _ := setupServerWithStore(t)
	mux := newAuthenticatedMux(t, srv, db)
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	// Seed a series. AddSeries auto-creates the primary source.
	sid, err := store.AddSeries(models.Series{
		Title:        "Source API Test",
		SourceURL:    "https://www.royalroad.com/fiction/4242/x",
		ProviderName: "royalroad",
		Status:       "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	// GET sources: should have 1 (the auto-seeded primary).
	resp := do(t, mux, "GET", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list: %d, body=%s", resp.Code, resp.Body.String())
	}
	var listed struct {
		Sources []models.SeriesSource `json:"sources"`
	}
	json.Unmarshal(resp.Body.Bytes(), &listed)
	if len(listed.Sources) != 1 || !listed.Sources[0].IsPrimary {
		t.Fatalf("expected 1 primary source, got %+v", listed.Sources)
	}

	// POST: add an alternate AO3 source.
	resp = do(t, mux, "POST", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, map[string]interface{}{
		"provider_name": "ao3",
		"source_url":    "https://archiveofourown.org/works/9999",
		"priority":      100,
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("add: %d, body=%s", resp.Code, resp.Body.String())
	}
	var created models.SeriesSource
	json.Unmarshal(resp.Body.Bytes(), &created)
	if created.IsPrimary {
		t.Error("new source should not be primary")
	}

	// GET again: 2 sources now, primary still royalroad.
	resp = do(t, mux, "GET", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, nil)
	json.Unmarshal(resp.Body.Bytes(), &listed)
	if len(listed.Sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(listed.Sources))
	}

	// POST promote: alt becomes primary.
	resp = do(t, mux, "POST",
		"/api/v1/library/"+strconvI(sid)+"/sources/"+strconvI(created.ID)+"/promote",
		plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("promote: %d, body=%s", resp.Code, resp.Body.String())
	}
	resp = do(t, mux, "GET", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, nil)
	json.Unmarshal(resp.Body.Bytes(), &listed)
	for _, s := range listed.Sources {
		if s.ID == created.ID && !s.IsPrimary {
			t.Error("alt should now be primary")
		}
	}

	// PATCH: disable + bump priority.
	resp = do(t, mux, "PATCH",
		"/api/v1/library/"+strconvI(sid)+"/sources/"+strconvI(created.ID),
		plaintext, map[string]interface{}{"priority": 25, "disabled": true})
	if resp.Code != http.StatusOK {
		t.Fatalf("patch: %d, body=%s", resp.Code, resp.Body.String())
	}

	// DELETE: cannot delete last source. Promote the original first so alt
	// isn't the only one (we deleted the alt).
	resp = do(t, mux, "POST",
		"/api/v1/library/"+strconvI(sid)+"/sources/"+strconvI(listed.Sources[0].ID)+"/promote",
		plaintext, nil)
	resp = do(t, mux, "DELETE",
		"/api/v1/library/"+strconvI(sid)+"/sources/"+strconvI(created.ID),
		plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("delete: %d, body=%s", resp.Code, resp.Body.String())
	}

	// DELETE last remaining: should 409.
	resp = do(t, mux, "GET", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, nil)
	json.Unmarshal(resp.Body.Bytes(), &listed)
	if len(listed.Sources) != 1 {
		t.Fatalf("expected 1 source remaining, got %d", len(listed.Sources))
	}
	resp = do(t, mux, "DELETE",
		"/api/v1/library/"+strconvI(sid)+"/sources/"+strconvI(listed.Sources[0].ID),
		plaintext, nil)
	if resp.Code != http.StatusConflict {
		t.Errorf("deleting last source: %d (want 409), body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "last_source") {
		t.Errorf("expected last_source error, got %s", resp.Body.String())
	}
}

// TestAddSourceRejectsMismatchedURL verifies the provider MatchURL check.
func TestAddSourceRejectsMismatchedURL(t *testing.T) {
	srv, tokens, db, store, _, _ := setupServerWithStore(t)
	mux := newAuthenticatedMux(t, srv, db)
	plaintext, _, _ := tokens.IssueToken(context.Background(), 1, "test", "")

	sid, _ := store.AddSeries(models.Series{
		Title:        "Mismatch Test",
		SourceURL:    "https://www.royalroad.com/fiction/1/x",
		ProviderName: "royalroad",
		Status:       "active",
	})

	// Try to add a royalroad provider but with an AO3 URL.
	resp := do(t, mux, "POST", "/api/v1/library/"+strconvI(sid)+"/sources", plaintext, map[string]interface{}{
		"provider_name": "royalroad",
		"source_url":    "https://archiveofourown.org/works/1",
	})
	if resp.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for URL/provider mismatch, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "url_mismatch") {
		t.Errorf("expected url_mismatch error, got %s", resp.Body.String())
	}
}

func strconvI(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
