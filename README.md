# webfictionpoller

A self-hosted tracker for web fiction updates. Polls Royal Road, SpaceBattles, SufficientVelocity, QuestionableQuesting, and FanFiction.net on a schedule, shows you what's new, and lets you mark chapters read.

Single Go binary, SQLite database, dark-themed web UI. Runs in Docker.

## what it does

- Tracks active series across multiple fiction sites
- Polls for new chapters on a configurable interval (default 15 minutes)
- Dashboard with two views: time-sorted (all chapters by date) and series-grouped
- Click any chapter to expand an inline preview of the content
- Rating per series (0.0-10.0 in 0.1 steps) to control sort order
- Mark individual chapters or entire series as read
- Import and export series lists via OPML
- Log viewer for troubleshooting poll errors
- Password-protected with session management

## supported sites

| Site | Method | Auth |
|------|--------|------|
| Royal Road | RSS + HTML scraping | No |
| SpaceBattles | XenForo threadmarks RSS | No |
| SufficientVelocity | XenForo threadmarks RSS | No |
| QuestionableQuesting | XenForo threadmarks RSS | Yes (cookies) |
| FanFiction.net | FlareSolverr proxy (Cloudflare bypass) | No |

## quick start

### Proxmox VE

The `proxmoxve.sh` script sets everything up in an LXC container: installs Docker, pulls the app image plus FlareSolverr, writes a docker-compose.yml, and starts the containers. The app runs on port 8080.

In the Proxmox VE shell:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/linuxnoodle/webfictionpoller/refs/heads/master/proxmoxve.sh)"
```

It assigns 2 CPU cores, 2GB RAM, and 8GB disk by default. Data ends up in `/opt/webfictionpoller/data/` inside the container.

To update later, run the same script again. It detects the existing install, pulls fresh images, and restarts.

### docker compose

```bash
docker compose up -d
```

Open `http://localhost:8080`. On first visit you will be asked to create an admin account.

Data lives in `./data/` on the host (database, logs).

### manual

```bash
go build -o webfictionpoller ./cmd/main.go
./webfictionpoller
```

Defaults to `:8080`, `data.db` in the current directory, 15-minute poll interval.

## environment variables

| Variable | Default | What it controls |
|----------|---------|-------------------|
| `DB_PATH` | `data.db` | SQLite database path |
| `ADDR` | `:8080` | Listen address |
| `POLL_INTERVAL` | `15m` | How often to poll for updates |
| `LOG_DIR` | `data/logs` | Where log files go |
| `FLARESOLVERR_URL` | `http://flaresolverr:8191` | FlareSolverr endpoint for FanFiction.net |

## adding series

1. **Manual**: Click the **+** button in the nav bar, paste a thread or fiction URL
2. **OPML import**: Settings menu (gear icon) -> "Import OPML" -> upload an OPML file from your RSS reader
3. **OPML export**: Settings menu -> "Export OPML" -> downloads a file you can import elsewhere

The app resolves RSS feed URLs back to thread URLs automatically. It handles `/threadmarks.rss`, `/syndication/`, and `/unread.rss` suffixes.

## how polling works

Each provider gets its own rate limiter (1 request/second). Workers add random jitter (500-2000ms) between requests. On 429 or 5xx responses, the HTTP helper retries up to 3 times with exponential backoff and respects `Retry-After` headers.

FanFiction.net goes through FlareSolverr to handle Cloudflare protection. That container is included in `docker-compose.yml`.

## dashboard

Two togglable views, persisted in localStorage:

**Time view** (default): All recent chapters sorted by publication date, grouped under day headers ("Today", "Yesterday", "January 15, 2026"). Infinite scroll loads more as you scroll down. Each row shows the provider favicon, series name, chapter title, inline rating editor, and a "Read" button that opens the chapter in a new tab and marks it read.

**Series view**: Chapters grouped under series cards, sorted by rating ascending. Each card has inline rating controls, a link to the source, and a "Mark All Read" button.

Clicking any chapter row expands an inline preview showing the chapter content (fetched from the source site and cached in the database). Only one preview is open at a time.

## logs

Settings menu -> "Logs" opens a log viewer that reads from the on-disk log file. Filter by info or error level. Log files auto-rotate at 10MB.

## project structure

```
cmd/main.go              entry point, router, scheduler
internal/
  auth/                   bcrypt auth, account setup
  database/               SQLite schema + migrations
  handlers/               HTTP handlers, templates, store queries
  logging/                file + stdout logging with rotation
  models/                 data types
  opml/                   OPML parser and exporter
  providers/              site-specific scrapers (royalroad, xenforo, fanfictionnet)
  worker/                 goroutine pool with per-provider rate limiting
```

Templates are embedded in the binary via `//go:embed`. No external template files needed at runtime.

## building from source

Requires Go 1.25+ and GCC (for CGO/sqlite3).

```bash
go build -o webfictionpoller ./cmd/main.go
```

Run tests:

```bash
go test ./...
```

## license

MIT
