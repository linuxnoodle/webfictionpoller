package v1_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/api"
	v1 "github.com/linuxnoodle/webfictionpoller/internal/api/v1"
	"github.com/linuxnoodle/webfictionpoller/internal/auth"
	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/handlers"
)

// setupServer spins a temp DB, creates a user, and wires the v1 server behind
// the bearer-auth middleware. Returns the server, the user creds, and a
// helper to make authenticated requests.
func setupServer(t *testing.T) (*v1.Server, *api.TokenStore, *sql.DB, string, string) {
	t.Helper()
	tmp, err := os.CreateTemp("", "api-v1-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	db, err := database.InitDB(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	username := "alice"
	password := "correct-horse-battery-staple"
	if err := auth.CreateUser(db, username, password); err != nil {
		t.Fatal(err)
	}

	tokens := api.NewTokenStore(db)
	store := handlers.NewStore(db)
	srv := v1.NewServer(db, tokens, store)
	return srv, tokens, db, username, password
}

func newAuthenticatedMux(t *testing.T, srv *v1.Server, db *sql.DB) *http.ServeMux {
	t.Helper()
	tokens := api.NewTokenStore(db)
	authz := api.NewAuthenticator(tokens, nil)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", srv.Routes(authz.Middleware(true), authz.HasUsersGate(db))))
	return mux
}

// do issues a request against the mux with an optional bearer token.
func do(t *testing.T, mux *http.ServeMux, method, path string, bearer string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestLoginIssuesBearerToken(t *testing.T) {
	srv, tokens, db, user, pass := setupServer(t)
	_ = tokens
	mux := newAuthenticatedMux(t, srv, db)

	resp := do(t, mux, "POST", "/api/v1/auth/login", "", map[string]string{
		"username": user, "password": pass, "label": "iPhone",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", resp.Code, resp.Body.String())
	}
	var got struct {
		Token    string `json:"token"`
		Label    string `json:"label"`
		UserID   int64  `json:"user_id"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token == "" || got.UserID == 0 || got.Username != user {
		t.Errorf("unexpected login response: %+v", got)
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	srv, _, db, user, _ := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)
	resp := do(t, mux, "POST", "/api/v1/auth/login", "", map[string]string{
		"username": user, "password": "wrong",
	})
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.Code)
	}
}

// TestBearerAuthRoundTrip demonstrates the full flow: login -> use token -> /auth/me.
func TestBearerAuthRoundTrip(t *testing.T) {
	srv, tokens, db, user, pass := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)

	// Issue a token directly via the store for test determinism.
	plaintext, _, err := tokens.IssueToken(context.Background(), 1, "test-token", "device-xyz")
	if err != nil {
		t.Fatal(err)
	}

	// /auth/me should succeed with the token.
	resp := do(t, mux, "GET", "/api/v1/auth/me", plaintext, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("auth/me with valid token: %d, body=%s", resp.Code, resp.Body.String())
	}

	// No Authorization header -> 401 (no session in this mux).
	resp = do(t, mux, "GET", "/api/v1/auth/me", "", nil)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", resp.Code)
	}

	// Garbage token -> 401.
	resp = do(t, mux, "GET", "/api/v1/auth/me", "wfp_deadbeef", nil)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", resp.Code)
	}
	_ = user
	_ = pass
}

func TestUnreadCountRequiresAuth(t *testing.T) {
	srv, _, db, _, _ := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)
	resp := do(t, mux, "GET", "/api/v1/unread-count", "", nil)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.Code)
	}
}

func TestTokenLifecycleIssueListRevoke(t *testing.T) {
	srv, tokens, db, user, pass := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)

	// Login to get a working token.
	resp := do(t, mux, "POST", "/api/v1/auth/login", "", map[string]string{
		"username": user, "password": pass,
	})
	var login struct{ Token string `json:"token"` }
	json.Unmarshal(resp.Body.Bytes(), &login)

	// Issue a second token via the API.
	resp = do(t, mux, "POST", "/api/v1/tokens", login.Token, map[string]string{"label": "iPad"})
	if resp.Code != http.StatusCreated {
		t.Fatalf("issue token: %d, body=%s", resp.Code, resp.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
		Label string `json:"label"`
	}
	json.Unmarshal(resp.Body.Bytes(), &created)
	if created.ID == 0 || created.Token == "" {
		t.Errorf("unexpected issue response: %+v", created)
	}

	// List tokens.
	resp = do(t, mux, "GET", "/api/v1/tokens", login.Token, nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("list tokens: %d", resp.Code)
	}

	// Revoke the new token.
	resp = do(t, mux, "DELETE", "/api/v1/tokens/"+strconv.FormatInt(created.ID, 10), login.Token, nil)
	if resp.Code != http.StatusOK {
		t.Errorf("revoke: %d, body=%s", resp.Code, resp.Body.String())
	}

	// The revoked token should now fail auth.
	resp = do(t, mux, "GET", "/api/v1/auth/me", created.Token, nil)
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("revoked token still works: %d", resp.Code)
	}
	_ = tokens
}

func TestOpenAPI(t *testing.T) {
	srv, _, db, _, _ := setupServer(t)
	mux := newAuthenticatedMux(t, srv, db)
	resp := do(t, mux, "GET", "/api/v1/openapi.json", "", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("openapi: %d", resp.Code)
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte("openapi")) {
		t.Error("openapi.json body missing 'openapi' key")
	}
}

// strconvInt removed — use strconv.FormatInt directly.
