package database

import (
	"os"
	"testing"
)

func TestOpen(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	db, err := Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if fkEnabled != 1 {
		t.Errorf("foreign_keys = %d, want 1", fkEnabled)
	}

	tables := []string{"users", "series", "chapters", "provider_configs", "sessions"}
	for _, table := range tables {
		var count int
		err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("table %q not found", table)
		}
	}
}

func TestOpenIdempotent(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	db1, err := Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	db2.Close()
}

// TestMigrationNamesUnique guards against accidental name collisions, which
// would silently skip a migration on one or both dialects.
func TestMigrationNamesUnique(t *testing.T) {
	seen := make(map[string]int, len(migrations))
	for _, m := range migrations {
		seen[m.name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("migration name %q appears %d times", name, n)
		}
	}
}

// TestSQLiteMigrationLedgerComplete opens a fresh SQLite database and asserts
// every migration name is recorded in _migrations (the SQLite runner applied
// them all). This catches cases where a migration was added to the list but
// the runner skips or mis-records it.
func TestSQLiteMigrationLedgerComplete(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	d, err := Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	rows, err := d.Query("SELECT name FROM _migrations")
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer rows.Close()
	applied := make(map[string]bool, len(migrations))
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
	}
		applied[n] = true
	}
	for _, m := range migrations {
		if !applied[m.name] {
			t.Errorf("sqlite ledger missing migration %q", m.name)
		}
	}
}

// TestPostgresMigrationParity runs ONLY when WFP_TEST_PG points at a Postgres
// DSN. It asserts that the Postgres runner records every migration name from
// the shared list in schema_migrations — i.e. both dialects converge on the
// same applied set. This is the guard against schema drift between SQLite and
// Postgres (the original P2a hazard).
func TestPostgresMigrationParity(t *testing.T) {
	pgURL := os.Getenv("WFP_TEST_PG")
	if pgURL == "" {
		t.Skip("set WFP_TEST_PG to a Postgres DSN to run Postgres parity tests")
	}
	d, err := Open(pgURL)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer d.Close()

	rows, err := d.Query("SELECT name FROM schema_migrations")
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	defer rows.Close()
	applied := make(map[string]bool, len(migrations))
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
	}
		applied[n] = true
	}
	for _, m := range migrations {
		if !applied[m.name] {
			t.Errorf("postgres schema_migrations missing migration %q", m.name)
		}
	}
}
