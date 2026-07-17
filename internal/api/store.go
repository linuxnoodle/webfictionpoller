// Package api implements the versioned JSON API consumed by mobile clients
// (iOS app) and any non-browser integrations. It lives under /api/v1/* and
// authenticates via per-device bearer tokens (bcrypt-hashed at rest).
//
// Browser sessions (scs cookie) are also accepted so the web UI can call the
// same endpoints during the transition; eventually the legacy /api/* routes
// will be removed in favour of this package.
package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

// TokenPrefix is the literal prefix every emitted bearer token carries.
// Clients store and send the full string; the store only persists the hash.
const TokenPrefix = "wfp_"

// TokenLifetime is the default validity window for newly issued tokens.
// Revocation is explicit (revoked_at); expiry is a backstop.
const TokenLifetime = 10 * 365 * 24 * time.Hour // ~10 years

// APIToken is the persisted record. Token itself is never stored — only the
// bcrypt hash. The plaintext is returned to the caller exactly once at issue.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"user_id"`
	Label      string     `json:"label"`
	DeviceID   string     `json:"device_id,omitempty"`
	TokenHash  string     `json:"-"` // never serialized
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// TokenStore manages api_tokens rows. It is a thin layer over *sql.DB so it
// composes cleanly with the eventual Store refactor (Phase 2 remainder).
type TokenStore struct {
	db *sql.DB
}

func NewTokenStore(db *sql.DB) *TokenStore { return &TokenStore{db: db} }

// IssueToken creates a new token for userID with the given label, persists
// the bcrypt hash, and returns the plaintext token exactly once. Callers
// must display and discard — there is no way to recover the plaintext later.
func (s *TokenStore) IssueToken(ctx context.Context, userID int64, label, deviceID string) (string, *APIToken, error) {
	plaintext, err := generateToken()
	if err != nil {
		return "", nil, fmt.Errorf("api: generating token: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("api: hashing token: %w", err)
	}
	expires := time.Now().Add(TokenLifetime)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (user_id, token_hash, label, device_id, expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, userID, string(hash), label, deviceID, expires)
	if err != nil {
		return "", nil, fmt.Errorf("api: inserting token: %w", err)
	}
	id, _ := res.LastInsertId()
	tok := &APIToken{
		ID:        id,
		UserID:    userID,
		Label:     label,
		DeviceID:  deviceID,
		CreatedAt: time.Now(),
		ExpiresAt: &expires,
	}
	return plaintext, tok, nil
}

// LookupToken finds the non-revoked, non-expired token whose hash matches
// plaintext. The plaintext is compared in constant time at the bcrypt layer;
// on a hit, last_used_at is updated and the record is returned.
//
// Returns (nil, nil) when the token does not exist or has been revoked —
// callers must not distinguish those cases from the client side.
func (s *TokenStore) LookupToken(ctx context.Context, plaintext string) (*APIToken, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, token_hash, label, device_id, created_at, last_used_at, expires_at, revoked_at
		FROM api_tokens
		WHERE revoked_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("api: querying tokens: %w", err)
	}
	// Materialize all candidate rows so we can release the connection BEFORE
	// running bcrypt (slow) and BEFORE issuing the markUsed UPDATE. With
	// SetMaxOpenConns(1) holding the connection across these would deadlock.
	type candidate struct {
		t  APIToken
		raw []byte
	}
	var candidates []candidate
	for rows.Next() {
		var t APIToken
		var raw []byte
		if err := rows.Scan(&t.ID, &t.UserID, &raw, &t.Label, &t.DeviceID, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
			rows.Close()
			return nil, err
		}
		t.TokenHash = string(raw)
		candidates = append(candidates, candidate{t: t, raw: raw})
	}
	rows.Close()

	for _, c := range candidates {
		// Check expiry before doing expensive bcrypt compare.
		if c.t.ExpiresAt != nil && time.Now().After(*c.t.ExpiresAt) {
			continue
		}
		if bcrypt.CompareHashAndPassword(c.raw, []byte(plaintext)) == nil {
			s.markUsed(ctx, c.t.ID)
			return &c.t, nil
		}
	}
	return nil, nil
}

func (s *TokenStore) markUsed(ctx context.Context, id int64) {
	if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, time.Now(), id); err != nil {
		logging.Error("[api] update last_used_at for token %d: %v", id, err)
	}
}

// ListTokensForUser returns every token for a user, including revoked ones
// (so the UI can show history). The token hash is omitted from the JSON.
func (s *TokenStore) ListTokensForUser(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, label, device_id, created_at, last_used_at, expires_at, revoked_at
		FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Label, &t.DeviceID, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// RevokeToken sets revoked_at on the token. Revocation is permanent — callers
// must issue a new token to re-authorize the device.
func (s *TokenStore) RevokeToken(ctx context.Context, tokenID, userID int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL
	`, time.Now(), tokenID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// ErrTokenNotFound is returned when a revoke request targets a token that
// does not exist, belongs to another user, or is already revoked.
var ErrTokenNotFound = errors.New("api: token not found")

// generateToken returns a new random plaintext token with the TokenPrefix.
// 32 bytes of entropy (~256 bits) is more than sufficient for bearer auth.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPrefix + hex.EncodeToString(b), nil
}
