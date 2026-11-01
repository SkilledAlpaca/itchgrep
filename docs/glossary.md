# Glossary

Terms specific to this project, or standard terms used in a specific way here. Alphabetical. Where a term is Cloudflare- or Docker-specific rather than itchgrep-specific, see [infrastructure.md](infrastructure.md) or [deployment.md](deployment.md) instead — this page covers only what you meet reading the code.

### Asset

One item in itch.io's game-asset catalog — a tileset, a sound pack, a font, and so on (not a full game). Represented in code as `models.Asset` (`pkg/models/models.go`): title, author, description, price, tags, and a few crawl-derived rankings. The whole collection is what `assets.json` holds; see [Data lifecycle](architecture.md#data-lifecycle).

### AuthorKey

The exact-match form of an asset's author name, folded to lowercase with whitespace collapsed (`models.AuthorKey`, `pkg/models/models.go`). Exists because the `?author=` filter has to derive the same key from a URL parameter that the index derived when the document was built — two subtly different foldings would mean an author link that matches nothing.

### Checkpoint

A partially-complete crawl's progress, written to `checkpoint.json` periodically during a crawl (`storage.Checkpoint`, `internal/storage/storage.go`) so a run killed part way through can resume instead of restarting from zero. Deleted automatically once a crawl publishes successfully. See `CheckpointVersion` (`internal/storage/storage.go`) for why a checkpoint from an older build of the code is deliberately discarded rather than resumed.

### Coverage

How much of itch.io's reported asset catalog a crawl actually collected, expressed as a fraction (`crawler.coverage`, `cmd/dataservice/main.go`): assets collected divided by itch.io's own reported total. Shown to visitors as the "X% of catalog indexable" line — see `templates.Coverage` (`internal/web/templates/coverage.go`).

### Coverage floor

The minimum coverage a crawl must reach before its results are allowed to replace the currently published index (`COVERAGE_FLOOR`, checked by `checkCoverageFloor`, `cmd/dataservice/main.go`). Exists so a stalled or truncated run cannot silently replace a good index with a worse one — see [ADR 0003](architecture/decisions/0003-coverage-floor-and-backoff.md).

### Distroless / nonroot

The container base image both `webserver` and `dataservice` run on (`gcr.io/distroless/base-debian12:nonroot`, both Dockerfiles). "Distroless" means the image ships no shell, no package manager, and no other executable besides the compiled binary. "`nonroot`" means it also bakes in a non-root user (`65532:65532`) so the process cannot run as root even by accident. See [ADR 0006](architecture/decisions/0006-distroless-nonroot-digest-pinned.md).

### Doc values

A bleve concept: a column-oriented copy of a field's values, stored alongside the normal inverted index, needed only when something has to *sort by* or *facet on* that field rather than merely search it. This project's index mapping (`internal/storage/index.go`) turns doc values off by default and enables them field by field, each with a comment naming the exact query that needs them — see [ADR 0001](architecture/decisions/0001-explicit-bleve-scorch-mapping.md).

### Engine (in-memory)

The webserver's answer to a request with no text query (`engine`, `internal/cache/engine.go`): per-asset records and precomputed orderings derived from the asset list at every refresh. It filters, counts the sidebar's tags, and selects the page in memory instead of asking the bleve index. See [architecture](architecture.md#how-a-search-request-flows).

### Epoch (rate limiter)

A generation counter inside the fetcher's shared rate limiter (`rateLimiter.epoch`, `internal/fetcher/fetcher.go`) that identifies the current "refusal episode." Every outbound request is tagged with the epoch it was issued under; only a 429 response reported under the *current* epoch is allowed to open a new fleet-wide pause. Without this, several requests already in flight when itch.io starts refusing would each report their own 429 moments apart, and each one would re-trigger a fresh pause — see [ADR 0004](architecture/decisions/0004-fleet-wide-429-pause-and-epoch.md).

### Facet

A bleve query feature that counts how many matching documents carry each distinct value of a field — used here for the sidebar's tag counts (`bleve.NewFacetRequest("TagSlugs", maxFacetTags)`, `internal/cache/search.go`). Distinct from `tagUniverse` (below): a facet only ever counts *the current search's matches*. The sidebar orders them by raw count on the unfiltered front page, and by *lift* once a query or filter narrows the results: how much more common a tag is among the matches than in the whole catalog (`Cache.liftTags`, `internal/cache/search.go`). Raw count would show the same catalog-wide favorites (`2d`, `pixel-art`) for every search. A tag needs at least `max(3, 1%)` of the matches, and a tag on every match is dropped.

### Hit cache

A bounded least-recently-used cache of text searches (`hitCache`, `internal/cache/hitcache.go`): 256 entries keyed by the filters minus the page, each holding ten pages of ordered hits, the total, and the ranked sidebar. It makes pages 2 to 10 of a search a slice of the first page's index pass, and a refresh clears it.

### FirstSeen

The moment an asset's game ID first appeared in a published `assets.json`, carried forward publish over publish once set (`Asset.FirstSeen`, `pkg/models/models.go`; the carry-forward logic is `stampFirstSeen`, `cmd/dataservice/main.go`). Zero means unknown — either the asset predates this field, or it is genuinely new and the current crawl hasn't published yet. This is the field the `/new` page and the Atom feed (`/feed`) order by, and it is the one piece of history that a fresh crawl *cannot* reconstruct if `assets.json` is lost — see [Data lifecycle](architecture.md#data-lifecycle).

### Fleet-wide pause

When itch.io returns a 429 (Too Many Requests), the fetcher's rate limiter pauses *every* concurrent worker, not just the one that received the 429 (`rateLimiter.pause`, `internal/fetcher/fetcher.go`). A crawl runs several requests concurrently against one shared limiter; a plain per-request backoff would just hand a paused worker's slot to another worker, so the aggregate request rate offered to itch.io would never actually drop. See [ADR 0004](architecture/decisions/0004-fleet-wide-429-pause-and-epoch.md).

### Follower (the), export-logs

`scripts/export-logs.sh`, a host-side shell script (not part of either container) that follows both containers' stdout logs forever, each in its own reconnecting loop. It splits the webserver's lines by their `"kind"` field into `access.jsonl` (one line per served request), `security.jsonl` (one line per blocked scanner probe), and `app.log` (everything else), and writes every dataservice line to `crawler.log`. It exists because Docker's own log driver is bounded (see `docker-compose.yml`'s `x-logging` block) and is not meant as durable storage on its own. See [deployment.md](deployment.md) for how to install it.

### IndexedAsset

The smaller, search-optimized shape an `Asset` is converted to before being written into the bleve index (`models.IndexedAsset`, `pkg/models/models.go`, built by `models.NewIndexedAsset`). It duplicates a few fields of `Asset` deliberately — for example `Tags` (analyzed as prose) alongside `TagSlugs` (indexed as exact keywords) — because search and exact filtering need the same underlying data analyzed two different ways. See [ADR 0001](architecture/decisions/0001-explicit-bleve-scorch-mapping.md).

### InvPopularity

"Inverse popularity": the page number an asset was found on in itch.io's own popularity-ordered browse view (`Asset.InvPopularity`). Smaller means more popular, hence "inverse" — it is a rank, not a score. Used as the default sort order and as the tiebreak for every other sort order (`sortOrder`, `internal/cache/search.go`).

### InvRecency / RecencyRank

`InvRecency` (`Asset.InvRecency`, `pkg/models/models.go`) is the page number an asset appeared on in itch.io's *newest-first* view — a rank by recency, the same way `InvPopularity` is a rank by popularity. Zero means "never seen in that view" (true for most of the catalog, since the view is capped at itch.io's 200-page limit). `RecencyRank()` (`pkg/models/models.go`) is the version actually indexed and sorted by: it maps the "unknown" zero to a value larger than any real rank, so unranked assets sort last instead of appearing to be the newest thing in the catalog.

### PageInFull

A flag on a crawl `Slice` (`fetcher.Slice.PageInFull`, `internal/fetcher/planner.go`) marking a view that must be paged all the way to its end rather than abandoned once its yield of new assets drops off. Set for views that are known to be exhaustible and worth exhausting — a small tag's whole view, or a filter applied to an oversized tag. See [ADR 0003](architecture/decisions/0003-coverage-floor-and-backoff.md) and the planner's own extensive comments (`internal/fetcher/planner.go`).

### Probe

A request shaped like a commodity vulnerability scanner's — `/wp-admin`, `/.env`, anything ending `.php`, and similar (`isProbeSuffixes`/`isProbePrefixes`, `internal/web/probes.go`). `ProbeFilter` recognizes these and answers with a bare 404 before the request reaches routing or the access log, after charging the client one token from the shared rate limiter, logging it separately as a `"kind":"probe"` line instead. See [ADR 0007](architecture/decisions/0007-privacy-posture.md) for what is and isn't logged about a probe.

### Publish

The moment a freshly built index and a fresh `assets.json` become the dataset the webserver serves. Two separate writes, deliberately in a fixed order — the index first, then `assets.json` — so the file the webserver watches for freshness never goes stale relative to the index it names. See `publishCrawl` (`cmd/dataservice/main.go`) and [ADR 0005](architecture/decisions/0005-index-before-assets-publish-order.md).

### Root view / newest root view

The **root view** is the unfiltered, untagged, unsorted itch.io browse view (`Slice.IsRoot`, `internal/fetcher/planner.go`) — the only view whose page numbers are a genuine, catalog-wide popularity rank, which is why it is always crawled first and never abandoned early. The **newest root view** is the same but sorted newest-first (`Slice.IsNewestRoot`, `internal/fetcher/planner.go`) — the only source of a recency rank (`InvRecency`), for the same reason: page numbers only mean the same thing across different slices when neither slice narrows the catalog by tag.

### Scorch

The name of bleve's default (and, as of bleve v2, only writable) on-disk index storage format. This project relies on that default rather than pinning it explicitly (`buildIndex`, `cmd/dataservice/selfheal.go`). Contrast with [upsidedown](#upsidedown).

### Self-heal

`dataservice`'s startup routine (`selfHealIndexFormat`, `cmd/dataservice/selfheal.go`) that detects an index this build cannot open — either the legacy [upsidedown](#upsidedown) format or a corrupted directory — and rebuilds it from the already-published `assets.json` and `rates.json`, in minutes, without waiting for or triggering a full recrawl. See [ADR 0008](architecture/decisions/0008-self-healing-index-rebuild.md).

### Slice

One browsable view of the catalog that a crawl can page through — a plain URL like `/game-assets/tag-pixel-art` or `/game-assets/newest` (`fetcher.Slice`, `internal/fetcher/planner.go`). A full crawl is the union of many slices, deduplicated by game ID, because itch.io caps any single view at 200 pages (`fetcher.MaxPagesPerView`) and the catalog is far larger than that. See `PlanSlices` (`internal/fetcher/planner.go`) for how the set of slices to crawl is chosen.

### Smoke test (`SCRAPE_MAX_PAGES`)

Setting the `SCRAPE_MAX_PAGES` environment variable caps the total pages a crawl fetches, turning a ~6 hour full crawl into a few minutes that still exercise every stage of the pipeline — tag discovery (shrunk too, unless `MAX_TAGS` is set explicitly), fetching, indexing, and publishing (`loadCrawlConfig`, `cmd/dataservice/main.go`). It deliberately disarms `COVERAGE_FLOOR` to 0, since a smoke-tested run is expected to cover only a small fraction of the catalog. Never set this in production — the resulting index is a small, arbitrary subset of the real catalog.

### Staging index

The bleve index directory a crawl builds into while it's in progress (`storage.StagingIndexPath`, `internal/storage/storage.go`), kept in the same directory as the published index specifically so publishing is a few renames within one filesystem (see `PublishIndex`) rather than a slower cross-filesystem copy. See [ADR 0005](architecture/decisions/0005-index-before-assets-publish-order.md).

### Tag universe

The full set of asset tags itch.io has, discovered by crawling itch.io's own tag directory (`fetcher.DiscoverTags`, `internal/fetcher/tags.go`) and cached in `tags.json`. Distinct from the *tag facet* the sidebar shows: the tag universe is every tag that exists, independent of whether the currently published dataset happens to have a matching asset for it (`Cache.tagUniverse`, `internal/cache/cache.go`) — it's what backs `/tag/{slug}` page validation and the sitemap, so a tag with zero matching assets today still gets a real page instead of a 404.

### Term vectors

A bleve field option (`IncludeTermVectors`) that records not just *which* terms are in a field but *where* — the positions needed to answer a phrase query ("this exact run of words, in this order"). Enabled only on the four fields that are ever phrase-searched (`Title`, `Description`, `Tags`, `Author`) — see `internal/storage/index.go` and [ADR 0001](architecture/decisions/0001-explicit-bleve-scorch-mapping.md).

### Token bucket

The rate-limiting algorithm behind the webserver's per-client request limiter (`web.Limiter`, `internal/web/ratelimit.go`): each client gets a bucket that starts full (the "burst" size), drains one token per request, and refills continuously at a fixed rate. A request is allowed only if the bucket has at least one token.

### Upsidedown

The legacy bleve v1 on-disk index format (built on BoltDB), superseded by [scorch](#scorch) in bleve v2. This project's binaries link bleve v2, which registers no reader for upsidedown at all — an index left over from a pre-v2 deployment is simply unopenable, which is exactly the condition `self-heal` (above) exists to detect and fix.
