// Package db is a thin wrapper around database/sql that smooths over the
// differences between SQLite and Postgres for callers in this codebase.
//
// Two main jobs:
//
//  1. Dialect-aware parameter rebinding. SQLite accepts `?` placeholders;
//     Postgres requires numbered `$1, $2, ...`. Callers keep writing `?` and
//     the wrapper rewrites the query at call time based on the configured
//     dialect. This avoids touching ~90 call sites whenever we add Postgres.
//
//  2. Dialect detection. Open() inspects its argument: `postgres://...`,
//     `postgresql://...`, or a libpq DSN containing `host=` uses Postgres via
//     pgx/stdlib; everything else is treated as a SQLite path/DSN.
//
// The wrapper embeds *sql.DB and re-exports Exec/Query/QueryRow (+ Context
// variants) with identical names so existing `s.db.Exec(...)` call sites
// compile unchanged when their `s.db` field changes from `*sql.DB` to `*db.DB`.
// Methods we don't shadow (Ping, Close, BeginTx, Stats, ...) fall through to
// the embedded *sql.DB.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	// Register both drivers.
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/mattn/go-sqlite3"
)

// Dialect labels which SQL engine is in use.
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// DB is a database/sql connection with dialect awareness. Embeds *sql.DB so
// non-query methods (Close, Ping, SetMaxOpenConns, etc.) pass through.
type DB struct {
	*sql.DB
	dialect Dialect
}

// Dialect returns the connection's dialect.
func (db *DB) Dialect() Dialect { return db.dialect }

// SQL returns the underlying *sql.DB. Use when a third-party package needs
// the raw connection (e.g. scs session stores). Callers should prefer the
// shadowed query methods on *DB so rebinding still applies.
func (db *DB) SQL() *sql.DB { return db.DB }

// IsPostgres reports whether the connection targets Postgres.
func (db *DB) IsPostgres() bool { return db.dialect == DialectPostgres }

// Open inspects connStr and opens the appropriate driver. A connStr beginning
// with `postgres://`, `postgresql://`, or a libpq DSN containing `host=` routes
// to Postgres via pgx/stdlib. Anything else is treated as a SQLite path.
func Open(connStr string) (*DB, error) {
	dialect := detectDialect(connStr)
	switch dialect {
	case DialectPostgres:
		sqlDB, err := sql.Open("pgx", connStr)
		if err != nil {
			return nil, fmt.Errorf("db: opening postgres: %w", err)
		}
		return &DB{DB: sqlDB, dialect: DialectPostgres}, nil
	case DialectSQLite:
		sqlDB, err := sql.Open("sqlite3", connStr)
		if err != nil {
			return nil, fmt.Errorf("db: opening sqlite: %w", err)
		}
		return &DB{DB: sqlDB, dialect: DialectSQLite}, nil
	}
	return nil, fmt.Errorf("db: unknown dialect for %q", connStr)
}

func detectDialect(connStr string) Dialect {
	if connStr == "" {
		return DialectSQLite
	}
	s := strings.TrimSpace(connStr)
	if strings.HasPrefix(s, "postgres://") ||
		strings.HasPrefix(s, "postgresql://") {
		return DialectPostgres
	}
	// libpq key=value DSN: "host=... user=... dbname=..."
	if !strings.HasPrefix(s, "/") && strings.Contains(s, " ") &&
		(strings.Contains(s, "host=") || strings.Contains(s, "dbname=")) {
		return DialectPostgres
	}
	return DialectSQLite
}

// rebind rewrites `?` placeholders to numbered `$1, $2, ...` when the dialect
// is Postgres. SQLite accepts `?` natively so the query passes through.
//
// Caveat: this is a naive scanner — it does NOT honor quoted-string literals
// containing `?`. None of our queries embed literal `?` in strings, but if one
// ever does, it will need manual escaping.
func (db *DB) rebind(query string) string {
	if db.dialect == DialectSQLite {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Shadowed *sql.DB query methods — each applies dialect-aware rebinding.
// Names + signatures match *sql.DB exactly so call sites don't change.
// ---------------------------------------------------------------------------

func (db *DB) Exec(query string, args ...interface{}) (sql.Result, error) {
	return db.DB.Exec(db.rebind(query), args...)
}

func (db *DB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return db.DB.ExecContext(ctx, db.rebind(query), args...)
}

func (db *DB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return db.DB.Query(db.rebind(query), args...)
}

func (db *DB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return db.DB.QueryContext(ctx, db.rebind(query), args...)
}

func (db *DB) QueryRow(query string, args ...interface{}) *sql.Row {
	return db.DB.QueryRow(db.rebind(query), args...)
}

func (db *DB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return db.DB.QueryRowContext(ctx, db.rebind(query), args...)
}

// Prepare wraps sql.DB.Prepare; the returned *sql.Stmt is NOT dialect-aware
// (placeholders bind at prepare time on the server side), so callers should
// pass already-rebound SQL. Exposed for the rare cases that need prepared
// statements.
func (db *DB) Prepare(query string) (*sql.Stmt, error) {
	return db.DB.Prepare(db.rebind(query))
}

func (db *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return db.DB.PrepareContext(ctx, db.rebind(query))
}

// ---------------------------------------------------------------------------
// Dialect-portable SQL helpers
// ---------------------------------------------------------------------------

// IgnoreConflict returns the dialect-portable "insert, do nothing on conflict"
// clause. Works on SQLite 3.24+ and Postgres.
const IgnoreConflict = "ON CONFLICT DO NOTHING"

// ILike returns a dialect-portable case-insensitive LIKE. SQLite's LIKE is
// already case-insensitive for ASCII; Postgres's is case-sensitive so we wrap
// both sides in LOWER(). Usage: ILike("title", "?") -> "LOWER(title) LIKE LOWER(?)".
func ILike(column, placeholder string) string {
	return fmt.Sprintf("LOWER(%s) LIKE LOWER(%s)", column, placeholder)
}

// LimitOffset returns a portable LIMIT/OFFSET clause.
func LimitOffset(limit, offset int) string {
	return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
}
