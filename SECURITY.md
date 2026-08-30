# Security

This document describes itchgrep's trust boundaries, where secrets live,
what's deliberately not logged, and how to report a vulnerability.

## Reporting a vulnerability

Open an issue at
[github.com/SkilledAlpaca/itchgrep/issues](https://github.com/SkilledAlpaca/itchgrep/issues).
This matches exactly what the site's own `/.well-known/security.txt`
publishes (`internal/web/robots.go`, per [RFC 9116](https://www.rfc-editor.org/rfc/rfc9116)):

```
Contact: https://github.com/SkilledAlpaca/itchgrep/issues
Expires: 2027-08-10T00:00:00Z
Preferred-Languages: en
Canonical: https://itchgrep.com/.well-known/security.txt
```

The contact is a public issue tracker, not an email address, because
`security.txt` is a public file that spam harvesters read too. **Whether the
issue tracker is reliably monitored, and what the fallback plan is if the
current maintainer becomes unreachable, is an open question** — see
[docs/open-questions.md](docs/open-questions.md#2-ownership-and-monitoring-of-the-securitycrawler-contact-channel).
If you're the maintainer reading this and that's still unresolved, treat it
as the single most important gap in this document to close.

`Expires` needs bumping annually. `TestSecurityTxtHasNotExpired`
(`internal/web/robots_test.go`) fails the build once that date passes, so
this can't rot silently.

## Trust boundaries

```
Internet → Cloudflare (WAF, Tunnel) → cloudflared (this stack) → webserver:8080 (public port 62686)
                                                                 → dataservice:8080 (loopback-only port 62685)
```

- **Public internet to Cloudflare.** Cloudflare terminates TLS, applies a WAF
  managed-challenge rule to any request carrying a query string (added after
  an August 2026 incident — see
  [docs/infrastructure.md](docs/infrastructure.md#the-waf-managed-challenge-rule)),
  and is the only thing the public internet ever actually reaches.
- **Cloudflare to the origin, via Tunnel.** `cloudflared` dials **outbound**
  to Cloudflare; no inbound port is opened on the host, no port is forwarded,
  and the origin's address never appears in DNS. This is stronger than a
  firewall rule — there is no port to scan or forget to close. See
  [docs/infrastructure.md](docs/infrastructure.md#what-a-cloudflare-tunnel-is).
- **The webserver.** Listens on `8080` inside its container, published on
  host port `62686`. This is the only port meant to ever be reachable from
  outside the host. It applies:
  - **Per-client rate limiting** (`internal/web/ratelimit.go`) — a search is
    expensive (three disjunctions, two fuzzy, over the whole catalogue), and
    nothing else in the request path pushes back. Probe traffic (below)
    draws from the same per-client budget as real requests, so an unlimited
    scanner sweep can't write to disk faster than a real client could be
    served. The limiter's own identity map is capped at 50,000 entries with
    a shared overflow bucket beyond that, so no identity source — a proxy
    header, an IPv6 /64 worth of distinct socket addresses — can grow it
    without bound.
  - **Proxy-header trust gated on `TRUSTED_PROXY_CIDRS`**
    (`internal/web/clientip.go`), defaulting under Compose to a single
    `/32` naming `cloudflared`'s own fixed address on the compose network,
    not the whole subnet — see
    [docs/infrastructure.md](docs/infrastructure.md#the-pinned-network-subnet).
    `CF-Connecting-IP` and `X-Forwarded-For` are attacker-controlled unless
    something upstream sets them, so they're honored only when the
    request's own socket address — which a header can never spoof — falls
    inside a configured trusted network, and the header's value is itself
    validated with `net.ParseIP` before being trusted as an identity (a
    value that isn't an address falls back to the socket address, the same
    as a missing header would). Fails closed: `TRUST_PROXY_HEADERS=true`
    with no valid `TRUSTED_PROXY_CIDRS` trusts nobody rather than trusting
    everybody. See
    [ADR 0002](docs/architecture/decisions/0002-trusted-proxy-cidrs-fail-closed.md).
  - **Security-event logging** — every request the scanner-probe filter
    blocks is recorded to `security.jsonl`; see
    [docs/observability.md](docs/observability.md).
  - **A restrictive Content-Security-Policy** plus `Referrer-Policy`,
    `X-Content-Type-Options`, and `Cross-Origin-Opener-Policy`
    (`internal/web/secheaders.go`). No `X-Frame-Options` (the CSP's
    `frame-ancestors 'none'` already covers it) and no HSTS at the origin
    (Cloudflare sets it at the edge; the same binary also answers on a
    plain-HTTP LAN port that must not be locked to HTTPS).
- **The dataservice.** Listens on `8080` inside its container, published on
  **`127.0.0.1:62685`** — loopback only, never `0.0.0.0`
  (`docker-compose.yml`). Its `/trigger-fetch` endpoint takes **no
  authentication** and starts a multi-hour crawl on a bare `POST`. **It must
  never be given a public hostname or exposed beyond loopback, on any port
  or through any proxy** — doing so hands any visitor who finds it a button
  to fire at will, with no rate limit or gate in front of it.
- **Data directory.** Both containers run as uid `65532` (distroless's
  `nonroot` user, not root — see
  [ADR 0006](docs/architecture/decisions/0006-distroless-nonroot-digest-pinned.md)).
  Neither program executes arbitrary code from the data it stores or serves.

## Secrets

- **`CLOUDFLARE_TUNNEL_TOKEN`** is the only credential this project handles.
  It lives in `.env`, next to `docker-compose.yml` on the production host,
  which is listed in `.gitignore` and must never be committed.
- **It is visible to anyone who can run `docker inspect` on the host** — it
  appears in the `cloudflared` container's environment. Host access
  therefore effectively grants tunnel access too; there's no additional
  secret store isolating it further today.
- **Rotation:** see
  [docs/disaster-recovery.md](docs/disaster-recovery.md#rotating-the-cloudflare-tunnel-token).
  Rotate immediately if the token is ever exposed (committed by mistake,
  leaked in a log, shared over an insecure channel).
- No other credential exists in this project: itch.io is scraped with no API
  key, and the European Central Bank exchange-rate feed is a public,
  unauthenticated endpoint.

## Privacy posture

- **Search query text is never logged**, anywhere, by either the access log
  or the metrics counters — only a boolean for whether a query string was
  present.
- **IP addresses are truncated, not hashed**, before being written to any
  log: `/24` for IPv4, `/48` for IPv6 (`AnonymizeIP`,
  `internal/web/clientip.go`). This is enough to recognize a repeat offender
  or a sweep from one allocation, not enough to identify an individual
  visitor. The full, untruncated address is used only in memory, for the
  rate limiter's own bookkeeping, and is never itself persisted.
- Full rationale in [ADR 0007](docs/architecture/decisions/0007-privacy-posture.md).

## Dependency security

- **`govulncheck`** runs on every push to `main` and every pull request (see
  [docs/ci-cd.md](docs/ci-cd.md)) and fails the build if a dependency has a
  known, reachable vulnerability.
- **Every base container image is pinned by digest**, not just by tag, in
  both Dockerfiles and in `docker-compose.yml`'s `cloudflared` image — a
  tag alone is a moving target that can silently swap in a different image
  on the next build. Refresh commands are documented alongside each pinned
  digest; see [docs/dependencies.md](docs/dependencies.md#base-container-images).
- Both runtime images are distroless and run as a non-root user — see
  [ADR 0006](docs/architecture/decisions/0006-distroless-nonroot-digest-pinned.md).

## Security-sensitive files

Changes to any of these deserve extra scrutiny before merging:

- `internal/web/clientip.go` — client identity resolution and proxy-header
  trust.
- `internal/web/ratelimit.go` — per-client rate limiting.
- `internal/web/logmiddleware.go` and `internal/web/probes.go` — what gets
  logged, and the scanner-probe filter.
- `internal/web/secheaders.go` — the Content-Security-Policy and other
  response headers.
- `docker-compose.yml` — port bindings (especially the dataservice's
  loopback-only publish) and `TRUSTED_PROXY_CIDRS`.
- `Dockerfile.webserver`, `Dockerfile.dataservice` — pinned base image
  digests and the non-root user.
- `.env` (production, on the host; never committed) — holds
  `CLOUDFLARE_TUNNEL_TOKEN`.

## Incident response

1. **Site down.** [docs/troubleshooting.md](docs/troubleshooting.md).
2. **Data lost or corrupted.** [docs/disaster-recovery.md](docs/disaster-recovery.md).
3. **Credential exposed, or unexpected access suspected.** Rotate the
   Cloudflare Tunnel token immediately (see Secrets, above, and
   [docs/disaster-recovery.md](docs/disaster-recovery.md#rotating-the-cloudflare-tunnel-token)).
   Review recent commits and GitHub Actions runs for anything unexpected.
   If GitHub account access itself is in question, see
   [docs/disaster-recovery.md](docs/disaster-recovery.md#github-account-access).
4. **A vulnerability report comes in through the issue tracker.** Confirm it
   against the running version, fix it following the normal change process
   in [CONTRIBUTING.md](CONTRIBUTING.md), and deploy following
   [docs/deployment.md](docs/deployment.md). There is no private disclosure
   channel today — see the open question at the top of this document.
