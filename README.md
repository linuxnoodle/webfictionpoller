# webfictionpoller

A self-hosted meta-polling engine for web fiction, light novels, manga, and
comics. Polls arbitrary sites on a schedule, surfaces what's new, and serves
everything for offline reading via the web UI, an iOS app, OPDS readers, or
the versioned JSON API.

Single Go binary, SQLite database (Postgres optional), filesystem or MinIO
binary storage, dark-themed web UI. Runs in Docker. Auto-updates via Watchtower.

## architecture

The app is built around a **plugin registry** with capability interfaces:

- **Compiled-in Go providers** register themselves at boot via `init()` in
  `internal/providers` (text) and `internal/comics` (manga). Adding a site
  that needs auth, API calls, or special scraping is a new Go package + one
  import line.
- **Declarative TOML providers** live in `data/providers/*.toml` and cover
  simple sites (RSS + CSS selectors) without recompiling. See
  `docs/provider.example.toml`.
- Every provider advertises **capabilities** (`Poller`, `SeriesLister`,
  `HTMLFetcher`, `Searcher`, `PageLister`, `LoginAuth`, etc.). Consumers
  query the registry via `WithCapability` rather than branching on type.

## supported sites (compiled-in)

| Site | Method | Auth |
|------|--------|------|
| Royal Road | RSS + HTML scraping | No |
| SpaceBattles | XenForo threadmarks RSS | No |
| SufficientVelocity | XenForo threadmarks RSS | No |
| QuestionableQuesting | XenForo threadmarks RSS | Login or cookies |
| FanFiction.net | FlareSolverr proxy | No |
| Archive of Our Own | HTML scraping | No |
| MangaDex | API | No |

## quick start

### docker compose

```bash
curl -O https://raw.githubusercontent.com/linuxnoodle/webfictionpoller/master/docker-compose.yml
mkdir -p data/providers data/blobs
docker compose up -d
```

Open `http://localhost:8080`. On first visit you create an admin account.

### manual

```bash
go build -o webfiction_poller ./cmd/main.go
./webfiction_poller
```

Defaults to `:8080`, `data.db` in the current directory, 15-minute poll
interval. Blob storage defaults to `data/blobs/`; declarative TOML providers
load from `data/providers/` (both created on demand).

## environment variables

| Variable | Default | What it controls |
|----------|---------|-------------------|
| `DB_PATH` | `data.db` | SQLite database path |
| `ADDR` | `:8080` | Listen address |
| `POLL_INTERVAL` | `15m` | Global text-poll cadence (per-provider overrides below) |
| `COMIC_POLL_INTERVAL` | `1h` | Comic chapter-list refresh interval |
| `LOG_DIR` | `data/logs` | Where log files go |
| `SECRET_KEY_PATH` | `data/secret.key` | Path to encryption key for stored provider passwords |
| `FLARESOLVERR_URL` | `http://flaresolverr:8191` | FlareSolverr endpoint for FanFiction.net |
| `WATCHTOWER_URL` | `http://watchtower:8080` | Watchtower HTTP API endpoint |
| `WATCHTOWER_TOKEN` | | Watchtower API token |
| `PROVIDERS_DIR` | `data/providers` | Declarative TOML provider directory |
| `STORAGE_BACKEND` | `fs` | Binary storage: `fs` or `minio` |
| `STORAGE_FS_ROOT` | `data/blobs` | Filesystem blob root (when `STORAGE_BACKEND=fs`) |
| `MINIO_ENDPOINT` | | MinIO host:port (when `STORAGE_BACKEND=minio`) |
| `MINIO_ACCESS_KEY` | | MinIO access key |
| `MINIO_SECRET_KEY` | | MinIO secret key |
| `MINIO_BUCKET` | `webfictionpoller` | MinIO bucket (auto-created) |
| `MINIO_USE_TLS` | `false` | Use TLS for MinIO connection |
| `MINIO_REGION` | | Optional MinIO region |
| `COOKIE_SECURE` | `true` | Set `false` to allow plain-HTTP cookies |

## per-provider polling intervals

The plugins page (`/admin/plugins`) lets you override each provider's polling
interval without restarting — useful for fast-update sites (Royal Road) vs
slow ones (MangaDex). Resolution order:

1. `poll_interval:<name>` setting (set via the plugins page UI)
2. `poll_interval_default` in the provider's `Meta()`
3. Global `POLL_INTERVAL`

## adding series

1. **Manual**: Click **+** in the nav bar, paste a URL
2. **OPML import**: Settings → Import OPML
3. **OPML export**: Settings → Export OPML
4. **Comics**: Browse → Comics tab, search, add to library

## questionablequesting authentication

QQ requires an account to access threadmarks RSS feeds. Configure via the
**Provider Configuration** page (`/admin/providers`):

- **Username + Password** (recommended): QQ credentials. The password is
  encrypted at rest with AES-256-GCM; the app logs in automatically to
  obtain session cookies.
- **Cookie Data** (fallback): Paste session cookies directly.

## API tokens (iOS / mobile clients)

The web UI exposes a **Tokens** page (`/admin/tokens`) for creating per-device
bearer tokens. Each token:

- Is bcrypt-hashed at rest (plaintext is shown once at creation)
- Carries a label and optional device ID
- Can be revoked independently

```bash
# Issue a token from the command line:
curl -X POST https://your-host/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"...","password":"...","label":"iPhone 15"}'

# Use it:
curl https://your-host/api/v1/library?kind=text \
  -H "Authorization: Bearer wfp_..."
```

OpenAPI spec at `/api/v1/openapi.json`.

## API surface (v1)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/v1/auth/login` | POST | Exchange credentials for a bearer token |
| `/api/v1/auth/me` | GET | Current user |
| `/api/v1/tokens` | GET/POST | List / issue bearer tokens |
| `/api/v1/tokens/{id}` | DELETE | Revoke a token |
| `/api/v1/library?kind=text\|comic` | GET | Tracked series |
| `/api/v1/library/{id}?kind=...` | GET | Series detail + chapters |
| `/api/v1/chapters?unread=true` | GET | Chapter feed |
| `/api/v1/chapters/{id}` | GET | Chapter metadata |
| `/api/v1/chapters/{id}/content` | GET | Cached chapter HTML |
| `/api/v1/chapters/{id}/read` | POST | Mark chapter read |
| `/api/v1/unread-count` | GET | Total unread |
| `/api/v1/poll/now` | POST | Trigger immediate poll cycle |
| `/api/v1/poll/status` | GET | Poll-cycle progress |
| `/api/v1/metrics/providers` | GET | Per-provider polling metrics |
| `/api/v1/providers` | GET | Registered provider catalog |
| `/api/v1/downloads/comics/{id}` | POST | Trigger comic chapter page download |
| `/api/v1/downloads/comics/{id}/status` | GET | Download progress |
| `/api/v1/downloads/comics/{id}/cbz` | GET | Stream CBZ bundle |

## OPDS feed

OPDS readers (Booky, Marvin, Panels, etc.) can subscribe to the catalog at:

```
https://USERNAME:PASSWORD@your-host/opds
```

Or using a bearer token in the `Authorization` header (for clients that
support it). The feed lists both text series (EPUB acquisition link) and
comic series (CBZ acquisition link).

## local downloads (offline reading)

- **Text**: enable the **Archive** flag on a series. The archiver fetches
  chapter HTML + inline images, gzips them in SQLite, and serves them via
  OPDS as generated EPUBs.
- **Comics**: click **Download** on a chapter. Pages are fetched into the
  blob store (filesystem by default, MinIO if configured) and served via
  `/comics/page/...` or streamed as a CBZ via `/api/v1/downloads/.../cbz`.

Switch `STORAGE_BACKEND=minio` and configure the `MINIO_*` env vars to use
presigned URLs (the iOS app fetches bytes directly from MinIO, bypassing
the Go server on the hot path).

## declarative providers (any site)

Drop a TOML file into `data/providers/` (see
`docs/provider.example.toml` for the full schema):

```toml
name = "somesite"
display_name = "Some Site"
homepage = "https://somesite.com"

[poll]
rss_feed_template = "https://somesite.com/rss/{id}"
interval = "30m"

[scrape]
series_title_selector    = "h1.fic-title"
chapter_list_selector    = "a.chapter-link"
chapter_content_selector = "div.chapter-content"
```

Restart the server to register the provider. The plugins page shows the new
entry alongside the compiled-in ones (badged `toml` vs `go`).

## updating

Push to `master` triggers GitHub Actions, which builds and pushes a Docker
image to `ghcr.io/linuxnoodle/webfictionpoller:latest`. Watchtower detects
the new image and restarts the container. You can also trigger an update
from the **Version & Updates** page.

## license

MIT
