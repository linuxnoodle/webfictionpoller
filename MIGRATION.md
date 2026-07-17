# Proxmox LXC — Migration Plan

**From:** pre-refactor build (commit at tag `pre-refactor-2026-07-16` and earlier)
**To:** current `master` HEAD (post-refactor + multi-source + Postgres support)
**Audience:** operator of an existing LXC running the production instance

---

## TL;DR

The migration is **low-risk** because every schema change is additive and
the data volume (`./data:/data`) is preserved across image swaps. In most
cases the only action required is:

1. Snapshot the LXC from the Proxmox UI (1-click rollback insurance).
2. Push master → GitHub Actions rebuilds the `:latest` image → Watchtower
   auto-pulls → app restarts → migrations run on boot (additive only).
3. Verify dashboard loads + QQ login still works.

For operators who want explicit control (recommended for first migration),
the manual path in §B pauses Watchtower, snapshots, swaps the image, and
verifies before re-enabling auto-updates.

The Postgres switch (§C) is **optional and separate** — staying on SQLite is
the right choice unless your library is large (>5 GB DB file or >100k rows).

---

## 1. Current state assessment

### What's running on the LXC today

```
/opt/webfictionpoller/
├── docker-compose.yml         # mounted read-only INTO the container
└── data/                      # the persistent volume (./data:/data)
    ├── data.db                # SQLite, WAL mode
    ├── data.db-wal            # ↳ checkpointed on clean shutdown
    ├── data.db-shm
    ├── secret.key             # AES-256-GCM key for provider passwords
    └── logs/
        └── app.log
```

- **Image:** `ghcr.io/linuxnoodle/webfictionpoller:latest` (built from pre-refactor source)
- **Sidecars:** `flaresolverr:8191` (FanFiction.net Cloudflare bypass), `watchtower` (auto-update)
- **Docker socket:** mounted into the container so the self-update endpoint works
- **15 SQLite migrations applied** (through `comic_reading_progress`)

### What changes after migration

| Area | Before | After | Action needed |
|---|---|---|---|
| **Schema** | 15 migrations | 18 migrations (3 new) | Auto-applied on boot |
| **`api_tokens` table** | absent | present (empty) | None |
| **`series_sources` table** | absent | present + seeded from existing series | None |
| **`data/blobs/` dir** | absent | created lazily on first comic page write | None |
| **`data/providers/` dir** | absent | scanned at startup (no-op if missing) | None |
| **Provider passwords** | AES-encrypted with `secret.key` | Same — `secret.key` is preserved via volume | **Do not lose `secret.key`** |
| **Existing comic pages** | In SQLite `comic_pages.data` BLOBs | Same (read fallback path); new pages go to FS | None — backward compatible |
| **Existing chapter content** | In `chapters.content_html` (gzipped) | Same | None |
| **`POLL_INTERVAL`** | Global | Global default + per-provider overrides (settings) | None — old behavior preserved |
| **OPDS feed** | Text only | Text + comics; bearer-token auth added | None — Basic auth still works |
| **API tokens** | n/a | Available via `/admin/tokens` page | Optional — issue when ready |

**No columns are renamed. No columns are dropped. No data is rewritten.**

---

## 2. Risk inventory

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `secret.key` lost | Low (volume mount preserves it) | **Critical** — QQ password undecryptable | §3 snapshot; verify key file present post-swap |
| WAL not checkpointed before swap | Medium | Last few writes lost (seconds of data) | Clean container stop before swap (§B step 5) |
| Migration fails mid-way | Low (additive only) | Container crashloops | §3 snapshot; §9 rollback |
| Watchtower updates mid-backup | Medium (auto-update races backup) | Inconsistent state | §B step 2 pauses Watchtower first |
| New image fails to start (Go build issue) | Low (CI tests gate the build) | Downtime | §3 snapshot; pin to previous tag for rollback |
| Out of disk (new blobs dir grows) | Low at migration time | Future writes fail | §7 post-migration monitoring |
| Docker Compose v2 syntax gap | Low | Container won't start | Both pre/post composes are v2-valid; verified |

The combination of "additive schema" + "volume-preserving image swap" + "LXC
snapshot for rollback" makes this a low-risk operation. The most dangerous
single action would be **losing `secret.key`** — every other failure mode is
recoverable from snapshot.

---

## 3. Pre-flight checklist (do this first, regardless of path)

Run on the LXC host (`pct enter <ctid>` or SSH):

```bash
# 1. Confirm the data layout matches expectations.
ls -la /opt/webfictionpoller/data/
# Expect: data.db, data.db-wal, data.db-shm, secret.key, logs/

# 2. Confirm the secret key is readable. This file is CRITICAL.
sudo cat /opt/webfictionpoller/data/secret.key | wc -c
# Expect: 32 (bytes — AES-256-GCM key)

# 3. Capture current row counts for post-migration comparison.
sudo sqlite3 /opt/webfictionpoller/data/data.db <<'SQL'
SELECT 'users',        COUNT(*) FROM users;
SELECT 'series',       COUNT(*) FROM series;
SELECT 'chapters',     COUNT(*) FROM chapters;
SELECT 'comic_series', COUNT(*) FROM comic_series;
SELECT 'comic_chapters',COUNT(*) FROM comic_chapters;
SELECT 'comic_pages',  COUNT(*) FROM comic_pages;
SELECT 'chapter_images',COUNT(*) FROM chapter_images;
SELECT 'migrations',   COUNT(*) FROM _migrations;
SQL
# Save this output — you'll compare against it after migration.

# 4. Check current container status and image digest.
cd /opt/webfictionpoller
sudo docker compose ps
sudo docker images ghcr.io/linuxnoodle/webfictionpoller
# Record the IMAGE ID — this is your rollback target if needed.

# 5. Confirm Watchtower is running (it controls auto-update timing).
sudo docker ps | grep watchtower
```

### Proxmox snapshot (do this even if you trust the process)

From the Proxmox web UI or CLI:

```bash
# On the Proxmox HOST (not inside the LXC):
qm snapshot <vmid> pre-refactor-migration-$(date +%Y%m%d)
# Or for LXC:
pct snapshot <ctid> pre-refactor-migration-$(date +%Y%m%d)
```

This is your **instant rollback button**. Costs ~5 seconds + disk space equal
to the LXC's changed blocks. Keep it for at least a week post-migration.

---

## 4. Migration path A — Watchtower auto-update (simplest)

Use this path if you trust the CI build and don't need manual control.

```bash
# 1. Take the Proxmox snapshot (§3).

# 2. Push master (or merge your refactor branch). GitHub Actions rebuilds:
#    https://github.com/linuxnoodle/webfictionpoller/actions
#    Image lands at ghcr.io/linuxnoodle/webfictionpoller:latest

# 3. Watchtower notices within its poll interval and restarts the container.
#    Default Watchtower poll is 24h; to force a faster check:
sudo docker exec watchtower /watchtower --run-once webfictionpoller-app 2>&1 | tail
# (adjust container name if yours differs)

# 4. Tail the logs to watch migrations apply:
cd /opt/webfictionpoller
sudo docker compose logs -f app | grep -E '\[main\]|\[migrat|\[scheduler'

# You should see lines like:
#   [main] starting webfiction_poller
#   [main] database dialect: sqlite
#   [main] blob store initialized: backend=fs
#   [main] loaded N declarative provider(s) from /data/providers

# 5. Verify per §6.
```

**Risk with this path:** you can't easily insert the "pause + backup + verify"
steps between Watchtower triggering and the container restarting. If you want
those guarantees, use path B.

---

## 5. Migration path B — Manual controlled migration (recommended)

Use this path for the **first** migration after the refactor. After it
succeeds once, subsequent updates can rely on path A.

```bash
# All commands run inside the LXC unless noted.
cd /opt/webfictionpoller

# === STEP 1: Snapshot (Proxmox host) ===
# From the Proxmox host:
pct snapshot <ctid> pre-refactor-manual-$(date +%Y%m%d-%H%M)

# === STEP 2: Stop Watchtower so it can't race us ===
sudo docker stop watchtower
# (record its name first via `sudo docker ps | grep watchtower`)

# === STEP 3: Take a file-level backup of the data dir ===
# This is redundant with the Proxmox snapshot but cheap insurance.
sudo cp -a /opt/webfictionpoller/data /opt/webfiction_poller/data.pre-migration.bak
# -a preserves mode, ownership, timestamps, symlinks
# Verify the backup:
sudo du -sh /opt/webfictionpoller/data /opt/webfictionpoller/data.pre-migration.bak
# Sizes should match within a few KB.

# === STEP 4: Stop the app cleanly ===
# A clean stop lets SQLite checkpoint its WAL into the main DB file.
sudo docker compose stop app
# Leave flaresolverr running — it doesn't depend on the app.

# Verify WAL is checkpointed:
sudo sqlite3 /opt/webfictionpoller/data/data.db 'PRAGMA wal_checkpoint(TRUNCATE);'
# Expect: "0|0|0" (no pending WAL frames)

# === STEP 5: Pull the new image ===
sudo docker compose pull app
# Record the new image digest:
sudo docker images ghcr.io/linuxnoodle/webfictionpoller --digests | head

# === STEP 6: Update docker-compose.yml if you want the new env vars ===
# The new compose adds: COMIC_POLL_INTERVAL, PROVIDERS_DIR, STORAGE_BACKEND,
# STORAGE_FS_ROOT. They're all optional (defaults work), but adding them now
# means future updates don't require editing.
#
# Either:
#   (a) edit /opt/webfictionpoller/docker-compose.yml to match the repo's
#       current docker-compose.yml (see git:master), OR
#   (b) leave the old one — env vars default sensibly inside the binary.
#
# Recommended: do (a). The new vars are:
#     COMIC_POLL_INTERVAL=1h
#     PROVIDERS_DIR=/data/providers
#     STORAGE_BACKEND=fs
#     STORAGE_FS_ROOT=/data/blobs

# === STEP 7: Start the app and watch migrations apply ===
sudo docker compose up -d app
sudo docker compose logs -f app | head -100

# Look for:
#   [main] starting webfiction_poller
#   [main] database dialect: sqlite
#   [main] blob store initialized: backend=fs
#   [comic-scheduler] refreshing N comic series
#   [scheduler] queued N series across M providers

# Migrations are silent on success — they apply on InitDB. To confirm they
# ran, query the migrations ledger:
sudo sqlite3 /opt/webfictionpoller/data/data.db \
  'SELECT name FROM _migrations ORDER BY name;' | tail -10
# Expect the last entries to be:
#   series_sources
#   series_sources_idx
#   series_sources_seed
# (plus api_tokens + api_tokens_idx + api_tokens_hash_idx)

# === STEP 8: Restart Watchtower ===
sudo docker start watchtower

# === STEP 9: Verify per §6. ===
```

### Downtime

Steps 4–7 take roughly **30–60 seconds** of app downtime. Watchtower being
stopped means no auto-update during the window (good — you control timing).

---

## 6. Post-migration verification

Run this checklist before declaring success. All of it is read-only.

### 6.1 Data integrity

```bash
# Re-run the row counts from §3 step 3 and diff.
sudo sqlite3 /opt/webfictionpoller/data/data.db <<'SQL'
SELECT 'users',         COUNT(*) FROM users;
SELECT 'series',        COUNT(*) FROM series;
SELECT 'chapters',      COUNT(*) FROM chapters;
SELECT 'comic_series',  COUNT(*) FROM comic_series;
SELECT 'comic_chapters',COUNT(*) FROM comic_chapters;
SELECT 'comic_pages',   COUNT(*) FROM comic_pages;
SELECT 'chapter_images',COUNT(*) FROM chapter_images;
SELECT 'series_sources',COUNT(*) FROM series_sources;  -- NEW; should == series count
SELECT 'migrations',    COUNT(*) FROM _migrations;      -- should be 18 (was 15)
SQL
```

**Expectations:**
- All pre-existing counts unchanged.
- `series_sources` count == `series` count (auto-seeded).
- `migrations` count went from 15 → 18.

### 6.2 Functional smoke test

Visit each URL in a browser:

| URL | Expected |
|---|---|
| `https://your-host/` | Dashboard loads, all your series present |
| `https://your-host/admin/plugins` | **NEW page** — lists 7 providers with capability badges |
| `https://your-host/admin/tokens` | **NEW page** — empty token list, "Add Token" form |
| `https://your-host/admin/providers` | QQ credentials still pre-loaded |
| `https://your-host/comics` | Comic library intact |
| `https://your-host/api/v1/openapi.json` | **NEW** — returns OpenAPI 3.0 JSON |
| `https://your-host/opds` | OPDS feed, same as before + comic entries |

### 6.3 QQ login still works (critical — uses preserved `secret.key`)

```bash
# Trigger a QQ poll manually and watch for auth success.
sudo docker compose logs -f app | grep -i 'questionablequesting'
# Then in the UI: open any QQ series, click "Sync Now".
# Expect: no "authentication required" errors in the log.
```

If QQ login fails post-migration, your `secret.key` was not preserved — see
§9 troubleshooting.

### 6.4 Filesystem layout

```bash
ls -la /opt/webfictionpoller/data/
# New entries that should appear over time (not necessarily immediately):
#   blobs/                  # created on first comic page write
#   providers/              # created if you add declarative TOML providers
```

### 6.5 App version endpoint

```bash
curl -s https://your-host/api/version | jq
# BuildCommit should reflect the new master HEAD git sha.
```

---

## 7. Post-migration cleanup (anytime in the next week)

```bash
# After 7 days of clean operation:

# 1. Drop the file-level backup.
sudo rm -rf /opt/webfictionpoller/data.pre-migration.bak

# 2. Drop the Proxmox snapshot (from the Proxmox host).
pct delsnapshot <ctid> pre-refactor-manual-YYYYMMDD-HHMM

# 3. OPTIONAL: VACUUM the SQLite DB to reclaim space from any churn.
#    Requires brief downtime.
sudo docker compose stop app
sudo sqlite3 /opt/webfictionpoller/data/data.db 'VACUUM;'
sudo docker compose start app
```

---

## 8. Migration path C — Switch to Postgres (optional, advanced)

**Skip this entirely unless**: your `data.db` is >5 GB, you have >100k
chapters, or you want HA via Postgres replication. SQLite handles
single-user webfiction_poller loads comfortably into the GB range.

If you do want Postgres:

```bash
# === ADD a Postgres service to docker-compose.yml ===
# Add alongside flaresolverr:
#
#   postgres:
#     image: postgres:16-alpine
#     environment:
#       POSTGRES_DB: webfictionpoller
#       POSTGRES_USER: wfp
#       POSTGRES_PASSWORD: <strong-password>
#     volumes:
#       - ./pgdata:/var/lib/postgresql/data
#     restart: unless-stopped
#     networks: [default]

# === Run the migration tool ===
sudo docker compose up -d postgres
sleep 10  # let Postgres accept connections

# The migrate tool is a separate Go binary. Run it from your dev machine
# (where the repo is checked out) against the LXC's Postgres:
cd /path/to/webfiction_poller
go run ./cmd/migrate \
  -from /path/to/lxc/data.db.snapshot \
  -to   "postgres://wfp:<password>@<lxc-host>:5432/webfictionpoller?sslmode=disable"
# It streams every table, batch-inserts into Postgres, syncs BIGSERIAL
# sequences. The SQLite source is opened read-only — never modified.

# === Point the app at Postgres ===
# Edit docker-compose.yml app environment:
#   DATABASE_URL=postgres://wfp:<password>@postgres:5432/webfictionpoller?sslmode=disable
# (DB_PATH is now ignored when DATABASE_URL is set.)

sudo docker compose up -d app
sudo docker compose logs -f app | grep -i 'dialect'
# Expect: [main] database dialect: postgres

# === Keep SQLite as a warm backup ===
# Don't delete data.db yet. If Postgres misbehaves:
#   1. unset DATABASE_URL
#   2. sudo docker compose up -d app
#   3. You're back on SQLite instantly (with whatever data was there at migrate time).
# After a week of clean Postgres operation, archive data.db offline.
```

**Critical caveats for path C:**
- The migration tool **does not migrate blob files** (it's DB-only). Existing
  comic page BLOBs in `comic_pages.data` migrate to a Postgres `BYTEA` column,
  but if you've been writing to `data/blobs/` on filesystem, those bytes
  stay on the FS — they're not affected by the DB switch. Just preserve
  `data/blobs/` across the transition.
- `secret.key` is still required regardless of DB dialect.

---

## 9. Rollback procedure

If anything goes wrong, roll back in this order (most-recent first):

### Option 1: Proxmox snapshot rollback (fastest, ~30 seconds)

```bash
# From the Proxmox host:
pct rollback <ctid> pre-refactor-manual-YYYYMMDD-HHMM
```

The LXC reverts to its pre-migration state including the old container image,
old data files, and old docker-compose.yml. No data loss.

### Option 2: File-level rollback (if Proxmox snapshot unavailable)

```bash
cd /opt/webfictionpoller
sudo docker compose down app

# Restore data dir from the file backup:
sudo rm -rf data
sudo cp -a data.pre-migration.bak data

# Re-pin the image to the pre-refactor digest you recorded in §3 step 4:
# Edit docker-compose.yml:
#   image: ghcr.io/linuxnoodle/webfictionpoller@sha256:<old-digest>
sudo docker compose up -d app
```

### Option 3: Pin to previous image tag (no data rollback needed)

If the new image is broken but the data migrations are fine (they're
additive — the old binary tolerates the new tables, just ignores them):

```bash
# Edit docker-compose.yml:
#   image: ghcr.io/linuxnoodle/webfictionpoller@sha256:<old-digest>
sudo docker compose up -d app
```

The old binary will run fine against the migrated schema. The extra tables
(`api_tokens`, `series_sources`) are just ignored. **This is the safest
rollback if you only need to undo the binary, not the data.**

---

## 10. Troubleshooting

### Container crashloops on startup

```bash
sudo docker compose logs app | tail -50
```

Common causes:
- **SQLite migration failure** — look for `running schema: ...`. Should be
  impossible given all migrations are `IF NOT EXISTS`, but if it happens,
  roll back via §9 option 1.
- **Blob store init failure** — look for `failed to init blob store`. Should
  not happen with `STORAGE_BACKEND=fs` (default). If you set
  `STORAGE_BACKEND=minio` without configuring the `MINIO_*` vars, the app
  will refuse to start. Fix: `unset STORAGE_BACKEND` or set it to `fs`.

### QQ login fails after migration

Likely cause: `secret.key` was not preserved.

```bash
# Verify the key file exists and matches the pre-migration backup:
sudo sha256sum /opt/webfictionpoller/data/secret.key
sudo sha256sum /opt/webfictionpoller/data.pre-migration.bak/secret.key
# Hashes MUST match.
```

If they differ or the file is missing, restore from backup and restart.
Without the original key, stored QQ passwords are unrecoverable — you'd
have to re-enter them in `/admin/providers`.

### Dashboard loads but no series show

Check the migration ledger:

```bash
sudo sqlite3 /opt/webfictionpoller/data/data.db \
  'SELECT name FROM _migrations ORDER BY name;'
```

All 18 entries should be present. If `series_sources_seed` is missing,
the multi-source scheduler has nothing to poll. Run the seed manually:

```bash
sudo docker compose exec app sh -c 'echo "SELECT 1" | sqlite3 /dev/null' 2>&1 || \
  echo "shell into container and run: sqlite3 /data/data.db \"<seed SQL>\""
# The seed SQL is in internal/database/db.go under {"series_sources_seed", ...}
```

### OPDS feed returns 401 where it used to work

The OPDS auth now accepts **either** Basic auth **or** bearer token. Basic
auth behavior is unchanged — if you're seeing 401, your OPDS reader's saved
credentials are likely the issue, not the migration. Re-enter them.

### `data/blobs/` grows large

This is the new BlobStore. Over time it accumulates comic page images. Use:

```bash
sudo du -sh /opt/webfictionpoller/data/blobs/
# Per-series breakdown:
sudo find /opt/webfictionpoller/data/blobs -type d -mindepth 2 -maxdepth 2 -exec \
  sh -c 'echo "$(du -sh "$1" | cut -f1) $1"' _ {} \;
```

To reclaim space: delete series you no longer track via the web UI (cascades
to its blobs), or selectively delete `data/blobs/comic-page/<chapterID>/`
directories for chapters you've already read.

---

## 11. Quick reference — what to actually type

For the typical case (path B, no Postgres, accept all defaults):

```bash
# 1. Proxmox host: snapshot
pct snapshot <ctid> pre-refactor-$(date +%Y%m%d)

# 2. LXC: pause watchtower, backup, stop, swap, start, resume watchtower
cd /opt/webfictionpoller
sudo docker stop watchtower
sudo cp -a data data.pre-migration.bak
sudo docker compose stop app
sudo sqlite3 data/data.db 'PRAGMA wal_checkpoint(TRUNCATE);'
sudo docker compose pull app
# (optional: edit docker-compose.yml to add new env vars per §5 step 6)
sudo docker compose up -d app
sudo docker compose logs --tail=50 app
sudo docker start watchtower

# 3. Verify: dashboard loads, QQ series syncs, /admin/plugins renders
# 4. If all good: nothing more to do. Keep the snapshot for 7 days.
# 5. If broken: pct rollback <ctid> pre-refactor-YYYYMMDD
```

Total operator time: ~10 minutes. Downtime: ~1 minute.

---

## Appendix: schema migration deltas (for the curious)

Three new migrations apply on first boot of the new binary, all additive:

```sql
-- 1. api_tokens (empty until you issue tokens via /admin/tokens)
CREATE TABLE IF NOT EXISTS api_tokens (
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
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);

-- 2. series_sources (auto-seeded with one primary per existing series)
CREATE TABLE IF NOT EXISTS series_sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id INTEGER NOT NULL,
    provider_name TEXT NOT NULL,
    source_url TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 100,
    is_primary INTEGER NOT NULL DEFAULT 0,
    last_ok DATETIME,
    last_fail DATETIME,
    last_error TEXT DEFAULT '',
    consecutive_fails INTEGER NOT NULL DEFAULT 0,
    disabled INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (series_id) REFERENCES series(id) ON DELETE CASCADE,
    UNIQUE(series_id, source_url)
);
CREATE INDEX IF NOT EXISTS idx_series_sources_series ON series_sources(series_id);

-- 3. series_sources_seed: back-fills one row per existing series
INSERT INTO series_sources (series_id, provider_name, source_url, priority, is_primary)
    SELECT s.id, s.provider_name, s.source_url, 0, 1 FROM series s
    WHERE NOT EXISTS (SELECT 1 FROM series_sources ss WHERE ss.series_id = s.id);
```

Every statement is idempotent (`IF NOT EXISTS` / `WHERE NOT EXISTS`), so
re-running on an already-migrated DB is a no-op. The seed migration only
inserts rows for series that don't yet have a source, so existing manually-
configured sources are preserved.
