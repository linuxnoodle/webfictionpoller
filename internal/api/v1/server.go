// Package v1 hosts the canonical, versioned JSON API. All routes are mounted
// under /api/v1 by the parent api package's Router function.
//
// Design notes:
//   - DTOs in dto.go decouple the wire format from internal models so the
//     schema can evolve without breaking mobile clients.
//   - Every handler reads the authenticated userID from the request context
//     (set by api.Authenticator).
//   - Errors use the structured {error, detail} envelope via writeAPIError.
package v1

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linuxnoodle/webfictionpoller/internal/api"
	"github.com/linuxnoodle/webfictionpoller/internal/auth"
	"github.com/linuxnoodle/webfictionpoller/internal/handlers"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

// Server bundles the dependencies every v1 handler needs. Construct once at
// startup; the router methods close over it.
type Server struct {
	db       *sql.DB
	tokens   *api.TokenStore
	store    *handlers.Store
}

func NewServer(db *sql.DB, tokens *api.TokenStore, store *handlers.Store) *Server {
	return &Server{db: db, tokens: tokens, store: store}
}

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
		r.Get("/chapters", s.chapterList)
		r.Get("/chapters/{id}", s.chapterGet)
		r.Get("/chapters/{id}/content", s.chapterContent)
		r.Post("/chapters/{id}/read", s.chapterMarkRead)
		r.Get("/unread-count", s.unreadCount)

		// Polling status / triggers.
		r.Get("/poll/status", s.pollStatus)
		r.Post("/poll/now", s.pollNow)

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
	// Try archived content first; fall back to live fetch via provider.
	content, err := s.store.GetChapterArchivedContent(id)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"chapter_id": id,
		"html":       content,
		"cached":     content != "",
	})
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
	// Placeholder; wired to WorkerPool progress in a subsequent commit once
	// the pool exposes status through an interface.
	api.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"active": false,
		"done":   0,
		"total":  0,
	})
}

func (s *Server) pollNow(w http.ResponseWriter, r *http.Request) {
	// Triggering poll requires WorkerPool access; this stub returns a 501
	// until the pool is wired through an interface.
	api.WriteError(w, http.StatusNotImplemented, "not_wired", "poll trigger via API coming in next commit")
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
