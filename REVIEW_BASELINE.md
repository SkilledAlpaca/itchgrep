# Review baseline — 2026-08-29

Temporary artifact for the deep-refine takeover pass. Deleted in the final cleanup.

Baseline commit: `0808278` on branch `refine/2026-08-29`.

## Measured state before any change

| Check | Command | Result |
|---|---|---|
| Build | `templ generate && go build ./...` | passes (exit 0) |
| Vet | `go vet ./...` | clean (exit 0) |
| Tests | `go test ./... -count=1` | 352 passed, 12 packages, 0 failures |
| Non-generated Go LOC | `find . -name '*.go' ! -name '*_templ.go' \| xargs wc -l` | 12,139 |
| Direct dependencies | `go.mod` require (non-indirect) | 8 |
| Total require lines | `go.mod` | 33 |
| Image sizes | `docker build` Dockerfile.webserver / Dockerfile.dataservice | webserver 48.4 MB, dataservice 47.4 MB |

## Required configuration (from docker-compose.yml / .env.example)

- `PORT`, `PAGE_SIZE`, `DATA_PATH`, `TRUST_PROXY_HEADERS`, `CRAWL_INTERVAL` (names per compose/env.example; `.env` itself is access-restricted and never read during this review)
- `CLOUDFLARE_TUNNEL_TOKEN` — secret, lives only in the gitignored `.env`
- `COMPOSE_PROJECT_NAME=itchgrep` pinned in the production `.env` on Unraid

## Runtime shape

- Two services: `webserver` (chi + templ + bleve, port 8080 internal) and `dataservice` (weekly crawler + index publisher, unauthenticated `/trigger-fetch`, must never get a public hostname).
- Startup without data: webserver comes up and answers 503 until the first crawl publishes; this is designed behavior, not a fault.
- Production host: Unraid, Docker Compose, Cloudflare Tunnel (`public` profile); data volume is a host path.
