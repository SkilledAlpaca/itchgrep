# 0008: Self-healing index rebuild at `dataservice` startup

Status: Accepted (implemented)

## Context

This project migrated its search index storage from bleve v1 (the "upsidedown" format, built on BoltDB) to bleve v2 ("scorch") — see [glossary: upsidedown](../../glossary.md#upsidedown) and [ADR 0001](0001-explicit-bleve-scorch-mapping.md) for why. bleve v2 registers no reader for the upsidedown format at all: a binary built against v2 cannot open a v1 index — not slowly, not with a warning, it simply fails.

This creates a specific, real deployment hazard: upgrading `dataservice` to a v2-linked build on a volume that still holds a v1 index leaves `assets.json` fresh (nothing wrong with it) while the index next to it is completely unopenable. Left alone, `scheduleCrawls` (`cmd/dataservice/main.go`) checks whether `assets.json` is *stale* to decide whether to crawl — and it isn't, so nothing would trigger a rebuild until the index's age (not its unopenability) crossed `CRAWL_INTERVAL`, which defaults to a week. For that entire week, the webserver would be serving 503s (or, if it had an index open from before the restart, would eventually try to reload and fail) — a self-inflicted outage that a full recrawl would eventually fix but far too slowly.

The identical symptom — `bleve.Open` failing — also occurs from ordinary corruption: a process killed mid-write, a disk fault, a half-copied directory. The fix is the same either way.

## Decision

`dataservice` runs `selfHealIndexFormat` (`cmd/dataservice/selfheal.go`) once, synchronously, at startup, **before** `scheduleCrawls` starts its own loop — deliberately synchronous, because a rebuild takes minutes and `scheduleCrawls` would otherwise see a fresh `assets.json` and wait up to a week before an unopenable index got fixed by the normal crawl cycle.

The check itself (`indexNeedsRebuild`) is meta-first and lock-timeout-tolerant, in that order, and must never do a plain `bleve.Open` on the *published* index: the webserver holds that exact directory open for the life of its process (`internal/cache/cache.go`), and scorch's bbolt lock defaults to an indefinite wait — an unconditional `bleve.Open` here would block `dataservice` forever the moment a crawl runs after the webserver has already loaded a published index, taking `scheduleCrawls` and `/trigger-fetch` down with it permanently and invisibly (`docker-compose.yml` deliberately gives `dataservice` no healthcheck, so nothing would even notice). The check instead:

1. Reads `index_meta.json` directly off disk, with no lock involved at all — a missing directory, an unreadable or unparseable meta file, or a non-`"scorch"` `index_type` all decide "needs rebuild" right here, covering a v1 upsidedown index and a half-written directory without ever touching bbolt.
2. Only once the meta file itself claims a healthy scorch index does it attempt an actual open, via `bleve.OpenUsing` with a `bolt_timeout` (5 seconds) rather than `bleve.Open`'s indefinite default. A timeout error means exactly one thing on this stack — the webserver already has the index open — which proves the index is openable and therefore fine, never "needs rebuild." Any other open error (corruption, a mapping mismatch) does mean rebuild.

If a rebuild is needed, `rebuildIndexFromPublishedData` (`selfheal.go`) reconstructs the index from data **already on disk** — `assets.json` and, if present, `rates.json` (falling back to the built-in rate table if not) — without recrawling itch.io at all. This is possible because `buildIndex` (`selfheal.go`), the function that actually constructs index documents and writes them in batches, is shared verbatim between the real crawl path (`publishCrawl`) and this rebuild path — the two never disagree about how a document is built, only about where the input assets came from. The rebuilt index is published through the same `storage.PublishIndex` atomic-rename sequence a real crawl uses (see [ADR 0005](0005-index-before-assets-publish-order.md)), and `assets.json` is deliberately **not** rewritten — it is exactly what was just read back out, so touching it again would only cost a write for no change in content.

## Alternatives considered

- **Require an operator to manually delete and rebuild the index on a format-incompatible upgrade.** Technically simplest, but turns a routine version upgrade into a manual runbook step that's easy to forget until the site is already serving 503s — exactly the failure this decision exists to prevent automatically.
- **Trigger a full recrawl instead of a local rebuild whenever the index is unopenable.** Correct in the sense that it would also fix the problem, but unnecessarily slow (hours instead of minutes) for a fix that doesn't need any new information from itch.io — everything needed to rebuild the index is already sitting in `assets.json`.
- **Make the check asynchronous, alongside `scheduleCrawls`'s own loop.** Rejected specifically because `scheduleCrawls`'s scheduling decision is driven by `assets.json`'s *age*, which self-heal does nothing to change — an async self-heal racing against a scheduler that has already decided "nothing to do for a week" would not reliably fix the problem before the webserver started failing.
- **Decide "needs rebuild" with a plain, unconditional `bleve.Open` on the published index.** This was the original implementation, and it is unsafe: the webserver holds that same directory open for its entire process lifetime, and scorch's bbolt lock waits indefinitely for a second opener rather than erroring. Any `dataservice` restart after the webserver had already loaded a published index — an OOM kill, `docker restart`, the two services racing to start together — would block `dataservice` inside this check forever, with no crawl ever scheduled again and nothing logged to explain why. The meta-first, lock-timeout-tolerant check described above exists specifically to close this hazard.

## Consequences

- A future storage-format migration (if bleve ever changes format again) should follow the same pattern: detect via the real `Open` call plus a metadata check, rebuild from already-published source data if possible, and run synchronously and early in `dataservice` startup.
- `buildIndex`'s two callers (`publishCrawl` and `rebuildIndexFromPublishedData`) must keep constructing documents identically — a divergence between them would mean a self-heal rebuild produces a subtly different index than a real crawl would have, defeating the "indistinguishable outcome" this design relies on. Any future change to how a document is built belongs in `buildIndex` itself, not duplicated into one caller.
- This only helps if `assets.json` (and ideally `rates.json`) are themselves intact — if the whole data directory is gone, this decision offers nothing, and only a fresh crawl (or a restore from backup — see [disaster-recovery.md](../../disaster-recovery.md)) recovers.
