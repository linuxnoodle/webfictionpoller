-- Postgres schema for webfictionpoller.
-- Ports the SQLite schema in internal/database/db.go one-to-one.
-- Migrations applied incrementally via the schema_migrations table; the
-- bootstrap schema below is the union of all current SQLite migrations.

CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS series (
    id            BIGSERIAL PRIMARY KEY,
    title         TEXT NOT NULL,
    author        TEXT NOT NULL DEFAULT '',
    source_url    TEXT NOT NULL UNIQUE,
    provider_name TEXT NOT NULL,
    rating        DOUBLE PRECISION NOT NULL DEFAULT -1.0 CHECK (rating >= -1 AND rating <= 10),
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'dropped', 'hiatus', 'binge')),
    summary       TEXT NOT NULL DEFAULT '',
    image_url     TEXT NOT NULL DEFAULT '',
    archive       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_series_provider ON series(provider_name);
CREATE INDEX IF NOT EXISTS idx_series_status   ON series(status);

CREATE TABLE IF NOT EXISTS chapters (
    id            BIGSERIAL PRIMARY KEY,
    series_id     BIGINT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    title         TEXT NOT NULL,
    url           TEXT NOT NULL,
    published_at  TIMESTAMPTZ,
    is_read       BOOLEAN NOT NULL DEFAULT FALSE,
    preview_html  TEXT NOT NULL DEFAULT '',
    content_html  BYTEA DEFAULT '',
    content_compressed BOOLEAN NOT NULL DEFAULT FALSE,
    word_count    INTEGER NOT NULL DEFAULT 0,
    premium       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(series_id, url)
);
CREATE INDEX IF NOT EXISTS idx_chapters_series_id   ON chapters(series_id);
CREATE INDEX IF NOT EXISTS idx_chapters_is_read     ON chapters(is_read);
CREATE INDEX IF NOT EXISTS idx_chapters_published_at ON chapters(published_at);

CREATE TABLE IF NOT EXISTS provider_configs (
    id                 BIGSERIAL PRIMARY KEY,
    provider_name      TEXT NOT NULL UNIQUE,
    cookie_data        TEXT NOT NULL DEFAULT '',
    username           TEXT NOT NULL DEFAULT '',
    encrypted_password TEXT NOT NULL DEFAULT '',
    login_tested       BOOLEAN NOT NULL DEFAULT FALSE,
    last_polled        TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chapter_images (
    id           BIGSERIAL PRIMARY KEY,
    chapter_id   BIGINT NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
    url          TEXT NOT NULL,
    data         BYTEA NOT NULL,
    content_type TEXT NOT NULL DEFAULT '',
    UNIQUE(chapter_id, url)
);
CREATE INDEX IF NOT EXISTS idx_chapter_images_chapter ON chapter_images(chapter_id);

CREATE TABLE IF NOT EXISTS reading_progress (
    series_id        BIGINT PRIMARY KEY REFERENCES series(id) ON DELETE CASCADE,
    chapter_id       BIGINT NOT NULL REFERENCES chapters(id) ON DELETE CASCADE,
    scroll_position  DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    label        TEXT NOT NULL DEFAULT '',
    device_id    TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);

CREATE TABLE IF NOT EXISTS comic_series (
    id           BIGSERIAL PRIMARY KEY,
    source_id    TEXT NOT NULL,
    title        TEXT NOT NULL,
    author       TEXT NOT NULL DEFAULT '',
    artist       TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    cover_url    TEXT NOT NULL DEFAULT '',
    source_url   TEXT NOT NULL UNIQUE,
    provider_name TEXT NOT NULL DEFAULT 'mangadex',
    status       TEXT NOT NULL DEFAULT 'active',
    genres       TEXT NOT NULL DEFAULT '',
    rating       DOUBLE PRECISION NOT NULL DEFAULT -1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_comic_series_provider ON comic_series(provider_name);

CREATE TABLE IF NOT EXISTS comic_chapters (
    id          BIGSERIAL PRIMARY KEY,
    series_id   BIGINT NOT NULL REFERENCES comic_series(id) ON DELETE CASCADE,
    source_id   TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    chapter_num TEXT NOT NULL DEFAULT '',
    volume_num  TEXT NOT NULL DEFAULT '',
    source_url  TEXT NOT NULL DEFAULT '',
    pages       INTEGER NOT NULL DEFAULT 0,
    is_read     BOOLEAN NOT NULL DEFAULT FALSE,
    downloaded  BOOLEAN NOT NULL DEFAULT FALSE,
    published_at TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_comic_chapters_series ON comic_chapters(series_id);

CREATE TABLE IF NOT EXISTS comic_pages (
    id           BIGSERIAL PRIMARY KEY,
    chapter_id   BIGINT NOT NULL REFERENCES comic_chapters(id) ON DELETE CASCADE,
    page_index   INTEGER NOT NULL,
    image_url    TEXT NOT NULL,
    data         BYTEA,
    content_type TEXT NOT NULL DEFAULT '',
    UNIQUE(chapter_id, page_index)
);
CREATE INDEX IF NOT EXISTS idx_comic_pages_chapter ON comic_pages(chapter_id);

CREATE TABLE IF NOT EXISTS comic_reading_progress (
    series_id  BIGINT PRIMARY KEY REFERENCES comic_series(id) ON DELETE CASCADE,
    chapter_id BIGINT NOT NULL REFERENCES comic_chapters(id) ON DELETE CASCADE,
    page_index INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- scs session store expects this exact shape.
CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BYTEA NOT NULL,
    expiry TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(token);

CREATE TABLE IF NOT EXISTS schema_migrations (
    name TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS series_sources (
    id                BIGSERIAL PRIMARY KEY,
    series_id         BIGINT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    provider_name     TEXT NOT NULL,
    source_url        TEXT NOT NULL,
    priority          INTEGER NOT NULL DEFAULT 100,
    is_primary        BOOLEAN NOT NULL DEFAULT FALSE,
    last_ok           TIMESTAMPTZ,
    last_fail         TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    consecutive_fails INTEGER NOT NULL DEFAULT 0,
    disabled          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(series_id, source_url)
);
CREATE INDEX IF NOT EXISTS idx_series_sources_series ON series_sources(series_id);

-- Seed each existing series with a primary source reflecting the legacy
-- series.source_url + provider_name columns. Idempotent: only inserts rows
-- for series that don't yet have any source.
INSERT INTO series_sources (series_id, provider_name, source_url, priority, is_primary)
SELECT s.id, s.provider_name, s.source_url, 0, TRUE
FROM series s
WHERE NOT EXISTS (
    SELECT 1 FROM series_sources ss WHERE ss.series_id = s.id
);

-- Mirror the SQLite _migrations ledger so both dialects report the same
-- applied state.
INSERT INTO schema_migrations (name) VALUES
    ('series_sources'),
    ('series_sources_idx'),
    ('series_sources_seed')
ON CONFLICT (name) DO NOTHING;
