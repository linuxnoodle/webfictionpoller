package handlers

import (
	"os"
	"testing"

	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/db"
)

// Postgres-backed store tests are OPT-IN via the WFP_TEST_PG environment
// variable. Set it to a Postgres DSN to run the full handlers store test
// suite against Postgres; leave it unset (the default) to run against an
// in-memory temp SQLite file, so CI environments without Docker still pass.
//
//	WFP_TEST_PG="postgres://wfp:wfp@localhost:5432/wfp_test?sslmode=disable" go test ./internal/handlers/...
//
// The same test functions run unchanged on both dialects because newTestStore
// / newTestStoreWithBlob route through openTestDB, which picks the dialect
// from the env var. This is what catches the dialect bugs (LastInsertId,
// un-rebound transactions, placeholder mismatches) that slipped through when
// only SQLite was exercised.

func pgTestURL() string { return os.Getenv("WFP_TEST_PG") }
func pgEnabled() bool   { return pgTestURL() != "" }

// openTestDB returns a *db.DB for the active test dialect: Postgres when
// WFP_TEST_PG is set, otherwise a fresh temp SQLite file. For Postgres it
// truncates every table in the public schema (RESTART IDENTITY CASCADE) so
// each test starts from a clean slate — mirroring the per-test temp-file
// isolation SQLite gets for free.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	if pgEnabled() {
		d, err := database.Open(pgTestURL())
		if err != nil {
			t.Fatalf("open pg test db %q: %v", pgTestURL(), err)
		}
		t.Cleanup(func() { d.Close() })
		// Wipe all data + reset sequences for isolation between tests.
		if _, err := d.Exec(`DO $$ DECLARE r RECORD;
		BEGIN
			FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname = 'public') LOOP
				EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' RESTART IDENTITY CASCADE';
			END LOOP;
		END $$`); err != nil {
			t.Fatalf("truncate pg tables for test isolation: %v", err)
		}
		return d
	}
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })
	d, err := database.Open(tmp.Name() + "?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
