# Crawl ETA + index history

Two features, both approved. Same repo conventions as the existing code: comments
explain constraints ("why"), not mechanics; tests are table-driven where natural;
`templ generate` must be run after any `.templ` change (binary lives at
`$(go env GOPATH)/bin/templ`).

## Feature A: fuzzy ETA in the crawl progress line

`cmd/dataservice/main.go` — `logProgress` (line ~895) prints every 5s:

    assets: %d/%d (%.1f%%), slices: %d/%d, pages: %d, 429s: %d

Append a fuzzy time-to-completion: `..., 429s: 0, roughly 2 hours left`.

### Estimator

- Pages are the unit of work: the crawl is rate-limited per page request, so
  remaining time is remaining pages × observed pace. "% of assets" must NOT be
  the basis — asset coverage saturates near 80% by design and would stall the
  estimate.
- At planning time (after `fetcher.PlanSlices`, main.go ~153), sum
  `Slice.PagesToFetch(itemsPerPage)` across the slices this run will actually
  crawl — i.e. **excluding slices already in the checkpoint's done set** on a
  resumed run, otherwise a resume shows a wildly inflated ETA. Store on the
  crawler struct as `plannedPages`.
- Observed pace = elapsed-since-run-start ÷ `pagesFetched` (this run's counter,
  which already excludes checkpointed work — consistent numerator/denominator).
  This absorbs 429 pauses automatically.
- Remaining = `max(0, plannedPages - pagesFetched)`. ETA = remaining × pace.
- Slices abandoned early via `SLICE_MIN_YIELD` mean `plannedPages` is an
  overestimate that shrinks as the crawl runs. That is the right direction to
  be wrong in; say so in a comment.
- `pagesFetched == 0` → print `estimating` instead of a duration.

### Fuzzy wording

One helper, `fuzzyETA(remaining time.Duration) string`, in a new file
`cmd/dataservice/eta.go` (keep main.go from growing):

- under 5 min → `almost done`
- under 1 hour → `roughly N0 minutes left` (nearest 10, minimum 10)
- 45m..90m boundary handling: anything that rounds to 1 hour → `roughly an hour left`
- above → `roughly N hours left` (nearest whole hour)

Table-driven tests in `cmd/dataservice/eta_test.go` covering each bucket and
both boundaries. No web/template changes in this feature.

## Feature B: per-crawl index history on /stats

### Record — `pkg/models`

```go
// CrawlRecord is what one crawl knew about itself at publish time. Recorded
// then because none of it can be reconstructed afterwards.
type CrawlRecord struct {
    When            time.Time // publish time, UTC
    DurationSeconds int64     // run start (this run, not checkpoint origin) to publish
    Indexed         int64     // assets in the published index
    Catalogue       int64     // itch.io's reported catalogue size at crawl start
    NewAssets       int64     // game ids absent from the previously published assets.json
    RemovedAssets   int64     // game ids previously published, now gone
    FreeAssets      int64     // Price == "" count; paid = Indexed - FreeAssets
    Pages           int64     // pages fetched this run
    SlicesDone      int64
    SlicesPlanned   int64
    Refusals        int64     // 429s from itch.io this run
    Tags            int64     // tag universe size used for planning
    Resumed         bool      // run picked up a checkpoint rather than starting fresh
}
```

### Persistence — `internal/storage`

- `HistoryFileName = "history.json"`, stored beside the other JSON files.
- `PutHistory(rec models.CrawlRecord) error` — read-modify-write append,
  newest last on disk, capped to the newest 104 entries (two years of weekly
  crawls). Missing file = empty history, not an error.
- `GetHistory() ([]models.CrawlRecord, error)` — missing file returns empty
  slice, nil error (same convention as `GetStats`).
- Tests: round-trip, append ordering, cap enforcement, missing-file case.

### Producer — `cmd/dataservice/main.go`

At publish time, in the same code path that calls `storage.PutStats`:

- Read the **outgoing** `assets.json` (the previous publish) BEFORE the new one
  overwrites it; diff game-id sets for `NewAssets` / `RemovedAssets`. First
  ever publish: previous set is empty → NewAssets = Indexed, RemovedAssets = 0;
  that is honest, leave it.
- Fill every field from state the crawler already has (pagesFetched,
  slicesDone, planned slice count, `fetcher.Refusals()`, tag count, resumed
  flag, run start time).
- A history write failure must NOT fail the publish — `logging.Warning` and
  continue. The index is the product; the history is bookkeeping.
- `CheckpointVersion` is NOT bumped — no `models.Asset` field changed.

### Consumer — webserver

- `HandleStats` loads history the same way it loads the other per-request
  storage-backed data (follow the existing pattern in handlers.go for
  stats/coverage; do not invent a new caching layer — the file is small and
  /stats already carries `statsMaxAge` cache headers).
- New view-model in `internal/web/templates/history.go` (+ `history_test.go`):
  rows newest-first, each exposing:
  - summary line: `Aug 29, 2026 — 96,903 assets (+1,204)`; delta vs previous
    entry computed at render time; oldest row says `first recorded crawl`
    instead of a delta; negative deltas render with a real minus.
  - detail fields, all formatted server-side: coverage (`Indexed/Catalogue` as
    `89.1% of 108,697`), duration (`6h 12m`), new/removed assets, free/paid
    split, pages fetched, slices `done/planned`, 429 refusals, tag count, and
    `resumed from checkpoint` only when true.
  - reuse `HumaneCount` for all asset counts.
- `stats.templ`: new `## Index history` section between the histogram and the
  back link. One native `<details>` per row; `<summary>` = the summary line;
  the body a small `<dl>` of the detail fields. No JavaScript. Empty history →
  the entire section is omitted (first deploy shows nothing until the next
  crawl publishes).
- `app.css`: minimal styling for the details rows consistent with
  `.stats-grid`; keep it small.
- Render tests: empty history renders no section; delta formatting (+/−/first
  row); resumed flag only shown when set.

### Privacy note (spec constraint, do not relitigate)

The /stats page deliberately excludes inbound operator signals (404s, probe
counts, inbound 429s) — see the comment atop `stats.templ`. Everything in
CrawlRecord is about the crawl's own outbound behavior and the public
catalogue, so it is publishable. Do not add inbound request counters to the
history.

## Acceptance

- `go build ./...`, `go vet ./...`, full `go test ./...` green.
- `templ generate` output committed alongside the `.templ` change.
- No change to request logging, rate limiting, or robots.txt.
