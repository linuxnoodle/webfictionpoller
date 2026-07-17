// cmd/migrate moves data from an existing SQLite database to a Postgres
// instance. Run it once when adopting Postgres; afterward point the app at
// the Postgres DATABASE_URL and remove the SQLite file.
//
// Usage:
//
//	go run ./cmd/migrate \
//	  -from /path/to/data.db \
//	  -to   "postgres://user:pass@host:5432/webfictionpoller?sslmode=disable"
//
// The tool is idempotent within a table: existing target rows are replaced
// via TRUNCATE at the start of each table. It does NOT delete the source DB.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/db"
)

func main() {
	from := flag.String("from", "", "Source SQLite path (required)")
	to := flag.String("to", "", "Target Postgres URL (required)")
	batchSize := flag.Int("batch", 500, "Row batch size for streaming copies")
	flag.Parse()

	if *from == "" || *to == "" {
		log.Fatal("both -from and -to are required")
	}

	start := time.Now()
	src, err := database.Open(*from + "?_foreign_keys=1&_journal_mode=ro")
	if err != nil {
		log.Fatalf("open source: %v", err)
	}
	defer src.Close()

	dst, err := database.Open(*to)
	if err != nil {
		log.Fatalf("open target: %v", err)
	}
	defer dst.Close()
	if !dst.IsPostgres() {
		log.Fatalf("target must be Postgres, got dialect %s", dst.Dialect())
	}

	ctx := context.Background()
	log.Printf("[migrate] source dialect=%s target dialect=%s", src.Dialect(), dst.Dialect())
	log.Printf("[migrate] verifying target schema (idempotent)")
	if err := ensurePostgresSchema(ctx, dst); err != nil {
		log.Fatalf("schema: %v", err)
	}

	tables := []migrationTable{
		// Order matters: parents before children due to FK constraints.
		{Name: "users", Columns: "id, username, password_hash"},
		{Name: "series", Columns: "id, title, author, source_url, provider_name, rating, status, summary, image_url, archive, created_at"},
		{Name: "chapters", Columns: "id, series_id, title, url, published_at, is_read, preview_html, content_html, content_compressed, created_at"},
		{Name: "provider_configs", Columns: "id, provider_name, cookie_data, username, encrypted_password, login_tested, last_polled"},
		{Name: "settings", Columns: "key, value"},
		{Name: "chapter_images", Columns: "id, chapter_id, url, data, content_type"},
		{Name: "reading_progress", Columns: "series_id, chapter_id, scroll_position, updated_at"},
		{Name: "api_tokens", Columns: "id, user_id, token_hash, label, device_id, created_at, last_used_at, expires_at, revoked_at"},
		{Name: "comic_series", Columns: "id, source_id, title, author, artist, description, cover_url, source_url, provider_name, status, genres, rating, created_at"},
		{Name: "comic_chapters", Columns: "id, series_id, source_id, title, chapter_num, volume_num, source_url, pages, is_read, downloaded, published_at, created_at"},
		{Name: "comic_pages", Columns: "id, chapter_id, page_index, image_url, data, content_type"},
		{Name: "comic_reading_progress", Columns: "series_id, chapter_id, page_index, updated_at"},
		{Name: "sessions", Columns: "token, data, expiry"},
	}

	var totalRows int64
	for _, t := range tables {
		n, err := copyTable(ctx, src, dst, t, *batchSize)
		if err != nil {
			log.Fatalf("table %s: %v", t.Name, err)
		}
		totalRows += n
		log.Printf("[migrate] %-26s %8d rows", t.Name, n)
	}

	log.Printf("[migrate] done: %d rows in %s", totalRows, time.Since(start).Round(time.Millisecond))

	// Sync sequences on Postgres so BIGSERIAL ids continue past the migrated max.
	if err := syncSequences(ctx, dst); err != nil {
		log.Printf("[migrate] WARNING: sequence sync failed: %v", err)
	}
}

type migrationTable struct {
	Name    string
	Columns string
}

// ensurePostgresSchema applies the Postgres DDL to the target. The schema
// itself is idempotent (CREATE TABLE IF NOT EXISTS), so this is safe to run
// repeatedly.
func ensurePostgresSchema(ctx context.Context, dst *db.DB) error {
	return database.EnsurePostgresSchema(dst)
}

// copyTable truncates the target table then streams rows from src to dst in
// batches of `batch`. Returns rows copied. Reads the same column list from
// both sides; type mismatches are surfaced as errors.
func copyTable(ctx context.Context, src, dst *db.DB, t migrationTable, batch int) (int64, error) {
	// Truncate target for idempotency. CASCADE because chapters depend on series.
	if _, err := dst.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", t.Name)); err != nil {
		return 0, fmt.Errorf("truncate: %w", err)
	}

	rows, err := src.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", t.Columns, t.Name))
	if err != nil {
		return 0, fmt.Errorf("select: %w", err)
	}
	defer rows.Close()

	// Inspect column count from the source cursor.
	types, err := rows.ColumnTypes()
	if err != nil {
		return 0, err
	}
	cols := len(types)

	// Build a parameterised insert: INSERT INTO t (cols) VALUES ($1,...), (...), ...
	// We batch `batch` rows per round-trip for throughput.
	placeholderGroups := make([]string, 0, batch)
	valueArgs := make([]interface{}, 0, batch*cols)
	paramsPerRow := "(?" // SQLite; the rebind wrapper translates to $N for dst.
	for i := 1; i < cols; i++ {
		paramsPerRow += ", ?"
	}
	paramsPerRow += ")"

	var total int64
	scanBuf := make([]interface{}, cols)
	scanPtrs := make([]interface{}, cols)
	for i := range scanPtrs {
		scanPtrs[i] = &scanBuf[i]
	}

	flush := func() error {
		if len(placeholderGroups) == 0 {
			return nil
		}
		stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON CONFLICT DO NOTHING",
			t.Name, t.Columns, joinStrings(placeholderGroups, ", "))
		if _, err := dst.ExecContext(ctx, stmt, valueArgs...); err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}
		total += int64(len(placeholderGroups))
		placeholderGroups = placeholderGroups[:0]
		valueArgs = valueArgs[:0]
		return nil
	}

	for rows.Next() {
		if err := rows.Scan(scanPtrs...); err != nil {
			return total, fmt.Errorf("scan: %w", err)
		}
		placeholderGroups = append(placeholderGroups, paramsPerRow)
		for _, v := range scanBuf {
			valueArgs = append(valueArgs, v)
		}
		if len(placeholderGroups) >= batch {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

// syncSequences bumps every BIGSERIAL sequence in the target past the max id
// already present. Without this, new inserts via Postgres would collide with
// migrated ids.
func syncSequences(ctx context.Context, dst *db.DB) error {
	seqTables := []string{"users", "series", "chapters", "provider_configs",
		"chapter_images", "api_tokens", "comic_series", "comic_chapters", "comic_pages"}
	for _, table := range seqTables {
		// Postgres auto-named sequence convention: <table>_id_seq.
		stmt := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence('%s', 'id'),
			           COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)`,
			table, table)
		if _, err := dst.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sync sequence %s: %w", table, err)
		}
	}
	return nil
}

func joinStrings(in []string, sep string) string {
	if len(in) == 0 {
		return ""
	}
	out := in[0]
	for _, s := range in[1:] {
		out += sep + s
	}
	return out
}

// guard against unused import on platforms without fs.ErrNotExist.
var _ = fs.ErrNotExist
var _ = os.Args
