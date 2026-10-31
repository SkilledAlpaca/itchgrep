# Patterns

Recipes for changes this project actually needs to make, derived from how the code already does the same kind of thing elsewhere. Each recipe ends the same way: **run the tests, update the docs.** That means, concretely:

```sh
templ generate          # if you touched any .templ file
go build ./...
go vet ./...
go test ./... -count=1
```

— see [testing.md](testing.md) for what the test suite covers and how to run a subset — and then update whichever of these documents describes what you changed, so the next person (possibly you, in a year) doesn't have to reverse-engineer it from the diff.

Two of the doc-set's usual recipes are deliberately not included here: **database migration** (there is no database — see [Data lifecycle](architecture.md#data-lifecycle); the closest equivalent is a change to `models.Asset` or the bleve mapping, covered below) and **authentication change** (nothing in this project authenticates a visitor; the closest equivalent is the trusted-proxy and rate-limiting configuration covered in [configuration.md](configuration.md)).

## Add a search filter

A filter is a value that narrows which assets match, as opposed to a sort, which only reorders them. Tracing how an existing filter (for example `Price`) runs end to end is the fastest way to see every place a new one touches:

1. **Add the field to `cache.SearchOptions`** (`internal/cache/search.go`) — the internal representation the search layer works with.
2. **Handle it in `buildQuery`** (`internal/cache/search.go`) — build a `query.Query` conjunct and append it to `conjuncts` (for a positive filter) or `exclusions` (for a "must not have" filter, the way `ExcludeTags` works). If the filter needs a range rather than an exact match, see `priceQuery` (`internal/cache/search.go`) for the `NumericRangeInclusiveQuery` pattern. If the field the filter queries doesn't exist in the index yet, do [Add a field to the index](#add-a-field-to-the-index) first.
3. **Add the field to `templates.Filters`** (`internal/web/templates/filters.go`), and:
   - Encode/decode it in `Values()` (`filters.go`) so it round-trips through a URL, omitting it when at its zero value (this keeps the URL space small and cacheable — see the comment on `Values()`).
   - Add `With<Field>`/`Without<Field>` methods following the existing toggle pattern (`WithPrice`, `WithAuthor`) if the UI needs to set or clear it.
4. **Parse it in `parseFilters`** (`internal/web/handlers.go`) — read it off the query string, and validate/bound it the way every other parameter is (`normalizeTag`, `clampAuthor`, and so on, all in the same file). An unrecognized value should normally be *dropped*, not rejected with a 400 — see the comment at the top of `parseFilters` for why (a stale or hand-edited URL should degrade gracefully rather than error).
5. **Pass it through in `h.find`** (`internal/web/handlers.go`) from `templates.Filters` into `cache.SearchOptions`.
6. **Render the control** in the relevant `.templ` file (most filters live in `internal/web/templates/results.templ` or `index.templ`). Build the link from the `With*`/`HasTag`-style methods on `Filters` and render it through `filterLink` (`results.templ`), never as a hand-written `<a>`: `filterLink` is the one place that adds `rel="nofollow"` (crawlers must not walk the combinatorial URL space), the `hx-*` attributes, and any `aria-current` state. If the filter is a toggle, mark the active state semantically with `aria-current="true"` as well as a CSS class.
7. **Thread it through the four places that carry filter state without a control of their own.** Skipping any of them loses the filter silently the next time the visitor does something else:
   - `hiddenFilters` (`results.templ`) — the hidden form fields shared by the search box and the currency form. Without a field here, typing a new search or changing currency drops the filter. It takes a flag for which form is asking, because the search box owns `q` and deliberately drops `sort` and `like`; decide which side your filter belongs on.
   - `ActiveFilters` (`results.templ`) — the removable chip row. Without a chip the filter is invisible once the visitor scrolls past the control that set it.
   - `Filters.Any` and `Filters.Narrowed` (`filters.go`) — `Any` decides whether the chip row and "Clear filters" render, `Narrowed` decides which advice `NoResults` gives. Add the field to whichever apply, and to `Cleared` if "Clear filters" should drop it.
   - `filterSummary` (`internal/web/templates/title.go`) — the one place that names the active filters. It feeds the document title (`PageTitle`, on the full page and in the `<title>` of the `/results` fragment) and the text the result-count live region announces after a swap; a filter missing here is absent from both.
8. **Keep the URL canonical.** `HandleIndex` answers `302` (cached for the page max-age) to `Filters.PageURL` whenever the request's query string differs from it. A filter that `Values()` does not encode is silently dropped by that redirect, so encode it. A redirect that needs a second hop, or loops, comes from parsing that is not idempotent: `parseFilters` followed by `PageURL` must reach a fixed point in one pass. Add the filter to the round-trip table in `TestCanonicalURLIsAFixedPoint` (`internal/web/handlers_test.go`). A filter holding a list needs a cap like `MaxTags`/`MaxNotTags`: stop parsing at the cap in `parseFilters`, and render the controls as plain text at the cap so they are not links to the current page.
9. If the filter should also apply to `/feed` or `/new`, check whether `HandleFeed`/`HandleNew` (`internal/web/handlers.go`) need the same field threaded through — both build their own `cache.SearchOptions` rather than reusing `h.find`.

## Add a UI page/route

Following the shape of `/tag/{slug}` (`internal/web/tag.go`) or `/new` (`HandleNew`, `internal/web/handlers.go`):

1. **Write the handler.** Either add a method to `internal/web/handlers.go` or, if it's substantial, its own file in `internal/web/` (the way `tag.go` and `sitemap.go` are split out). It should build whatever view model the page needs and render it through `internal/web/templates`.
2. **Register the route** in `cmd/webserver/main.go`. Put it inside the rate-limited `r.Group`. Only liveness and readiness probes (`/healthz`, `/readyz`) sit outside it; the comment above the group explains why the earlier "cheap enough to skip" exemptions for `/static/*`, `sitemap.xml`, and the 404 page were removed. A page view goes in the inner group tagged with `web.RateLimitedGroupMarker`. Register both `r.Get` and `r.Head` for any page a link checker, uptime monitor, or preview fetcher might `HEAD` — every existing page route does this.
3. **Add it to `liveRoutes`** in `internal/web/probes.go` if it's a real page — otherwise `ProbeFilter` doesn't need to know about it (it only ever blocks paths, never allows them), but the map is also the cheap, auditable guarantee that the probe filter can never accidentally shadow a real route.
4. **Classify it in `internal/metrics/metrics.go`** — add a `route<Name>` constant and a case in `RecordRequest`'s switch if it should get its own line in the `/stats` breakdown, rather than falling into the catch-all `other` bucket. If it's added inside `newRouter`'s rate-limited group in `cmd/webserver/main.go` (tagged with `web.RateLimitedGroupMarker`, the way every page route already is), `cmd/webserver/router_test.go`'s `TestEveryRateLimitedRouteHasABreakdownClassification` genuinely enforces this: it builds the real router with `chi.Walk` and asserts every route the marker tags has a non-`other` classification, so skipping this step fails CI rather than silently under-counting on `/stats`. (An earlier version of this test lived in `internal/metrics/metrics_test.go` and only compared a hand-copied route list against itself — it could not actually see `main.go`'s router and passed no matter what. It's been replaced by the router-walking version described here.)
5. **Write the template.** Add a `.templ` file (or a `templ` block in an existing one) under `internal/web/templates/`, following [templ conventions](languages/go.md#templ). Remember `templ generate` before the code will compile. Render only what belongs inside `<main>`: the skip link, the masthead and `<main id="main">` come from `Layout`/`LayoutHead` (`layout.templ`), so a page passes a `templates.Shell` (the masthead's age and coverage; `Search: true` for a page built around a result list, which points the skip link at the results heading) instead of rendering its own masthead.
6. **Consider crawlability.** If the page should be indexed by search engines, add it to the `Sitemap` document (`internal/web/templates/sitemap.go` and `NewSitemap`); if it should explicitly *not* be indexed (like `/results`, which is a fragment with no layout), add a `Disallow` line to `robotsTxt` (`internal/web/robots.go`).

## Add or change a configuration value

Following the shape of any existing `COVERAGE_TARGET`-style variable:

1. **Read it with the shared helper**, never `os.Getenv` directly for anything that needs parsing — use `config.EnvFloat`, `config.EnvNonNegativeFloat`, `config.EnvInt`, `config.EnvDuration`, or `config.EnvBool` (`internal/config/config.go`), each of which already handles "unset," "empty," and "unparseable" consistently and logs a warning on a bad value rather than failing silently. If both `webserver` and `dataservice` need to agree on the same value, put the read in `internal/config` itself (the way `CrawlInterval` is shared) rather than duplicating the parse in both binaries.
2. **Add it to `docker-compose.yml`**, in the relevant service's `environment:` block, as `NAME: "${NAME:-default}"`, with a comment explaining what it does and why the default is what it is — every existing variable in that file follows this shape.
3. **Add it to `.env.example`** if it's something an operator would plausibly want to override on a real deployment (most crawl-tuning variables are; most fixed architectural choices are not). Include a comment.
4. **Document it in [configuration.md](configuration.md)** — the full reference for every environment variable lives there; this page only covers the *pattern* for adding one.
5. Run the tests, update the docs (see the top of this page) — `internal/config/config_test.go` is the place to add a test for a new parsing edge case if the helper itself changes.

## Add a field to the index

This is the one recipe with a real consequence you have to think through, because it spans three files that all have to agree, plus a nuance about what happens to *data already on disk*:

1. **Add the field to `models.Asset`** (`pkg/models/models.go`) if it's something the crawl itself discovers, or to `models.IndexedAsset` (`models.go`) directly if it's derived purely from other fields already on `Asset` (the way `SortTitle` or `AuthorKey` are).
2. **Populate it in `NewIndexedAsset`** (`models.go`) if it's a new `IndexedAsset` field.
3. **Add a mapping entry in `IndexMapping`** (`internal/storage/index.go`). Follow the existing pattern exactly: start from `text()`, `kw()`, or `num()` depending on the field's shape, and only turn on `Store`/`DocValues`/`IncludeTermVectors` for what an actual query in `internal/cache/search.go` needs — write a comment naming that query, the way every existing field mapping does. The mapping defaults everything off (`m.StoreDynamic = false`, `m.DocValuesDynamic = false`, `index.go`) specifically so a forgotten field fails *safe* (unsearchable, which is loudly obvious) rather than silently reintroducing a stored copy nobody asked for. See [ADR 0001](architecture/decisions/0001-explicit-bleve-scorch-mapping.md).
4. **Think about the reindex consequence.** A field added to `Asset` is populated only by a *fresh crawl* — it will be empty (its zero value) on every asset already sitting in `assets.json`. Two follow-on effects matter:
   - **The published index doesn't get the new field until the next successful crawl actually runs and publishes.** `dataservice`'s self-heal rebuild (`cmd/dataservice/selfheal.go`, see [ADR 0008](architecture/decisions/0008-self-healing-index-rebuild.md)) rebuilds the index from the *existing* `assets.json` without recrawling — so it will **not** backfill a brand-new `Asset` field either. Only a real crawl populates it.
   - **If the crawl collects the new field per-slice** (the way `Tags` and `InvRecency` are accumulated across many page fetches), a checkpoint resumed mid-crawl from before your change would silently produce assets with the field left empty, and the slices that would have populated it are already marked done and never revisited. If that's a risk for your field, bump `storage.CheckpointVersion` (`internal/storage/storage.go`) — its own doc comment explains exactly this failure mode and lists the fields that have required a bump before.
5. Run the tests, update the docs — and note in [configuration.md](configuration.md) or wherever relevant that the new field only appears after the next crawl, if that's not obvious from context.

## Add or upgrade a dependency

1. `go get <module>@<version>` (or `go get -u <module>` for the latest version), then `go mod tidy` to clean up `go.mod`/`go.sum`.
2. Run `(cd tools && go tool govulncheck -C .. ./...)` — CI runs this on every push, every pull request, and daily (`.github/workflows/ci.yml`), so a new dependency with a reachable known vulnerability will fail CI; catching it locally first is faster.
3. If the dependency is `github.com/a-h/templ`, nothing else needs bumping by hand: both CI (`ci.yml`) and `Dockerfile.webserver` install the `templ` CLI by deriving the version straight from `go.mod` (`templ_version="$(go list -m -f '{{.Version}}' github.com/a-h/templ)"`), so bumping `go.mod` alone is enough for both to pick up the new version automatically. This used to be a separate, manually pinned literal in the Dockerfile that could (and did) drift out of sync — see [docs/dependencies.md](dependencies.md#the-templ-cli).
4. Both Dockerfiles also pin their base images (`golang:1.27` and `gcr.io/distroless/base-debian12:nonroot`) by exact digest rather than by tag alone, for reproducibility — see [ADR 0006](architecture/decisions/0006-distroless-nonroot-digest-pinned.md). These are a separate kind of "dependency" from a Go module and are refreshed deliberately, not as a drive-by part of an unrelated change:
   ```sh
   docker buildx imagetools inspect golang:1.27
   docker buildx imagetools inspect gcr.io/distroless/base-debian12:nonroot
   ```
   Update the digest (and the comment, if the referenced command output changed) in both Dockerfiles together, since they're pinned to the same two images.
5. Run the tests, update the docs — check [dependencies.md](dependencies.md) for anything else this project tracks about its dependency set.

## Deploy

See [deployment.md](deployment.md) for the full deploy procedure.

## Roll back

See [disaster-recovery.md](disaster-recovery.md) for how to recover from a bad deploy or a corrupted/lost artifact, including which of the artifacts listed in [Data lifecycle](architecture.md#data-lifecycle) can be regenerated automatically and which cannot.

## General change-management sequence

For any change, regardless of which recipe above it falls under:

1. Read the doc comment at the top of the package(s) you're about to touch first — this codebase's comments are unusually dense with the *why* behind a decision, and a change that looks like an obvious simplification often has a specific reason spelled out right there for why it isn't one.
2. Make the smallest change that accomplishes the goal.
3. `templ generate` if you touched any `.templ` file.
4. `go build ./...`, `go vet ./...`, `go test ./... -count=1`.
5. `gofmt -l .` (excluding generated `*_templ.go` files) — CI fails on unformatted code; see `.github/workflows/ci.yml`.
6. Update whichever document in this set describes the thing you changed.
7. Commit, following the existing log's style (see `git log` — short, present-tense, plain-language summaries of *why* a change was made, not just what changed).
