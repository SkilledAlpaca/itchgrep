# Configuration reference

itchgrep is configured entirely through **environment variables**: named
values that the operating system (or, for a container, the container
runtime) hands to a running program, kept outside the program's source code.
This document lists every environment variable either binary (`webserver`,
`dataservice`) or operational script in this repository reads, where each one
is read, and what happens if you set it wrong.

If you only want to run the app locally, see
[docs/getting-started.md](getting-started.md) — it links back here for
detail but doesn't require reading this whole page first.

## How a setting reaches the process

Three different paths deliver an environment variable, and which one applies
determines whether editing `.env` actually does anything:

1. **`docker compose up`.** Compose reads a file named `.env`, next to
   `docker-compose.yml`, and uses it in two ways: to fill in `${VAR}`
   placeholders inside `docker-compose.yml` itself, and — only for variables
   that `docker-compose.yml` explicitly lists under a service's
   `environment:` block — to set the value the container's process actually
   sees. **A variable in `.env` that no `${VAR}` placeholder in
   `docker-compose.yml` references never reaches the container**, no matter
   how it's spelled. `MAX_TAGS` is exactly this trap — see its entry below.
2. **A host script** (`scripts/export-logs.sh`, `scripts/update.sh`,
   `scripts/bootstrap-host.sh`). These are plain shell scripts run directly
   on the Unraid host, outside Docker Compose entirely. They read the
   environment variables listed in their own `${VAR:-default}` lines,
   inherited from whatever shell (or Unraid User Scripts entry) launches
   them. Setting these in the compose `.env` file does nothing — that file
   is only read by `docker compose`.
3. **Running a binary directly** (`go run ./cmd/webserver`, `task webserver`,
   `task smoke`). Here the Go process reads its environment directly with no
   Compose layer in between, so exporting a variable in your shell (or
   setting it in a Taskfile `env:` block) always reaches it.

Copy `.env.example` to `.env` to start (see
[docs/getting-started.md](getting-started.md)); it's read automatically by
`docker compose` and is excluded from Git by `.gitignore`.

## All configuration values

Change safety:

- **Safe to change** — a normal operational knob; changing it can't corrupt
  data or take the site down by itself.
- **Change only if you understand the consequences** — changing it wrong can
  degrade the crawl, break rate limiting, or take the tunnel down; read the
  variable's detail section first.
- **Never change manually** — fixed by design for this deployment; changing
  it requires also changing something else it's paired with (a volume mount,
  a container name a script depends on), or it has no effect where you'd
  expect it to.

| Setting | Location | Required | Default | Valid values | Purpose | Change safety |
| --- | --- | --- | --- | --- | --- | --- |
| [`CRAWL_INTERVAL`](#crawl_interval) | `docker-compose.yml` (both services), `.env.example` (commented), `internal/config/config.go` | No | `168h` (1 week) | Go duration string, or `0` | How stale the index may get before an automatic rebuild | Safe to change |
| [`SCRAPE_RPS`](#scrape_rps) | `docker-compose.yml`, `.env.example`, `internal/fetcher/fetcher.go` | No | `2` | Positive integer, ≤ `1000` | Outbound requests per second to itch.io during a crawl | Change only if you understand the consequences |
| [`SCRAPE_MAX_PAGES`](#scrape_max_pages) | `docker-compose.yml`, `.env.example`, `cmd/dataservice/main.go` | No | Unset (unlimited) | Positive integer | Caps total pages fetched, for a fast partial "smoke" crawl | Change only if you understand the consequences |
| [`COVERAGE_TARGET`](#coverage_target) | `docker-compose.yml`, `.env.example`, `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`) | No | `0.95` | Non-negative float | Stops the crawl once this fraction of the catalog is collected | Safe to change |
| [`COVERAGE_FLOOR`](#coverage_floor) | `docker-compose.yml`, `.env.example`, `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`) | No | `0.75` | Non-negative float | Refuses to publish a crawl that covers less than this fraction | Change only if you understand the consequences |
| [`SLICE_MIN_YIELD`](#slice_min_yield) | `docker-compose.yml`, `.env.example`, `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`) | No | `0.05` | Non-negative float | Abandons a crawl view once it stops finding new assets | Change only if you understand the consequences |
| [`TAG_CACHE_MAX_AGE`](#tag_cache_max_age) | `docker-compose.yml`, `.env.example`, `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`) | No | `168h` | Positive Go duration | How long the cached tag list is reused before rediscovery | Safe to change |
| [`CHECKPOINT_MAX_AGE`](#checkpoint_max_age) | `docker-compose.yml`, `.env.example` (commented), `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`) | No | `24h` | Positive Go duration | How stale an in-progress crawl checkpoint may be before it's ignored | Safe to change |
| [`MAX_TAGS`](#max_tags) | `.env.example`, `internal/config/config.go`, `cmd/dataservice/main.go` (`loadCrawlConfig`), `internal/fetcher/tags.go` | No | `1000` | Positive integer | Upper bound on the discovered tag vocabulary | Change only if you understand the consequences |
| [`DATA_DIR`](#data_dir) | `docker-compose.yml`, `internal/storage/storage.go` | No | `data` (relative to the working directory) | Filesystem path | Where the crawl database (`assets.json`, `index.bleve/`, and so on) lives | Never change manually (under Compose) |
| [`DATA_PATH`](#data_path) | `docker-compose.yml` (top level) | No | Named Docker volume `itchgrep-data` | Absolute host path, or omit | Whether the shared data directory is a host folder or a Docker-managed volume | Change only if you understand the consequences |
| [`COMPOSE_PROJECT_NAME`](#compose_project_name) | `docker-compose.yml` (Compose built-in) | Yes in production | Basename of the checkout directory | Any short identifier, no spaces | Names the Compose project (containers, networks, volumes) | Never change manually |
| [`CLOUDFLARE_TUNNEL_TOKEN`](#cloudflare_tunnel_token) | `docker-compose.yml` (`cloudflared` service) | Only with `--profile public` | Empty (tunnel refuses to start) | Opaque token string from Cloudflare | Authenticates the `cloudflared` tunnel to your Cloudflare account | Change only if you understand the consequences |
| [`TRUST_PROXY_HEADERS`](#trust_proxy_headers) | `docker-compose.yml`, `internal/web/ratelimit.go` | No | `false` | `true`/`false` (any `strconv.ParseBool` spelling) | Whether to trust `CF-Connecting-IP` / `X-Forwarded-For` for the visitor's real address | Change only if you understand the consequences |
| [`TRUSTED_PROXY_CIDRS`](#trusted_proxy_cidrs) | `docker-compose.yml`, `internal/web/clientip.go` | Only if `TRUST_PROXY_HEADERS=true` | `172.30.99.10/32` (Compose default only) | Comma-separated CIDR list | Which peers' proxy headers are trusted | Change only if you understand the consequences |
| [`RATE_LIMIT_RPS`](#rate_limit_rps) | `docker-compose.yml`, `internal/web/ratelimit.go` | No | `5` | Float; `0` or negative disables limiting | Per-client request budget for the webserver | Safe to change |
| [`RATE_LIMIT_BURST`](#rate_limit_burst) | `docker-compose.yml`, `internal/web/ratelimit.go` | No | `20` | Float | How many requests a client may burst above the steady rate | Safe to change |
| [`PAGE_SIZE`](#page_size) | `docker-compose.yml`, `Dockerfile.webserver`, `cmd/webserver/main.go` | No | `36` | Positive integer | Assets shown per page of results | Safe to change |
| [`PORT`](#port) | `docker-compose.yml`, `cmd/webserver/main.go`, `cmd/dataservice/main.go` | No | `8080` | Numeric TCP port | Which port the binary listens on inside its container | Change only if you understand the consequences |
| [`LOG_EXPORT_DIR`](#log_export_dir) | `.env.example` (comment only), `scripts/export-logs.sh` | No | `/mnt/user/appdata/itchgrep/logs` | Absolute host path | Where the host-side log follower writes `access.jsonl`, `security.jsonl`, `app.log`, and `crawler.log` | Safe to change |
| [`CONTAINER_NAME`](#container_name) | `scripts/export-logs.sh`, `scripts/update.sh` | No | `itchgrep-webserver` | Docker container name | Which container the script targets | Change only if you understand the consequences |
| [`DATASERVICE_CONTAINER_NAME`](#dataservice_container_name) | `scripts/export-logs.sh` | No | `itchgrep-dataservice` | Docker container name | Which container the dataservice-side log follower targets | Change only if you understand the consequences |
| [`RECONNECT_DELAY_SECONDS`](#reconnect_delay_seconds) | `scripts/export-logs.sh` | No | `5` | Positive integer (seconds) | Delay between reconnect attempts when the log follower loses the container | Safe to change |
| [`BASE_PATH`](#base_path) | `scripts/bootstrap-host.sh` | No | `/mnt/user/appdata/itchgrep` | Absolute host path | Base directory under which `src/`, `logs/`, `data/` are created | Change only if you understand the consequences |

Two host-facing knobs are single numbers hardcoded inside their scripts, not
environment variables — you can't override them without editing the script:
`update.sh`'s health-check wait (`HEALTH_TIMEOUT_SECONDS=60`, fixed at 60
seconds) and `export-logs.sh`'s give-up threshold
(`MAX_CONSECUTIVE_FAILURES=60`, fixed at 60 consecutive failed reconnect
attempts — about 5 minutes at the default `RECONNECT_DELAY_SECONDS`).

## Crawl behavior (dataservice)

### `CRAWL_INTERVAL`

Read by **both** binaries — `internal/config/config.go`'s `CrawlInterval()`
— which is deliberate: the dataservice uses it to decide when to start the
next crawl, and the webserver uses the same value to tell visitors when the
next rebuild is due. A single shared reader means the two can't drift onto
different schedules.

- **Format**: a Go duration string — a number followed by a unit, such as
  `24h`, `30m`, or `168h`. Not a secret.
- **Obtaining a value**: pick any duration; there's nothing to look up.
- **Example**: `CRAWL_INTERVAL=72h`
- **If missing**: uses the default, `168h` (one week).
- **If set but invalid** (doesn't parse, or is negative): the value is
  ignored, a warning is logged, and the default is used. A typo here is
  deliberately *not* read as "never crawl again" — only the literal value
  `0` disables scheduled rebuilds (the index then only changes when
  something calls `/trigger-fetch`).

### `SCRAPE_RPS`

Read only by the dataservice, in `internal/fetcher/fetcher.go`'s
`initRateLimiter()`. Paces every outbound request to itch.io, including
retries.

- **Format**: a positive integer, requests per second.
- **Obtaining a value**: the shipped default (`2`) is deliberately polite;
  see [docs/troubleshooting.md](troubleshooting.md) before raising it if
  you're seeing `429` responses — the likely cause is a missing cookie jar,
  not an insufficient rate.
- **Example**: `SCRAPE_RPS=3`
- **If missing**: uses the default, `2`.
- **If set but invalid** (non-numeric, zero, or negative): silently ignored,
  **no warning is logged** — this is one of the few variables in this table
  that doesn't warn on a bad value, because it's read with a bare
  `strconv.Atoi` rather than the shared `internal/config` helpers the other
  crawl-tuning variables use. The default of `2` is used regardless.
- **If set above `1000`**: clamped to `1000` (`maxScrapeRPS` in the code)
  with a logged warning, so an absurd value can't collapse the request
  spacing to zero and disable pacing entirely.

### `SCRAPE_MAX_PAGES`

Read only by the dataservice, in `cmd/dataservice/main.go`'s
`loadCrawlConfig()`. Caps the total number of pages fetched across every
crawl view, producing a deliberately partial index — this is the mechanism
behind the local "smoke crawl" described in
[docs/getting-started.md](getting-started.md).

- **Format**: a positive integer (page count).
- **Obtaining a value**: `40` finishes in a couple of minutes against a
  fresh, empty catalog view; the `task smoke` recipe in `Taskfile.dist.yml`
  uses this value.
- **Example**: `SCRAPE_MAX_PAGES=40`
- **If missing**: unset, meaning no cap — a full crawl (roughly six hours).
- **If set but invalid** (non-numeric, zero, or negative): ignored with a
  logged warning; behaves as if unset.
- **Side effects when set to any valid value**: `COVERAGE_FLOOR` is forced to
  `0` for that run (a deliberately partial crawl must not be blocked by the
  publish floor), and — only if `MAX_TAGS` itself is not also set —
  `MAX_TAGS` is shrunk to `25` for that run, so tag discovery doesn't spend
  ~17 minutes walking the full tag universe before a "couple of minutes"
  smoke test even starts fetching pages. **Never set this in production**;
  it produces an index covering roughly 1% of the catalog.

### `COVERAGE_TARGET`

Read only by the dataservice, via `internal/config/config.go`'s
`EnvNonNegativeFloat`. An upper bound the crawl stops at once reached.

- **Format**: a decimal fraction between `0` and `1`.
- **Example**: `COVERAGE_TARGET=0.9`
- **If missing**: uses the default, `0.95`.
- **If set but invalid** (doesn't parse, or negative): ignored with a logged
  warning; default used.
- **In practice**: a full crawl exhausts every reachable view at roughly
  89% coverage, well under this default, so it essentially never triggers.

### `COVERAGE_FLOOR`

Read only by the dataservice, via the same `EnvNonNegativeFloat` helper.
Publishing safeguard: a crawl that ends below this fraction leaves the
previously published index in place rather than replacing it with something
worse.

- **Format**: a decimal fraction between `0` and `1`.
- **Example**: `COVERAGE_FLOOR=0.7`
- **If missing**: uses the default, `0.75`.
- **If set but invalid**: ignored with a logged warning; default used.
- **The constraint that matters**: keep this **below** what a healthy full
  crawl actually reaches (measured at roughly 89% coverage), or every
  successful run gets rejected as "below floor" and the index never updates
  again. See [docs/troubleshooting.md](troubleshooting.md) for the log line
  this produces when it trips.

### `SLICE_MIN_YIELD`

Read only by the dataservice, via `EnvNonNegativeFloat`. Abandons one
tag-filtered crawl view once it stops yielding assets not already seen from
other views.

- **Format**: a decimal fraction, measured as new assets per page.
- **Example**: `SLICE_MIN_YIELD=0.1`
- **If missing**: uses the default, `0.05` (under roughly 2 new assets per
  36-item page).
- **If set but invalid**: ignored with a logged warning; default used.
- **Trade-off**: raising it ends views sooner (a faster, less complete
  crawl); lowering it walks more overlap for a small coverage gain.

### `TAG_CACHE_MAX_AGE`

Read only by the dataservice, via `internal/config/config.go`'s
`EnvDuration`. Tag discovery costs roughly one itch.io request per tag, so
the discovered vocabulary is cached and only refreshed once it's this old.

- **Format**: a Go duration string.
- **Example**: `TAG_CACHE_MAX_AGE=336h`
- **If missing**: uses the default, `168h` (one week).
- **If set but invalid** (doesn't parse, zero, or negative): ignored with a
  logged warning; default used. Unlike `CRAWL_INTERVAL`, zero has no special
  "disabled" meaning here — it's rejected like any other bad value.

### `CHECKPOINT_MAX_AGE`

Read only by the dataservice, via `EnvDuration`. A crawl checkpoints itself
periodically so a killed run can resume instead of restarting from zero;
this bounds how old a checkpoint may be before it's ignored and the crawl
starts fresh instead.

- **Format**: a Go duration string.
- **Example**: `CHECKPOINT_MAX_AGE=12h`
- **If missing**: uses the default, `24h`.
- **If set but invalid**: ignored with a logged warning; default used.

### `MAX_TAGS`

Read only by the dataservice, via `internal/config/config.go`'s `EnvInt`,
falling back to `internal/fetcher/tags.go`'s `DefaultMaxTags` constant.
Upper bound on how many tags discovery will visit.

- **Format**: a positive integer.
- **Example**: `MAX_TAGS=1500`
- **If missing**: uses the default, `1000`. In practice, discovery converges
  at roughly 130 asset tags on its own, well under either bound.
- **If set but invalid** (non-numeric, zero, or negative): ignored with a
  logged warning; default used.
- **The gotcha**: **`docker-compose.yml` never lists `MAX_TAGS` in the
  dataservice's `environment:` block**, so under `docker compose up`, setting
  `MAX_TAGS` in `.env` has no effect whatsoever — verified by rendering the
  Compose config with a test override present and confirming the variable
  never reaches the container. `docker-compose.yml`'s own comment on this
  already says so directly: there is no pass-through line, so exporting
  `MAX_TAGS` in the shell or `.env` has no effect on a compose run, and a
  production override requires adding a literal `MAX_TAGS: "<value>"` line
  to that block for the run, then removing it afterward. `.env.example`
  matches this: it ships `MAX_TAGS` commented out, with a note that it only
  applies to a direct run (`go run ./cmd/dataservice`, `task dataservice`,
  `task smoke`), never to `docker compose up`.

## Storage and networking (both services, Compose)

### `DATA_DIR`

Read by both binaries, in `internal/storage/storage.go`'s `Dir()`. The single
directory both processes treat as the entire storage layer:
`assets.json`, `firstseen.json`, `tags.json`, `checkpoint.json`, `rates.json`,
`stats.json`, `traffic.json`, `history.json`, and the `index.bleve/` search index.

- **Format**: a filesystem path, relative or absolute.
- **Not a secret.**
- **Example** (local dev only): `DATA_DIR=./data`
- **If missing**: defaults to `data`, relative to wherever the process was
  started — convenient for a bare `go run`, since it keeps data in the
  working directory.
- **Under Docker Compose**: both services hardcode `DATA_DIR=/data` directly
  in `docker-compose.yml` — it is **not** wired to a `${DATA_DIR}`
  placeholder, so it cannot be overridden through `.env` at all for the
  containerized deployment. What *is* configurable is which host location
  `/data` maps to — see `DATA_PATH` below. Do not add a `DATA_DIR`
  environment override to `docker-compose.yml` unless you also change the
  volume's mount target to match; the two must agree, or the container reads
  and writes to a directory the volume never touches.

### `DATA_PATH`

A Compose-only variable — substituted into the top-level `volumes:` mapping
in `docker-compose.yml` (`${DATA_PATH:-itchgrep-data}`). No Go code reads it;
it decides what backs the container's `/data` mount.

- **Format**: either an absolute host path (a **bind mount** — the container
  reads and writes a real folder on the host filesystem) or left unset (a
  **named volume** — Docker manages the storage location itself, under
  `itchgrep-data`).
- **Not a secret.**
- **Example**: `DATA_PATH=/mnt/user/appdata/itchgrep/data`
- **If missing**: uses the named Docker volume `itchgrep-data`.
- **If set to a path that doesn't exist**: Docker creates it automatically
  on first `up`, owned by whichever user Docker runs as on the host — **not**
  the uid the container needs. See the next paragraph.
- **The consequence that matters**: both `webserver` and `dataservice` run
  as uid `65532` (a non-root user baked into their distroless base image —
  see `Dockerfile.webserver` and `Dockerfile.dataservice`). A **fresh named**
  volume works with no extra step: both images ship an empty `/data`
  directory owned by `65532:65532` (a `COPY --chown` in each Dockerfile), and
  Docker seeds a brand-new named volume's ownership from the image's own
  mount point on first use. A **named volume created by an older image
  build** (before that `COPY --chown` existed) is still root-owned, and
  upgrading onto this image alone doesn't fix it — see
  [docs/troubleshooting.md](troubleshooting.md) for the fix. A
  **bind-mounted** `DATA_PATH` is never `chown`'d automatically by Docker
  either way, so the first time you point it at a host directory you must
  run, once (`scripts/bootstrap-host.sh` does this automatically when run as
  root — see [docs/deployment.md](deployment.md)):

  ```
  chown -R 65532:65532 <DATA_PATH>
  ```

  Skipping this is not a degraded mode — both services fail to write, and
  the webserver can never open the search index at all. See
  [docs/troubleshooting.md](troubleshooting.md).

### `COMPOSE_PROJECT_NAME`

A Docker Compose built-in, not something any file in this repository
declares a default for — Compose falls back to the basename of the directory
`docker-compose.yml` lives in when this is unset.

- **Format**: a short identifier — letters, digits, underscores, hyphens; no
  spaces.
- **Not a secret.**
- **Example**: `COMPOSE_PROJECT_NAME=itchgrep`
- **If missing**: Compose derives it from the checkout directory's name. On
  the production host the checkout lives at
  `/mnt/user/appdata/itchgrep/src`, so an unset value there produces the
  project name `src`, **not** `itchgrep`.
- **Why it must be pinned in production**: both `container_name:` values in
  `docker-compose.yml` (`itchgrep-webserver`, `itchgrep-dataservice`) are
  fixed regardless of project name, but the project name still governs the
  network, the named volume, and the `cloudflared` container's name
  (`<project>-cloudflared-1`, the one service with no pinned name). Running
  `docker compose up` from `src/` without `COMPOSE_PROJECT_NAME` set creates
  a second, independent stack that fails to start — usually at network
  creation, because the pinned `172.30.99.0/24` subnet overlaps the first
  stack's, otherwise on the container-name conflict or the already-bound host
  ports. This happened in production; see
  [docs/troubleshooting.md](troubleshooting.md) for the fix and
  [docs/deployment.md](deployment.md) for why the production `.env` pins it.

### `CLOUDFLARE_TUNNEL_TOKEN`

Read only by the `cloudflared` service in `docker-compose.yml`
(`TUNNEL_TOKEN: "${CLOUDFLARE_TUNNEL_TOKEN:-}"`), which only exists at all
when the stack is brought up with `--profile public`.

- **Format**: an opaque token string issued by Cloudflare — long, base64-like
  text.
- **This is a secret.** It lives only in the production `.env` file, which
  is excluded from Git. Never print it, paste it into an issue, or commit it.
- **Obtaining a value**: creating a new tunnel from scratch is covered in
  [docs/infrastructure.md](infrastructure.md#creating-the-tunnel); rotating
  the token on an existing tunnel is covered in
  [docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token)
  (the authoritative page for the current dashboard path, since it does get
  reorganized over time). This entry only documents the variable itself.
- **Example** (obviously not real): `CLOUDFLARE_TUNNEL_TOKEN=example-fake-token-do-not-use`
- **If missing**: defaults to an empty string. Deliberately defaulted rather
  than required, so a plain local `docker compose up` (no `--profile
  public`) never needs a Cloudflare account. With the `public` profile
  active and this unset, `cloudflared` starts and exits immediately, saying
  so in its own logs.
- **If wrong** (expired, revoked, or mistyped): `cloudflared` fails to
  authenticate and keeps retrying; the tunnel never comes up, so the public
  site is unreachable while the loopback port keeps working normally. See
  [docs/troubleshooting.md](troubleshooting.md).

### `TRUST_PROXY_HEADERS`

Read only by the webserver, via `internal/web/ratelimit.go`'s
`TrustProxyHeaders()` (a `config.EnvBool` call). Governs whether the
per-client rate limiter and the access log trust the `CF-Connecting-IP` /
`X-Forwarded-For` request headers as the visitor's real address, instead of
the raw TCP connection address.

- **Format**: any string `strconv.ParseBool` accepts — `true`, `false`,
  `1`, `0`, `t`, `f`, `T`, `F`, `TRUE`, `FALSE`, `True`, `False`.
- **Not a secret.**
- **Example**: `TRUST_PROXY_HEADERS=true`
- **If missing**: defaults to `false` — the raw connection address is used.
  This is the only safe default for a stack that might also be reached
  directly, with no Cloudflare in front of it at all (a plain local `docker
  compose up`).
- **If set but invalid** (not a recognized boolean spelling): ignored with a
  logged warning; default (`false`) used.
- **Why the default matters**: those headers are attacker-controlled unless
  something upstream (Cloudflare) overwrites them first. Turning this on
  without a trusted proxy in front lets any client mint a fresh identity per
  request and bypass the rate limiter entirely. Turn it on only when
  `--profile public` (the Cloudflare tunnel) is in use, and always alongside
  `TRUSTED_PROXY_CIDRS` below.

### `TRUSTED_PROXY_CIDRS`

Read only by the webserver, in `internal/web/clientip.go`. Restricts which
peers' `TRUST_PROXY_HEADERS` trust actually applies to — a request must
arrive from **within** one of these networks (checked against the real
socket address, which a header can never spoof) before its proxy headers are
believed at all.

- **Format**: one or more CIDR blocks (a network address plus a prefix
  length, such as `172.30.99.0/24`), comma-separated.
- **Not a secret.**
- **Example**: `TRUSTED_PROXY_CIDRS=172.30.99.0/24`
- **If missing**: `docker-compose.yml` supplies `172.30.99.10/32` —
  `cloudflared`'s own fixed address on the Compose network (see its
  `networks:` entry in `docker-compose.yml`), not the whole subnet. A single
  address rather than the whole `172.30.99.0/24` matters because the
  webserver's host port, although bound to loopback, can still be reached by
  other host processes: Docker's userland proxy can
  deliver locally originated traffic to the container with the bridge
  gateway (which sits inside that /24) as the peer address, so trusting the
  whole subnet would let anything that can reach the published port at all
  impersonate a Cloudflare hop — a single `/32` only ever matches the one
  peer that is actually `cloudflared`. Running the webserver binary directly
  (outside Compose) with this unset means **no network is trusted at all**,
  even if `TRUST_PROXY_HEADERS=true` — the code has no built-in fallback of
  its own, only Compose supplies one.
- **If set but every entry is malformed**: each bad entry is skipped and
  logged individually; if nothing valid remains and
  `TRUST_PROXY_HEADERS=true`, a warning names the situation explicitly ("proxy
  headers will not be trusted from any peer").

### `RATE_LIMIT_RPS`

Read only by the webserver, in `internal/web/ratelimit.go`'s `NewLimiter()`
(a `config.EnvFloat` call). Per-client steady-state request budget — search
is not a cheap request (multiple fuzzy passes over the whole index), and
nothing else in the request path pushes back on a client sending many at
once.

- **Format**: a number (integer or decimal), requests per second.
- **Example**: `RATE_LIMIT_RPS=10`
- **If missing**: uses the default, `5`.
- **If set but invalid** (doesn't parse as a float): ignored with a logged
  warning; default used.
- **Special value**: `0` or any negative number **disables rate limiting
  entirely** — logged explicitly at startup so it's visible rather than
  silent.

### `RATE_LIMIT_BURST`

Read only by the webserver, alongside `RATE_LIMIT_RPS`. How many requests a
single client may send in a burst before the steady per-second rate applies.

- **Format**: a number.
- **Example**: `RATE_LIMIT_BURST=40`
- **If missing**: uses the default, `20`.
- **If set but invalid**: ignored with a logged warning; default used.

### `PAGE_SIZE`

Read only by the webserver, in `cmd/webserver/main.go`'s
`initializeCache()`. How many assets are shown per page of search results.

- **Format**: a positive integer.
- **Example**: `PAGE_SIZE=48`
- **If missing or invalid** (doesn't parse as an integer): logged as an
  error naming the bad value, and `36` is used. Note this variable is parsed
  with its own inline `strconv.ParseInt` call rather than the shared
  `internal/config` helpers the crawl-tuning variables use, so the exact log
  wording differs, though the fallback behavior is the same in spirit.
- **Worth knowing before you change it**: it's fixed for the life of a
  running server. Changing it changes what "page 2" of a previously shared
  search URL means, so treat it as a deploy-time setting, not something to
  flip casually on a live site.

### `PORT`

Read by both binaries — `cmd/webserver/main.go`'s `resolvePort()` and the
equivalent inline logic in `cmd/dataservice/main.go` — as the TCP port each
listens on **inside its own container or process**. Not the port a browser
or `curl` uses from outside a container; see `docker-compose.yml`'s `ports:`
mappings for that (`127.0.0.1:62685:8080` for the dataservice,
`127.0.0.1:62686:8080` for the webserver, both mapping to the same internal `8080`).

- **Format**: a numeric TCP port, `1`–`65535`.
- **Example**: `PORT=8081`
- **If missing**: defaults to `8080` for both binaries.
- **If set to something that isn't a usable port** (non-numeric, or a port
  already in use): the process fails to bind and exits — logged as `Server
  failed: ...` (webserver) or `Server failed to start: ...` (dataservice).
  See [docs/troubleshooting.md](troubleshooting.md) for what that looks like
  from `docker ps`.
- **Under Docker Compose**: both services hardcode this to `8080` inside the
  container; changing it there also requires updating the matching `ports:`
  mapping in `docker-compose.yml` (and, for the webserver, the tunnel's
  `http://webserver:8080` route). The `healthcheck:` needs no change:
  `/server -healthcheck` reads the same `PORT` through `resolvePort()`.
  When running two binaries directly on the same machine (see
  [docs/getting-started.md](getting-started.md)), they need **different**
  values — `Taskfile.dist.yml` sets `8080` for `dataservice` and `8081` for
  `webserver` for exactly this reason.

## Host log-export scripts

These three variables are read by shell scripts that run directly on the
Unraid host, outside Docker Compose. See
[docs/deployment.md](deployment.md) for when and how they run, and
[docs/observability.md](observability.md) for what the files they produce
contain.

### `LOG_EXPORT_DIR`

Read by `scripts/export-logs.sh`. Where the follower writes `access.jsonl`,
`security.jsonl`, and `app.log` (webserver) and `crawler.log` (dataservice).

- **Format**: an absolute host filesystem path.
- **Not a secret.**
- **Example**: `LOG_EXPORT_DIR=/mnt/user/appdata/itchgrep/logs`
- **If missing**: defaults to `/mnt/user/appdata/itchgrep/logs`, which is
  also the default `BASE_PATH`-relative `logs/` directory
  `scripts/bootstrap-host.sh` creates.
- **If set to a path that doesn't exist**: the script creates it (`mkdir
  -p`) on startup.
- **Changing it on an existing deployment**: also update the matching path
  in `scripts/itchgrep-logrotate.conf` (or let `export-logs.sh` reinstall it
  automatically — it rewrites that file's path on every run when
  `LOG_EXPORT_DIR` differs from the checked-in default), and update the
  Unraid User Scripts entry if it references the path directly.

### `CONTAINER_NAME`

Read independently by **two** scripts with the same default:
`scripts/export-logs.sh` (the webserver container to follow the logs of —
see `DATASERVICE_CONTAINER_NAME` below for the dataservice side of that same
script) and `scripts/update.sh` (which container to poll for a healthy
status after an update). They are not linked to each other — setting one
does not set the other.

- **Format**: a Docker container name.
- **Not a secret.**
- **Example**: `CONTAINER_NAME=itchgrep-webserver`
- **If missing**: defaults to `itchgrep-webserver`, matching the
  `container_name:` pinned in `docker-compose.yml`.
- **If wrong** (names a container that doesn't exist, or isn't running):
  `export-logs.sh` logs repeated "cannot inspect" retries and eventually
  exits non-zero after about 5 minutes; `update.sh` waits out its 60-second
  health-check timeout and then prints rollback guidance, even if the actual
  deploy succeeded under a different container name.
- **When to change it**: only for a second, differently named stack on the
  same host (a staging copy) — not for normal operation.

### `DATASERVICE_CONTAINER_NAME`

Read only by `scripts/export-logs.sh`, the dataservice-side counterpart to
`CONTAINER_NAME` above — the follower now runs two independent reconnect
loops, one per container, so each needs its own name.

- **Format**: a Docker container name.
- **Not a secret.**
- **Example**: `DATASERVICE_CONTAINER_NAME=itchgrep-dataservice`
- **If missing**: defaults to `itchgrep-dataservice`, matching the
  `container_name:` pinned in `docker-compose.yml`.
- **If wrong**: the same "cannot inspect" retry-then-give-up behavior as
  `CONTAINER_NAME` above, but scoped to the dataservice follower only — the
  script treats either follower giving up as a reason to stop both and exit
  non-zero (see `scripts/export-logs.sh`'s own comment on `main`), so a wrong
  value here still takes down webserver log capture too, not just the
  dataservice's.
- **When to change it**: only for a second, differently named stack on the
  same host — not for normal operation.

### `RECONNECT_DELAY_SECONDS`

Read only by `scripts/export-logs.sh`. Delay between attempts to reattach to
the container's log stream after a disconnect (a restart, an image rebuild,
a Docker daemon reload).

- **Format**: a positive integer, seconds.
- **Example**: `RECONNECT_DELAY_SECONDS=10`
- **If missing**: defaults to `5`.
- **If set but not a valid positive integer**: the script's `sleep` call
  fails on that value at runtime — treat this as a script parameter to set
  correctly rather than one with graceful fallback behavior.

## Host bootstrap script

### `BASE_PATH`

Read only by `scripts/bootstrap-host.sh`. The parent directory under which
`src/` (the checkout), `logs/` (`LOG_EXPORT_DIR`'s default), and `data/`
(`DATA_PATH`'s typical value) are created.

- **Format**: an absolute host filesystem path.
- **Not a secret.**
- **Example**: `BASE_PATH=/mnt/user/appdata/itchgrep`
- **If missing**: defaults to `/mnt/user/appdata/itchgrep`, matching every
  other default in this table that assumes the standard Unraid layout.
- **If changed from the default**: every other path-based default in this
  document (`DATA_PATH`, `LOG_EXPORT_DIR`, `CONTAINER_NAME`'s implicit
  assumptions) is written relative to `/mnt/user/appdata/itchgrep` and does
  **not** follow a changed `BASE_PATH` automatically — you'd need to set
  each of them explicitly to match. This script is normally run once, at
  first setup; see [docs/deployment.md](deployment.md).

## A variable you might find in old notes

If you find `INDEXER_TIMEOUT_SECONDS` in old notes or shell history, it's
dead: no Go source, script, or `docker-compose.yml` reads it. It belonged to
a one-shot "indexer" bootstrap sidecar container that has since been removed
(`docker-compose.yml`'s own header comment describes the removal);
`scheduleCrawls` in `cmd/dataservice/main.go` now starts the first crawl on
its own, with no sidecar or timeout involved.
