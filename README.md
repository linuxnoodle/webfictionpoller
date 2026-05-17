# webfictionpoller

A self-hosted tracker for web fiction updates. Polls Royal Road, SpaceBattles, SufficientVelocity, QuestionableQuesting, and FanFiction.net on a schedule, shows you what's new, and lets you mark chapters read.

Single Go binary, SQLite database, dark-themed web UI. Runs in Docker. Auto-updates via Watchtower.

## supported sites

| Site | Method | Auth |
|------|--------|------|
| Royal Road | RSS + HTML scraping | No |
| SpaceBattles | XenForo threadmarks RSS | No |
| SufficientVelocity | XenForo threadmarks RSS | No |
| QuestionableQuesting | XenForo threadmarks RSS | Yes (login or cookies) |
| FanFiction.net | FlareSolverr proxy (Cloudflare bypass) | No |

## quick start

### Proxmox VE

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/linuxnoodle/webfictionpoller/refs/heads/master/proxmoxve.sh)"
```

2 CPU, 2GB RAM, 8GB disk. Data in `/opt/webfictionpoller/data/`. Auto-updates via Watchtower.

### docker compose

```bash
curl -O https://raw.githubusercontent.com/linuxnoodle/webfictionpoller/master/docker-compose.yml
docker compose up -d
```

Open `http://localhost:8080`. On first visit you create an admin account.

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
| `SECRET_KEY_PATH` | `data/secret.key` | Path to encryption key for stored passwords |
| `FLARESOLVERR_URL` | `http://flaresolverr:8191` | FlareSolverr endpoint for FanFiction.net |
| `WATCHTOWER_URL` | `http://watchtower:8080` | Watchtower HTTP API endpoint |
| `WATCHTOWER_TOKEN` | | Watchtower API token |

## adding series

1. **Manual**: Click **+** in the nav bar, paste a URL
2. **OPML import**: Settings -> Import OPML
3. **OPML export**: Settings -> Export OPML

## questionablequesting authentication

QQ requires an account to access threadmarks RSS feeds. Configure via the **Provider Configuration** page:

- **Username + Password** (recommended): Enter your QQ account credentials. The password is encrypted at rest with AES-256-GCM and the app logs in automatically to obtain session cookies.
- **Cookie Data** (fallback): Paste session cookies directly if you prefer to manage them manually.

## updating

Push to `master` triggers GitHub Actions, which builds and pushes a Docker image to `ghcr.io/linuxnoodle/webfictionpoller:latest`. Watchtower detects the new image and restarts the container. You can also trigger an update from the **Version & Updates** page.

## license

MIT
