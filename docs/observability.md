# Observability

This document explains where itchgrep's logs and metrics come from, what they mean, and how to tell normal operation from a real problem. It assumes no prior familiarity with Docker logging drivers or structured logs.

For what the two containers are and how they're deployed, see [docs/infrastructure.md](infrastructure.md) and [docs/deployment.md](deployment.md).

## Log pipeline, end to end

Both `itchgrep-webserver` and the `dataservice` container write everything to their own standard output (stdout) as plain text or single-line JSON. Nothing inside either container writes to a log file — `internal/logging.Init()` takes no arguments and always writes to stdout; both `cmd/webserver/main.go` and `cmd/dataservice/main.go` call it the same way, so there is no log file to read from inside the (distroless, shell-less) container itself.

The line then travels through four stages before it's durable:

1. **Container stdout.** The application writes one line per event: leveled text lines (`INFO:`, `WARN:`, `ERRO:`, `DEBG:`, `FATL:`) from `internal/logging`, and level-free single-line JSON records for access and probe events (see "Structured log kinds" below).
2. **Docker's `json-file` log driver.** Docker captures that stdout and buffers it on the host, capped by `docker-compose.yml`'s shared `x-logging` block:
   ```yaml
   x-logging: &logging
     driver: json-file
     options:
       max-size: "10m"
       max-file: "3"
   ```
   That's 10 MB per file, 3 files kept, per service — 30 MB total per service. Without a cap, Docker's default `json-file` driver keeps every line ever written, growing without bound. This 30 MB is deliberately just a hand-off buffer, not the log's actual home — see the next stage.
3. **The host-side follower, `scripts/export-logs.sh`.** A shell script that runs continuously on the Unraid host (outside Docker Compose, installed as an Unraid User Scripts entry — see [docs/deployment.md](deployment.md)) and follows **both** containers forever, each in its own independent reconnecting loop: `docker logs -f --tail 0 itchgrep-webserver` and `docker logs -f --tail 0 itchgrep-dataservice`. The webserver stream is split by its JSON `"kind"` field into three files; the dataservice stream — which carries no `"kind"` discriminator, just plain `INFO:`/`WARN:`/`ERRO:` text — goes entirely to a fourth file. Either follower giving up (the container stays unreachable for about 5 minutes) stops both and exits the whole script non-zero, so a silent half-captured state is never mistaken for a healthy one.
4. **Durable, rotated files** under `/mnt/user/appdata/itchgrep/logs/` (the default `LOG_EXPORT_DIR`):
   - `access.jsonl` — one JSON line per served request (`"kind":"access"`), from the webserver.
   - `security.jsonl` — one JSON line per request the scanner-probe filter blocked (`"kind":"probe"`), from the webserver.
   - `app.log` — everything else the webserver logs: the plain `INFO:`/`WARN:`/`ERRO:` text lines.
   - `crawler.log` — every line the dataservice logs (crawl progress, coverage figures, 429 counts, self-heal messages), unsplit.

   These are rotated daily and kept 30 days by `logrotate`, using `scripts/itchgrep-logrotate.conf`:
   ```
   /mnt/user/appdata/itchgrep/logs/*.jsonl /mnt/user/appdata/itchgrep/logs/*.log {
       daily
       rotate 30
       compress
       missingok
       notifempty
       create 0640 root root
   }
   ```
   Because Unraid's `/etc` is a tmpfs ramdisk (wiped on every reboot), a `logrotate.d` config dropped in by hand would silently disappear at the next array start. `scripts/export-logs.sh` works around this by reinstalling this file to `/etc/logrotate.d/itchgrep` on every single run, so one User Scripts entry keeps both the follower and the 30-day retention guarantee alive.

**Both containers are captured.** `export-logs.sh` follows `itchgrep-webserver` (`CONTAINER_NAME`) and `itchgrep-dataservice` (`DATASERVICE_CONTAINER_NAME`) independently — see [docs/configuration.md](configuration.md#dataservice_container_name). This closes what used to be a gap: the dataservice's crawl logs (progress, coverage, 429 counts, self-heal messages) previously lived only in Docker's own 30 MB-capped `json-file` buffer and were lost whenever that container was recreated (for example, by `docker compose up -d --build`). They're now durable in `crawler.log` the same way the webserver's logs are.

## Structured log kinds

### Access records (`"kind":"access"`)

Written by `internal/web/logmiddleware.go`'s `RequestLogger` middleware, one line per request that reaches routing (that is, everything the scanner-probe filter didn't already turn away — see below). Fields:

| Field | Meaning |
|---|---|
| `ts` | Request start time, RFC 3339, UTC |
| `kind` | Always `"access"` |
| `ip` | The client's address, **truncated** — see "Privacy posture" below |
| `method` | HTTP method |
| `path` | Request path only, truncated to 512 bytes |
| `query` | `true`/`false` — whether a query string was present. **Never the query text itself.** |
| `route` | The chi route pattern matched (e.g. `/tag/{slug}`), or empty if nothing matched |
| `status` | HTTP status code returned |
| `bytes` | Response body bytes written |
| `dur_ms` | Request duration in milliseconds |
| `ua` | User-Agent header, truncated to 256 bytes |

### Probe records (`"kind":"probe"`)

Written by `internal/web/probes.go`'s `ProbeFilter`, which runs ahead of routing and the access logger. It matches the shape of commodity vulnerability scanner sweeps — paths like `/wp-admin`, `/.env`, `/phpmyadmin`, anything ending `.php` — and answers with a bare 404 before the request reaches anything else. Fields are the same as an access record minus `query`, `route`, `status`, `bytes`, and `dur_ms` (a probe is always a bare 404, so those would be constant). This is the origin-side backstop for the same traffic the Cloudflare WAF rules block at the edge — see [docs/infrastructure.md](infrastructure.md#waf-rules).

Probe traffic draws from the **same per-client rate-limit budget** as real requests — `ProbeFilter` charges a token before writing the JSON record and answering the 404, using the same `Limiter` instance the rate-limited route group uses. This means a sustained scanner sweep from one client is throttled the same way a real client hammering the site would be, and its record-writing (a disk write per hit) is bounded by that same budget rather than unlimited.

### Privacy posture: what is and isn't stored

- **Query text is never logged**, anywhere, by either record kind — only a boolean for whether a query string was present. `internal/metrics`'s own package comment states the same constraint applies to the in-memory counters.
- **IP addresses are truncated, not hashed.** `AnonymizeIP` (`internal/web/clientip.go`) masks an IPv4 address to its `/24` network and an IPv6 address to its `/48` network before it is ever written to a log line — for example, `203.0.113.47` is stored as `203.0.113.0`. This is enough to recognize a repeat offender or a sweep from one allocation, not enough to identify an individual visitor. The full, untruncated IP is used only in memory, for the rate limiter's own bookkeeping — it is never itself persisted.
- The follower's gap markers (below) carry no IP or path at all.

### Follower gap markers

`scripts/export-logs.sh` writes its own JSON lines directly into `access.jsonl`, independent of the container:

```json
{"ts":"2026-08-29T12:00:00Z","kind":"follower","event":"start"}
{"ts":"2026-08-29T12:05:11Z","kind":"follower","event":"reconnect"}
```

- `"event":"start"` — the follower began following the container for the first time since it last started.
- `"event":"reconnect"` — the follower's `docker logs -f` stream dropped (container restart, Docker daemon reload) and it reconnected.

`docker logs -f --tail 0` only replays lines written **after** the follower reconnects — anything logged while it was disconnected is lost, not replayed, because Docker's log stream has no resumable cursor to replay from. These markers exist specifically to make that gap visible in `access.jsonl` rather than silent: if you see a `start`/`reconnect` marker with a suspiciously long gap in timestamps before it, that gap is genuinely missing data, not "no traffic occurred."

## Log analysis recipes

These `jq` queries answer the questions the exported logs exist for. Run them on the Unraid host, in the export directory (`cd /mnt/user/appdata/itchgrep/logs`); `jq` ships with Unraid. The records they read are the [access records](#access-records-kindaccess) described earlier.

Top talkers by (network-truncated) IP:

```bash
jq -s -c 'group_by(.ip) | map({ip: .[0].ip, count: length}) | sort_by(-.count)' access.jsonl
```

404 clusters on routes that actually exist (a live route 404ing a lot usually means a stale link, not a scanner — probe records never carry a non-empty `route`):

```bash
jq -s -c '
  map(select(.status == 404 and .route != ""))
  | group_by(.route)
  | map({route: .[0].route, count: length})
  | sort_by(-.count)
' access.jsonl
```

p95 request duration by route:

```bash
jq -s -c '
  group_by(.route)
  | map({
      route: .[0].route,
      p95: ((map(.dur_ms) | sort) as $s | $s[(($s | length - 1) * 0.95 | floor)]),
      count: length
    })
' access.jsonl
```

## Metrics

`internal/metrics` is an in-process, no-PII tally — not a log reader. It exists because no log file exists inside the container — log lines go to stdout, into Docker's `json-file` buffer on the host and then the exported files — and the distroless runtime image has no Docker client to read them back even if that were desirable (it isn't — see the package comment: "counters cannot leak what they never store").

**What is counted:** total requests, broken down by route (`/`, `/results`, `/about`, `/stats`, `/static/*`, `/new`, `/feed`, `/tag/{slug}`, `/saved`, and an `other` bucket for everything else — this breakdown is exhaustive by construction, so it always sums to the total), a separate `searches` counter (a search actually answered: HTTP 200 for the first page of results narrowed by a query or at least one filter — tags, exclusions, similar-to, author, or price. Redirects, load-more pages (`page` above 1), and requests that carry only a currency or sort are not counted; see `isSearchRequest` in `internal/web/logmiddleware.go`. The 90-day grid and the total use this one definition. Totals recorded before this definition include those requests and are not rewritten), a 90-day ring of searches by UTC day, and — internally only, never rendered on the public page — a count of blocked scanner probes.

**Where it's stored:** `traffic.json` in the shared `/data` directory (`internal/storage.PutTraffic`/`GetTraffic`). The `Days` field holds the 90 `{EpochDay, Searches}` buckets (UTC Unix seconds / 86400) behind the activity grid; buckets older than 90 days are dropped on restore. A file from before this field existed, with an `Hours` array and no `Days`, still loads: the old hourly data is ignored and the grid starts empty. This is the one metrics artifact that survives a restart; see [docs/disaster-recovery.md](disaster-recovery.md) for why it's classified as unregenerable.

**Persistence cadence:** every 5 minutes (`trafficSnapshotInterval` in `cmd/webserver/main.go`), via `Counters.StartSnapshotter`. A clean shutdown (SIGTERM/SIGINT, what `docker stop` sends) also writes one final snapshot on the way out via `PersistNow`, so only a hard kill (`docker kill -9`, an OOM) can lose up to 5 minutes of counts.

**What's exposed publicly:** the `/stats` page renders these counters (`internal/web/templates/traffic.go`, `stats_templ.go`) — total requests by route, the 90-day search activity grid (one cell per UTC day, git-contribution style, rendered as an accessible table), process uptime, and "counting since" (which predates the current process if `traffic.json` was restored). It also renders separate, non-metrics data on the same page: crawl coverage (from `stats.json`) and crawl history (from `history.json`) — those come from `internal/storage`, not from `internal/metrics`.

## Health checks

`GET /healthz` and `HEAD /healthz` (`internal/web/healthz.go`) answer `200 ok` (plain text, `Cache-Control: no-store`) the moment the HTTP router is up — with **no dependency on data having loaded**. This is deliberate: on a first deployment, nothing has crawled yet and the cache can legitimately stay unready for hours; a health check that failed on that condition would have Docker restart-loop a webserver that is working exactly as designed. `/results` and other data-dependent routes report that state themselves, with a `503`.

`GET /readyz` and `HEAD /readyz` are `/healthz`'s opposite number: they DO depend on the cache, answering `200 ready` only once a published dataset has actually loaded and the cache can serve searches (an index and its asset list are both in memory), and `503 no data` for as long as it hasn't. This exists for an operator or an external uptime monitor that wants to tell "the process is up" apart from "the site actually has something to serve" — a distinction `/healthz` deliberately can't make. It's mounted beside `/healthz` (outside the rate-limited group, and inside `probes.go`'s `liveRoutes` so it's never mistaken for a scanner probe) but **deliberately not wired into `docker-compose.yml`'s `healthcheck:` stanza**, which stays on `/healthz` for the same reason `/healthz` itself has no data dependency: a freshly started `--profile public` stack's `cloudflared` (gated on `service_healthy`) must still come up while the first crawl is in progress, not wait on it.

`docker-compose.yml`'s healthcheck stanza only exists on the `webserver` service:

```yaml
healthcheck:
  test: ["CMD", "/server", "-healthcheck"]
  interval: 30s
  timeout: 5s
  retries: 3
  start_period: 10s
```

`/server -healthcheck` is the webserver binary's own flag — it does a `GET` against its own `/healthz` and exits 0 or 1. This indirection exists because the distroless runtime image has no shell and no `curl` for Compose to exec directly. `cloudflared`'s `depends_on: webserver: condition: service_healthy` waits on exactly this check before it starts routing public traffic at the webserver.

**The dataservice deliberately has no `healthcheck:` stanza at all.** Its only endpoint, `/trigger-fetch`, has no cheap liveness signal that doesn't also depend on crawl outcome — a failed or slow crawl must not mark the container unhealthy and restart-loop it mid-crawl, discarding hours of progress every time it does.

To read health state directly:

```
docker inspect -f '{{.State.Health.Status}}' itchgrep-webserver
```

Expect `starting` briefly after a (re)start, then `healthy`. There is no equivalent command for the dataservice — judge its health from its logs and from `/stats`' coverage figure instead (see below).

## What normal looks like vs. what warrants attention

| Observation | Normal? | Why |
|---|---|---|
| `503` from `/results` right after a fresh deploy or on a brand-new host | **Normal** | No crawl has published yet; the cache reports "not ready" until the first crawl finishes — see `initializeCache` in `cmd/webserver/main.go`. Can legitimately last hours on a first run. |
| A burst of `"kind":"probe"` lines in `security.jsonl` | **Normal, background noise** | Commodity vulnerability scanners sweep the whole internet continuously; the probe filter exists specifically to answer these cheaply. `app.log` logs an aggregate `INFO: blocked %d scanner probes` line whenever the count moves between snapshotter ticks — a single scanner sweep showing up there is expected, not an incident. |
| Sustained `5xx` status codes in `access.jsonl` for routes other than the pre-data `503` above | **Not normal** | Investigate: check `app.log` around the same timestamps for a stack trace or repeated error, and confirm the cache actually loaded (`/healthz` alone won't tell you — `/readyz` answers `200 ready` or `503 no data`; `app.log` shows "Initial cache load failed" while it isn't). |
| `crawler.log` showing repeated `"Coverage ... below COVERAGE_FLOOR"` across successive crawl cycles | **Not normal** | The dataservice is refusing to publish; the site keeps serving an increasingly stale dataset. See [docs/troubleshooting.md](troubleshooting.md#crawl-fails-or-coverage-below-floor) for what can cause this and [ADR 0003](architecture/decisions/0003-coverage-floor-and-backoff.md) for the gate's design. |
| `crawler.log` showing `"Published index is unusable (...); rebuilding..."` followed shortly by `"Self-heal rebuild complete"` | **Normal, self-correcting** | The index self-heal path (`cmd/dataservice/selfheal.go`) ran and fixed itself in minutes with no downtime — see [docs/disaster-recovery.md](disaster-recovery.md#index-corruption). Worth noting for your own awareness, not an incident. |
| Gaps between a `follower`/`reconnect` marker and the previous line, more than a minute or so | **Investigate the host, not the app** | The follower itself dropped its connection to Docker — check whether the Docker daemon restarted, or whether the User Scripts entry is even still running. |
| `itchgrep-webserver`'s health status stuck at `starting` or flips to `unhealthy` and stays there | **Not normal** | The process isn't answering `/healthz` at all — check `docker logs itchgrep-webserver` and `docker inspect -f '{{.State.OOMKilled}}' itchgrep-webserver` (see [docs/troubleshooting.md](troubleshooting.md#webserver-container-unhealthy-or-restart-looping)). Wrong `/data` ownership does not cause this; it leaves the container healthy and the site on `503`. |

## NOT TESTED

The health-check and self-heal log-line behaviors above were verified by reading the source (`internal/web/healthz.go`, `cmd/dataservice/selfheal.go`, `cmd/webserver/main.go`) and the compose healthcheck stanza, not by triggering each condition live in this environment. The log pipeline's four-stage description was verified against every script and config file it names; the actual host-side `logrotate` run and the Unraid User Scripts entry itself were not exercised from here — see [docs/deployment.md](deployment.md) for that installation procedure.
