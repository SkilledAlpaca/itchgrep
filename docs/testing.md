# Testing

As of this branch, the suite is **443 tests across 10 packages** (of the 12
packages in this module, `internal/logging` and `pkg/models` have no test
files — see "What is not tested, and why" below), all passing (`go test
./... -count=1`). This page explains what that suite actually covers, what
it honestly does not, how to run it, how to add to it, and how to read a
failure.

## What is tested

Nearly everything that does not require a live network connection to itch.io is covered, and covered thoroughly — this codebase favors many small, narrowly-named tests over few broad ones. Some examples of what's exercised:

- **Pure logic with no I/O at all** — the crawl planner (`internal/fetcher/planner_test.go`, 25 tests: slice validity, URL grammar, page-count arithmetic), the rate limiter's pause/epoch bookkeeping (`internal/fetcher/fetcher_test.go`), price parsing (`pkg/money/money_test.go`), the coverage-floor and backoff decisions (`cmd/dataservice/main_test.go`).
- **HTML/XML parsing against fixed input** — `ParseAssetPage` against real captured HTML fragments (`internal/fetcher/parse_test.go`), the Atom feed and sitemap XML (`internal/web/templates/render_test.go`, `internal/web/sitemap_test.go`).
- **HTTP behavior against a fake server** — `internal/fetcher/tags_test.go` points `baseURL`/the tag-directory URL at an `httptest.Server` to test `DiscoverTags`, `FetchAssetPage`'s 404-as-exhausted handling, and retry/429 behavior, without ever reaching the real itch.io.
- **Storage round-trips against a real temporary filesystem** — `internal/storage/storage_test.go` writes and reads back every artifact listed in [Data lifecycle](architecture.md#data-lifecycle) using `t.TempDir()`, including the atomic-rename publish sequence.
- **The index-self-heal decision and rebuild** — `cmd/dataservice/selfheal_test.go` builds real fixture indexes (a valid scorch one, a mangled `index_meta.json`, a missing directory) and asserts `indexNeedsRebuild` and the rebuild path both behave correctly against each.
- **HTTP handlers end to end, in-process** — `internal/web/handlers_test.go` (34 tests) builds a real `cache.Cache` loaded from fixture data and drives handlers through `httptest.NewRecorder()`, asserting on status codes, headers, and rendered content.
- **Rendered templates** — `internal/web/templates/render_test.go` (39 tests) renders real `templ` components to a string and asserts on the resulting HTML, including cross-cutting properties like "every page credits the original author" (`TestAttributionSurvivesOnEveryPage`).
- **Concurrency-sensitive code** — `internal/cache`'s single-flight refresh, the crawler's mutex-guarded `record`/`checkpoint` methods, and the rate limiter's fleet-wide pause all have tests that exercise them from multiple goroutines.

## What is not tested, and why

Be honest with yourself about this list before changing any of it — a passing suite does not mean these paths are safe to assume correct.

- **The live crawl path itself.** `crawlAndCollect` and `publishCrawl` (`cmd/dataservice/main.go`) are the functions that actually call `fetcher.GetAssetCount` and `fetcher.FetchAssetPage` against `https://itch.io` for real, then build and publish an index from what comes back. Nothing in the suite runs this end to end — it *cannot*, without either hitting the real site in CI (fragile, slow, and rude to itch.io) or a much larger fake-server harness than exists today. What **is** tested is every piece this function is built from: the coverage-floor gate (`TestCheckCoverageFloorRejectsBelowFloor`), the `FirstSeen`-stamping diff logic (`TestStampFirstSeenMarksOnlyGenuineNewcomers`), tag/recency accumulation (`TestRecordAccumulatesTagsAcrossSlices`), and the index build itself (`buildIndex`, exercised indirectly through `selfheal_test.go`'s rebuild tests, which call the same function). The actual live crawl is verified in practice by the [smoke test](glossary.md#smoke-test-scrape_max_pages) (`SCRAPE_MAX_PAGES`), run by hand — see [getting-started.md](getting-started.md).
- **`fetcher.GetAssetCount`.** Unlike `FetchAssetPage` and `DiscoverTags`, nothing overrides `assetCountURL` to point it at a fake server — there is no test for this function at all. It is a small function (fetch one page, regex out a number), but it is real, live-network code with zero coverage.
- **`cmd/dataservice/rates.go`'s `fetchECBRates`.** The real HTTP call to the European Central Bank is not tested; only the resulting XML *parsing* is, indirectly, via `pkg/money`'s own parse tests. `currentRates`'s fallback chain (live fetch → stored snapshot → baked-in table) is exercised in comments and by inspection, not by a test that forces the live fetch to fail.
- **`internal/logging`.** This package has no test file at all. It is a thin wrapper around the standard library's `log` package, which lowers the risk, but a change here is genuinely unverified by the suite.
- **`pkg/models`.** Also no test file. It's data-shape definitions (`Asset`, `IndexedAsset`, and so on) with a handful of small derived-field methods (`AuthorKey`, `RecencyRank`) that are exercised indirectly through the packages that call them (`internal/storage`, `internal/cache`) rather than directly.
- **Real container behavior** — the Dockerfiles, the `nonroot` user's filesystem permissions, `docker-compose.yml`'s health checks and restart policies. These are infrastructure, not Go code, and are outside what `go test` can reach; see [deployment.md](deployment.md) for how that's verified instead.

If you're about to touch any of the above, treat it as changing untested code: read it slowly, and consider whether the change is small enough to reason about by hand or whether it's worth writing the test that's missing first.

## How to run everything

Generate the `templ` files first — nothing in `internal/web/templates` compiles without them (see [languages/go.md#templ](languages/go.md#templ)):

```sh
templ generate
go test ./... -count=1
```

`-count=1` forces every package to actually re-run its tests rather than reusing a cached "this package's inputs haven't changed" result from Go's test cache — worth using whenever you want to be sure, at some small cost in speed.

**A single package:**

```sh
go test ./internal/cache/...
```

**A single test, or every test matching a pattern** (`-run` takes a regular expression matched against test names):

```sh
go test ./internal/cache/... -run TestTagUniverseIsLoadedFromDiscoveryNotFromThisPublishsAssets
go test ./cmd/dataservice/... -run TestBackoff       # matches every test starting with TestBackoff
```

**Verbose output**, showing every test name and its pass/fail as it runs, rather than only a summary:

```sh
go test ./... -v -count=1
```

## How to add a test

Follow the convention every existing test file already uses (see [languages/go.md#testing-convention](languages/go.md#testing-convention) for the mechanics of `t.Helper()`, `t.TempDir()`, and `t.Setenv()`):

1. Put it in `<subject>_test.go`, in the same directory and `package` as the code it tests.
2. Name it `TestSomethingDescriptive` — state the behavior being verified in the name itself, long if it needs to be. A reader should be able to understand what's guaranteed just from `go test -v` output, without opening the file.
3. Use `require` (from `testify`) for a precondition the rest of the test can't proceed without (a fixture that failed to load, a nil that would panic on the next line); use `assert` for the actual behavior being checked, so one failing assertion doesn't hide a second one right after it.
4. If the test needs the filesystem, use `t.TempDir()` and, if the code under test reads `DATA_DIR`, `t.Setenv("DATA_DIR", t.TempDir())` — see `internal/cache/cache_test.go` for the pattern. Go cleans both up automatically when the test ends, including on failure.
5. If the test needs to fake an HTTP dependency, point the relevant package-level `var` (`baseURL`, `assetCountURL`, `tagDirectoryURL`, `browseSitemapURL` — all in `internal/fetcher`) at an `httptest.Server`, and restore it with `t.Cleanup` — see `internal/fetcher/tags_test.go` for the pattern.
6. Run the new test on its own first (`go test ./path/... -run TestYourNewTest -v`) to confirm it actually exercises what you think it does before running the full suite.

## How to read a failure

`go test` prints, for each failing test, the file and line of the failed assertion, what was expected, and what was actually found — `testify`'s `assert`/`require` output is deliberately verbose about this. A few things specific to this codebase's failure modes:

- **A failure in `internal/web/templates`** almost always means either a `.templ` file was edited without running `templ generate` afterward (the compiled template still reflects the old source — re-run it and try again), or a rendered string genuinely changed shape; the assertion will usually show the offending HTML fragment directly.
- **A failure in `internal/storage` or `internal/cache`** that mentions a path under `/tmp` is almost always fine to re-run once — but if it persists, check whether the test's `t.TempDir()`/`t.Setenv("DATA_DIR", ...)` setup is missing, since a test that accidentally reads the *real* `DATA_DIR` (or no `DATA_DIR` at all, falling back to `./data`) will behave inconsistently between machines and between runs.
- **A `panic` inside a test**, rather than a clean `assert`/`require` failure, usually means a `nil` was dereferenced where a fixture should have supplied a real value — check the setup a few lines above the panic's line number, not the line itself.
- **CI failures that don't reproduce locally** are most often the `gofmt` check (`.github/workflows/ci.yml`) — run `gofmt -l .` (excluding `*_templ.go` files) locally before pushing — or `govulncheck` flagging a newly disclosed vulnerability in an existing dependency, which is a real finding to investigate, not a flake.
