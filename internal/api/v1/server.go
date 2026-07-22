package v1

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/api"
	"github.com/linuxnoodle/webfictionpoller/internal/auth"
	"github.com/linuxnoodle/webfictionpoller/internal/blob"
	"github.com/linuxnoodle/webfictionpoller/internal/comics"
	"github.com/linuxnoodle/webfictionpoller/internal/db"
	"github.com/linuxnoodle/webfictionpoller/internal/download"
	"github.com/linuxnoodle/webfictionpoller/internal/handlers"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

// Server bundles the dependencies every v1 handler needs. Construct once at
// startup; the router methods close over it.
type Server struct {
	db        *db.DB
	tokens    *api.TokenStore
	store     Store
	pool      *worker.WorkerPool
	downloads *download.Tracker
	blob      blob.Store
}

func NewServer(database *db.DB, tokens *api.TokenStore, store Store) *Server {
	return &Server{db: database, tokens: tokens, store: store, downloads: download.NewTracker()}
}

// SetPool wires the worker pool. Optional; if unset, /poll/* and /metrics
// return 503.
func (s *Server) SetPool(p *worker.WorkerPool) { s.pool = p }

// SetBlobStore wires the blob backend so download endpoints can return
// presigned MinIO URLs when available. Optional.
func (s *Server) SetBlobStore(b blob.Store) { s.blob = b }

// Routes returns the /api/v1 subrouter. authz is the middleware chain that
// applies to every authenticated route; the /auth/* group is gated separately
// by HasUsersGate so login is reachable on first run.
func (s *Server) Routes(authz func(http.Handler) http.Handler, hasUsersGate func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/openapi.json", s.openAPISpec)

	// /auth/login is always public — it's how clients obtain a token.
	r.Post("/auth/login", s.authLogin)

	r.Group(func(r chi.Router) {
		r.Use(authz)
		r.Get("/auth/me", s.authMe)
		r.Post("/auth/logout", s.authLogout)

		// Token management (must be authenticated as a user, not just any token).
		r.Get("/tokens", s.listTokens)
		r.Post("/tokens", s.issueToken)
		r.Delete("/tokens/{id}", s.revokeToken)

		// Library + chapter discovery.
		r.Get("/library", s.libraryIndex)
		r.Get("/library/{id}", s.libraryDetail)
		r.Get("/library/{id}/sources", s.listSeriesSources)
		r.Post("/library/{id}/sources", s.addSeriesSource)
		r.Patch("/library/{id}/sources/{sourceID}", s.updateSeriesSource)
		r.Delete("/library/{id}/sources/{sourceID}", s.deleteSeriesSource)
		r.Post("/library/{id}/sources/{sourceID}/promote", s.promoteSeriesSource)
		r.Get("/chapters", s.chapterList)
		r.Get("/chapters/{id}", s.chapterGet)
		r.Get("/chapters/{id}/content", s.chapterContent)
		r.Post("/chapters/{id}/cache", s.cacheChapterContent)
		r.Post("/chapters/{id}/read", s.chapterMarkRead)
		r.Get("/unread-count", s.unreadCount)

		// Polling status / triggers.
		r.Get("/poll/status", s.pollStatus)
		r.Post("/poll/now", s.pollNow)
		r.Get("/metrics/providers", s.providerMetrics)

		// Downloads (offline reading). Mirrors the comic_handlers endpoints
		// but lives under the versioned API and surfaces presigned MinIO URLs
		// when the blob backend supports them.
		r.Post("/downloads/comics/{chapterID}", s.downloadComicChapter)
		r.Get("/downloads/comics/{chapterID}/status", s.downloadComicChapterStatus)
		r.Get("/downloads/comics/{chapterID}/cbz", s.downloadComicChapterCBZ)

		// Text chapter downloads (for iOS offline reading).
		r.Get("/downloads/chapters/{chapterID}", s.downloadTextChapter)

		// Provider introspection.
		r.Get("/providers", s.providersList)
	})
	return r
}

// ---------------------------------------------------------------------------
// Auth + token endpoints
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Label    string `json:"label"`    // device label for the issued token
	DeviceID string `json:"device_id"` // optional, client-generated
}

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	UserID    int64     `json:"user_id"`
	Username  string    `json:"username"`
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !api.JSONDecode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		api.WriteError(w, http.StatusBadRequest, "missing_credentials", "")
		return
	}
	uid, err := auth.Authenticate(s.db, req.Username, req.Password)
	if err != nil {
		logging.Info("[api/v1] failed login for %q from %s", req.Username, r.RemoteAddr)
		api.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "")
		return
	}
	label := req.Label
	if label == "" {
		label = "iOS " + time.Now().Format("2006-01-02")
	}
	plaintext, tok, err := s.tokens.IssueToken(r.Context(), uid, label, req.DeviceID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "token_issue_failed", err.Error())
		return
	}
	logging.Info("[api/v1] issued token for %q (label=%q)", req.Username, label)
	api.WriteJSON(w, http.StatusOK, loginResponse{
		Token:     plaintext,
		ExpiresAt: derefTime(tok.ExpiresAt),
		UserID:    uid,
		Username:  req.Username,
	})
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := api.UserIDFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	var username string
	_ = s.db.QueryRowContext(r.Context(), `SELECT username FROM users WHERE id = ?`, uid).Scan(&username)
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":  uid,
		"username": username,
	})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	// Stateless bearer tokens can't be "logged out" without revocation.
	// Clients just discard the token. We expose this endpoint for symmetry
	// with the session-based web logout.
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

type createTokenRequest struct {
	Label    string `json:"label"`
	DeviceID string `json:"device_id"`
}

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := api.UserIDFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	var req createTokenRequest
	if !api.JSONDecode(w, r, &req) {
		return
	}
	if req.Label == "" {
		req.Label = "New token " + time.Now().Format("2006-01-02 15:04")
	}
	plaintext, tok, err := s.tokens.IssueToken(r.Context(), uid, req.Label, req.DeviceID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "token_issue_failed", err.Error())
		return
	}
	// Token plaintext is returned ONCE. Client must store it.
	api.WriteJSON(w, http.StatusCreated, map[string]interface{}{
		"token":     plaintext,
		"id":        tok.ID,
		"label":     tok.Label,
		"expires_at": derefTime(tok.ExpiresAt),
		"created_at": tok.CreatedAt,
	})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	uid, ok := api.UserIDFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	tokens, err := s.tokens.ListTokensForUser(r.Context(), uid)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"tokens": tokens})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := api.UserIDFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	if err := s.tokens.RevokeToken(r.Context(), id, uid); err != nil {
		api.WriteError(w, http.StatusNotFound, "token_not_found", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// ---------------------------------------------------------------------------
// Library + chapter endpoints
// ---------------------------------------------------------------------------

func (s *Server) libraryIndex(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "", "text":
		series, err := s.store.ListSeries()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		out := make([]seriesSummary, 0, len(series))
		for _, ser := range series {
			out = append(out, toSeriesSummary(ser))
		}
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{"series": out, "kind": "text"})
	case "comic":
		series, err := s.store.ListComicSeries()
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		out := make([]comicSeriesSummary, 0, len(series))
		for _, ser := range series {
			out = append(out, toComicSeriesSummary(ser))
		}
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{"series": out, "kind": "comic"})
	default:
		api.WriteError(w, http.StatusBadRequest, "invalid_kind", "kind must be text or comic")
	}
}

func (s *Server) libraryDetail(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	if kind == "comic" {
		s.comicLibraryDetail(w, r, id)
		return
	}
	// Default text.
	ser, err := s.store.GetSeriesByID(id)
	if err != nil || ser == nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "")
		return
	}
	chapters, err := s.store.GetReaderChapters(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp := map[string]interface{}{
		"series":   toSeriesSummary(*ser),
		"chapters": toReaderChapters(chapters),
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) comicLibraryDetail(w http.ResponseWriter, r *http.Request, id int64) {
	ser, err := s.store.GetComicSeriesByID(id)
	if err != nil || ser == nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "")
		return
	}
	chapters, err := s.store.GetComicChapters(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	resp := map[string]interface{}{
		"series":   toComicSeriesSummary(*ser),
		"chapters": toComicChapters(chapters),
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Multi-source failover endpoints
// ---------------------------------------------------------------------------

// listSeriesSources returns every source for a series with health metadata.
//
//	GET /api/v1/library/{id}/sources
func (s *Server) listSeriesSources(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	sources, err := s.store.ListSources(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if sources == nil {
		sources = []models.SeriesSource{}
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"sources": sources})
}

type addSourceRequest struct {
	ProviderName string `json:"provider_name"`
	SourceURL    string `json:"source_url"`
	Priority     int    `json:"priority"`
}

// addSeriesSource attaches a new alternate source to a series.
//
//	POST /api/v1/library/{id}/sources
//
// The provider must exist in the registry and its MatchURL must accept the
// supplied URL. The first source for a series auto-becomes the primary.
func (s *Server) addSeriesSource(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	var req addSourceRequest
	if !api.JSONDecode(w, r, &req) {
		return
	}
	if req.ProviderName == "" || req.SourceURL == "" {
		api.WriteError(w, http.StatusBadRequest, "missing_fields", "provider_name and source_url required")
		return
	}
	p, ok := plugin.Default.Get(req.ProviderName)
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "unknown_provider", "no provider named "+req.ProviderName+" registered")
		return
	}
	if !p.MatchURL(req.SourceURL) {
		api.WriteError(w, http.StatusBadRequest, "url_mismatch",
			"URL does not match the provider's expected pattern")
		return
	}
	src, err := s.store.AddSource(id, req.ProviderName, req.SourceURL, req.Priority)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, src)
}

type updateSourceRequest struct {
	Priority int  `json:"priority"` // -1 = leave unchanged
	Disabled bool `json:"disabled"`
}

// updateSeriesSource toggles disabled / adjusts priority.
//
//	PATCH /api/v1/library/{id}/sources/{sourceID}
func (s *Server) updateSeriesSource(w http.ResponseWriter, r *http.Request) {
	sourceID, err := parseID(chi.URLParam(r, "sourceID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_source_id", "")
		return
	}
	var req updateSourceRequest
	if !api.JSONDecode(w, r, &req) {
		return
	}
	if err := s.store.UpdateSource(sourceID, req.Priority, req.Disabled); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// deleteSeriesSource removes a source. Deleting the last remaining source is
// rejected (a series must always have at least one).
//
//	DELETE /api/v1/library/{id}/sources/{sourceID}
func (s *Server) deleteSeriesSource(w http.ResponseWriter, r *http.Request) {
	sourceID, err := parseID(chi.URLParam(r, "sourceID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_source_id", "")
		return
	}
	if err := s.store.DeleteSource(sourceID); err != nil {
		switch err {
		case handlers.ErrSourceNotFound:
			api.WriteError(w, http.StatusNotFound, "source_not_found", "")
		case handlers.ErrLastSource:
			api.WriteError(w, http.StatusConflict, "last_source",
				"cannot delete the only remaining source for a series")
		default:
			api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		}
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// promoteSeriesSource makes a source the primary for its series.
//
//	POST /api/v1/library/{id}/sources/{sourceID}/promote
func (s *Server) promoteSeriesSource(w http.ResponseWriter, r *http.Request) {
	sourceID, err := parseID(chi.URLParam(r, "sourceID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_source_id", "")
		return
	}
	if err := s.store.PromoteSource(sourceID); err != nil {
		if err == handlers.ErrSourceNotFound {
			api.WriteError(w, http.StatusNotFound, "source_not_found", "")
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) chapterList(w http.ResponseWriter, r *http.Request) {
	// Reuse the existing time-view query: most recent chapters across active series.
	pageSize := 50
	page := 0
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := parseIntParam(p); err == nil && n > 0 {
			page = n - 1
		}
	}
	unreadOnly := r.URL.Query().Get("unread") == "true"
	chapters, err := s.store.GetTimeView(page, pageSize, "received", unreadOnly)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	out := make([]chapterFeedItem, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, chapterFeedItem{
			ID:          c.ID,
			SeriesID:    c.SeriesID,
			Title:       c.Title,
			URL:         c.URL,
			PublishedAt: c.PublishedAt,
			IsRead:      c.IsRead,
			SeriesTitle: c.SeriesTitle,
			Provider:    c.ProviderName,
		})
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"chapters": out})
}

func (s *Server) chapterGet(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	ch, err := s.store.GetChapterWithProvider(id)
	if err != nil || ch == nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, chapterFeedItem{
		ID:          ch.ID,
		SeriesID:    ch.SeriesID,
		Title:       ch.Title,
		URL:         ch.URL,
		PublishedAt: ch.PublishedAt,
		IsRead:      ch.IsRead,
		SeriesTitle: ch.SeriesTitle,
		Provider:    ch.ProviderName,
	})
}

func (s *Server) chapterContent(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	meta := s.chapterMeta(r, id)
	if meta == nil {
		api.WriteError(w, http.StatusNotFound, "not_found", "")
		return
	}

	// Premium chapters: return locked status without attempting fetch.
	if meta.Premium {
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"chapter_id": id,
			"html":       "",
			"cached":     false,
			"premium":    true,
			"word_count": meta.WordCount,
			"title":      meta.Title,
		})
		return
	}

	// Cached: return immediately.
	if meta.HasContent && meta.HTML != "" {
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"chapter_id": id,
			"html":       meta.HTML,
			"cached":     true,
			"premium":    false,
			"word_count": meta.WordCount,
			"title":      meta.Title,
		})
		return
	}

	// Not cached: live-fetch via the provider's ContentFetcher.
	ch, _ := s.store.GetChapterWithProvider(id)
	if ch == nil {
		api.WriteError(w, http.StatusNotFound, "chapter_not_found", "")
		return
	}
	p, ok := plugin.Default.Get(ch.ProviderName)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", "")
		return
	}
	cf := plugin.AsContentFetcher(p)
	if cf == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "no_content_capability", "")
		return
	}

	content, fetchErr := cf.FetchChapter(ch.URL)
	if fetchErr != nil {
		logging.Error("[api/v1] live-fetch chapter %d: %v", id, fetchErr)
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"chapter_id": id,
			"html":       "",
			"cached":     false,
			"premium":    false,
			"error":      fetchErr.Error(),
			"title":      meta.Title,
		})
		return
	}

	// Best-effort async cache for subsequent reads.
	go func() {
		type saver interface {
			SaveChapterContent(id int64, content string) error
			SetChapterWordCount(id int64, n int) error
		}
		if sv, ok := s.store.(saver); ok {
			_ = sv.SaveChapterContent(id, content.BodyHTML)
			if content.WordCount > 0 {
				_ = sv.SetChapterWordCount(id, content.WordCount)
			}
		}
	}()

	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"chapter_id": id,
		"html":       content.BodyHTML,
		"cached":     false,
		"premium":    content.Premium,
		"word_count": content.WordCount,
		"title":      meta.Title,
	})
}

// chapterMeta is a small helper that calls GetChapterForReader and handles
// the nil case. Returns nil when the chapter doesn't exist.
func (s *Server) chapterMeta(r *http.Request, id int64) *handlers.ChapterContentMeta {
	// We need the store's concrete method; v1.Store interface doesn't expose
	// GetChapterForReader, so we type-assert to *handlers.Store for now.
	// This will be cleaned up when the Store interface is extended.
	type chapterMetaFetcher interface {
		GetChapterForReader(id int64) (*handlers.ChapterContentMeta, error)
	}
	if f, ok := s.store.(chapterMetaFetcher); ok {
		meta, err := f.GetChapterForReader(id)
		if err != nil || meta == nil {
			return nil
		}
		return meta
	}
	return nil
}

func (s *Server) chapterMarkRead(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	if _, err := s.store.MarkChapterRead(id); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// cacheChapterContent accepts chapter HTML from the phone (which fetched
// directly from the source site) and stores it in the server cache. This is
// the 'push back' half of the bidirectional caching system: phone fetches
// content on its residential IP (no Cloudflare) → pushes to server → next
// request hits server cache.
//
//	POST /api/v1/chapters/{id}/cache
//	Body: {"html": "<chapter content>"}
func (s *Server) cacheChapterContent(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	var req struct {
		HTML      string `json:"html"`
		WordCount int    `json:"word_count,omitempty"`
	}
	if !api.JSONDecode(w, r, &req) {
		return
	}
	if req.HTML == "" {
		api.WriteError(w, http.StatusBadRequest, "empty_html", "")
		return
	}
	type saver interface {
		SaveChapterContent(id int64, content string) error
		SetChapterWordCount(id int64, n int) error
	}
	sv, ok := s.store.(saver)
	if !ok {
		api.WriteError(w, http.StatusInternalServerError, "store_incompatible", "")
		return
	}
	if err := sv.SaveChapterContent(id, req.HTML); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if req.WordCount > 0 {
		_ = sv.SetChapterWordCount(id, req.WordCount)
	}
	logging.Info("[api/v1] phone-pushed cache for chapter %d (%d chars)", id, len(req.HTML))
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "cached": true})
}

func (s *Server) unreadCount(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetDashboardStats()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"unread": stats.UnreadChapter})
}

// ---------------------------------------------------------------------------
// Polling status / triggers
// ---------------------------------------------------------------------------

func (s *Server) pollStatus(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "pool_not_configured", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, s.pool.PollProgress())
}

func (s *Server) pollNow(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "pool_not_configured", "")
		return
	}
	count, err := s.pool.SubmitAll(s.store)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]interface{}{
		"queued": count,
		"status": s.pool.PollProgress(),
	})
}

func (s *Server) providerMetrics(w http.ResponseWriter, r *http.Request) {
	if s.pool == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "pool_not_configured", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"providers": s.pool.MetricsSnapshots(),
	})
}

// ---------------------------------------------------------------------------
// Downloads (offline reading bundles)
// ---------------------------------------------------------------------------

// comicChapterDownloadKey namespaces tracker keys for comic chapter downloads.
// Kept identical to handlers.comicDownloadKey so the web UI and API share
// progress state — a download triggered from the browser shows up in API
// polls and vice versa.
func comicChapterDownloadKey(id int64) string {
	return fmt.Sprintf("comic-chapter:%d", id)
}

// downloadComicChapter triggers a background download of every page in the
// chapter. Returns 202 with the tracker status; clients poll /status.
//
//	POST /api/v1/downloads/comics/{chapterID}
func (s *Server) downloadComicChapter(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "chapterID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	chapter, err := s.store.GetComicChapterByID(id)
	if err != nil || chapter == nil {
		api.WriteError(w, http.StatusNotFound, "chapter_not_found", "")
		return
	}
	series, _ := s.store.GetComicSeriesByID(chapter.SeriesID)
	provider, err := lookupComicProvider(series)
	if err != nil {
		api.WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", err.Error())
		return
	}

	store := s.store
	status := s.downloads.Start(comicChapterDownloadKey(id), 0, func(ctx context.Context, rep download.Reporter) error {
		pages, err := provider.PageList(chapter.SourceID)
		if err != nil {
			return fmt.Errorf("page list: %w", err)
		}
		rep.SetTotal(len(pages))
		if len(pages) == 0 {
			return fmt.Errorf("upstream returned 0 pages")
		}
		for _, pg := range pages {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if store.ComicPageBlobStored(ctx, id, pg.Index) {
				rep.Inc()
				continue
			}
			if err := fetchAndCacheComicPageV1(ctx, store, provider, id, pg); err != nil {
				logging.Error("[api/v1] download page %d chapter %d: %v", pg.Index, id, err)
				continue
			}
			rep.Inc()
		}
		return nil
	})

	api.WriteJSON(w, http.StatusAccepted, map[string]interface{}{
		"ok":     true,
		"key":    comicChapterDownloadKey(id),
		"status": status,
	})
}

// downloadComicChapterStatus returns the current progress of a chapter
// download. If no job has been recorded, falls back to the chapter's stored
// `downloaded` flag.
//
//	GET /api/v1/downloads/comics/{chapterID}/status
func (s *Server) downloadComicChapterStatus(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "chapterID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	status, ok := s.downloads.Status(comicChapterDownloadKey(id))
	if !ok {
		chapter, _ := s.store.GetComicChapterByID(id)
		state := download.StateFailed
		if chapter != nil && chapter.Downloaded {
			state = download.StateComplete
		}
		status = download.Status{Key: comicChapterDownloadKey(id), State: state}
	}
	api.WriteJSON(w, http.StatusOK, status)
}

// downloadComicChapterCBZ streams a CBZ (zip of page images) for offline
// reading. Pages are read from the blob store on the fly; the response is
// streamed so memory use stays flat regardless of chapter size.
//
//	GET /api/v1/downloads/comics/{chapterID}/cbz
func (s *Server) downloadComicChapterCBZ(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "chapterID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	chapter, err := s.store.GetComicChapterByID(id)
	if err != nil || chapter == nil {
		api.WriteError(w, http.StatusNotFound, "chapter_not_found", "")
		return
	}
	pages, err := s.store.GetComicChapterPages(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if len(pages) == 0 {
		api.WriteError(w, http.StatusNotFound, "no_pages_cached", "download the chapter first")
		return
	}

	filename := fmt.Sprintf("chapter-%d.cbz", id)
	if chapter.ChapterNum != "" {
		filename = fmt.Sprintf("chapter-%s.cbz", sanitizeCBZName(chapter.ChapterNum))
	}
	w.Header().Set("Content-Type", "application/vnd.comicbook+zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	zw := zip.NewWriter(w)
	ctx := r.Context()
	for _, pg := range pages {
		if ctx.Err() != nil {
			return
		}
		name := fmt.Sprintf("%05d.jpg", pg.Index)
		fw, err := zw.Create(name)
		if err != nil {
			return
		}
		rc, _, err := s.store.GetComicPageReader(ctx, id, pg.Index)
		if err != nil {
			logging.Error("[api/v1] cbz read page %d: %v", pg.Index, err)
			return
		}
		if _, err := io.Copy(fw, rc); err != nil {
			rc.Close()
			return
		}
		rc.Close()
	}
	if fw, err := zw.Create("ComicInfo.xml"); err == nil {
		fmt.Fprintf(fw, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<ComicInfo xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" `+
			`xmlns:xsd="http://www.w3.org/2001/XMLSchema">`+
			`<PageCount>%d</PageCount><Number>%s</Number></ComicInfo>`,
			len(pages), chapter.ChapterNum)
	}
	if err := zw.Close(); err != nil {
		logging.Error("[api/v1] cbz close: %v", err)
	}
}

// downloadTextChapter fetches a text chapter for offline reading. Returns
// the cached HTML when available; live-fetches + caches on demand when not.
// Premium chapters return 402.
//
//	GET /api/v1/downloads/chapters/{chapterID}
func (s *Server) downloadTextChapter(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "chapterID"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_id", "")
		return
	}
	meta := s.chapterMeta(r, id)
	if meta == nil {
		api.WriteError(w, http.StatusNotFound, "chapter_not_found", "")
		return
	}
	if meta.Premium {
		api.WriteJSON(w, http.StatusPaymentRequired, map[string]interface{}{
			"premium":   true,
			"cached":    false,
			"chapter_id": id,
			"title":     meta.Title,
		})
		return
	}
	if meta.HasContent {
		api.WriteJSON(w, http.StatusOK, map[string]interface{}{
			"chapter_id": id,
			"html":       meta.HTML,
			"word_count": meta.WordCount,
			"premium":    false,
			"cached":     true,
			"title":      meta.Title,
		})
		return
	}
	// Not cached. Live-fetch via the provider's ContentFetcher.
	// chapterMeta already validated the chapter exists; we need the provider.
	ch, _ := s.store.GetChapterWithProvider(id)
	if ch == nil {
		api.WriteError(w, http.StatusNotFound, "chapter_not_found", "")
		return
	}
	p, ok := plugin.Default.Get(ch.ProviderName)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", "")
		return
	}
	cf := plugin.AsContentFetcher(p)
	if cf == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "no_content_capability", "")
		return
	}
	content, err := cf.FetchChapter(ch.URL)
	if err != nil {
		api.WriteError(w, http.StatusBadGateway, "fetch_failed", err.Error())
		return
	}
	// Best-effort cache for next request.
	go func() {
		type contentSaver interface {
			SaveChapterContent(id int64, content string) error
			SetChapterWordCount(id int64, n int) error
		}
		if sv, ok := s.store.(contentSaver); ok {
			_ = sv.SaveChapterContent(id, content.BodyHTML)
			if content.WordCount > 0 {
				_ = sv.SetChapterWordCount(id, content.WordCount)
			}
		}
	}()
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"chapter_id": id,
		"html":       content.BodyHTML,
		"word_count": content.WordCount,
		"premium":    content.Premium,
		"cached":     false,
		"title":      meta.Title,
	})
}

// lookupComicProvider resolves a comic provider by name via plugin.Default.
// Returns the legacy comics.ComicProvider interface.
func lookupComicProvider(series *models.ComicSeries) (comics.ComicProvider, error) {
	if series == nil {
		return nil, fmt.Errorf("series is nil")
	}
	p, ok := plugin.Default.Get(series.ProviderName)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered", series.ProviderName)
	}
	cp, ok := p.(comics.ComicProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q does not implement ComicProvider", series.ProviderName)
	}
	return cp, nil
}

// fetchAndCacheComicPageV1 mirrors handlers.fetchAndCacheComicPage but lives
// in the API package to avoid a dependency cycle.
func fetchAndCacheComicPageV1(ctx context.Context, store Store, provider comics.ComicProvider, chapterID int64, pg models.ComicPage) error {
	req, err := http.NewRequestWithContext(ctx, "GET", pg.ImageURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	return store.SaveComicPage(chapterID, pg.Index, pg.ImageURL, data, ct)
}

func sanitizeCBZName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "chapter"
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Providers introspection
// ---------------------------------------------------------------------------

func (s *Server) providersList(w http.ResponseWriter, r *http.Request) {
	// Pull live metadata from the plugin registry so iOS clients can show
	// provider pickers, capability badges, and auth modes without hardcoding.
	providers := plugin.Default.All()
	out := make([]providerInfo, 0, len(providers))
	for _, p := range providers {
		m := p.Meta()
		authModes := make([]string, 0, len(m.AuthModes))
		for _, a := range m.AuthModes {
			authModes = append(authModes, string(a))
		}
		out = append(out, providerInfo{
			Name:              m.Name,
			DisplayName:       m.DisplayName,
			Kind:              string(m.Kind),
			Homepage:          m.Homepage,
			AuthModes:         authModes,
			PollIntervalDefault: m.PollIntervalDefault,
		})
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{"providers": out})
}

// ---------------------------------------------------------------------------
// OpenAPI spec
// ---------------------------------------------------------------------------

func (s *Server) openAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(openAPIYAMLJSON))
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
