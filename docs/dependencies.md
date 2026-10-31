# Dependencies

This document lists everything itchgrep depends on that isn't itchgrep's own code: Go modules, a vendored JavaScript file, a pinned code-generation tool, base container images, and the external services the running application talks to. For each, it names what happens if the dependency breaks or goes away.

## Direct Go dependencies

These are the modules listed under the first `require` block in `go.mod` — the ones itchgrep's own code imports directly. Everything else in `go.mod` is an indirect (transitive) dependency, pulled in by these. This page deliberately quotes no versions, because Dependabot bumps them every week: read the current ones in `go.mod` (and, for `govulncheck`, in `tools/go.mod`).

| Module | Purpose in this project |
|---|---|
| `github.com/PuerkitoBio/goquery` | Parses the HTML fragments itch.io's browse endpoint returns, so `internal/fetcher` can pull game listings out of them (a jQuery-like API over Go's HTML parser). |
| `github.com/a-h/templ` | Compiles `.templ` template files (`internal/web/templates/*.templ`) into plain Go code (`*_templ.go`) at build time. Not a runtime dependency of the built binary — see "The templ CLI" below. |
| `github.com/blevesearch/bleve/v2` | The full-text search engine the whole site is built on: indexes every asset and answers fuzzy/exact/phrase queries, facets, and sorts. See "bleve's index format" below. |
| `github.com/go-chi/chi/v5` | The webserver's HTTP router: routing, route patterns for the access log and `/stats` breakdown, and the `Recoverer` panic middleware. The dataservice uses the standard library's `http.ServeMux` instead. |
| `go.etcd.io/bbolt` | Imported only for `bolt.ErrTimeout`, so `cmd/dataservice/selfheal.go`'s `indexNeedsRebuild` can tell "the webserver holds the index lock" apart from a corrupt index. bleve's scorch format also uses it internally. |
| `github.com/stretchr/testify` | Test-only: assertion helpers used throughout `*_test.go` files. Never compiled into either production binary. |

### Upgrading a dependency

```
go get -u github.com/<module>@latest
go mod tidy
go test ./...
```

Then run `go vet ./...` and, if `templ` version changed, re-read "The templ CLI" below — its pinned version is read *from* `go.mod`, so bumping `github.com/a-h/templ` also changes what CI installs and what a fresh `git clone` needs installed locally.

Dependabot (`.github/dependabot.yml`) opens at most two grouped pull requests a week: one for Go modules, covering both the application module and `tools/go.mod`, and one for the GitHub Actions versions. Review and merge them like any other change; CI runs on each. Docker base images are deliberately not covered (see [ADR 0006](architecture/decisions/0006-distroless-nonroot-digest-pinned.md)).

`tools/go.mod` is a separate module that pins developer tools, currently `govulncheck`. Upgrade a tool there, not in the root `go.mod`:

```
cd tools && go get -tool golang.org/x/vuln/cmd/govulncheck@latest && go mod tidy
```

### bleve's index format tie

The published search index (`index.bleve/` inside the shared `/data` directory) is written in bleve's own on-disk format for whichever bleve version built it. `internal/storage/index.go`'s `IndexMapping()` documents this as a *contract* between the dataservice (which writes it) and the webserver (which reads it) — both binaries must agree on the mapping, and a wrong-format or unopenable index (for example, one built by an older bleve major version) is exactly the condition `cmd/dataservice/selfheal.go`'s self-heal path exists to detect and fix automatically at startup, by rebuilding the index from `assets.json` rather than requiring a fresh six-hour crawl. See [docs/disaster-recovery.md](disaster-recovery.md#index-corruption).

**Breaking-change concern:** a bleve major-version bump (this project has already been through one, v1 → v2 — see the `selfHealIndexFormat` doc comment) is exactly the kind of change the self-heal path is a safety net for, but it's worth re-reading `internal/storage/index.go` in full before upgrading across a major version regardless, since the field mappings themselves (`Store`, `DocValues`, `IncludeTermVectors` per field) are load-bearing for specific queries in `internal/cache/search.go` — a mapping change that silently drops a doc-values column a query relies on to sort would not be caught by the self-heal path at all, since the resulting index is still perfectly openable.

## Vendored JavaScript: htmx

`internal/web/static/htmx.min.js` is a vendored, third-party file, embedded into the webserver binary via the `//go:embed` in `internal/web/static/static.go` and served at a content-fingerprinted `/static/htmx.min.<hash>.js`. It is not a Go module and does not appear in `go.mod`.

- **Version:** 1.9.10, confirmed from the string literally inside the minified file itself (`version:"1.9.10"`) — see `internal/web/static/VENDOR.md`, which documents this as the authoritative source of truth over any version number written down elsewhere, including in that same file.
- **Why vendored instead of loaded from a CDN:** removes a cross-origin request and its DNS/TLS/latency cost from every page load, keeps the page working with no outbound network access, and removes any risk of a CDN silently serving a different build than the one this project tested against.
- **Refresh procedure:** documented in full in [`internal/web/static/VENDOR.md`](../internal/web/static/VENDOR.md) — download the new version's `dist/htmx.min.js`, replace the vendored file, confirm the new file's own `version:"..."` string matches what you meant to fetch, update the version note in `VENDOR.md`, then run the full test suite and manually exercise the search UI (htmx's breaking changes tend to show up as silent swap failures, not build errors).
- **Exact original fetch URL:** unresolved — see [docs/open-questions.md](open-questions.md).

## The templ CLI

`internal/web/templates/*.templ` files are source; `*_templ.go` files are what `templ generate` produces from them and what actually compiles into the webserver binary. The `templ` CLI itself is not a Go module dependency of the running program — it's a build-time code generator, and its version must match the `github.com/a-h/templ` *library* version pinned in `go.mod`, since the generated code depends on library internals matching.

Both `Dockerfile.webserver` and CI install it pinned to that exact version, never to `@latest`:

```
templ_version="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"
go install "github.com/a-h/templ/cmd/templ@${templ_version}"
```

To generate templates locally after editing a `.templ` file, install `templ` the same way and run `templ generate` from the repository root before building or testing.

## Base container images

Both `Dockerfile.dataservice` and `Dockerfile.webserver` use the same two base images, each **pinned by tag plus digest** on the `FROM` lines of `Dockerfile.webserver` and `Dockerfile.dataservice` (read the current references there; this page deliberately doesn't copy them). Refreshing them is a deliberate manual step, not an automatic one — see [ADR 0006](architecture/decisions/0006-distroless-nonroot-digest-pinned.md).

- **Builder stage:** the `golang:1.27` image.
- **Runtime stage:** `gcr.io/distroless/base-debian12:nonroot`.

A tag like `golang:1.27` or `gcr.io/distroless/base-debian12:nonroot` is a moving target — it gets repushed on every point release and base-image rebuild, so a tag-only `FROM` is not reproducible: the exact same Dockerfile can build a materially different image tomorrow. Pinning by digest fixes the exact bytes; the tag is kept alongside the digest in the `FROM` line purely so a human reading it still sees which version it is, without resolving the hash by hand.

**Refresh command** (documented identically in both Dockerfiles, deliberate rather than automatic):

```
docker buildx imagetools inspect golang:1.27
docker buildx imagetools inspect gcr.io/distroless/base-debian12:nonroot
```

Then update the `FROM` line's digest (and confirm the Go version with `docker run --rm golang:1.27 go version` if bumping the tag itself, not just the digest).

The `cloudflared` image in `docker-compose.yml` is pinned the same way (tag plus digest, on the `cloudflared` service's `image:` line) and documented there, refreshed via `docker buildx imagetools inspect cloudflare/cloudflared:latest` then confirmed with `docker run --rm cloudflare/cloudflared:latest --version` — see [docs/infrastructure.md](infrastructure.md).

## External services

### itch.io

What's consumed: public browse-page HTML and its JSON envelope, fetched by `internal/fetcher`. See [docs/infrastructure.md](infrastructure.md#itchio) for the identifying `User-Agent` and rate/politeness posture in full.

**Failure behavior:** a `429` triggers a fleet-wide pause (`internal/fetcher/fetcher.go`'s `rateLimiter.pause`), honoring whatever `Retry-After` itch.io sent, before the crawl continues — this is not adaptive throttling (an earlier version that halved its own rate on each 429 was removed once measurement showed itch.io's 429s come from a missing session cookie, not the request rate, so slowing down further bought nothing). If itch.io blocks the crawler outright, or changes its markup enough that parsing collects too little of the catalog, the **coverage gate** (`COVERAGE_FLOOR`, default 0.75, `checkCoverageFloor` in `cmd/dataservice/main.go`) refuses to publish that run at all — the previous, still-good dataset stays live and the site keeps serving it, stale but intact, rather than being replaced with a worse one. There is no outbound alert when this happens repeatedly; see [docs/observability.md](observability.md) for what to watch for.

### European Central Bank exchange rates

What's consumed: the ECB's daily euro reference rates, a public XML file with no API key and no terms to accept — `pkg/money/rates.go`, constant `ECBSource`:

```
https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml
```

Fetched once per crawl (not on a separate schedule) and stored to `rates.json`, because the same snapshot is baked into the search index's `PriceUSD` field — fetching rates on a different cadence than the crawl would leave the stored dollar values and the displayed exchange rate describing different days.

**Failure behavior:** if the ECB endpoint is unreachable or returns something `ParseECB` can't parse, the crawl falls back to `money.Fallback()` — a small, hand-maintained table baked into the binary, dated to when it was last updated (currently 2026-08-09) and always rendered to visitors with a "≈" and that date attached, so a stale figure is never presented as current. This fallback is deliberately only good enough for rough conversions and price-based sorting/filtering — nothing else in the codebase uses it for anything more precise.

### Cloudflare

Covered in full in [docs/infrastructure.md](infrastructure.md#cloudflare) — DNS, the tunnel, and the [WAF rules](infrastructure.md#waf-rules). From a dependency-cost perspective: the operator reports this runs on Cloudflare's free tier; nothing in this repository contradicts that (no paid-tier-only feature, such as load balancing or a custom TLS certificate upload, is referenced anywhere in the compose file or code).

### GitHub

Covered in full in [docs/infrastructure.md](infrastructure.md#github) — source hosting, Actions CI ([docs/ci-cd.md](ci-cd.md)), and the security/crawler contact channel. GitHub's free tier for a public repository covers everything this project uses (unlimited public-repo Actions minutes, unlimited public-repo storage).

## Costs

| Item | Cost | Notes |
|---|---|---|
| Domain (`itchgrep.com`) renewal | Managed externally | Registration and renewal are handled by the operator outside this project's scope. |
| Cloudflare | Free tier (operator-reported) | Nothing in the repository requires a paid Cloudflare feature. |
| GitHub | Free tier | Public repository; CI runs on GitHub-hosted runners at no cost for public repos. |
| Unraid host | Owner-operated hardware | Not a hosted/cloud cost with a recurring bill from a provider — but real hardware someone has to maintain, power, and eventually replace. |
| itch.io, ECB | No cost | Both are free, public, key-less data sources. |
