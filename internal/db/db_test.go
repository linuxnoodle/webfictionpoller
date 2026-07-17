package db

import (
	"strings"
	"testing"
)

func TestDetectDialect(t *testing.T) {
	cases := []struct {
		in   string
		want Dialect
	}{
		{"", DialectSQLite},
		{"data.db", DialectSQLite},
		{"/abs/path/data.db?_foreign_keys=1&_journal_mode=WAL", DialectSQLite},
		{"data.db?_journal_mode=WAL", DialectSQLite},
		{"postgres://user:pass@host:5432/dbname?sslmode=disable", DialectPostgres},
		{"postgresql://user@host/db", DialectPostgres},
		{"host=localhost user=postgres dbname=app", DialectPostgres},
		{"host=db.example.com port=5432 sslmode=require", DialectPostgres},
	}
	for _, c := range cases {
		if got := detectDialect(c.in); got != c.want {
			t.Errorf("detectDialect(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRebindSQLitePassesThrough(t *testing.T) {
	db := &DB{dialect: DialectSQLite}
	in := "SELECT id, name FROM users WHERE id = ? AND active = ?"
	if got := db.rebind(in); got != in {
		t.Errorf("sqlite should pass through: got %q", got)
	}
}

func TestRebindPostgresNumbered(t *testing.T) {
	db := &DB{dialect: DialectPostgres}
	cases := []struct {
		in, want string
	}{
		{
			"SELECT id FROM users WHERE id = ?",
			"SELECT id FROM users WHERE id = $1",
		},
		{
			"INSERT INTO chapters (series_id, title, url) VALUES (?, ?, ?)",
			"INSERT INTO chapters (series_id, title, url) VALUES ($1, $2, $3)",
		},
		{
			"SELECT * FROM t WHERE a = ? AND b = ? AND c = ? AND d = ? AND e = ? AND f = ? AND g = ? AND h = ? AND i = ? AND j = ? AND k = ?",
			"SELECT * FROM t WHERE a = $1 AND b = $2 AND c = $3 AND d = $4 AND e = $5 AND f = $6 AND g = $7 AND h = $8 AND i = $9 AND j = $10 AND k = $11",
		},
		{
			"SELECT * FROM t", // no placeholders
			"SELECT * FROM t",
		},
		{
			"SELECT '?' AS literal", // quoted question mark — we don't escape, treat as placeholder
			"SELECT '$1' AS literal",
		},
	}
	for _, c := range cases {
		if got := db.rebind(c.in); got != c.want {
			t.Errorf("rebind(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestRebindCountsArgs verifies every placeholder gets a unique number even
// when there are gaps in the source (there shouldn't be, but check).
func TestRebindCountsArgs(t *testing.T) {
	db := &DB{dialect: DialectPostgres}
	got := db.rebind("UPDATE t SET a=?, b=?, c=? WHERE id=?")
	for i := 1; i <= 4; i++ {
		needle := "$" + itoa(i)
		if !strings.Contains(got, needle) {
			t.Errorf("rebind result %q missing %s", got, needle)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
