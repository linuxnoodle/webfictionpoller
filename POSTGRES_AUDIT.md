# Postgres / SQLite Dialect Audit & Remediation Plan

Triggered by: `/series/add` 500 on the Postgres deployment. Root cause was
`Store.AddSeries` returning the `LastInsertId()` error, which pgx/stdlib
reports as `driver.ErrSkip`. Fixed in `store_chapters.go` + `comic_store.go`.

This audit found **six bug classes**. All are now fixed and covered by a
Postgres integration test harness (`WFP_TEST_PG`). The harness itself caught
two additional live bugs (boolean-literal inserts + a SQLite-only `IS NOT`
operator) that the static audit missed — see P1d and P1e.

## Status: COMPLETE

Every item below is implemented. Verified by running the full handlers +
api/v1 + database + db test suites against both a real Postgres 16 container
and in-memory SQLite.

---

## P0 — Live 500s (already triggered)

| # | Site | Cause | Status |
|---|------|-------|--------|
| 1 | `handlers/store_chapters.go` `AddSeries` | `LastInsertId()` err returned on pgx | **FIXED** |
| 2 | `handlers/comic_store.go` `AddComicSeries` | same | **FIXED** |

Pattern: `result, err := db.Exec(...); id, err := result.LastInsertId(); if err != nil { return err }`.
pgx/stdlib returns `driver.ErrSkip` from `LastInsertId()` → error propagates →
handler 500s. Other call sites swallow the error with `_` (see P1).

---

## P1 — Live 500s / silent failures on Postgres (not yet reported)

### P1a — Transactions bypass the rebind wrapper

`db.DB` shadows `Exec/Query/QueryRow` to rebind `?`→`$N`. But `BeginTx` /
`Begin` fall through to the embedded `*sql.DB`, returning a raw `*sql.Tx`.
Calls on `*sql.Tx` are standard `sql.Tx` methods — **no rebinding**. Every
`tx.Exec(... ? ...)` runs with `?` unrewritten. On Postgres via pgx/stdlib
this errors (invalid placeholder / missing bind var).

Affected sites (grep `tx.Exec`):

| Site | SQL | Impact |
|------|-----|--------|
| `handlers/store_sources.go:199` `SetPrimarySource` | `UPDATE series_sources SET is_primary = FALSE WHERE series_id = ?` | **500** — promoting an alt source |
| `handlers/store_sources.go:202` `SetPrimarySource` | `UPDATE series_sources SET is_primary = TRUE, priority = 0 WHERE id = ?` | **500** |
| `handlers/store_sources.go:205` `SetPrimarySource` | `UPDATE series SET provider_name = ?, source_url = ? WHERE id = ?` | **500** |
| `handlers/store_archive.go:213` `ClearArchive` | `DELETE FROM chapter_images WHERE chapter_id IN (SELECT id FROM chapters WHERE series_id = ?)` | **500** — clearing series archive |
| `handlers/store_archive.go:218` `ClearArchive` | `UPDATE chapters SET content_html = NULL, content_compressed = FALSE, preview_html = '' WHERE series_id = ?` | **500** |
| `handlers/store_archive.go:233` `ClearArchiveChapter` | `DELETE FROM chapter_images WHERE chapter_id = ?` | **500** |
| `handlers/store_archive.go:238` `ClearArchiveChapter` | `UPDATE chapters SET content_html = NULL, ... WHERE id = ?` | **500** |

**Fix (structural):** add a dialect-aware `Tx` wrapper (`db.DB.BeginTx`
returns `*db.Tx` shadowing `Exec/Query/QueryRow` with rebind), OR rebind
manually at each tx call site via a helper `s.db.Rebind(query)` exposed from
`db.DB`. The `Tx` wrapper is the cleaner long-term fix — it removes the
trap permanently.

### P1b — `LastInsertId()` swallowed → wrong/silent results

These ignore the error with `_`, so they don't 500, but the returned `id`
is `0` on Postgres:

| Site | Code | Consequence |
|------|------|-------------|
| `handlers/store_sources.go:45` `AddSource` | `id, _ := res.LastInsertId(); ...; return GetSourceByID(id)` | `GetSourceByID(0)` → no row → **AddSource returns `(nil, nil)`** silently. Adding an alt source on Postgres appears to succeed but returns nothing. |
| `api/store.go:75` `IssueToken` | `id, _ := res.LastInsertId(); tok.ID = id` | Token works (auth is hash-based) but **`APIToken.ID == 0`** in the create response / any UI listing the just-created token. |

**Fix:** same `RETURNING id` pattern as the P0 fixes. `AddSource` already
has a `UNIQUE(series_id, source_url)` conflict path via `GetSourceByURL` —
the RETURNING variant folds the success path into `QueryRow(...).Scan(&id)`.

### P1c — Other insert paths (verify, not yet confirmed broken)

These use `ON CONFLICT DO NOTHING` + `RowsAffected` only, so they don't
call `LastInsertId`. They should be safe but verify:

- `handlers/store_chapters.go` `InsertChapters` — uses `RowsAffected`, OK
- `handlers/comic_store.go` `UpsertComicChapters` — uses `RowsAffected`, OK
- `handlers/store_admin.go:163` admin import insert — ignores Result, OK

---

## P1d — Boolean-column literal inserts (FOUND BY PG HARNESS)

Postgres BOOLEAN columns reject integer literals. SQLite accepts `1`/`0`
(boolean affinity), so these passed SQLite tests silently.

| Site | Was | Fixed to |
|------|-----|----------|
| `store_chapters.go` `AddSeries` seed insert | `VALUES (?, ?, ?, 0, 1)` for `is_primary` | `VALUES (?, ?, ?, 0, TRUE)` |
| `store_admin.go` `UpsertProviderConfig` insert | `VALUES (..., 0)` for `login_tested` | `VALUES (..., FALSE)` |
| `store_admin.go` `UpsertProviderConfig` CASE | `THEN 0` | `THEN FALSE` |

Convention going forward: use `TRUE`/`FALSE` (both dialects accept) or bind a
Go `bool` param — never `0`/`1` for a BOOLEAN column.

## P1e — SQLite-only `IS NOT` operator (FOUND BY PG HARNESS)

`UpsertProviderConfig` used `encrypted_password IS NOT excluded.encrypted_password`.
`IS NOT` as a not-equal operator is a **SQLite extension**; Postgres parses
`IS NOT` only as the NULL-check prefix (`IS NOT NULL`) and threw `syntax error
at or near "excluded"`. Fixed to `<>` — safe because the column is
`NOT NULL DEFAULT ''` on both dialects, so `<>` never sees a NULL.

---

## P2 — Structural / maintenance hazards

### P2a — Postgres migration system was decorative  ✅ FIXED

`applyPostgresSchema` runs the entire `pgschema.sql` once via `IF NOT EXISTS`.
The `schema_migrations` table receives only three hardcoded rows at the file
tail. **There is no incremental migration runner for Postgres.** Consequences:

1. `pgschema.sql` is a hand-maintained union of every SQLite migration. Any
   new SQLite `ALTER TABLE` added to `migrations[]` in `db.go` must be
   mirrored into `pgschema.sql` **manually** — nothing enforces this.
2. On an **existing** Postgres DB, `IF NOT EXISTS` is a no-op for tables
   that already exist, so new columns added to `pgschema.sql` later are
   **never applied**. Only fresh Postgres DBs get the current schema. The
   deployed instance (`wp.demonstrated.dev`) will silently miss future
   schema changes.

The pgschema header comment claims "Migrations applied incrementally via the
schema_migrations table" — the code does not do this. Either implement a
real runner mirroring `applySQLiteSchema` (loop over a shared migration list,
check `schema_migrations`, apply + record), or rewrite the comment to match
reality and add a test that asserts SQLite and Postgres schemas have
identical column sets.

**Recommended fix:** unify on one migration list consumed by both dialects.
Each migration entry carries optional dialect-specific SQL. SQLite runner
stays as-is; add a Postgres runner that iterates the same list against
`schema_migrations`. Drop the giant hand-synced `pgschema.sql` bootstrap, or
keep it only as the fresh-DB baseline and require all future changes to go
through the migration list.

### P2b — `rebind` caveat (literal `?` in strings)

`db.rebind` is a naive byte scanner: it does not honor quoted-string
literals. If a query ever embeds a literal `?` inside a SQL string (e.g.
`WHERE name LIKE 'a?b'`), rebind rewrites it to `$N` and corrupts the query.
No current query does this; the existing `db_test.go` documents the behavior
(`TestRebindPostgresNumbered` case `'?' AS literal` → `'$1'`). Guard: the
convention is documented in `db.go`; the PG test suite is the runtime guard.
Low urgency until the first such query lands.

### P2c — Hardcoded `$N` placeholders  ✅ MITIGATED

The Postgres-only code paths (`AddSeries`/`AddComicSeries`/`IssueToken`
RETURNING branches, `internal/database/db.go` runner, `cmd/migrate`) use
literal `$1..$N` inside `IsPostgres()` guards or PG-only files. The Tx wrapper
fix (P1a) removed the main trap (raw `*sql.Tx` bypassing rebind). The PG test
harness now exercises every such path, so a future edit that moves a `$N`
query out of its guarded branch fails CI when `WFP_TEST_PG` is set.

### P2d — `content_html` type mismatch  ✅ VERIFIED SAFE

SQLite `content_html TEXT` vs Postgres `BYTEA`. Scans read into `[]byte`;
go-sqlite3 stores `[]byte` as BLOB regardless of TEXT affinity, so compressed
bytes round-trip on both. The archive/content store tests now run on both
dialects and pass, empirically confirming the round-trip. Documented here for
future maintainers; no code change needed.

### P2e — `comic_chapters.published_at` is `TEXT` on both dialects

Consistent across dialects (not a divergence bug), but unlike
`chapters.published_at` (DATETIME/TIMESTAMPTZ → `time.Time`). Noted for any
future cross-table chapter logic. No change.

---

## P3 — Test gaps  ✅ FIXED

Added an opt-in Postgres test harness:

- `internal/handlers/store_pg_test.go` — `openTestDB(t)` returns a Store DB
  for the active dialect: Postgres when `WFP_TEST_PG` is set, else a temp
  SQLite file. For Postgres it `TRUNCATE ... RESTART IDENTITY CASCADE`s every
  public table so each test gets clean-state isolation equivalent to SQLite's
  per-test temp file. `newTestStore` and `newTestStoreWithBlob` route through
  it, so **the entire existing handlers store suite now runs on Postgres
  unchanged** when the env var is set.
- `internal/db/db_test.go` — `TestTxRebindSQLitePassesThrough`,
  `TestTxRebindPostgresNumbered`, `TestBeginReturnsDialectAwareTx` (regression
  guards for the Tx wrapper).
- `internal/database/db_test.go` — `TestMigrationNamesUnique`,
  `TestSQLiteMigrationLedgerComplete`, `TestPostgresMigrationParity` (the
  schema-drift guard).

Run locally:

```
docker run -d --name wfp-pgtest --network host \
  -e POSTGRES_DB=wfp_test -e POSTGRES_USER=wfp -e POSTGRES_PASSWORD=wfp \
  postgres:16-alpine -c port=55432
WFP_TEST_PG="postgres://wfp:wfp@localhost:55432/wfp_test?sslmode=disable" \
  go test ./internal/db/... ./internal/database/... ./internal/handlers/... ./internal/api/v1/...
```

---

## Remediation priority order  (all COMPLETE)

1. ✅ **P1a** (tx rebind) — `db.Tx` wrapper; `DB.Begin/BeginTx` return `*Tx`.
2. ✅ **P1b** (`AddSource`, `IssueToken`) — RETURNING id / fetch-by-URL.
3. ✅ **P3** (Postgres test harness) — landed first, caught P1d/P1e.
4. ✅ **P2a** (unified migration runner) — Postgres incremental ledger.
5. ✅ **P1d/P1e** (boolean literals + `IS NOT`) — found + fixed via harness.
6. ✅ **P2b–P2e** — documented / verified / mitigated.

---

## Patch checklist

- [x] `AddSeries` — RETURNING id on Postgres
- [x] `AddComicSeries` — RETURNING id on Postgres
- [x] `db.Tx` wrapper; all tx sites rebind automatically (PromoteSource,
      DeleteSeriesArchive, DeleteChapterArchive)
- [x] `AddSource` — fetch by URL, no LastInsertId (dialect-free)
- [x] `IssueToken` — RETURNING id on Postgres
- [x] Boolean-literal inserts (`is_primary=1`, `login_tested=0`) → TRUE/FALSE
- [x] `IS NOT` SQLite-only operator → `<>`
- [x] Postgres integration test harness (`WFP_TEST_PG`)
- [x] Tx rebind regression tests (`internal/db`)
- [x] Migration parity + uniqueness tests (`internal/database`)
- [x] Unified SQLite/Postgres migration runner (Postgres incremental ledger)
