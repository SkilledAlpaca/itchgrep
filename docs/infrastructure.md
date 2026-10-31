# Infrastructure

itchgrep depends on several things that live outside this repository: a physical host, Docker, Cloudflare, GitHub, and itch.io itself. This document describes each piece, what it's for, and what breaks if it disappears. It assumes no prior familiarity with Docker, DNS, or Cloudflare.

For the application's own configuration (environment variables), see [docs/configuration.md](configuration.md). For step-by-step deployment procedures, see [docs/deployment.md](deployment.md). For the CI pipeline, see [docs/ci-cd.md](ci-cd.md).

## The Unraid host

Production runs on an Unraid server. The operator reports this is owner-operated hardware, not a cloud VM or a managed host — there is no provider dashboard or SLA behind it; if the machine fails, someone has to physically repair or replace it.

### Directory layout

Three directories under `/mnt/user/appdata/itchgrep/` hold everything the deployment needs:

| Directory | Purpose |
|---|---|
| `src/` | The git checkout of this repository — what `docker compose` is run from, and what `scripts/update.sh` pulls into. |
| `logs/` | `LOG_EXPORT_DIR` — where `scripts/export-logs.sh` writes `access.jsonl`, `security.jsonl`, `app.log`, and `crawler.log`. See [docs/observability.md](observability.md). |
| `data/` | `DATA_PATH` — the crawl database, bind-mounted into both containers at `/data`. See [docs/disaster-recovery.md](disaster-recovery.md) for what's in it. |

`scripts/bootstrap-host.sh` creates all three (`mkdir -p`, idempotent — safe to re-run) on first-time setup, or after restoring an appdata backup. When run as root (the normal case on Unraid), it also `chown`s `data/` to `65532:65532`, the uid both container images run as; run as any other user, it skips that step and prints the command to run by hand.

### The host has no `git` binary

Unraid does not ship `git`. `scripts/update.sh` (the production update runbook) and any manual clone both work around this by running the `alpine/git` Docker image instead of a native command. The command below uses the bare image name for readability; `scripts/update.sh` pins the image by tag and digest in its `GIT_IMAGE` line.

```
docker run --rm -v "$(pwd)":/git alpine/git -c safe.directory=/git pull https://github.com/SkilledAlpaca/itchgrep.git main
```

`-c safe.directory=/git` is required because git refuses to operate on a repository it doesn't itself own by default, and the checkout's on-disk owner (whatever uid created it) rarely matches the uid git runs as inside this throwaway container.

**What breaks if this is missed:** nothing automatically — but running a bare `git pull` on the host will fail with "git: command not found," which can look like a broken deployment rather than a missing tool.

### `/etc` is a ramdisk, wiped every boot

Unraid's root filesystem — including `/etc` — is a tmpfs ramdisk rebuilt from scratch on every array start. Anything dropped into `/etc` by hand (for example, a `logrotate` config) silently disappears at the next boot. `scripts/export-logs.sh` works around this for its own `logrotate` config by reinstalling `/etc/logrotate.d/itchgrep` from `scripts/itchgrep-logrotate.conf` on every single run — see [docs/observability.md](observability.md) for the detail.

**What breaks if you forget this quirk:** any manual system-level config change made directly under `/etc` (not just the logrotate one) is lost on the next reboot with no warning, unless the thing that depends on it also reinstalls it the way `export-logs.sh` does.

### The log follower's User Scripts entry

`scripts/export-logs.sh` is not run by Docker Compose — it runs continuously on the host as an Unraid **User Scripts** plugin entry named `itchgrep-log-export`, configured **"At Startup of Array,"** background-only. See [docs/deployment.md](deployment.md) for the install steps.

**What breaks if this entry is missing or stops running:** request, security, webserver application, and crawler logs stop being captured into durable, rotated files. The application itself is unaffected — logs still accumulate inside Docker's own capped 30 MB `json-file` buffer per container (see [docs/observability.md](observability.md)) — but you lose the ability to investigate anything older than that buffer, and the 30-day retention guarantee silently stops applying to nothing (there's nothing left to rotate).

**What breaks if the whole host disappears:** total outage of everything — the site, the crawl, the logs. See [docs/disaster-recovery.md](disaster-recovery.md#full-host-loss-recovery) for the rebuild procedure.

## Docker and Compose

### Project name is pinned

The operator reports `COMPOSE_PROJECT_NAME=itchgrep` is set explicitly in the production `.env` file. Docker Compose otherwise derives the project name from the checkout directory's own name — and every container, network, and named-volume name it creates is derived from that project name. Pinning it means those names stay stable regardless of what the checkout directory is actually called.

**What breaks if it isn't pinned (or is changed):** `scripts/export-logs.sh`'s two container-name defaults (`itchgrep-webserver` and `itchgrep-dataservice`) still work regardless, because both are separately pinned by `container_name:` in `docker-compose.yml` — the project name doesn't govern either. What does still shift with the project name is the named Docker volume (when `DATA_PATH` is unset): an unpinned or changed project name means Compose creates (or looks for) a *different* named data volume than the one already holding the crawl database — effectively starting from an empty `/data` on the next `docker compose up`, invisibly, with no error. A bind-mounted `DATA_PATH` (production's setup) isn't affected by this, since it names an absolute host path rather than a project-derived volume name.

### Two services, one optional third

`dataservice` (crawls itch.io, builds the index) and `webserver` (serves the site) always run. `cloudflared` is opt-in, behind a Compose profile:

```
docker compose --profile public up -d
```

A local `docker compose up` (no `--profile public`) never starts `cloudflared` and needs no Cloudflare account at all — the webserver is still reachable directly at `http://localhost:62686`.

### The pinned network subnet

`docker-compose.yml`'s `networks:` block pins the Compose network to a specific subnet rather than letting Docker allocate one automatically:

```yaml
networks:
  default:
    ipam:
      config:
        - subnet: 172.30.99.0/24
```

This exists for one specific reason: `cloudflared` is pinned to `172.30.99.10` on this network (its `networks: default: ipv4_address:` entry), and the webserver's `TRUSTED_PROXY_CIDRS` (see [docs/configuration.md](configuration.md#trusted_proxy_cidrs)) defaults to exactly that one address, `172.30.99.10/32` — not the whole subnet, because traffic through Docker's userland proxy can arrive from the bridge gateway, which sits inside the `/24`. Without a *pinned* subnet, Docker's automatic address assignment can shift across a daemon restart or a second stack on the same host, so that address could stop belonging to `cloudflared`: real visitor IPs stop being honored, or the wrong peer gets trusted.

**What breaks if the subnet changes without updating `TRUSTED_PROXY_CIDRS` to match:** the webserver stops honoring the proxy headers Cloudflare sends, so every visitor is logged and rate-limited as if they were the single IP of the Cloudflare tunnel connection itself — one visitor's abusive traffic can rate-limit every other visitor.

### `restart: unless-stopped`

Every long-running service carries this restart policy, so Docker brings each back up after a crash or a full host reboot — without it, a reboot would leave `cloudflared` in particular reconnecting its tunnel to an origin that never came back up, serving public `502`s while the Cloudflare tunnel dashboard reports the tunnel itself as healthy (because the tunnel connection *is* fine — only the origin behind it is down).

## Cloudflare

### What a Cloudflare Tunnel is

`cloudflared`, running as a container in this stack, dials **outbound** to Cloudflare and keeps that connection open. Cloudflare then proxies public requests for `itchgrep.com` down that same outbound connection to the webserver. No inbound port is ever opened on the host, no port forwarding is configured on the router, and the origin's real network address never appears anywhere in DNS — it cannot be reached at all except through Cloudflare. This is a stronger property than a firewall rule: there is no port to scan or forget to close.

The tunnel routes the public hostname at `http://webserver:8080` — the Compose service name, resolved on the Compose network, not `localhost`.

**The dataservice must never be given a public hostname.** Its `/trigger-fetch` endpoint takes no authentication and starts a multi-hour crawl on a bare `POST` — see [docs/configuration.md](configuration.md) and `cmd/dataservice/main.go`'s `handleFetchTrigger`. A public route to it, on any port, would hand any visitor who finds it a button to fire at will.

### Creating the tunnel

This is a one-time setup step, needed before the first `--profile public`
deploy (see [docs/deployment.md](deployment.md)) and again if the tunnel
itself is ever recreated from scratch (rather than just re-tokened — see
[docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token)).

1. In the Cloudflare dashboard for the account that owns `itchgrep.com`, go
   to **Networking** → **Tunnels** (moved out of the separate Zero Trust
   dashboard in February 2026), and create a new tunnel. **Dashboard navigation labels change over time** — Cloudflare
   has already reorganized tunnel management once (see
   [docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token)
   for the dated note on that move); if this path doesn't match what you
   see, look for "Tunnels" under whatever networking section the current
   dashboard shows, and correct this document once you've found it.
2. Copy the token the dashboard shows you into the production `.env` file as
   `CLOUDFLARE_TUNNEL_TOKEN` — see
   [docs/configuration.md](configuration.md#cloudflare_tunnel_token).
3. Add a public hostname route: `itchgrep.com` → `http://webserver:8080`.
   `webserver` is the Compose service name, not `localhost` — `cloudflared`
   resolves it on the Compose network, the same way `docker-compose.yml`'s
   own header comment for the `cloudflared` service describes.
4. Bring the stack up with `--profile public` (see
   [docs/deployment.md](deployment.md)) and confirm the tunnel shows
   connected in the dashboard.

**This project's existing production tunnel's exact configuration** (when it
was created, its exact route list beyond the one described above, any
additional settings) is `UNKNOWN — REQUIRES HUMAN CONFIRMATION` — see
[docs/open-questions.md](open-questions.md#4-full-cloudflare-account-inventory)
for the standing entry covering the full Cloudflare account inventory; this
section isn't a second copy of that question, just the creation steps for
setting up a tunnel from nothing.

### WAF rules

Checked against the live zone on 2026-10-31. The rules were created after an incident in August 2026, when Googlebot (from the `66.249.64.0/19` range) and OpenAI's `OAI-SearchBot` exhaustively crawled every filter-permutation query URL the site's UI can generate — an effectively unbounded URL space, since every tag/price/sort combination is its own URL. The code-level fix for the same incident is `robots.txt`'s `Disallow: /*?*` (`internal/web/robots.go`); both bots respect it, so the WAF rules are a backstop for crawlers and scanners that don't.

Custom rules, evaluated in this order:

1. **Block** CMS and PHP probe paths (`.php`, `.phtml`, `.aspx`, `.jsp`, `/wp-`, `/wordpress`, `/cgi-bin/`, `/vendor/`, `/phpmyadmin`, `/xmlrpc`, `/.env`, `/.git`), two scraper user agents on URLs with a query string (`meta-webindexer`, `Lightpanda`), and one seedbox ASN. The rule excludes `cf.client.bot` where that matters.
2. **Managed challenge** on the path `/` only, for a query longer than 100 characters, a `not` parameter with three or more comma-separated values (raw or `%2C`), or traffic from a few hosting and proxy ASNs.
3. **Managed challenge** on the path `/` with a non-empty query, for stale browser user agents (Chrome older than 140, Firefox 120 and 121, and a truncated `Safari/537.3` user agent).

There is also one rate-limit rule: the path `/`, 20 requests per 10 seconds per IP, blocked for 10 seconds.

`/results` is deliberately excluded from the challenge rules: htmx requests it in the background, and a challenge page returned to an htmx request cannot be solved. `/static/*` is never challenged.

**What breaks if these rules are removed:** nothing breaks immediately, but the site is exposed to the same exhaustive-crawl pattern from any bot that ignores `robots.txt` — each such crawl runs the expensive fuzzy-search path on every hit (see `internal/web/robots.go`'s own comment).

### Static asset caching

The zone's Browser Cache TTL (4 hours) overrides the origin's `Cache-Control` for anything shorter, and Cloudflare's edge keeps its own copy. Serving CSS and JS at fixed URLs therefore meant a deploy could pair new HTML with the old stylesheet and script for hours.

The templates now link content-fingerprinted paths, such as `/static/app.3f2a9c1b7d04.css` (`internal/web/static/static.go`, `templates.Asset`). The hash is the first 12 hex characters of the file's SHA-256, computed at startup, so every release that changes a file names a URL no cache has seen. A request whose hash matches the current file is served with `Cache-Control: public, max-age=31536000, immutable`. The bare name (`/static/app.css`) is served with `max-age=300`. So is a well-formed but stale hash (`/static/app.<any other 12 hex characters>.css`): the handler serves the current file, never with `immutable`. This covers the window after every deploy. Cloudflare caches the HTML pages for the origin's five minutes, and browsers keep back/forward copies, so for a short while some HTML still names the previous hashes. A `404` there would give those visitors an unstyled page without scripts, which is the incident this scheme exists to prevent. Unknown files, the wrong extension, and a hash segment that is not exactly 12 lowercase hex characters return `404`.

The fingerprint is in the path on purpose, not a `?v=` query string. That keeps asset caching independent of the zone's cache level (whether it keys on the query string) and of the query-string WAF rules above. Nothing in the zone needs to be purged on deploy.

### DNS

`itchgrep.com` resolves through Cloudflare's DNS (the operator owns the domain — see the README for the project's ownership and attribution note). DNS records and the domain registration are managed by the operator outside this project's scope. The zone's TLS/proxy mode is not verifiable from this repository — see [docs/open-questions.md](open-questions.md).

**What breaks if Cloudflare (the account, or the service generally) disappears:** total outage of the public site — `itchgrep.com` stops resolving to anything reachable, and even if DNS somehow still resolved, the tunnel that makes the origin reachable at all depends on the same account. The stack itself keeps running unaffected and stays reachable directly on the host's own network (`http://<host-ip>:62686`) the entire time, since the webserver's port binding doesn't depend on Cloudflare in any way.

## GitHub

The repository is `SkilledAlpaca/itchgrep` (public), a GPL-3.0 fork of `wintermute-cell/itchgrep`. It hosts:

- The source code and commit history.
- GitHub Actions CI — see the `on:` block in `.github/workflows/ci.yml` and [docs/ci-cd.md](ci-cd.md) for when it runs.
- The issue tracker, which is the contact address both `security.txt` and the crawler's own `User-Agent` header point back to (see below, and [docs/open-questions.md](open-questions.md) for who actually monitors it).

**What breaks if GitHub access is lost:** no new deploys can be pulled — `scripts/update.sh` pulls from this exact URL, and a first deploy clones from it — and CI stops running for anyone. The already-running production stack is entirely unaffected until the next update is needed. Security reports sent to the linked issue tracker would go unseen.

## itch.io

itch.io is the upstream data source: everything the site indexes and serves comes from crawling itch.io's public browse pages. There is no API key or partnership — this is a polite, identified scraper.

### Identification

Every outbound request carries a `User-Agent` header identifying the crawler and linking back to this repository:

```
itchgrep/1.0 (+https://github.com/SkilledAlpaca/itchgrep)
```

(`internal/fetcher/fetcher.go`, constant `UserAgent`, set in `doGet`.) This points at the fork, not the original upstream project — the fork is who is actually making the requests, and the whole point of the link is to reach them.

### Rate and politeness posture

- **2 requests/second by default** (`SCRAPE_RPS`, `defaultScrapeRPS` in `internal/fetcher/fetcher.go`), paced by a shared rate limiter across every concurrent worker. This rate is fixed, not adaptive — see [docs/dependencies.md](dependencies.md) for why.
- A **fleet-wide pause**: if itch.io returns `429 Too Many Requests`, every worker (not just the one that saw the 429) is held back for the `Retry-After` duration itch.io sent.
- A **shared cookie jar**: itch.io was measured to refuse cookieless clients with a `429` about half the time regardless of request rate — the fetcher carries a persistent cookie jar specifically to avoid this.

**What breaks if itch.io blocks the crawler, or changes its page markup enough to break parsing:** crawls fail or under-collect. A coverage gate (`COVERAGE_FLOOR`, default 0.75) refuses to *publish* a run that collected too little of the catalog, leaving the previous, still-good dataset in place rather than replacing it with a worse one. The site keeps serving the last successfully published data indefinitely — there is no automatic alert for "the crawl has been failing for weeks," so this is something to watch for (see [docs/observability.md](observability.md)'s "what warrants attention" table) rather than something the system surfaces on its own.

## Summary: single points of failure

| Dependency | If it disappears | Blast radius |
|---|---|---|
| Unraid host | Total outage | Everything — see [docs/disaster-recovery.md](disaster-recovery.md) |
| Cloudflare | Public site unreachable | Public access only — LAN access to the same stack is unaffected |
| GitHub | No new deploys, CI stops, security contact unreachable | Deployment and maintenance workflow, not the running site |
| itch.io | Crawl data goes stale | Search results age; the site keeps serving what it already has |
| The `itchgrep-log-export` User Scripts entry | Logs stop being durably captured | Observability only — the site itself is unaffected |
