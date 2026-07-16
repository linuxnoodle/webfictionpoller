package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/linuxnoodle/webfictionpoller/internal/api"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

// Token management UI. The actual token store lives in internal/api; handlers
// here are a thin wrapper that calls into it using the session-authenticated
// user ID resolved via h.userIDOf (injected by main.go from the scs session).
//
// Routes (added by main.go):
//
//	GET  /admin/tokens           list + create form
//	POST /admin/tokens           issue token (returns plaintext once)
//	POST /admin/tokens/revoke?id=N

func (h *Handler) tokenStore() *api.TokenStore { return h.tokens }

// TokensPage renders the token management UI.
func (h *Handler) TokensPage(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.userIDOf == nil {
		http.Error(w, "token store not initialized", http.StatusInternalServerError)
		return
	}
	uid, ok := h.userIDOf(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	tokens, err := h.tokens.ListTokensForUser(r.Context(), uid)
	if err != nil {
		internalError(w, r, err)
		return
	}
	renderTemplate(w, r, "tokens", map[string]interface{}{
		"Page":   "tokens",
		"Tokens": tokens,
	})
}

// CreateTokenForm handles the POST from the admin form. On success the
// plaintext token is rendered once in the page so the user can copy it.
func (h *Handler) CreateTokenForm(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.userIDOf == nil {
		http.Error(w, "token store not initialized", http.StatusInternalServerError)
		return
	}
	uid, ok := h.userIDOf(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	label := r.FormValue("label")
	deviceID := r.FormValue("device_id")
	if label == "" {
		label = "New token"
	}
	plaintext, tok, err := h.tokens.IssueToken(r.Context(), uid, label, deviceID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	logging.Info("[admin] user %d issued token %d (label=%q)", uid, tok.ID, label)
	tokens, _ := h.tokens.ListTokensForUser(r.Context(), uid)
	renderTemplate(w, r, "tokens", map[string]interface{}{
		"Page":          "tokens",
		"Tokens":        tokens,
		"NewTokenPlain": plaintext,
		"NewTokenID":    tok.ID,
		"NewTokenLabel": label,
	})
}

// RevokeTokenForm handles POST /admin/tokens/revoke?id=N.
func (h *Handler) RevokeTokenForm(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.userIDOf == nil {
		http.Error(w, "token store not initialized", http.StatusInternalServerError)
		return
	}
	uid, ok := h.userIDOf(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		idStr = r.FormValue("id")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.tokens.RevokeToken(r.Context(), id, uid); err != nil {
		logging.Error("[admin] revoke token %d: %v", id, err)
	}
	w.Header().Set("HX-Redirect", "/admin/tokens")
	w.WriteHeader(http.StatusOK)
}

// CreateTokenAPI is a JSON variant for in-page AJAX; mirrors the v1 endpoint
// but authenticated via session.
func (h *Handler) CreateTokenAPI(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil || h.userIDOf == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "token store not initialized"})
		return
	}
	uid, ok := h.userIDOf(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "unauthorized"})
		return
	}
	var req struct {
		Label    string `json:"label"`
		DeviceID string `json:"device_id"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "invalid json"})
			return
		}
	}
	if req.Label == "" {
		req.Label = "New token"
	}
	plaintext, tok, err := h.tokens.IssueToken(r.Context(), uid, req.Label, req.DeviceID)
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token": plaintext,
		"id":    tok.ID,
		"label": req.Label,
	})
}
