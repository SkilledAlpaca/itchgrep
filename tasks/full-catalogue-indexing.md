# Spec: full-catalogue indexing

Frozen scope. Implement items 1–9 only. Do not refactor beyond them.

Repo: Go 1.22 module `itchgrep`. Two binaries: `cmd/dataservice` (scrapes
itch.io, builds a bleve index, uploads to GCS) and `cmd/webserver` (serves
search over that index). Local dev uses a fake-gcs-server emulator.

## Goal

Index all ~108,578 itch.io game assets. Today the scrape reaches ~7,200 of
them, then requests ~2,800 nonexistent pages.

## Build/verify commands

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
templ generate        # only if you changed a .templ file
go build ./...
go vet ./...
go test ./internal/... -count=1 -race
gofmt -l ./cmd ./internal ./pkg     # must print nothing
```

## Measured facts — these are established, do not re-derive

All figures measured against live itch.io on 2026-08-09.

| Fact | Value |
| --- | --- |
| Total assets (`.game_count` on `/game-assets`) | 108,578 |
| Items per page (`num_items` in the JSON) | 36 |
| **Hard pagination cap per view** | **200 pages** (page 200 → 200, page 202 → 404) |
| Therefore reachable per view | ~7,200 |
| Tag facet URL | `/game-assets/tag-<slug>` |
| Tag intersection | Works: `/game-assets/tag-pixel-art/tag-sprites` → 10,599 |
| Canonical ordering | Sort segment first, then tags **sorted lexicographically**. Non-canonical → 301 (`tag-pixel-art/tag-16x16` → `tag-16x16/tag-pixel-art`). |
| **Max 2 tag segments** | 3 tags → **403** (`tag-16x16/tag-pixel-art/tag-characters`) |
| **Sort is incompatible with 2 tags** | `newest/tag-16x16/tag-pixel-art` → **403**; `newest/tag-pixel-art` → 200 |
| Sort segments | `newest`, `top-rated`, `new-and-popular` (plus the default, which orders differently again) |
| `top-rated` is a filter, not just an ordering | pixel-art: 16,800 vs 36,324 |
| Arbitrary tags are facetable | `tag-cyberpunk` 1,673, `tag-isometric` 1,549 |
| Co-tags surfaced per tag page | ~31 |
| **Asset tag directory** `/tags/assets` | **607 tags in one document**; `?page=2`/`?page=3` repeat the same set. Allowed by robots.txt. |
| Tags per asset | **5.08** — 551,657 tag-mass over 608 tags / 108,585 assets. An early 12-asset sample suggested ~9; it was biased toward well-curated listings. Do not use 9. |
| `sitemaps/browse.xml` | 949 URLs, 39 of them `/game-assets` facets |
| `/search` | **Disallowed by robots.txt — do not use** |
| `sitemaps/games_*.xml` | 39 × 50,000 URLs, no classification. Useless here. |

## Decisions already made — do not re-litigate

- **Tag facets are a set cover, not a partition.** There is no `NOT tag-X`
  facet, so views cannot be split into disjoint halves. Coverage is the union
  of tag views, deduplicated by `GameId`. Completeness is therefore not
  provable, only measurable against 108,578.
- **Overlap is the dominant cost.** At ~9 tags per asset, crawling every tag
  view naively fetches each asset ~9 times (~27,000 requests, 7.5h at 1 rps).
  Item 5 exists to bound this. Do not skip it.
- **Do not touch the rate limiter or the cookie jar.** `internal/fetcher`
  already paces requests and retains the `itchio_token` cookie, without which
  itch.io refuses ~50% of requests regardless of rate. That is settled.
- **No new module dependencies.** Do not run `go mod tidy`.
- **Keep `/trigger-fetch` on GET.** Cloud Scheduler and the Taskfile use GET.

## Configuration

New environment variables, all optional, all read in `cmd/dataservice`:

| Var | Default | Meaning |
| --- | --- | --- |
| `COVERAGE_TARGET` | `0.95` | Stop crawling once this fraction of 108,578 distinct assets is collected. |
| `COVERAGE_FLOOR` | `0.90` | Below this, refuse to publish (item 8). |
| `SLICE_MIN_YIELD` | `0.05` | Abandon a slice when new (unseen) assets drop below this fraction **of the items on a page** — at 36 items, below ~2 new assets per page. Measure over a trailing window of 5 pages, not a single page, so one dense page does not keep a spent slice alive. |
| `TAG_CACHE_MAX_AGE` | `168h` | Rediscover the tag universe when the cache is older than this. |
| `MAX_TAGS` | `1000` | Bound on tags discovery may visit. Discovery costs one request per tag, so this is the main cost of a cold start. Measured: the asset tag graph converges at ~131 tags well before this bound. |

`SCRAPE_RPS` keeps its current meaning. `SCRAPE_MAX_PAGES` now caps **total
pages fetched across all slices combined**, not pages within one view, and
still exists so a smoke test can exercise the whole pipeline in minutes. Since
such a run is deliberately partial it must also disarm `COVERAGE_FLOOR` (or it
would always refuse to publish) and shrink `MAX_TAGS` (or discovery, which the
page budget does not govern, would outlast the crawl it is meant to bound).

---

## Item 1 — cap pages per view at 200 (`cmd/dataservice/main.go`)

`nPages` is `ceil(assetCount / numItems)` = 3017, but no view serves past page
200. The scrape currently makes ~2,800 requests that 404, logging an error
each time.

Every place a page count is derived, it must be
`min(ceil(count/numItems), maxPagesPerView)` with `maxPagesPerView = 200`.
Put the constant in `internal/fetcher` next to the code that knows the URL
shape, and export it.

## Item 2 — treat a 404 as end-of-pagination, not an error (`internal/fetcher/fetcher.go`)

`FetchAssetPage` currently hits the `resp.StatusCode != http.StatusOK` branch
on a 404, logs `logging.Error`, and returns `false` — indistinguishable from a
real failure. Add an explicit 404 case returning a distinguishable
"exhausted" signal so a slice can stop cleanly. A 404 is a normal terminal
condition for a slice and must not log at error level.

## Item 3 — tag universe discovery (`internal/fetcher/tags.go`, new file)

```go
type Tag struct {
    Slug  string // e.g. "pixel-art", without the "tag-" prefix
    Count int64  // parsed from .game_count on the tag page
}

// DiscoverTags fetches browse.xml itself to build its own seed set; callers
// pass nil. The seed parameter exists so tests can supply slugs directly and
// stay off the network.
func DiscoverTags(seed []string) ([]Tag, error)
```

Seed from `https://itch.io/tags/assets`, itch.io's own asset tag directory,
which lists ~607 slugs in one document. Fall back to the 39 `/game-assets`
facet URLs in `https://itch.io/sitemaps/browse.xml` only if that fails, and
warn loudly when falling back. Then BFS: harvest `/game-assets/tag-*` links
from each tag page fetched. Deduplicate by slug. Every request must go through
the existing `doGet` so it is paced and carries cookies.

**The seed source is the single biggest lever on coverage.** Seeding from the
sitemap alone converges at ~135 tags — a clique of popular tags that link to
each other — and the long tail never appears. Since an asset is only fully
pageable if it carries a tag under the 200-page ceiling, the long tail *is* the
coverage. Measured: 135 tags → 21.8% and still climbing slowly; 608 tags →
79.3% with every slice exhausted.

Bound the crawl: stop at 1,000 distinct tags or when a BFS level yields no new
slugs, whichever comes first. Log the final count.

## Item 4 — slice planner (`internal/fetcher/planner.go`, new file)

```go
type Slice struct {
    Sort  string   // "" for default, else "newest" | "top-rated" | "new-and-popular"
    Tags  []string // canonical order; empty means the root view
    Count int64    // reported result count, for ordering
}

func PlanSlices(tags []Tag, itemsPerPage int64) []Slice
```

**Pure function. No I/O. This is the unit that must be thoroughly tested.**

**The URL grammar is the binding constraint.** Only these slice shapes exist:

```
/game-assets                       root
/game-assets/<sort>                3 sorts
/game-assets/tag-A                 any single tag
/game-assets/<sort>/tag-A          3 sorts x any single tag
/game-assets/tag-A/tag-B           tag pairs, DEFAULT SORT ONLY, A < B
```

Three tags is a 403. Sort plus two tags is a 403. There is no deeper
subdivision, so the planner cannot recurse until every leaf fits — coverage is
whatever the union of these shapes yields.

The algorithm follows from one observation: **an asset is fully reachable if it
carries at least one tag whose total count is at or under the ceiling.** Assets
carry ~9 tags and most tags are small, so this covers the large majority.

Rules:

1. Ceiling is `maxPagesPerView * itemsPerPage` (~7,200).
2. The root view (no tags, default sort) is always the first slice — see item 6.
3. **Small tag** (`Count` <= ceiling) → one slice. Fully covers that tag.
4. **Big tag** (`Count` > ceiling) → four slices: default, `newest`,
   `new-and-popular`, `top-rated`. These are four distinct orderings, so four
   distinct ~7,200 windows, at no discovery cost.
5. **Residual** — assets all of whose tags are big — is reached by pairing big
   tags with each other: `tag-A/tag-B` for every pair of big tags, `A < B`
   lexicographically. Big tags are few (measured: pixel-art 36k, sprites 15k,
   characters 9.9k, tileset 7.6k), so this is hundreds of slices, not
   thousands. Do **not** generate pairs involving small tags — those assets are
   already covered by rule 3, and the pair count would explode.
6. Order the output largest `Count` first, so the highest-yield slices run
   before the coverage target is met.

Emit every slice already in canonical form. A slice that would 301 or 403 is a
planner bug, not a runtime condition.

## Item 5 — coverage controller (`cmd/dataservice/main.go`)

The crawl is driven by slices, not by a flat page range. Maintain a
`map[string]struct{}` of seen `GameId`s across all slices.

- Skip a page's assets that are already seen; count only new ones.
- Track new-assets-per-page per slice. When it falls below `SLICE_MIN_YIELD`,
  abandon that slice and move to the next. This is what keeps the ~9x overlap
  from turning into 27,000 requests.
- Stop the whole crawl when distinct assets reach
  `COVERAGE_TARGET * totalAssets`, or when slices are exhausted.
- Log coverage every progress tick alongside the existing counters:
  `assets: 41203/108578 (37.9%), slices: 12/380, 429s: 0`

## Item 6 — preserve popularity ranking (`pkg/models`, `cmd/dataservice/main.go`)

`InvPopularity` is currently the page number from the single global browse
ordering, and search relevance depends on it. Per-slice page numbers are **not
comparable** across slices — page 3 of `tag-fonts` and page 3 of
`tag-pixel-art` mean different things. Ignoring this silently degrades ranking.

Rule: crawl the root view first and record the true global rank (its page
number) for every asset it yields. For assets first seen in any later slice,
set `InvPopularity = maxRootRank + sliceRank`, so root-ranked assets always
sort ahead of slice-only assets. Record which branch assigned the value.

## Item 7 — cache the tag universe (`internal/storage/storage.go`)

Discovery costs several hundred requests and the tag set barely moves. Persist
it as `tags.json` in the same bucket as `assets.json`, via functions mirroring
the existing `PutAssets`/`GetAssets`. Reuse the cached set when it is present
and younger than `TAG_CACHE_MAX_AGE`; otherwise rediscover and overwrite.

## Item 8 — publish floor (`cmd/dataservice/main.go`)

A run that stalls at 40% must not silently replace a good index.

After crawling, if coverage is below `COVERAGE_FLOOR`, log an error naming the
achieved coverage and return **without** calling `storage.PutFS` or
`storage.PutAssets`, leaving the previous index in place. At or above the
floor, publish as today. The existing "index first, then assets.json" order
must not change — the indexer waits on `assets.json` as the completion signal.

## Item 9 — tests

Required, in `internal/fetcher/planner_test.go` unless noted:

- `PlanSlices` leaves an under-ceiling tag as a single slice.
- `PlanSlices` expands an over-ceiling tag into exactly four sort variants.
- **Every emitted slice satisfies the URL grammar**: at most 2 tags, and never
  a sort together with 2 tags. Assert over the whole output, not a sample —
  this is the constraint that returns 403 in production.
- `PlanSlices` emits tag pairs in lexicographic order (no slice would 301).
- `PlanSlices` generates pairs only among big tags, never involving a small tag.
- `PlanSlices` puts the root view first and orders the rest largest-first.
- `PlanSlices` terminates on a pathological tag set (every tag over ceiling).
- Page-count capping returns 200, not 3017, for a 108,578-item view.
- A 404 is reported as exhausted, not as an error (`fetcher_test.go`,
  `httptest.Server`).
- Coverage controller: an already-seen `GameId` does not count toward new
  yield, and a slice below `SLICE_MIN_YIELD` is abandoned.

Do not write tests that hit the live itch.io network.

## Measured outcome (2026-08-09)

A full run against live itch.io, `COVERAGE_FLOOR=0`, `SCRAPE_RPS=2`:

| | Result |
| --- | --- |
| Distinct assets | **86,084 / 108,585 = 79.3%** |
| Tags discovered | 608 (16 big, 592 small; small-tag mass 242,891) |
| Slices | 777 planned, all exhausted |
| Pages fetched | 8,931 |
| 429s | **1** |
| Wall clock | ~75 min crawl + ~5 min discovery |

`COVERAGE_TARGET` of 0.95 was **not** reached; the crawl ended by exhausting
every slice, not by hitting the target. 79.3% is what the union of these views
yields — consistent with the design note that coverage here is measurable, not
provable. Raising it further needs a new *kind* of view, not more of these.

Two defects found only by running it, both outside the original 9 items:

- **`fake-gcs-server` persisted nothing.** `-data` is a load directory;
  `-filesystem-root` (default `/storage`) is where it stores. The compose file
  mounted the volume on `-data`, so writes died with the container and every
  run was a cold start. Fixed by mounting the volume as `-filesystem-root`.
- **`loadOrDiscoverTags` swallowed the `GetTags` error**, making a broken cache
  indistinguishable from a cold start. It hid the above. Now logged.

## Out of scope

- Assets carrying no tags at all. Unreachable through facets; sampling put
  this in the low single digits, and the coverage counter will show it.
- Any use of `/search`.
- Changing the bleve mapping or the webserver.
