# 0003: Coverage floor gates publishing; repeated failures back off

Status: Accepted (implemented)

## Context

A crawl can end up incomplete for reasons that have nothing to do with a bug — a network blip, itch.io throttling harder than usual, a process killed mid-run. Coverage (the fraction of itch.io's reported catalogue actually collected) is a real, measured number, not a guarantee: `internal/fetcher/planner.go`'s own comments record that a *healthy, complete* crawl still only reaches roughly 89% of the catalogue, because the plan is a union of tag-filtered views and itch.io offers no way to query "assets with none of the discovered tags at all." A crawl that stalled early might report 40% coverage; one that never started collecting (a broken fetch from the first page onward) would report 0%.

If the dataservice always published whatever it collected, a bad run would silently replace a good, nearly-complete index with a worse one — and worse, publishing something is indistinguishable in the logs from the crawl having succeeded, so nobody would notice until visitors started failing to find things that used to be there.

Separately: if a crawl keeps failing, retrying it on the normal schedule (or worse, immediately) turns a transient problem into continuous hammering of itch.io, which is both rude and likely to make things worse (more 429s, not fewer).

## Decision

**Coverage floor.** `checkCoverageFloor` (`cmd/dataservice/main.go`) compares the crawl's measured coverage against `COVERAGE_FLOOR` (default `0.75`) before `fetchAndStoreAssets` is allowed to proceed to publishing. Below the floor, the run returns an error, publishing never happens, and the previously published index is left completely untouched. The default floor is set deliberately below the ~89% a healthy crawl actually reaches — high enough to reject a genuinely stalled run, low enough that a normal run's natural shortfall never trips it.

**Backoff.** `scheduleCrawls` (`cmd/dataservice/main.go`) tracks `consecutiveFailures` and, on each failure, waits `backoffDelay` (`main.go`) before trying again: 15 minutes, then 1 hour, then 6 hours, then falling back to the normal `CRAWL_INTERVAL` cadence — never waiting *longer* than the configured interval, so a short interval configured for testing is never defeated by an oversized backoff step. A failure that persists past three escalating retries is treated as something a human needs to look at (an itch.io markup change, most likely), not something worth retrying every few hours indefinitely.

## Alternatives considered

- **Publish whatever a crawl collects, unconditionally.** Simplest, but turns "the crawl degraded" into a silent data-quality regression rather than a loud, logged refusal.
- **A fixed coverage floor with no configurability.** Rejected because `SCRAPE_MAX_PAGES` (the smoke-test mode) deliberately produces a small, partial index by design — a fixed floor would make smoke-testing impossible without a code change, so `loadCrawlConfig` disarms the floor to `0` automatically whenever `SCRAPE_MAX_PAGES` is set (`cmd/dataservice/main.go`).
- **Retry a failed crawl immediately, or on a fixed short interval regardless of how many times it has already failed.** Either would turn a persistent failure (not a transient blip) into sustained load against itch.io while a human is presumably still unaware anything is wrong.

## Consequences

- `COVERAGE_FLOOR` must stay below whatever coverage a healthy crawl actually reaches, or *every* run gets rejected — see the comment on the constant and on the `docker-compose.yml`/`.env.example` variable for the currently measured figure. If itch.io's catalogue shape changes such that a healthy crawl's typical coverage drops, this floor may need lowering to match, not the other way around.
- A stalled run's *previous* index keeps being served indefinitely until a run finally clears the floor — there is no separate alerting on "the index is old because crawls keep failing" beyond the backoff schedule's own log lines and whatever `staleAfter`-driven UI warning `templates.Freshness` shows visitors (see `internal/web/templates/freshness.go`). Operationally, watching the log (or `history.json`, via `/stats`) for repeated failures is the way to catch this before a visitor does.
- The three-step backoff schedule is hardcoded, not configurable — deliberately, since it is meant as damage control for an unexpected failure, not a tuning knob.
