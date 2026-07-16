# webfiction_poller — Refactor & Feature Plan

Goal: turn site-specific polling into modular plugins, fix comics page, make local
downloads work for all features, expose clean API for iOS app, support arbitrary sites.

## Locked decisions (2026-07-16)

1. **Plugin model**: compiled-in Go providers via `init()` self-registration. **No scripting, no WASM, no Lua.** Pure Go. Adding a site = new Go package + blank import.
2. **Data layer**: **PostgreSQL** for relational metadata + **filesystem (default) or MinIO/S3** for binary blobs (images, EPUB/CBZ bundles). **Dragonfly optional** as LRU cache for hot reads. **NOT ScyllaDB** — workload is highly relational, low write volume, single-node; Scylla's distributed-wide-column strengths go unused while its complexity (no joins/FKs, quorum ops, eventual consistency) actively hurts. SQLite BLOB behaviour (WAL bloat, serialized writes via `SetMaxOpenConns(1)`) is the real bug being fixed.
3. **iOS auth**: per-device bearer tokens (32-byte random, bcrypt-hashed at rest, labeled, revocable).
4. **Arbitrary-site support**: (c) both — easy compiled-in Go provider path + declarative TOML manifest for simple RSS+selector sites.
5. **Polling**: per-provider intervals + per-provider rate/concurrency (global default + per-provider override).
6. **Web UI + iOS**: both first-class clients. Old `/api/*` (htmx) routes stay; new `/api/v1/*` is canonical for mobile + future SPA.
7. **Declarative provider authoring**: filesystem-only initially (`data/providers/*.toml` loaded on startup). Web UI editor deferred.

## Current state (what we have)

- **6 text providers** + **1 comic provider** hardcoded in `cmd/main.go:62-72`.
- Two parallel, incompatible provider interfaces:
  - `providers.Provider` (text): `Name/MatchURL/FetchSeriesMetadata/PollUpdates/FetchChapterContent/FetchComments/RequiresAuth/SetCookies/SupportsLogin/Login`
  - `comics.ComicProvider`: `Name/SearchManga/MangaDetails/ChapterList/PageList`
- God `Store` struct (`handlers/store.go`) — text + comics + archive + admin + reader all on one `*sql.DB`.
- 4 duplicated HTTP helpers: `providers/http.go`, `comics/provider.go doGet`, `safefetch/client.go`, inline `http.Get` in comic download.
- Provider metadata scattered 4 places: `cmd/main.go`, `models.ProviderNames()`, `models.ProviderFavicon()`, `models.ProviderFaviconSource()`.
- Worker pool: hardcoded 4 workers, 1 Hz per-provider-per-worker rate limit, single global `POLL_INTERVAL`.
- Auth: session cookie + CSRF only. OPDS uses HTTP Basic. **No token auth** — iOS blocker.
- Archive = gzipped HTML + image BLOBs in SQLite. No on-disk file storage.
- Comic download = async cache of page image BLOBs in SQLite.
- `comicProviderByName` falls back to "pick any" — breaks with >1 comic provider.
- Favicon prefetch blocks startup hitting all upstreams.

## Target architecture

```
cmd/main.go                      — wiring only, no provider names
internal/
  plugin/                        — NEW: registry, capability interfaces, metadata
    registry.go                    Provider interface + Registry + Capabilities
    catalog.go                     built-in provider manifest (name, kind, capabilities, favicon)
    declarative.go                 RSS+selector config-driven provider (for "simple" sites)
  provider/
    text/                         — text providers (one pkg each)
      royalroad/  ao3/  fanfictionnet/  xenforo/  (xenforo exports SB/SV/QQ constructors)
    comic/                        — comic providers
      mangadex/
    shared/                       — NEW: unified HTTP client, retry, UA, rate limit, SSRF guard
      http.go                      (absorbs providers/http.go + safefetch + comics doGet)
      auth.go                      cookie jar, login form helpers
  store/                          — NEW: split god Store
    series.go chapter.go archive.go comic.go config.go settings.go reader.go progress.go
    interface.go                   Store interfaces consumed by workers/handlers/opds
  api/                            — NEW: iOS-ready JSON API (versioned, token auth)
    v1/                            /api/v1/* — see API surface below
    auth.go                        bearer token + session bridge
    dto.go                         stable DTOs (separate from internal models)
  worker/                         — refactor: per-provider rate/concurrency config
  archive/                        — promote archiver, add on-disk storage backend
    storage.go                     interface { SQLite | Filesystem }
  opds/                           — extend to comics (page-based acquisition)
  handlers/                       — web UI only; JSON moves to internal/api
  ios/                            — placeholder, empty for now (app built later)
  models/                         — shared domain types
```

### Core plugin contract (proposed)

Single `Provider` with **capability interfaces**. Code asks "does this provider implement `Searcher`?" rather than branching on type. Avoids forcing comic ops onto text providers.

```go
// internal/plugin/registry.go
package plugin

type Kind string
const (
    KindText  Kind = "text"
    KindComic Kind = "comic"
)

type Meta struct {
    Name        Kind    // "royalroad", "mangadex", ...
    DisplayName string  // "Royal Road"
    Kind        Kind
    Homepage    string  // for favicon prefetch + attribution
    FaviconURL  string
    AuthModes   []string // {"none"}, {"cookies"}, {"login"}, {"flaresolverr"}
    Rate        RateSpec // per-provider default: e.g. 1 req/s, burst 2
}

type RateSpec struct {
    RequestsPerSecond float64
    Burst             int
    Concurrency       int
}

// Base capability — every plugin implements.
type Provider interface {
    Meta() Meta
    MatchURL(rawURL string) bool
}

// Text capabilities
type SeriesLister   interface { FetchSeriesMetadata(url string) (models.Series, error) }
type Poller         interface { PollUpdates(series models.Series) ([]models.Chapter, error) }
type HTMLFetcher    interface { FetchChapterContent(url string) (string, error) }
type CommentFetcher interface { FetchComments(url string) ([]providers.Comment, error) }

// Comic capabilities
type Searcher       interface { Search(query string, page int) (*comics.MangasPage, error) }
type ChapterLister  interface { ChapterList(sourceID string) ([]comics.ComicChapter, error) }
type PageLister     interface { PageList(chapterSourceID string) ([]comics.ComicPage, error) }

// Auth capabilities
type CookieAuth     interface { SetCookies(string) error }
type LoginAuth      interface { Login(user, pass string) error }
type CredentialSrc  interface { SetCredentialSource(fn func() (string, string, bool)) }

// Registry
type Registry struct { ... }
func (r *Registry) Register(p Provider)
func (r *Registry) Get(name string) Provider
func (r *Registry) ByURL(url string) Provider
func (r *Registry) WithCapability(cap interface{}) []Provider  // e.g. pass (*Poller)(nil)
```

This way:
- `worker/pool.go` queries `registry.WithCapability((*Poller)(nil))`.
- Comics UI queries `WithCapability((*Searcher)(nil))`.
- `MatchURL` lives on the base — every provider URL-routable.

### Built-in providers (Go, compiled-in)

Compiled-in registry with `init()` self-registration. Each provider package has:

```go
// internal/provider/text/royalroad/royalroad.go
func init() {
    plugin.Default.Register(&RoyalRoadProvider{...})
}
```

`cmd/main.go` imports for side-effects:
```go
import (
    _ "github.com/.../internal/provider/text/royalroad"
    _ "github.com/.../internal/provider/text/ao3"
    _ "github.com/.../internal/provider/text/fanfictionnet"
    _ "github.com/.../internal/provider/text/xenforo"   // registers SB+SV+QQ
    _ "github.com/.../internal/provider/comic/mangadex"
)
```

**No Go `plugin` package** (linux-only, fragile across versions, bad UX for Docker single-binary). **No scripting/WASM/Lua** — pure Go, type-safe, idiomatic.

### Declarative TOML providers (for "any site" without recompile)

For sites that are just "RSS feed + CSS selectors", support a TOML manifest loaded from `data/providers/*.toml`:

```toml
# data/providers/somesite.toml
name = "somesite"
display_name = "Some Site"
homepage = "https://somesite.com"
kind = "text"

[poll]
rss_feed_template = "https://somesite.com/rss/{id}"
interval = "30m"

[scrape]
series_title_selector    = ".fic-title"
chapter_list_selector    = ".chapter-link"
chapter_content_selector = ".chapter-content"
```

`plugin/declarative.go` parses these at startup and registers a generic `DeclarativeProvider`. Implements `Poller` + `HTMLFetcher` only; no auth/login. **Authoring is filesystem-only** (drop TOML into `data/providers/`, restart to load). Web UI editor deferred.

All output from declarative scrapers routed through existing `safefetch` SSRF guard + bluemonday sanitizer — never raw-inserted into responses.

---

## API surface for iOS (new `internal/api/v1`)

Stable, versioned, **token-authenticated** JSON. Token model: per-device bearer tokens,
32-byte random, stored hashed (bcrypt) in `api_tokens` table. Created/revoked via web UI.

```
POST   /api/v1/auth/login            {username,password} → {token, expires_at}
POST   /api/v1/auth/logout
GET    /api/v1/auth/me

GET    /api/v1/library               ?kind=text|comic
GET    /api/v1/library/{id}          series + chapter list + progress
POST   /api/v1/library               add by URL or {provider, source_id}
PATCH  /api/v1/library/{id}          rating/status/archive
DELETE /api/v1/library/{id}

GET    /api/v1/chapters              ?series_id & unread_only & page
GET    /api/v1/chapters/{id}         metadata
GET    /api/v1/chapters/{id}/content html
POST   /api/v1/chapters/{id}/read
POST   /api/v1/library/{id}/read-all
GET    /api/v1/unread-count

POST   /api/v1/progress              {series_id, chapter_id, scroll|page_index}

GET    /api/v1/poll/status
POST   /api/v1/poll/now              ?series_id or all

GET    /api/v1/providers             list + capabilities + auth status
POST   /api/v1/providers/{name}/auth {cookies | username,password}
POST   /api/v1/providers/{name}/test

GET    /api/v1/search?q=             cross-provider (text + comic)

GET    /api/v1/downloads             manifest: what's cached offline
GET    /api/v1/downloads/chapters/{id}      full chapter bundle (html+images zip)
GET    /api/v1/downloads/comics/{chapterId} CBZ or zip of pages
POST   /api/v1/downloads/comics/{chapterId} request background download
GET    /api/v1/downloads/status/{chapterId}

GET    /api/v1/opds                  (existing OPDS, also token-authed)
```

DTOs in `internal/api/v1/dto.go` decoupled from `models.*` so internal schema can evolve.

Old `/api/*` (web UI) routes stay until web UI migrates; then deprecated.

### Auth middleware

```
Authorization: Bearer <token>      → API
Cookie: session=...                 → web UI (existing)
```

`apiAuthMiddleware` tries bearer first, falls back to session. CSRF exempt for bearer (it's not browser-originated).

---

## Local downloads — design

Right now: text archive HTML + comic page images are BLOBs in SQLite (bloats DB, fights WAL, slows backups). For iOS we need:

1. **Manifest endpoint** — what's downloaded, byte size, mtime, completeness.
2. **Bundle endpoint** — single GET returning full chapter (html + inlined images) or comic chapter (CBZ/zip).
3. **Background download request** — iOS says "cache this", server fetches, status polled.
4. **Storage backend abstraction** — binaries on filesystem (default) or MinIO/S3 (for presigned URLs to iOS).

```go
type BlobStore interface {
    Put(ctx context.Context, kind string, id int64, name string, r io.Reader) (size int64, err error)
    Get(ctx context.Context, kind string, id int64, name string) (io.ReadCloser, error)
    Delete(ctx context.Context, kind string, id int64) error
    Size(ctx context.Context, kind string, id int64) (int64, error)
    List(ctx context.Context, kind string, id int64) ([]string, error)
    Presign(ctx context.Context, kind string, id int64, name string, ttl time.Duration) (string, error) // MinIO/S3 only
}
```

Implementations:
- `fsBlob` — `data/blobs/{kind}/{id}/{name}` (default)
- `minioBlob` — S3-compatible, enables presigned URLs so iOS fetches bytes directly (Go server not in hot path)

Pick via `STORAGE_BACKEND=fs|minio`. **SQLite BLOB path removed** during Postgres migration (Phase 2).

---

## Phased plan

### Phase 0 — Prep & safety nets  *(~0.5 day)*
- [ ] Add integration test harness: temp Postgres (or sqlite for unit) + tmp FS, run migrations, seed fixtures.
- [ ] Snapshot current behaviour in `store_test.go` extension (chapter insert, archive, comic page).
- [ ] Create `refactor/` branch, tag `pre-refactor-2026-07-16`.
- [ ] Add `pgx`, `golang-migrate`, MinIO SDK deps to `go.mod`.

### Phase 1 — Plugin registry + capability interfaces  *(~1.5 days)*
**Files:**
- [ ] NEW `internal/plugin/registry.go` — `Provider`, capability ifaces, `Registry`, `Default`.
- [ ] NEW `internal/plugin/catalog.go` — `Meta`, `Kind`, `RateSpec`, favicon/homepage data.
- [ ] NEW `internal/provider/shared/http.go` — unified `Client` with retry, UA, SSRF guard, rate limit hook (absorbs `providers/http.go` + `safefetch`).
- [ ] NEW `internal/provider/shared/auth.go` — cookie jar + login form helpers (extracted from xenforo).
- [ ] Migrate `internal/providers/royalroad.go` → `internal/provider/text/royalroad/` implementing capability ifaces.
- [ ] Migrate `ao3.go`, `fanfictionnet.go` similarly.
- [ ] Migrate `xenforo.go` → `internal/provider/text/xenforo/` with SB/SV/QQ subproviders; keep auth + relogin logic.
- [ ] Migrate `comics/mangadex.go` → `internal/provider/comic/mangadex/`.
- [ ] Update `cmd/main.go`: drop hardcoded list, use blank imports + `plugin.Default`.
- [ ] Update `worker/pool.go` + `archiver.go` + `comic_handlers.go` to consume registry via capabilities.

**Acceptance:** existing tests pass; dashboard, comics page, QQ login all work end-to-end.

### Phase 2 — Split god Store + PostgreSQL migration + FS blob backend  *(~3 days)*
- [ ] NEW `internal/store/` with split files (series/chapter/archive/comic/config/settings/reader/progress).
- [ ] NEW `internal/store/interface.go` — consumed by `worker`, `opds`, `api`, `handlers` (extend existing `ArchiverStore` + `opds.Store` pattern).
- [ ] NEW `internal/store/postgres/` — `pgx`-based implementation. Schema ported from current SQLite DDL: types map cleanly (`INTEGER PK AUTOINCREMENT` → `BIGSERIAL`, `TEXT` → `TEXT`, `DATETIME` → `TIMESTAMPTZ`, `BOOLEAN` → `BOOLEAN`). `_migrations` → `schema_migrations` (golang-migrate style).
- [ ] NEW `internal/archive/storage.go` — `BlobStore` interface + `fsBlob` (default) + `minioBlob` impls.
- [ ] NEW `cmd/migrate` — one-shot SQLite→Postgres data migration tool. Streams rows in batches, writes BLOBs to FS backend, verifies counts.
- [ ] Remove `SetMaxOpenConns(1)` write-serialization hack — Postgres handles concurrent writers natively.
- [ ] `DATABASE_URL=postgres://...` env replaces `DB_PATH`. SQLite kept behind build-tag for unit tests only.
- [ ] Keep `handlers.Store` as thin façade during transition, remove after handlers migrate.

**Acceptance:** schema ports cleanly, `cmd/migrate` moves existing user data losslessly, FS blob backend serves images, existing tests pass against Postgres.

### Phase 3 — Fix comics page + comic downloads  *(~1.5 days)*
- [ ] Audit `comic_browse.html` + `comic_reader.html` for broken interactions.
- [ ] Fix `comicProviderByName` "pick any" fallback — require explicit provider, surface selection in UI.
- [ ] Wire comic polling into `worker/pool.go` (currently on-demand only) — add `Poller` impl for comics that refreshes chapter list on schedule, configured per-provider interval.
- [ ] Fix `ComicDownloadChapterAPI` — currently no status/progress; add `/api/v1/downloads/status/{id}` polling endpoint. Pages now stored via `BlobStore` (FS/MinIO), not DB BLOB.
- [ ] Add CBZ generation endpoint (zip of pages + `comicinfo.xml`) for iOS, streamed from BlobStore.
- [ ] Tests for download → cache → serve → status flow.

**Acceptance:** user can search MangaDex, add to library, mark for download, read offline in reader; status visible; iOS can fetch CBZ bundle.

### Phase 4 — Worker improvements  *(~1 day)*
- [ ] Per-provider rate/concurrency from `Meta.Rate` (replace global 1 Hz × 4 workers).
- [ ] Per-provider polling interval override (settings table) with global default. Different sites update at different rates (RR fast, QQ slow, MangaDex slow).
- [ ] Replace startup favicon prefetch blocking with lazy on-demand cache (`favicon.go`).
- [ ] Worker metrics: per-provider last-poll, last-error, chapter-yield — surface in plugins page.

### Phase 5 — Declarative provider support (optional, "any site")  *(~1 day)*
- [ ] NEW `internal/plugin/declarative.go` — TOML-driven generic scraper.
- [ ] `data/providers/*.toml` loading at startup.
- [ ] UI: `/admin/providers/declarative` — list, edit, test.
- [ ] Capability: implements `Poller` + `HTMLFetcher` only; no auth/login.

### Phase 6 — iOS-ready API (`internal/api/v1`)  *(~2.5 days)*
- [ ] NEW `internal/api/` + `v1/` routes mounted at `/api/v1/*`.
- [ ] NEW `api_tokens` Postgres table: `id, user_id, token_hash, label, device_id, created_at, last_used_at, expires_at, revoked_at`. Tokens are per-device, labeled ("qed's iPhone"), bcrypt-hashed at rest.
- [ ] Token management UI in `/admin/tokens` — create (shown once), list, revoke.
- [ ] Bearer auth middleware with session fallback (web UI unaffected).
- [ ] DTO layer decoupled from internal models.
- [ ] Implement all endpoints listed in "API surface" above.
- [ ] Downloads endpoints return presigned MinIO URLs when `STORAGE_BACKEND=minio`, else stream from FS via Go server.
- [ ] OpenAPI spec at `/api/v1/openapi.json` (generated or hand-written).
- [ ] Integration tests with bearer token against seeded Postgres.

**Acceptance:** all mobile-relevant flows work via API alone (no HTML parsing needed). Web UI keeps working unchanged.

### Phase 7 — Plugins page UI  *(~0.5 day)*
- [ ] NEW template `plugins.html` at `/admin/plugins`.
- [ ] Shows: registry contents grouped by kind, capability badges, auth status, per-provider rate config, enable/disable toggles, declarative-provider editor.
- [ ] Replaces or extends current `/admin/providers` page.

### Phase 8 — OPDS extension  *(~0.5 day)*
- [ ] Extend OPDS feed to include comic series with page-acquisition links.
- [ ] Allow bearer-token auth on `/opds/*` in addition to Basic.
- [ ] Per-series OPDS subfeed (currently flat root only).

### Phase 9 — Docs, Docker, release  *(~0.5 day)*
- [ ] Update README: provider plugin authoring guide, declarative provider spec, API docs.
- [ ] Dockerfile: add `data/providers/` volume mount, `data/blobs/` volume mount.
- [ ] Bump schema migration version; document backup/restore implications of new tables.
- [ ] CI: add plugin-registry integrity test (every registered Meta is valid, no dup names).

---

## Total effort

| Phase | Est |
|-------|-----|
| 0 Prep | 0.5d |
| 1 Plugin registry (Go compiled-in) | 1.5d |
| 2 Store split + SQLite→Postgres + FS blobs | 3.0d |
| 3 Comics fix | 1.5d |
| 4 Worker improvements | 1.0d |
| 5 Declarative TOML providers | 1.0d |
| 6 iOS API (per-device tokens) | 2.5d |
| 7 Plugins page | 0.5d |
| 8 OPDS extension | 0.5d |
| 9 Docs/release | 0.5d |
| **Total** | **~12.5 days** |

Sequencing: 0→1→2 hard prerequisite. 3,4,6 independent after 2. 5,7,8 after 1. 9 last.

---

## Migration & rollback

- Every phase leaves old routes/handlers working until replaced.
- DB migrations additive only (new tables/columns), no destructive changes until major version bump.
- `cmd/main.go` keeps backward-compat env vars; new ones added alongside.
- Tagged `pre-refactor-2026-07-16` lets us diff against original at any point.

## Risk callouts

- **XenForo auth refactor** is the riskiest — QQ login is fiddly. Keep logic intact, only relocate.
- **Schema split temptation** — resist merging text + comic tables. Different shapes; unified interface is enough.
- **Declarative scraper security** — user-supplied selectors + URL templates = XSS/SSRF surface. Reuse `safefetch` SSRF guard, sanitize all output through existing bluemonday policy.
- **Token storage** — bcrypt-hashed, never plaintext. Token shown once at creation.

---

## Resolved decisions (2026-07-16)

1. **Plugin discovery** — compiled-in Go `init()` registry. No scripting/WASM.
2. **Storage** — PostgreSQL + FS/MinIO blobs. NOT ScyllaDB.
3. **iOS auth** — per-device bearer tokens.
4. **Any site** — (c) compiled-in Go + declarative TOML.
5. **Polling** — per-provider intervals.
6. **Web UI + iOS** — both first-class.
7. **Declarative authoring** — filesystem-only initially.

## Open questions for user

None blocking. Proceed to Phase 0.
