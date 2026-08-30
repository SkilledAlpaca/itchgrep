# Go, for maintaining this project

This page teaches the subset of the Go programming language you need to read and change this codebase. It does not try to teach Go in general — only the parts this project actually uses, illustrated with real excerpts from this repository so you can find the full context yourself. If a term here is unfamiliar, check [glossary.md](../glossary.md) first; it may be a project term rather than a Go one.

Go is a compiled, statically typed language: you write `.go` files, run `go build` to turn them into a single standalone program (a *binary*), and that program runs without needing Go installed on the machine it runs on.

## Module and package layout

A Go **module** is one codebase with one dependency list. This whole repository is one module, declared in `go.mod`:

```
module itchgrep

go 1.27.0
```

The `module itchgrep` line is why every import inside this repository starts with `itchgrep/`, for example `itchgrep/internal/storage`. The `go 1.27.0` line pins the minimum Go language version the code requires.

A **package** is a directory of `.go` files that share a `package <name>` declaration at the top of each file. Everything in a package can see everything else in the package without an import; a file in a different package needs an explicit import. This project's packages are laid out under three roots:

- `cmd/webserver` and `cmd/dataservice` — each is `package main`: the entry point of one of the two programs this module builds. See [architecture.md](../architecture.md#components) for what each one does.
- `internal/...` — packages only this module is allowed to import. Go enforces this: no other module could import `itchgrep/internal/cache` even if it wanted to. This is where nearly all of the project's logic lives.
- `pkg/...` — packages meant to be reusable even outside this module (`pkg/models`, `pkg/money`). In practice nothing outside this repository imports them yet, but the directory name signals the intent.

Every package file opens with a package-level doc comment on the `package` line explaining what the package is for — for example `internal/storage/storage.go:1-26` or `internal/fetcher/fetcher.go:1-7`. Reading that comment first is the fastest way to orient yourself in an unfamiliar package.

## Imports

An `import` block lists the packages a file uses, standard library first, then this module's own packages, then third-party ones — see the top of `internal/web/handlers.go:10-25`:

```go
import (
	"errors"
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/metrics"
	"itchgrep/internal/storage"
	"itchgrep/internal/web/templates"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)
```

An unused import is a compile error in Go — the build refuses to proceed rather than warn. If you delete the last use of an imported package, delete the import line too, or `go build` will fail with `imported and not used`.

## Functions and methods

A plain function is declared with `func`, a name, typed parameters, and a return type:

```go
// internal/cache/cache.go:397-409 (excerpt of the pattern; see file for the
// full body)
func syncDir(dir string) {
```

A **method** is a function attached to a type, declared with a receiver in parentheses before the name. This project uses value receivers for small, read-only types and pointer receivers for anything that mutates state or is expensive to copy. Compare a value receiver on `Asset` (`pkg/models/models.go:82-87`):

```go
func (a Asset) RecencyRank() int64 {
	if a.InvRecency <= 0 {
		return unranked
	}
	return a.InvRecency
}
```

against a pointer receiver on `Cache`, which holds a mutex and must never be copied (`internal/cache/cache.go:143-147`):

```go
func (c *Cache) Rates() money.Rates {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.rates
}
```

A function can return more than one value — the idiom `(value, error)` is everywhere in this codebase, and is the shape Go's own standard library uses too. See [error handling](#error-handling) below.

## Structs

A `struct` groups named fields into one type. `models.Asset` (`pkg/models/models.go:17-73`) is the central example — it is what one crawled game asset looks like once parsed:

```go
type Asset struct {
	GameId      string
	Title       string
	Author      string
	Description string
	Link        string
	ThumbUrl    string
	Tags        []string
	Price       string
	PayWhatYouWant bool
	InvPopularity  int64
	InvRecency     int64
	FirstSeen      time.Time
}
```

Fields starting with an uppercase letter (`GameId`, `Title`, ...) are **exported** — visible from other packages, and this is also what makes `json.Marshal` include them when a struct is written to disk (see `internal/storage/storage.go`'s `writeJSON`). A lowercase field (for example `cacheLock` on `Cache`) is private to its own package. This is Go's entire visibility system: there is no `public`/`private` keyword, only the capitalization of the name.

A struct literal builds a value by naming its fields:

```go
// cmd/dataservice/selfheal.go, inside rebuildIndexFromPublishedData's callers
Checkpoint{
	Assets:      assets,
	DoneSlices:  make([]string, 0, len(c.doneSlices)),
	MaxRootRank: c.maxRootRank,
	TotalAssets: c.totalAssets,
	Recency:     recency,
}
```

## Error handling

Go has no exceptions. A function that can fail returns an `error` as its last value, and the caller is expected to check it immediately with `if err != nil`. This project follows that convention everywhere — see `internal/storage/storage.go:172-186`:

```go
func readJSON(name string, v any) (time.Time, error) {
	p := pathTo(name)
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, fmt.Errorf("os.Stat %s: %w", p, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, fmt.Errorf("os.ReadFile %s: %w", p, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return time.Time{}, fmt.Errorf("json.Unmarshal %s: %w", p, err)
	}
	return fi.ModTime(), nil
}
```

Two things worth noticing in that example, because they recur throughout the codebase:

- **Error wrapping.** `fmt.Errorf("os.Stat %s: %w", p, err)` uses the `%w` verb to wrap the original error inside a new one that adds context (which operation failed, and on what path) without discarding the original. A caller further up can still test the underlying cause with `errors.Is` — see `internal/web/handlers.go:534-539`, which distinguishes `cache.ErrNotReady` from every other error this way:

  ```go
  func statusFor(err error) int {
  	if errors.Is(err, cache.ErrNotReady) {
  		return http.StatusServiceUnavailable
  	}
  	return http.StatusInternalServerError
  }
  ```

- **An error is just a value.** It can be compared, stored, logged, or ignored like any other value — there is no `try`/`catch` block forcing a particular structure. This project's comments frequently explain *why* a particular error is treated as fatal versus recoverable; read those comments, since the choice is rarely accidental (for example, `internal/storage/storage.go:130-160` explains at length why a directory-sync failure is logged once and swallowed rather than returned).

## Goroutines, channels, and atomics

Go's concurrency primitives appear in this codebase only where they solve a real problem the comments explain — not as a general style. Three uses are worth learning by name, because you will meet them again if you touch the crawler or the cache.

**Goroutines** (`go someFunc()`) start a function running concurrently, without blocking the caller. The crawl scheduler is one long-running goroutine, started once at process startup (`cmd/dataservice/main.go:52`):

```go
go scheduleCrawls(config.CrawlInterval())
```

That goroutine runs for the entire life of the process, waking up every 15 minutes to check whether a crawl is due. Contrast that with a short-lived goroutine spawned per unit of work, such as one per page fetched concurrently during a crawl (`cmd/dataservice/main.go`, `fetchPages`):

```go
var wg sync.WaitGroup
for i := 0; i < n; i++ {
	wg.Add(1)
	go func(i int) {
		defer wg.Done()
		// ... fetch one page ...
	}(i)
}
wg.Wait()
```

`sync.WaitGroup` is how the calling code waits for a known number of goroutines to finish: `Add(1)` before starting each one, `Done()` (via `defer`, so it always runs) when it finishes, and `Wait()` blocks until every `Add` has been matched by a `Done`.

**`sync.RWMutex`** protects data that many goroutines read concurrently but only occasionally write. `Cache` uses one because every incoming HTTP request reads the loaded dataset, while a refresh occasionally swaps in a new one (`internal/cache/cache.go:49, 143-147, 385-397`):

```go
type Cache struct {
	cacheLock sync.RWMutex
	// ...
}

func (c *Cache) Rates() money.Rates {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.rates
}
```

Many goroutines can hold an `RLock` (read lock) at once; a `Lock` (write lock) — used when `doRefresh` swaps the loaded dataset in — waits for every reader to finish first and excludes new ones until it's done. This is why `doRefresh` does all the slow work (reading files, opening the index) *before* taking the write lock, and holds it only for the instant of assignment (`internal/cache/cache.go:385-397`).

**`sync/atomic`** provides variables that many goroutines can read and update at once without a lock at all, for simple counters and single-value flags. `dataservice` uses `atomic.Bool` to guarantee only one crawl ever runs at a time (`cmd/dataservice/main.go:32, 261-264`):

```go
var scrapeInProgress atomic.Bool

func startScrape(onDone func(error)) bool {
	if !scrapeInProgress.CompareAndSwap(false, true) {
		return false
	}
	// ...
}
```

`CompareAndSwap(false, true)` atomically checks "is this currently false?" and, only if so, sets it to `true` — returning whether it succeeded. Two goroutines calling this at the same instant can never both get `true` back, which is exactly the guarantee needed to stop two crawls from writing the same index directory at once.

`atomic.Pointer[T]` is the same idea for a pointer instead of a bool. `activeCrawler` (`cmd/dataservice/main.go:39`) lets a shutdown signal find the currently-running crawler (if any) without a mutex:

```go
var activeCrawler atomic.Pointer[crawler]
```

**`sync.OnceValue`** builds a value the first time it's asked for and caches it forever after, which is how this project reads certain environment variables exactly once and shares the result everywhere it's needed — for example `TrustProxyHeaders` (`internal/web/ratelimit.go:17-19`):

```go
var TrustProxyHeaders = sync.OnceValue(func() bool {
	return config.EnvBool("TRUST_PROXY_HEADERS", false)
})
```

Calling `TrustProxyHeaders()` from anywhere in the program runs the inner function only on the very first call; every later call (from any goroutine) gets the same cached result instantly. This guarantees the rate limiter and the request logger can never disagree about whether proxy headers are trusted, since they call the same cached function rather than parsing the environment variable twice.

## `defer`

`defer someCall()` schedules `someCall()` to run when the surrounding function returns, no matter which `return` statement it exits through, or even if it panics. It is used throughout this project to pair an acquire with its release — a lock with its unlock, a file open with its close — right next to each other in the source, rather than scattered across every exit path:

```go
// internal/cache/cache.go:161-163
func (c *Cache) HasRecency() bool {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.hasRecency
}
```

## Testing convention

Every test file in this repository is named `<subject>_test.go` sitting beside the code it tests, in the same package — Go's `go test` command finds these automatically. Test functions are named `TestSomethingDescriptive` — this codebase favors long, sentence-like test names that state the behavior being verified, for example `TestTagUniverseIsLoadedFromDiscoveryNotFromThisPublishsAssets` (`internal/cache/cache_test.go:41`). Read the name as the specification; the test body proves it.

Assertions go through `github.com/stretchr/testify`'s `assert` (report a failure and keep running the rest of the test) and `require` (report a failure and stop the test immediately — used when a later step would panic on bad input, like using a value that failed to load). Both appear side by side, for example `internal/cache/cache_test.go:52-56`:

```go
assert.Equal(t, []string{"pixel-art", "unreleased"}, c.TagSlugs(), "sorted, not tags.json's own order")
assert.True(t, c.HasTag("pixel-art"))
assert.False(t, c.HasTag("no-such-tag"))
```

`t.Helper()` marks a function as a test helper, so a failure inside it is reported at the helper's *caller's* line, not buried inside the helper itself — see `loadedCacheWithTags` (`internal/cache/cache_test.go:17-19`). `t.TempDir()` and `t.Setenv(...)` give a test its own scratch directory and its own environment variable value that Go automatically cleans up when the test ends — used throughout to point `DATA_DIR` at an isolated temporary directory per test, so tests never share or corrupt each other's data.

For what is and is not tested, how to run the suite, and how to add a new test, see [testing.md](../testing.md).

## Build, vet, and test commands

Run these from the repository root:

```sh
go build ./...   # compile everything; catches type errors and typos
go vet ./...     # static analysis: catches likely bugs that still compile
go test ./...    # run every test in every package
```

`./...` means "this package and every package below it" — the standard way to target the whole module. Add `-count=1` to `go test` to force it to actually re-run tests rather than reusing a cached result from an unchanged package:

```sh
go test ./... -count=1
```

Before any of these will work on a fresh clone, you must generate the `templ` files first — see the next section, and [getting-started.md](../getting-started.md) for the full first-run sequence.

## templ

`.templ` files (for example `internal/web/templates/index.templ`) are this project's HTML templates: a small extension of Go syntax that lets a function return a piece of HTML with Go values spliced in. A `.templ` file is not valid Go on its own — a code generator called `templ` reads it and writes an ordinary `.go` file next to it with the matching name plus `_templ.go` (so `index.templ` produces `index_templ.go`).

You run the generator with:

```sh
templ generate
```

The generated `*_templ.go` files are **not committed to git** — `.gitignore` excludes them by pattern (`internal/web/templates/*_templ.go`, see `.gitignore`), and they are regenerated as the first step of every build (see both Dockerfiles, and `go build`/`go test` on a fresh clone — nothing compiles until they exist). If you edit a `.templ` file and forget to re-run `templ generate`, your changes will not appear, because the compiler is still building the stale generated file.

A `.templ` file declares a **component** with the `templ` keyword instead of `func`, and can call another component the way a function calls another function. Here is a real one, `IndexAge` (`internal/web/templates/freshness.go`'s sibling `.templ` file — see also `internal/web/templates/index.templ:9-41`), which shows a value from a plain Go struct spliced into an HTML attribute and a text node:

```templ
templ IndexAge(f Freshness, c Coverage) {
	if f.Known() {
		<p class={ "index-age", templ.KV("is-stale", f.Stale()) }>
			index updated
			<time datetime={ f.DateTime() } title={ f.Absolute() }>{ f.Label() }</time>
		</p>
	}
}
```

The plain-Go types a `.templ` file renders (`Freshness`, `Coverage`, `Filters`, and others) are hand-written `.go` files living in the same `internal/web/templates` package — see `internal/web/templates/freshness.go` and `coverage.go`. Splitting it this way keeps every piece of view logic (deciding *what* a label says) in ordinary, testable Go, while the `.templ` file only decides *where on the page* it goes.

See `internal/web/templates/render_test.go` for how these components are tested: render the component to a string with `component.Render(ctx, &buf)`, then assert on the resulting HTML.
