# Feature roadmap — five waves

All approved. Waves land sequentially because they share files. Each wave is one
executor work order; read only your wave plus the global constraints.

## Global constraints (every wave, non-negotiable)

- **Light and efficient is a feature.** No new Go dependencies, no new JS
  libraries, no build steps. JavaScript only where a feature is impossible
  without it (Wave D); everything else is server-rendered and works with
  scripting off. Native HTML elements over widgets.
- Every new page sends explicit `Cache-Control` (follow `cacheable` /
  `statsMaxAge` precedent in handlers.go) and renders inside the existing
  Layout/Masthead.
- Every new query the webserver runs must be bounded (explicit caps, no
  unbounded result sets) and go through the existing rate-limited route group.
- Privacy posture unchanged: nothing new is logged, no query text or full IPs
  anywhere, localStorage never syncs to the server.
- Comment style: constraints and reasons, matching surrounding files.
- `templ generate` binary: `"$(go env GOPATH)/bin/templ"`. Run after .templ
  edits. Acceptance per wave: `go build ./...`, `go vet ./...`,
  `go test ./...` green, `gofmt -l` clean.
- robots.txt currently: `Disallow: /results`, `Disallow: /*?*`, `Allow: /`.
  Clean paths added by these waves (`/new`, `/tag/…`, `/sitemap.xml`,
  `/opensearch.xml`, `/saved`, `/feed`) are crawl-allowed by default; do NOT
  reopen query-URL crawling.

## Wave A — FirstSeen + /new page

**Model.** `models.Asset` gains `FirstSeen time.Time` (zero = unknown, predates
this feature). `IndexedAsset` gains `FirstSeenUnix int64` (0 = unknown).
`NewIndexedAsset` copies it. Do NOT bump `CheckpointVersion`: FirstSeen is
stamped at publish from the previous publish, never collected per-slice, so a
resumed checkpoint is unaffected.

**Stamping (cmd/dataservice/main.go).** At publish, the code already reads the
outgoing `assets.json` (`previousAssets`) for the history diff. Extend that
same block: build `map[GameId]FirstSeen` from previousAssets; for each asset in
the new publish, carry the previous FirstSeen forward if non-zero, else stamp
`publishedAt` ONLY for ids absent from the previous publish. Ids present
before with zero FirstSeen stay zero — old stock must never masquerade as new.
First stamped publish therefore marks only genuine newcomers.

**Search (internal/cache/search.go).** Support (a) filtering to
`FirstSeenUnix >= cutoff` and (b) sorting by FirstSeenUnix descending. Follow
the existing query-building patterns; bleve numeric range query.

**Page.** `GET /new`: masthead + "New this week" heading + up to 72 newest
FirstSeen-stamped assets as the existing card grid (reuse the card component;
extract it if needed rather than duplicating markup). No pagination — a page,
not a feed. Cutoff: `FirstSeenUnix > 0`, sorted newest first, cap 72; the
heading states the window honestly using the newest crawl time (freshness data
already reaches templates). Empty state (index predates the feature): explain
that arrivals start being recorded from the next weekly crawl. Cacheable with
`fragmentMaxAge`. Add a "New" link to the masthead nav (update the "two links"
comment — it argued against a third *external* link; this is a site
destination). Metrics: record it under its route like other pages
(RecordRequest happens in middleware already — nothing to do beyond routing).
Tests: stamping carry-forward/zero-stays-zero, search filter+sort, render
tests for populated and empty states.

## Wave B — Atom feed per search

`GET /feed` (registered beside the other GET routes): reuses `parseFilters`
exactly — any share URL's query string is a valid feed query. Result: entries
= matching assets with `FirstSeenUnix > 0`, newest first, cap 50. Valid Atom
(hand-built via templ or `encoding/xml` — no dependency): feed id/self link =
`https://itchgrep.com/feed?...` (canonical filter order via the existing
ShareURL mechanics), title describes the filters, `updated` = newest entry's
FirstSeen (or newest crawl time when empty — an empty feed is valid, not an
error). Entry: id = asset's itch.io link, title, author, `updated` =
FirstSeen, link = itch.io page, summary = description text (escaped), price
appended. `Content-Type: application/atom+xml; charset=utf-8`,
`Cache-Control: public, max-age=3600` — feed readers poll; the index changes
weekly. Add one `<link rel="alternate" type="application/atom+xml">` to the
results page head so readers auto-discover the feed for the current filters.
Tests: valid XML round-trip (parse what you render), filter passthrough,
empty-feed case, cap.

## Wave C — tag pages, sitemap, OpenSearch

- `GET /tag/{slug}`: server-rendered page for one tag — the index page
  pre-filtered to that tag (reuse HandleIndex internals with a Filters carrying
  the tag; canonicalize by 301-redirecting unknown-cased/invalid slugs to
  lowercase or 404 for tags not in the cached tag universe). These exist so
  crawlers have clean per-tag paths now that query URLs are disallowed. In-page
  filter links keep their normal `/?tags=…` form (uncrawlable, fine for
  humans). Add `<link rel="canonical">` on these pages.
- `GET /sitemap.xml`: `/`, `/about`, `/stats`, `/new`, plus every `/tag/{slug}`
  from the cached tag universe (bounded ~1,000). Built per request from the
  cache, `Cache-Control: public, max-age=86400`. Reference it from robots.txt
  (`Sitemap:` line).
- `GET /opensearch.xml`: static descriptor served like robots.txt — short name
  ITCHGREP, description, search template `https://itchgrep.com/?q={searchTerms}`,
  self URL. `<link rel="search" type="application/opensearchdescription+xml">`
  in the layout head.
- Tests: tag page renders filtered results + 404s an unknown slug; sitemap
  lists the fixed pages and tags and is parseable XML; opensearch descriptor
  parseable, template URL correct; robots.txt gains the Sitemap line (update
  robots_test).

## Wave D — bookmarks + keyboard shortcut (the only JS wave)

- Each result card gets a save toggle (a button with `data-game-id` and the
  minimal fields needed to re-render a card: title, author, link, thumb,
  price). `app.js` (vanilla, no deps): toggle persists to
  `localStorage["itchgrep-saved"]` as an id-keyed object, capped at 500
  entries (drop oldest); cards reflect saved state on render (htmx swaps
  included — hook the existing htmx:afterSwap listener pattern).
- `GET /saved`: server renders masthead + empty grid + a `<template>` card
  skeleton; JS fills it from localStorage. With scripting off the page says
  saved items live in this browser and need JavaScript — the one honest
  degradation in the site, stated rather than hidden.
- `/` keypress focuses the search input unless focus is already in an input/
  textarea/select. Escape blurs it. A few lines, same file.
- Nothing about saves is ever sent to the server; say so on the /saved page.
- Tests: render test for the save button's data attributes and the /saved
  shell. JS itself has no test harness in this repo — keep it small and
  obvious instead.

## Wave E — more like this

- `?like={gameId}` parameter (validated: length-bounded, no path chars) on the
  index + results endpoints, parsed into Filters. When present, the search
  layer looks up that document's TagSlugs and Title (bleve stored fields),
  builds a disjunction over those tag slugs, excludes the source id from
  results, sorts by popularity. Other filters still narrow it (tags become
  additional conjuncts as usual). Unknown id → normal empty-result page.
- Each card gets a "similar" link (`/?like={id}`), rendered like the existing
  tag chips. The active-filter chips row shows `similar to <title>` with the
  usual remove-chip behavior.
- Query URLs stay uncrawlable via robots — no new crawl surface.
- Tests: parse/validation, the query construction (working set: excludes self,
  matches tag overlap), chip render, share URL round-trip.
