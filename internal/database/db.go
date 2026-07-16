package database

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

var schema = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS series (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL,
    author TEXT DEFAULT '',
    source_url TEXT NOT NULL UNIQUE,
    provider_name TEXT NOT NULL,
    rating REAL DEFAULT -1.0 CHECK(rating >= -1 AND rating <= 10),
    status TEXT DEFAULT 'active' CHECK(status IN ('active', 'dropped', 'hiatus', 'binge')),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS chapters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id INTEGER NOT NULL,
    title TEXT NOT NULL,
    url TEXT NOT NULL,
    published_at DATETIME,
    is_read BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE,
    UNIQUE(series_id, url)
);

CREATE TABLE IF NOT EXISTS provider_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_name TEXT NOT NULL UNIQUE,
    cookie_data TEXT DEFAULT '',
    last_polled DATETIME
);

CREATE INDEX IF NOT EXISTS idx_chapters_series_id ON chapters(series_id);
CREATE INDEX IF NOT EXISTS idx_chapters_is_read ON chapters(is_read);
CREATE INDEX IF NOT EXISTS idx_series_provider ON series(provider_name);
CREATE INDEX IF NOT EXISTS idx_series_status ON series(status);

CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    data BLOB NOT NULL,
    expiry TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token);

CREATE INDEX IF NOT EXISTS idx_chapters_published_at ON chapters(published_at);

CREATE TABLE IF NOT EXISTS _migrations (name TEXT PRIMARY KEY);
`

type migration struct {
	name string
	sql  string
}

var migrations = []migration{
	{"add_preview_html", "ALTER TABLE chapters ADD COLUMN preview_html TEXT DEFAULT ''"},
	{"unrated_rating", "UPDATE series SET rating = -1 WHERE rating = 5.0 OR rating = 0"},
	{"provider_credentials", "ALTER TABLE provider_configs ADD COLUMN username TEXT DEFAULT ''"},
	{"provider_encrypted_password", "ALTER TABLE provider_configs ADD COLUMN encrypted_password TEXT DEFAULT ''"},
	{"series_summary", "ALTER TABLE series ADD COLUMN summary TEXT DEFAULT ''"},
	{"series_image_url", "ALTER TABLE series ADD COLUMN image_url TEXT DEFAULT ''"},
	{"provider_login_tested", "ALTER TABLE provider_configs ADD COLUMN login_tested BOOLEAN DEFAULT 0"},
	{"series_archive", "ALTER TABLE series ADD COLUMN archive BOOLEAN DEFAULT 0"},
	{"chapter_content_html", "ALTER TABLE chapters ADD COLUMN content_html TEXT DEFAULT ''"},
	{"chapter_images", `CREATE TABLE IF NOT EXISTS chapter_images (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chapter_id INTEGER NOT NULL,
		url TEXT NOT NULL,
		data BLOB NOT NULL,
		content_type TEXT DEFAULT '',
		FOREIGN KEY (chapter_id) REFERENCES chapters(id) ON DELETE CASCADE,
		UNIQUE(chapter_id, url)
	)`},
	{"chapter_images_idx", "CREATE INDEX IF NOT EXISTS idx_chapter_images_chapter ON chapter_images(chapter_id)"},
	{"settings_table", `CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`},
	{"content_compressed", "ALTER TABLE chapters ADD COLUMN content_compressed BOOLEAN DEFAULT 0"},
	{"reading_progress", `CREATE TABLE IF NOT EXISTS reading_progress (
		series_id INTEGER PRIMARY KEY,
		chapter_id INTEGER NOT NULL,
		scroll_position REAL DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE,
		FOREIGN KEY (chapter_id) REFERENCES chapters(id) ON DELETE CASCADE
	)`},
	{"comic_series", `CREATE TABLE IF NOT EXISTS comic_series (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_id TEXT NOT NULL,
		title TEXT NOT NULL,
		author TEXT DEFAULT '',
		artist TEXT DEFAULT '',
		description TEXT DEFAULT '',
		cover_url TEXT DEFAULT '',
		source_url TEXT NOT NULL UNIQUE,
		provider_name TEXT NOT NULL DEFAULT 'mangadex',
		status TEXT DEFAULT 'active',
		genres TEXT DEFAULT '',
		rating REAL DEFAULT -1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`},
	{"comic_series_idx", `CREATE INDEX IF NOT EXISTS idx_comic_series_provider ON comic_series(provider_name)`},
	{"comic_chapters", `CREATE TABLE IF NOT EXISTS comic_chapters (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		series_id INTEGER NOT NULL,
		source_id TEXT NOT NULL UNIQUE,
		title TEXT NOT NULL,
		chapter_num TEXT DEFAULT '',
		volume_num TEXT DEFAULT '',
		source_url TEXT DEFAULT '',
		pages INTEGER DEFAULT 0,
		is_read BOOLEAN DEFAULT 0,
		downloaded BOOLEAN DEFAULT 0,
		published_at TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (series_id) REFERENCES comic_series(id) ON DELETE CASCADE
	)`},
	{"comic_chapters_idx", `CREATE INDEX IF NOT EXISTS idx_comic_chapters_series ON comic_chapters(series_id)`},
	{"comic_pages", `CREATE TABLE IF NOT EXISTS comic_pages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chapter_id INTEGER NOT NULL,
		page_index INTEGER NOT NULL,
		image_url TEXT NOT NULL,
		data BLOB,
		content_type TEXT DEFAULT '',
		FOREIGN KEY (chapter_id) REFERENCES comic_chapters(id) ON DELETE CASCADE,
		UNIQUE(chapter_id, page_index)
	)`},
	{"comic_pages_idx", `CREATE INDEX IF NOT EXISTS idx_comic_pages_chapter ON comic_pages(chapter_id)`},
	{"comic_reading_progress", `CREATE TABLE IF NOT EXISTS comic_reading_progress (
		series_id INTEGER PRIMARY KEY,
		chapter_id INTEGER NOT NULL,
		page_index INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (series_id) REFERENCES comic_series(id) ON DELETE CASCADE,
		FOREIGN KEY (chapter_id) REFERENCES comic_chapters(id) ON DELETE CASCADE
	)`},
	{"api_tokens", `CREATE TABLE IF NOT EXISTS api_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		label TEXT NOT NULL DEFAULT '',
		device_id TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_used_at DATETIME,
		expires_at DATETIME,
		revoked_at DATETIME,
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`},
	{"api_tokens_idx", `CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id)`},
	{"api_tokens_hash_idx", `CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash)`},
}

func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("running schema: %w", err)
	}

	for _, m := range migrations {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM _migrations WHERE name = ?", m.name).Scan(&count); err != nil {
			continue
		}
		if count == 0 {
			db.Exec(m.sql)
			db.Exec("INSERT INTO _migrations (name) VALUES (?)", m.name)
		}
	}

	return db, nil
}
