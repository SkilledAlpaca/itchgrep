# Spec: pre-deploy hardening

Frozen scope. Fix items 1-14 only. Do not refactor beyond them.

Repo: Go 1.22 module `itchgrep`. Two binaries: `cmd/dataservice` (scrapes
itch.io, builds a bleve index, uploads to GCS) and `cmd/webserver` (serves
search over that index). Local dev uses a fake-gcs-server emulator.

## Build/verify commands

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
templ generate        # only if you changed a .templ file
go build ./...
go vet ./...
```

`templ` v0.2.598 is already installed. Generated `*_templ.go` files are
gitignored but must exist for the build to work; they are already present.

## Decisions already made — do not re-litigate

- **Pagination is 1-indexed everywhere.** `QueryCache` already is
  (`(pageIndex-1)*pageSize`). `Page` must be changed to match. Templates
  bootstrap at `/assets/1` and `/query/1` and must not be changed.
- **No new module dependencies.** Implement single-flight with stdlib only
  (mutex + channel or `sync.Cond`). Do not add `golang.org/x/sync`. Do not run
  `go mod tidy`.
- **Keep `/trigger-fetch` on GET.** Cloud Scheduler and the Taskfile both use
  GET; changing it breaks deployed infrastructure.

---

## Group A — `internal/storage/storage.go`, `internal/fetcher/fetcher.go`

**A8. One GCS client, not one per call.** `createClient` is called by every
exported function in `storage.go`. `cache.IsCacheExpired()` runs on every HTTP
request, so today every search builds a fresh GCS client and TLS connection.
Replace with a package-level client initialised once via `sync.Once`, reused by
all functions. Callers must no longer `defer client.Close()`.

**A9. Kill the `os.Setenv` race.** `createClient` writes
`STORAGE_EMULATOR_HOST` on every call — a data race under concurrency. The env
var must still be set (the GCS library reads it on some upload paths), but set
it exactly once, inside the `sync.Once`.

**A12. Response body leak.** `fetcher.go` `FetchAssetPage` has
`defer resp.Body.Close()` *inside* a retry loop, so bodies are held until the
function returns — up to 21 per call, across ~1500 calls. Close explicitly on
every path instead.

**A13a. HTTP client hygiene.** `fetcher.go` uses `http.Get` on
`http.DefaultClient`, which has **no timeout** — a hung connection leaks a
goroutine permanently. Add a package-level `*http.Client` with a 30s timeout,
and send a `User-Agent` identifying the crawler and linking the repo
(`itchgrep/1.0 (+https://github.com/wintermute-cell/itchgrep)`). Use it for
both `FetchAssetPage` and `GetAssetCount`. Preserve the existing backoff and
429 handling.

---

## Group B — `internal/cache/cache.go`, `cmd/webserver/main.go`, `internal/web/handlers.go`

**B1 + B2. Pagination bounds and off-by-one.** `Page()` computes
`start := pageNum * c.pageSize` with no lower bound. `GET /assets/-1` reaches
`c.data[-36:0]` and panics — unauthenticated and trivially reachable. It is
also 0-indexed while the rest of the app is 1-indexed, so the bootstrap request
`/assets/1` skips the first 36 assets entirely.

- `Page(pageNum)`: reject `pageNum < 1` with an error.
  `start := (pageNum - 1) * c.pageSize`.
- If `start >= len(c.data)`, return an **empty slice and nil error** — not an
  error. `asset_page.templ` only emits the next-page trigger when
  `len(assets) > 0`, so an empty page is how infinite scroll terminates
  cleanly. Returning an error makes the browser show a 400 at the end of the
  list.
- `QueryCache`: reject `pageIndex < 1` the same way.

**B3. Server must not come up half-initialised.** `initializeCache` in
`cmd/webserver/main.go` discards `RefreshDataCache()`'s error. On an empty or
unreachable bucket the process starts with `c.index == nil` and nil-derefs on
the first search.

- Log the failure and retry the initial load in a background goroutine
  (~15s interval) until it succeeds. Do **not** block `ListenAndServe` — Cloud
  Run enforces a startup deadline.
- Add an exported sentinel (e.g. `cache.ErrNotReady`). `Page` and `QueryCache`
  return it when the index/data are not yet loaded, instead of dereferencing
  nil.
- Handlers map that sentinel to **503 Service Unavailable**; keep 400 for
  genuinely malformed page numbers.
- Add `chi.Recoverer` middleware in `main.go` so any residual panic returns 500
  rather than an empty response.

**B4 + B5 + B10. Atomic index swap with single-flight.** Current
`RefreshDataCache`:

```go
if c.index != nil { c.index.Close() }        // old index destroyed first
indexPath, err := storage.GetFS(...)         // err shadowed on next line, lost
c.index, err = bleve.Open(indexPath)
```

Three defects: the error from `GetFS` is silently dropped; the live index is
closed before the replacement exists, so one transient GCS failure leaves the
server permanently degraded; and the whole multi-MB download happens while
holding the write lock, blocking every reader.

Rewrite so that:
- `GetFS` extracts to a **unique temp directory** (`os.MkdirTemp`), not `"."`.
  Concurrent refreshes currently extract to the same path and corrupt each
  other.
- The download and `bleve.Open` happen **outside** the lock.
- The write lock is taken only for the pointer swap.
- The old index is closed and its temp dir removed only **after** the new one
  opens successfully. On any failure, keep serving the old index and return the
  error.
- Check every returned error. Do not shadow.
- **Single-flight:** `IsCacheExpired()` (RLock) followed by `RefreshDataCache()`
  (Lock) is a TOCTOU race — N concurrent requests all see "expired" and all
  start a full download. Collapse concurrent refreshes so exactly one runs and
  the others wait for its result.

---

## Group C — `cmd/dataservice/main.go`, `internal/web/templates/asset_page.templ`, `Dockerfile.webserver`

**C6. `hx-vals` JSON injection.** `asset_page.templ`:

```go
hx-vals={ fmt.Sprintf("{\"query\": \"%s\"}", query) }
```

The raw user query is interpolated into a JSON document with no escaping, so
any query containing a `"` produces malformed JSON and infinite scroll silently
stops. Build the object with `encoding/json` (e.g. `json.Marshal` of a
`map[string]string`) in a small helper and pass the result. Keep the rendered
markup otherwise identical.

**C7. `logging.Fatal` on the request path.** `fetchAndStoreAssets` calls
`logging.Fatal` when `GetAssetCount` fails and when the first page fetch fails.
`logging.Fatal` calls `os.Exit(1)`, so a transient itch.io error kills the whole
service. Replace both with `logging.Error` + `return`. `logging.Fatal` in
`main()` for a failed `ListenAndServe` is correct and stays.

**C11. Concurrent scrape guard.** `/trigger-fetch` spawns a goroutine with no
in-progress check. Two triggers run two scrapes that both write the same
`storage.IndexDirName` directory and corrupt the index. Guard with an
`atomic.Bool` (or mutex): if a scrape is already running, respond
**409 Conflict** and do not start a second. Release the guard when the scrape
finishes, including on the error paths.

**C13b. Bound the scraper.** `fetchAndStoreAssets` launches one goroutine per
page (~1500 at once) and spreads them with `time.Sleep(rand(0, nPages/9))`.
That is an uncontrolled burst against a third-party site. Replace with a
bounded worker pool: **max 8 concurrent requests**, paced by a
`time.Ticker` at **~5 requests/second**. Keep the existing progress logging and
the `assetsChan` collection pattern. Remove the random-sleep spreading, which
this supersedes.

**C14. templ version skew.** `Dockerfile.webserver` installs
`templ@v0.2.543` while `go.mod` requires `v0.2.598`. Generated code and runtime
library can drift. Pin the install to `v0.2.598`. Also uppercase the
`as builder` → `AS builder` on the same line to silence the buildkit warning.

---

## Out of scope

Stale comments, dead `HandleHello`, `handle404` returning HTTP 200,
`.dockerignore` contents, the 100 MB storage test, and Cloud Run memory sizing.
Leave them alone.
