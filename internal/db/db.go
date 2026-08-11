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

// rebindQuery rewrites `?` placeholders to numbered `$1, $2, ...` when the
// dialect is Postgres. SQLite accepts `?` natively so the query passes
// through. Shared by DB and Tx so transactions get the same rebinding.
//
// Caveat: this is a naive scanner — it does NOT honor quoted-string literals
// containing `?`. None of our queries embed literal `?` in strings, but if one
// ever does, it will need manual escaping.
func rebindQuery(dialect Dialect, query string) string {
	if dialect == DialectSQLite {
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

func (db *DB) rebind(query string) string { return rebindQuery(db.dialect, query) }

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
// Transactions
//
// Begin/BeginTx are shadowed to return *Tx (below) instead of a raw *sql.Tx.
// The raw *sql.Tx methods do NOT rebind placeholders, so writing `?` inside a
// transaction works on SQLite but silently breaks Postgres (`?` is not a
// valid placeholder there). *Tx shadows the same query methods as DB so tx
// call sites compile unchanged and get rebinding for free. Non-query methods
// (Commit, Rollback, Stmt) fall through to the embedded *sql.Tx.
// ---------------------------------------------------------------------------

// Begin is a dialect-aware replacement for *sql.DB.Begin. Always prefer this
// over the raw method so the returned transaction rebinds placeholders.
func (db *DB) Begin() (*Tx, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, dialect: db.dialect}, nil
}

// BeginTx is the context-aware variant of Begin.
func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, dialect: db.dialect}, nil
}

// Tx is a dialect-aware transaction. Obtain one via DB.Begin / DB.BeginTx.
// Its query methods rebind `?`→`$N` exactly like DB; Commit/Rollback/Stmt and
// every other *sql.Tx method pass through the embedded *sql.Tx.
type Tx struct {
	*sql.Tx
	dialect Dialect
}

func (tx *Tx) rebind(query string) string { return rebindQuery(tx.dialect, query) }

func (tx *Tx) Exec(query string, args ...interface{}) (sql.Result, error) {
	return tx.Tx.Exec(tx.rebind(query), args...)
}

func (tx *Tx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return tx.Tx.ExecContext(ctx, tx.rebind(query), args...)
}

func (tx *Tx) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return tx.Tx.Query(tx.rebind(query), args...)
}

func (tx *Tx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return tx.Tx.QueryContext(ctx, tx.rebind(query), args...)
}

func (tx *Tx) QueryRow(query string, args ...interface{}) *sql.Row {
	return tx.Tx.QueryRow(tx.rebind(query), args...)
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return tx.Tx.QueryRowContext(ctx, tx.rebind(query), args...)
}

// Prepare wraps *sql.Tx.Prepare; the returned *sql.Stmt is NOT dialect-aware
// (placeholders bind at prepare time on the server side), so callers should
// pass already-rebound SQL.
func (tx *Tx) Prepare(query string) (*sql.Stmt, error) {
	return tx.Tx.Prepare(tx.rebind(query))
}

func (tx *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return tx.Tx.PrepareContext(ctx, tx.rebind(query))
}
