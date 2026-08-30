# Refine findings queue — 2026-08-29 (takeover pass)

Consolidated from five independent reviewers (architecture/quality/perf, security/deps,
beginner/docs/deploy, operations/recovery, tribal knowledge). Deduplicated; every entry
spot-verified against the code by the coordinator. Work top-down. This file is a
temporary review artifact, deleted in the final cleanup.

## P0

- [ ] **F1 Reboot survival**: only `cloudflared` has `restart: unless-stopped`; after a host
  reboot the tunnel comes back and the origin does not — public 502s until manual
  intervention. Add `restart: unless-stopped` to webserver + dataservice (not the one-shot
  indexer). (docker-compose.yml)
- [ ] **F2 /trigger-fetch exposure**: unauthenticated state-changing GET published on
  `0.0.0.0:62685`; LAN- and CSRF-triggerable multi-hour crawls of a third party. Bind
  loopback (`127.0.0.1:62685:8080`), require POST. (docker-compose.yml,
  cmd/dataservice/main.go, Taskfile, README)
- [ ] **F3 Backup/restore absent**: `history.json`, `traffic.json`, and the FirstSeen stamps in
  `assets.json` are unregenerable; the documented reset (`down -v`) destroys them with no
  warning, and the documented storage inventory lists 4 of 9 artifacts. Runbook + corrected
  inventory + `down -v` warning. (README/docs)

## P1

- [ ] **F4 Raw queries logged**: `search.go:243` + `describe()` write visitor query text and
  author filters to the persistent app log, contradicting the stated privacy posture. Strip
  content; log shape only. (internal/cache/search.go, internal/metrics doc comment)
- [ ] **F5 ?page= overflow panic**: no upper bound → int64 overflow → negative slice bounds;
  unauthenticated 500s that bypass the access log. Clamp in pageParam + guard in
  browse/search. (internal/web/handlers.go, internal/cache)
- [ ] **F6 EOL toolchain + vulnerable deps + no CI**: golang:1.22 images, x/net v0.21 with 10
  reachable vulns via goquery, bleve v1 abandoned. Bump Go + deps, govulncheck clean, add a
  minimal GitHub Actions workflow. (Dockerfiles, go.mod, .github/workflows)
- [ ] **F7 Index triple-bloat + legacy backend**: default dynamic mapping stores `_all`,
  stored fields, and doc values nobody reads; upsidedown/boltdb backend. Move to bleve
  v2 + scorch + explicit mapping; measure size before/after. (internal/storage/index.go,
  internal/cache, cmd/dataservice)
- [ ] **F8 Proxy-header trust**: `CF-Connecting-IP` trusted from any peer while port 62686 is
  LAN-published — rate limiter bypass + log poisoning. Trust the header only from the
  compose network. (internal/web/clientip.go, ratelimit.go)
- [ ] **F9 templ claim false**: README says generation "not required to build"; a fresh clone
  fails to build/test without it. Document the prerequisite. (README)
- [ ] **F10 .env.example incomplete**: omits every production variable the README tells you to
  set (CLOUDFLARE_TUNNEL_TOKEN, TRUST_PROXY_HEADERS, DATA_PATH, COMPOSE_PROJECT_NAME,
  CRAWL_INTERVAL, RATE_LIMIT_*, …). Regenerate to cover everything compose interpolates.
- [ ] **F11 Coverage figures contradict**: 89% (README, code, tests) vs 79.5% (compose,
  planner comment); floors 0.75/0.70/0.75. Reconcile to the current measured figure
  everywhere, one default.
- [ ] **F12 Failure → continuous crawl**: any failed publish leaves the mtime stale, so the
  15-minute scheduler re-runs a 6-hour crawl back-to-back forever (itch.io block risk).
  Backoff on consecutive failures. (cmd/dataservice/main.go)
- [ ] **F13 Logrotate on a ramdisk**: /etc is wiped every Unraid boot; retention promise
  silently voids. Install the conf from the follower script at array start. (scripts/,
  README)
- [ ] **F14 Container-name coupling**: follower depends on `itchgrep-webserver-1`, which only
  exists because of an undocumented COMPOSE_PROJECT_NAME pin; README documents the wrong
  derivation. Set `container_name:` in compose + fail-loud follower + docs.
- [ ] **F15 No update runbook**: host has no git; the update command lives in shell history;
  documented deploy command omits `--build` (silently redeploys stale code). scripts/update
  runbook + fix the command everywhere.
- [ ] **F16 Cloudflare estate unrecorded**: second WAF rule (managed challenge on query URLs),
  tunnel hostname routes, DNS, registrar, account owner — none in the repo. Document; open
  questions where unknowable.
- [ ] **F17 Indexer sidecar removable**: scheduler declines the first crawl for a reason the
  CAS guard already handles; the whole curl container + INDEXER_TIMEOUT exists to paper
  over it. Scheduler runs the first crawl; delete the service. (cmd/dataservice,
  docker-compose.yml)
- [ ] **F18 No SIGTERM handling**: both services lose up to 5 min of counters / 3 min of crawl
  per restart, and restarts are routine. Graceful shutdown + final snapshot/checkpoint.
- [ ] **F19 Dataservice server unhardened**: bare `http.ListenAndServe` on DefaultServeMux, no
  timeouts (Slowloris). Explicit server + own mux. (cmd/dataservice/main.go)
- [ ] **F20 Health**: no /healthz, no compose healthchecks, cloudflared routes before the
  origin binds. Add /healthz + Go probe + compose healthchecks + depends_on healthy.

## P2

- [ ] **F21 Containers run as root**: distroless base defaults uid 0 with rw data volume. Move
  to `:nonroot`, document the one-time host chown. (Dockerfiles, docs)
- [ ] **F22 statusFor labels server faults 400**: index failures invisible to 5xx monitoring.
  500 default; 400 only for client causes. (internal/web/handlers.go)
- [ ] **F23 dataMap duplicates the corpus**: `map[string]models.Asset` copies every asset
  beside the slice. `map[string]int` into c.data. (internal/cache/cache.go)
- [ ] **F24 Checkpoint rewrites ~26 MB every 3 min** (~3-4 GB writes/crawl), lock held during
  copy. Scale cadence to work at risk. (cmd/dataservice/main.go)
- [ ] **F25 MAX_TAGS sentinel defeated**: compose always sets it, so the documented smoke-test
  shrink never fires. Drop from compose env or compare against default. (docker-compose.yml)
- [ ] **F26 .dockerignore gaps**: data/, data-smoke/, .env (tunnel token) enter the build
  context. Invert to an allowlist.
- [ ] **F27 Floating image tags**: golang:1.22, distroless untagged, two :latest (incl.
  cloudflared holding the tunnel credential). Pin tags + digests.
- [ ] **F28 Tunnel-token rotation undocumented**; token visible in `docker inspect`. Runbook +
  consider file-based secret. (docs)
- [ ] **F29 Follower gaps invisible**: `--tail 0` drops lines during downtime with no marker;
  nothing supervises the script. Reconnect markers + fail-loud. (scripts/export-logs.sh)
  ALSO: compose now pins `container_name: itchgrep-webserver` (no `-1` suffix) — update the
  follower default CONTAINER_NAME and README:345 to match, and flag the host-side script
  refresh in the deploy runbook.
- [ ] **F30 fetchAndStoreAssets untestable**: 235-line pipeline, eight untested early-return
  paths encoding load-bearing ordering. Extract named stages with tests. (cmd/dataservice)
- [ ] **F31 Host bootstrap undocumented**: three load-bearing host dirs created by hand.
  bootstrap script + docs. (scripts/)
- [ ] **F32 Contact-channel ownership**: security.txt + crawler UA point at a GitHub account
  whose monitoring/handover is undocumented. (docs)

## P3

- [ ] **F33 writeAtomic doesn't fsync the parent dir** (crash can revert traffic.json). Sync
  dir after rename. (internal/storage/storage.go)
- [ ] **F34 Unbounded logged path** while UA is truncated; MaxHeaderBytes default 1 MB.
  Truncate path in both records; set MaxHeaderBytes. (internal/web)
- [ ] **F35 templ.SafeURL on scraped href** (asset.Link) skips scheme validation. Use
  templ.URL. (templates/asset_page.templ)
- [ ] **F36 Metrics route breakdown omits /new, /feed, /tag, /saved**; /tag misses the
  searches counter; numbers don't sum. Exhaustive buckets + subset test. (internal/metrics)
- [ ] **F37 Discarded facet on /new and /feed**: most expensive part of the query thrown away.
  Facets flag in SearchOptions. (internal/cache)
- [ ] **F38 Triplicated count formatter, copy-pasted 429 retry loop, forked env helpers.**
  Consolidate. (templates, fetcher, config)
- [ ] **F39 htmx 1.9.10 vendored with no provenance.** VENDOR note + refresh task. (static/)
- [ ] **F40 Stale comments**: metrics probe-logging claim, crawl-duration estimates (1h vs
  6h across four files), planner 79.5% figure. Fix with F11.
- [ ] **F41 README direct-run snippet double-binds :8080**; Taskfile already solves it.
  Align. (README)
- [ ] **F42 Log-follower docs**: RECONNECT_DELAY_SECONDS undocumented; logrotate glob
  hardcodes the default LOG_EXPORT_DIR. (README, scripts)
- [ ] **F43 REVIEW_BASELINE.md holds the only record of production facts** — migrate to
  permanent docs before final cleanup.
