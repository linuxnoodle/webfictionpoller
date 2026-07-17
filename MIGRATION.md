# LXC Migration — Pre-refactor → Master

Short version. Schema changes are additive; `./data` volume preserves everything.

## Steps

```bash
# 0. Proxmox host — snapshot the LXC (rollback button)
pct snapshot <ctid> pre-refactor-$(date +%Y%m%d)

# 1. Inside the LXC
cd /opt/webfictionpoller

# 2. Stop Watchtower so it can't race the swap
docker stop watchtower

# 3. File-level backup of data (cheap insurance)
cp -a data data.pre-migration.bak

# 4. Stop app + checkpoint SQLite WAL
docker compose stop app
sqlite3 data/data.db 'PRAGMA wal_checkpoint(TRUNCATE);'

# 5. Pull new image
docker compose pull app

# 6. Start app, watch migrations apply
docker compose up -d app
docker compose logs --tail=30 app
# Expect: [main] database dialect: sqlite
#         [main] blob store initialized: backend=fs

# 7. Restart Watchtower
docker start watchtower
```

Downtime: ~1 minute.

## Verify

- Dashboard loads, all series present
- Open a QQ series → Sync Now → no auth errors (means `secret.key` survived)
- Visit `/admin/plugins` (new page, lists providers)
- Row counts unchanged:
  ```bash
  sqlite3 data/data.db \
    'SELECT COUNT(*) FROM series; SELECT COUNT(*) FROM series_sources; SELECT COUNT(*) FROM _migrations;'
  # series_sources should equal series; migrations went 15 → 18
  ```

## Rollback if anything breaks

```bash
# Proxmox host:
pct rollback <ctid> pre-refactor-YYYYMMDD
```

Done in 30 seconds. No data loss.

## Cleanup (after ~7 days of clean operation)

```bash
rm -rf data.pre-migration.bak
pct delsnapshot <ctid> pre-refactor-YYYYMMDD
```

## Notes

- Postgres switch is optional and not needed unless `data.db` > 5 GB. Skip it.
- New env vars (`COMIC_POLL_INTERVAL`, `PROVIDERS_DIR`, `STORAGE_BACKEND=fs`, `STORAGE_FS_ROOT=/data/blobs`) all default sensibly — no compose edit required.
- `secret.key` is the one file you must not lose. The volume + backup covers it.
