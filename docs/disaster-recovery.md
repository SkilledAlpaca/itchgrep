# Disaster recovery

**Backups of itchgrep's data are managed outside this repository** — the operator handles them at the host level, not through anything in this project's scripts or compose file. This document explains what can fail, what would be lost without a backup, how to take a manual snapshot when you want one (for example, before a risky change), and how to restore.

This document assumes no prior familiarity with Docker or backup tooling. Every command below states where to run it and what to expect.

## What can fail, and how you recognize it

| Symptom | Likely cause | Where to check |
|---|---|---|
| `itchgrep.com` doesn't load at all | Cloudflare tunnel down, a Cloudflare-side outage, or the webserver container itself is down | `docker inspect -f '{{.State.Health.Status}}' itchgrep-webserver` (run on the host); Cloudflare dashboard tunnel status |
| Site loads, but search shows "no data yet" / every search returns `503` | No crawl has ever published (expected on a brand-new deployment) or every crawl since has failed | `crawler.log` under `LOG_EXPORT_DIR` for dataservice errors; `/stats` page's coverage figure |
| Search works, but `/new` is empty or results look stale | The scheduled crawl hasn't successfully published in a while (`CRAWL_INTERVAL` elapsed with no success), or the coverage gate is rejecting every run | `history.json`'s latest entries (via `/stats`); `crawler.log` for `"Coverage ... below COVERAGE_FLOOR"` |
| Site stays on `503` although crawls have published | `index.bleve/` is corrupted and the automatic self-heal is failing, or `/data` isn't writable by uid 65532 | `curl http://127.0.0.1:62686/readyz` (`503 no data` confirms); `crawler.log` for `"Self-heal rebuild"` lines or permission errors; see "Index corruption" below |
| The public site is unreachable but the LAN address (`http://<host>:62686`) works fine | Cloudflare-side problem specifically — tunnel disconnected, DNS issue, account issue | `docker logs` on the `cloudflared` container; Cloudflare dashboard |
| Host disk filling up unexpectedly | `index.bleve/` growth (the largest thing under `/data`) or logs not actually rotating | `df -h` on the Unraid host; confirm `scripts/export-logs.sh`'s logrotate reinstall is actually running — see [docs/observability.md](observability.md) |

For the full request-log-based signal set (what a normal `503` looks like vs. a real outage), see [docs/observability.md](observability.md).

## What data can be lost

Everything itchgrep persists lives under one directory (`/data` inside the containers; `DATA_PATH` on the host — see [docs/architecture.md](architecture.md) for the complete storage lifecycle). The table below is the backup-relevant summary — **regenerable** means a fresh crawl or an automatic rebuild recreates it with no permanent loss; **not regenerable** means, once it's gone, it's gone for good.

| File | Regenerable? | How |
|---|---|---|
| `index.bleve/` (the search index) | **Yes, fully** | The dataservice self-heals it automatically at startup if it's missing or corrupt, rebuilding from `assets.json` in minutes with no crawl needed — see "Index corruption" below. |
| `assets.json` — the asset list itself | **Mostly** | A fresh crawl of itch.io rebuilds it from scratch. |
| `assets.json` — the `FirstSeen` timestamp on each asset | **No** | `FirstSeen` is carried forward, id by id, from the previous publish (`stampFirstSeen` in `cmd/dataservice/main.go`). A crawl run with no prior `assets.json` to carry it forward from stamps *everything* it finds as newly arrived at that moment — permanently losing every asset's true arrival date. This is what powers `/new` and the Atom feed. |
| `tags.json` (cached tag vocabulary) | **Yes** | Rediscovered automatically once `TAG_CACHE_MAX_AGE` (default 168h) elapses, or immediately if the file is simply missing. Costs several hundred extra itch.io requests to rebuild — a performance cost, not a data-loss risk. |
| `rates.json` (exchange-rate snapshot) | **Yes** | Refetched from the European Central Bank on every crawl; falls back to a baked-in table if unavailable. See [docs/dependencies.md](dependencies.md). |
| `stats.json` (crawl coverage) | **Yes** | Rewritten every crawl; describes only the current publish, carries no history of its own. |
| `checkpoint.json` (in-progress crawl resume state) | **Transient by design** | Deleted automatically on every successful publish. A missing one just means the next crawl starts from zero instead of resuming a partial one — never worth backing up. |
| `history.json` (per-crawl record, up to ~2 years) | **No** | Append-only; nothing else recreates lost entries. |
| `traffic.json` (visitor traffic counters and the 90-day daily search ring) | **No** | Accumulated since counting began; nothing else recreates lost counts. |

**In short: back up `assets.json`, `tags.json`, `rates.json`, `stats.json`, `history.json`, and `traffic.json`. Skip `checkpoint.json` and `index.bleve/`.**

## Backup

### Backups are handled at the host level, outside this repository

Nothing in this repository — no cron job, script, or compose service — backs up `/data`; the operator manages backups at the host level instead. The manual procedure below is therefore not the primary backup: it exists for ad hoc snapshots, for example right before a risky change or upgrade, and as the reference for what is and isn't safe to copy while the stack runs.

### Is it safe to copy `/data` while the stack is running?

**Mostly yes, with one exception.** Every file in the "regenerable/not regenerable" table above except `index.bleve/` is written by `internal/storage`'s `writeAtomic`: a new version is written to a temporary file in the same directory, flushed to disk, and only then **renamed** into place. A rename is atomic — anyone reading the file at the same moment sees either the complete old version or the complete new one, never a half-written file. So copying `assets.json`, `history.json`, `traffic.json`, and the rest while the stack keeps running will never hand you a corrupted file.

What it *can* hand you is a set of files taken at slightly different moments relative to each other — a crawl publishes `index.bleve/`, then `assets.json`, then (moments later, as a separate step) `stats.json` and `history.json`. A backup taken in the middle of that sequence could pair a brand-new `assets.json` with the *previous* crawl's `history.json` entry. This is a few minutes of skew between files at the exact moment a crawl is publishing — not corruption, and given the default weekly crawl schedule, a narrow window to hit at all.

**`index.bleve/` is different, and is excluded from the backup below for exactly this reason.** Publishing a new index (`storage.PublishIndex` in `internal/storage/storage.go`) is a *three-step* rename dance across an entire directory tree (move the current index aside, move the new one into its place, delete the old one) — not one atomic operation the way a single file's rename is. A backup tool that reads the directory at the wrong instant mid-publish could see the published index briefly missing entirely. Since the index is fully regenerable from `assets.json` regardless (see "Index corruption" below), the simplest safe answer is: never back it up at all.

For a perfectly consistent snapshot with zero skew risk, stop the stack first (`docker compose --profile public down` — **not** `down -v`, see the warning below) before copying. For a routine, low-effort backup while the site stays up, copying live is safe against corruption and the skew risk is small in practice — just avoid treating a backup taken during an active crawl as more precise than it is.

### Manual backup procedure

Run this **on the Unraid host**, from any directory:

```
tar -czf /mnt/user/appdata/itchgrep/data-backup-$(date +%Y%m%d-%H%M).tar.gz \
  -C /mnt/user/appdata/itchgrep/data \
  assets.json tags.json rates.json stats.json history.json traffic.json
```

This creates a single compressed archive containing exactly the six files worth keeping, named with the current date and time so successive backups don't overwrite each other. Adjust the first path (where the archive is written) if you have a dedicated backup location — see the note below.

**NOT TESTED** — this repository has never actually been backed up or restored end to end; the command above is derived directly from `internal/storage`'s file names and the atomicity analysis above, not verified against a live restore.

**Where to put the resulting archive** is not something this repository specifies — the operator's host-level backup arrangement is the primary copy of this data, so an ad hoc snapshot from this procedure only needs to live somewhere outside `/mnt/user/appdata/itchgrep/` (so it doesn't share fate with the files it protects) for as long as the risky change that prompted it is in flight.

### What a backup of this data needs to cover, and how often

Whatever backs up this directory — the operator's host-level arrangement is the primary one — the facts that matter when judging its coverage and schedule:

- `traffic.json` is rewritten every 5 minutes while the webserver runs (see [docs/observability.md](observability.md)) — some loss here is already an accepted trade-off even without any backup (a hard container kill loses up to 5 minutes of counts on its own), so anything daily or better fully covers it.
- `assets.json`'s `FirstSeen` data and `history.json` change **once per crawl** — weekly by default (`CRAWL_INTERVAL=168h`; see [docs/configuration.md](configuration.md) for overriding it). A backup at least once per crawl interval means an outage never costs more than one crawl's worth of arrival-date and history data.
- The files worth archiving are small JSON documents (a few megabytes for the whole catalog); `index.bleve/` is regenerable and safe to exclude entirely (see above).

## Restore

Follow these steps **on the Unraid host**, in order, assuming you have a backup archive from the procedure above.

1. **Stop the stack.** From the repository checkout (for example, `/mnt/user/appdata/itchgrep/src`):
   ```
   docker compose --profile public down
   ```
   Expect the command to print each container stopping and being removed. This does **not** delete the data volume — only the containers.

2. **Confirm the data path is what you expect to restore into.**
   ```
   ls /mnt/user/appdata/itchgrep/data
   ```
   If this already contains files you want to keep, back those up separately first — the next step overwrites anything with the same name.

3. **Extract the backup archive into the data directory:**
   ```
   tar -xzf /path/to/your/data-backup-YYYYMMDD-HHMM.tar.gz -C /mnt/user/appdata/itchgrep/data
   ```

4. **Fix ownership.** Both containers run as uid `65532` (a non-root user baked into the distroless base image — see [ADR 0006](architecture/decisions/0006-distroless-nonroot-digest-pinned.md)), and a bind-mounted data path is never automatically `chown`'d by Docker:
   ```
   chown -R 65532:65532 /mnt/user/appdata/itchgrep/data
   ```
   Skipping this step is not a degraded mode — neither container can write to `/data`, so the containers start and the webserver reports healthy, but the crawl refuses to run and the site stays on `503` (`/readyz` answers `503 no data`).

5. **Start the stack:**
   ```
   docker compose --profile public up -d
   ```

6. **Watch for the automatic index rebuild.** Because the backup deliberately excludes `index.bleve/`, the dataservice will find no usable index on this fresh restore and rebuild one from `assets.json` on its own — no action needed from you. Watch its logs for confirmation:
   ```
   docker logs -f itchgrep-dataservice
   ```
   Expect a line like `Published index is unusable (...); rebuilding as scorch from assets.json`, followed within a few minutes by `Self-heal rebuild complete`.

7. **Verify.**
   - `curl http://127.0.0.1:62686/healthz` from the host should return `ok`.
   - Open `/stats` in a browser and confirm the crawl history and traffic counters look like what you expected to restore, not empty.
   - Open `/new` and confirm it shows a spread of arrival dates, not everything dated "today" — that would indicate the `FirstSeen` data didn't actually restore.

**NOT TESTED** — steps 1–7 are the logical restore sequence derived from reading `docker-compose.yml`, `cmd/dataservice/selfheal.go`, and the Dockerfiles' ownership comments; they have not been exercised against a real backup archive in this environment.

## Full-host-loss recovery

If the Unraid host itself is destroyed or unavailable, and you're rebuilding on new hardware (or any other Docker host):

1. Provision the new host with Docker and Docker Compose installed. Out of scope for this document.
2. Recreate the three appdata directories. Either run `scripts/bootstrap-host.sh` after cloning the repo (step 3 below gets you the script), or create them by hand first:
   ```
   mkdir -p /mnt/user/appdata/itchgrep/{src,logs,data}
   ```
3. Clone the repository. The Unraid host has no native `git`, so this uses the `alpine/git` Docker image the same way `scripts/update.sh` does (which pins the image in its `GIT_IMAGE` line; the short name below is for readability) — see [docs/infrastructure.md](infrastructure.md):
   ```
   docker run --rm -v /mnt/user/appdata/itchgrep/src:/git alpine/git clone https://github.com/SkilledAlpaca/itchgrep.git /git
   ```
4. Reconstruct `.env` in the `src/` directory, starting from `cp
   .env.example .env`. See [docs/configuration.md](configuration.md) for the
   complete variable list and their defaults. The four values a public
   production deployment needs — matching
   [docs/deployment.md](deployment.md#write-the-production-env-file)'s
   table exactly — are:
   - `CLOUDFLARE_TUNNEL_TOKEN` — **re-obtained**, from the Cloudflare
     dashboard; see "Credential loss" below for where and how.
   - `COMPOSE_PROJECT_NAME=itchgrep` — **re-obtained** as a fact, not a
     secret: the operator reports this is pinned in production, so set it
     explicitly so the network, the `cloudflared` container name, and any
     named volume match what this document's commands expect (the webserver
     and dataservice container names are pinned independently). See
     [docs/infrastructure.md](infrastructure.md).
   - `DATA_PATH` — a host path; set to `/mnt/user/appdata/itchgrep/data` to
     match `scripts/bootstrap-host.sh`'s layout.
   - `TRUST_PROXY_HEADERS=true` — **not** re-obtained from anywhere, but
     must be set explicitly: it defaults to `false`, and a rebuilt host
     behind the tunnel with this left at its default logs and rate-limits
     every visitor as the single `cloudflared` container address instead of
     their own. See [docs/configuration.md](configuration.md#trust_proxy_headers).
   Everything else in `.env.example`, including `TRUSTED_PROXY_CIDRS`, ships
   with a workable default and does not need to be sourced from anywhere
   external. `LOG_EXPORT_DIR` is not part of `.env` at all — see step 6
   below.
5. Restore the latest data backup into the freshly created `data/` directory — follow the RESTORE procedure above (steps 3–4 specifically: extract, then `chown -R 65532:65532`).
6. Install the log follower as an Unraid User Scripts entry (`itchgrep-log-export`, "At Startup of Array," background-only) — see [docs/deployment.md](deployment.md) for the exact steps. If you want logs written somewhere other than the default `/mnt/user/appdata/itchgrep/logs`, set `LOG_EXPORT_DIR` in that User Scripts entry's own environment, not in `.env` — `scripts/export-logs.sh` runs outside Docker Compose entirely, so `.env` has no effect on it. See [docs/configuration.md](configuration.md#log_export_dir).
7. Bring up the stack:
   ```
   docker compose --profile public up -d --build
   ```
8. Verify per the RESTORE procedure's step 7 above.

**If no backup exists at all,** the stack still recovers on its own with zero manual data intervention — the dataservice starts a full crawl unconditionally the moment it finds no published index (see `scheduleCrawls` in `cmd/dataservice/main.go`). What you permanently lose in that case: every asset's true `FirstSeen` date (everything will show as arriving the moment the fresh crawl publishes), the entire `history.json` crawl record, and every `traffic.json` visitor count ever accumulated. This is the concrete cost of having no backup at all, and the reason even an infrequent one is worth keeping.

**NOT TESTED, end to end.** No rebuild-from-scratch has been rehearsed against this host. Every step above is derived from reading `scripts/bootstrap-host.sh`, `scripts/update.sh`, `docker-compose.yml`, and `cmd/dataservice/main.go`'s `scheduleCrawls`, not from a live exercise.

## Credential loss

### GitHub account access

The operator reports the project is hosted under the `SkilledAlpaca` GitHub organization/account. Losing access to it blocks pulling new code (`scripts/update.sh`'s pull step) and CI, and makes the security-contact link in `security.txt` and the crawler's `User-Agent` (both pointing at this repo's issue tracker) unreachable to fix. The already-running production stack is unaffected until the next deploy is needed. Who else has admin access, and the account's own 2FA recovery setup, are not verifiable from this repository — see [docs/open-questions.md](open-questions.md).

### Cloudflare account access

Losing this blocks DNS for `itchgrep.com` and the tunnel entirely — the public site goes down the moment the existing `cloudflared` connection drops for any reason (a container restart, a host reboot) and can't reconnect. Account recovery details are not verifiable from this repository — see [docs/open-questions.md](open-questions.md).

### Rotating the Cloudflare Tunnel token

Per Cloudflare's own published documentation (current as of 2026 — Cloudflare moved tunnel management from the separate "Zero Trust" dashboard into the main Cloudflare dashboard in a February 2026 change, so confirm the exact current menu path yourself, since dashboards do get reorganized):

1. In the Cloudflare dashboard, go to **Networking > Tunnels**.
2. Select the itchgrep tunnel.
3. Choose **Rotate token**.
4. Copy the new token shown.

After rotating, the *old* token immediately stops being able to establish new connections, but an already-connected `cloudflared` keeps running on its existing connection until it's restarted — so rotating the token doesn't cause an immediate outage, but the new token must be deployed before the next restart or the tunnel won't reconnect.

To deploy the new token to this stack:

1. On the host, edit the production `.env` file (in the repository checkout directory, next to `docker-compose.yml`) and set `CLOUDFLARE_TUNNEL_TOKEN` to the new value. `.env` is gitignored — this is a host-local edit, never committed.
2. Recreate the `cloudflared` container so it picks up the new value:
   ```
   docker compose --profile public up -d --no-deps cloudflared
   ```
3. Confirm the tunnel shows as connected in the Cloudflare dashboard.

**Rotate immediately if the token is ever exposed.** The token is a credential, and the operator reports it is also visible to anyone who can run `docker inspect` on the host (it appears in the container's environment) — so host access effectively grants tunnel access too.

**NOT TESTED** against this specific deployment. The rotation procedure above is Cloudflare's own general documentation, not something exercised against this project's tunnel.

## `docker compose down -v` destroys unregenerable data

**Never run `docker compose down -v` as a casual reset.** The `-v` flag deletes the named Docker volume outright — and if you're running on the default named volume (`itchgrep-data`, used whenever `DATA_PATH` isn't set), that command permanently destroys `history.json`, `traffic.json`, and every asset's `FirstSeen` stamp, with **no self-heal path back** — those are exactly the three things this document classifies as not regenerable, above.

`docker compose down` (no `-v`) is safe: it stops and removes the containers only, leaving the volume — and everything in it — untouched.

If you've set `DATA_PATH` to a host bind-mount instead of the named volume (production does this — see [docs/configuration.md](configuration.md)), `-v` doesn't touch it at all, since a bind mount is a plain host directory, not a Docker-managed volume. But a bare `rm -rf` on that host path is equally destructive and just as unrecoverable without a backup.

## Index corruption

**This is normally self-correcting, with no downtime.** `cmd/dataservice/selfheal.go`'s `selfHealIndexFormat` runs once, automatically, every time the dataservice starts, before it schedules any crawl. It tries to open the published index the same way the webserver does; if that fails — a wrong-format index left over from an old bleve version, or a half-written directory from a crash mid-publish — it rebuilds a fresh index from `assets.json` in minutes, with **no recrawl of itch.io needed**. The webserver keeps serving the previous (stale but working) index the entire time this runs, and swaps to the new one automatically once it's published.

Watch for this pair of log lines in `crawler.log` (see [docs/observability.md](observability.md)):

```
Published index is unusable (<reason>); rebuilding as scorch from assets.json - this takes minutes, the webserver keeps serving the old data meanwhile
Self-heal rebuild complete
```

**Manual fallback,** if you ever want to force this deliberately (for example, to test it, or if you suspect an index problem the automatic check somehow isn't catching):

1. Stop the stack: `docker compose --profile public down` (not `-v`).
2. Delete the index directory from the data path: `rm -rf /mnt/user/appdata/itchgrep/data/index.bleve`
3. Restart: `docker compose --profile public up -d`
4. The dataservice will find no openable index on its next startup and run the exact same automatic rebuild described above.

This is the same code path the automatic self-heal already exercises on a genuinely corrupt index — forcing it manually is deliberately no different.
