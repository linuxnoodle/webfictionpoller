package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/alexedwards/scs/v2"

	"github.com/linuxnoodle/webfictionpoller/internal/db"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

// ctxKey is unexported so context values can only be read via the helpers
// in this file — prevents other packages from forging auth state.
type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyTokenID
	ctxKeyUsername
)

// Authenticator resolves bearer tokens and falls back to a browser session
// when no Authorization header is present. The session fallback is for the
// web UI; mobile clients always use bearer.
type Authenticator struct {
	tokens *TokenStore
	sm     *scs.SessionManager // may be nil if session fallback is disabled
}

func NewAuthenticator(tokens *TokenStore, sm *scs.SessionManager) *Authenticator {
	return &Authenticator{tokens: tokens, sm: sm}
}

// Middleware returns an http middleware that authenticates the request via
// either a bearer token or a session cookie. On success the userID is placed
// in the request context. On failure a 401 JSON response is written.
//
// When requireAuth is false (e.g. setup flow with no users yet), the request
// passes through without authentication — hasUsersGate is the canonical way
// to express this.
func (a *Authenticator) Middleware(requireAuth bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Vary", "Authorization")
			if userID, tokID, ok := a.bearerUserID(r); ok {
				ctx := context.WithValue(r.Context(), ctxKeyUserID, userID)
				ctx = context.WithValue(ctx, ctxKeyTokenID, tokID)
				*r = *r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}
			if !requireAuth {
				next.ServeHTTP(w, r)
				return
			}
			// Session fallback for browser-based callers.
			if a.sm != nil {
				if v := a.sm.Get(r.Context(), "userID"); v != nil {
					ctx := context.WithValue(r.Context(), ctxKeyUserID, v)
					if uname, ok := a.sm.Get(r.Context(), "username").(string); ok {
						ctx = context.WithValue(ctx, ctxKeyUsername, uname)
					}
					*r = *r.WithContext(ctx)
					next.ServeHTTP(w, r)
					return
				}
			}
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "")
		})
	}
}

// bearerUserID extracts and validates a bearer token from the Authorization
// header. Returns the userID and tokenID on success.
func (a *Authenticator) bearerUserID(r *http.Request) (int64, int64, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return 0, 0, false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return 0, 0, false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return 0, 0, false
	}
	t, err := a.tokens.LookupToken(r.Context(), tok)
	if err != nil {
		logging.Error("[api] token lookup: %v", err)
		return 0, 0, false
	}
	if t == nil {
		return 0, 0, false
	}
	return t.UserID, t.ID, true
}

// UserIDFromContext returns the authenticated user ID, or false.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(int64)
	return v, ok
}

// TokenIDFromContext returns the bearer token ID used for this request, if any.
// Returns false for session-authenticated requests (no token).
func TokenIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxKeyTokenID).(int64)
	return v, ok
}

// HasUsersGate passes requests through when no users exist (first-run setup),
// otherwise applies requireAuth. Useful for the /auth/login endpoint that must
// be reachable before any account exists.
func (a *Authenticator) HasUsersGate(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var n int
			err := database.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&n)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "db_error", err.Error())
				return
			}
			a.Middleware(n > 0)(next).ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// writeAPIError writes a structured JSON error. detail is included only when
// non-empty; it may leak internals so callers should pass user-safe messages
// in production (we don't currently distinguish environments).
func writeAPIError(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := map[string]string{"error": code}
	if detail != "" {
		resp["detail"] = detail
	}
	json.NewEncoder(w).Encode(resp)
}

// WriteJSON writes a JSON response with a status code. Exported for v1 handlers.
func WriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes a structured JSON error. Exported for v1 handlers.
func WriteError(w http.ResponseWriter, status int, code, detail string) {
	writeAPIError(w, status, code, detail)
}

// JSONDecode is a helper that bounds request body size and reports a clean
// error on malformed input. Exported for v1 handlers.
func JSONDecode(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	return jsonDecode(w, r, dst)
}

// jsonDecode is the unexported impl used internally by auth middleware.
func jsonDecode(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}
