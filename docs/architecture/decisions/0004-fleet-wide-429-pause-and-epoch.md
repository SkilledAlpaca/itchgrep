# 0004: Fleet-wide 429 pause with an epoch guard, and a non-adaptive base rate

## Context

The crawler fetches multiple pages concurrently (`maxConcurrentRequests = 8`, `cmd/dataservice/main.go`) against one shared rate limiter (`internal/fetcher/fetcher.go`). Two separate problems had to be solved:

**A 429 has to hold back every worker, not just the one that saw it.** With several requests in flight against one shared limiter, a plain per-request backoff (the worker that got the 429 sleeps, the others don't) just hands that worker's slot to another worker — the aggregate rate offered to itch.io never actually drops, so the 429s keep coming.

**itch.io's 429s are not primarily about request rate at all.** `httpClient`'s own doc comment (`internal/fetcher/fetcher.go`) records the measured finding: itch.io refuses cookieless clients roughly half the time (8 of 16 requests refused, in one measurement) *no matter how slowly they ask*, at the same one-request-per-second pace that saw 0 of 16 refused once a cookie jar was added. An earlier version of the rate limiter halved its own rate on every 429 and crept back up afterward — and that adaptation cost real throughput without reducing refusals, because the refusals were never rate-driven in the first place. It could also ratchet the rate down to a floor that took minutes of clean traffic to recover from, for a problem slowing down did nothing to fix.

## Decision

**The base rate is fixed, not adaptive.** `SCRAPE_RPS` sets a constant request spacing; nothing in the limiter adjusts it based on observed refusals. The actual fix for itch.io's refusals is a cookie jar on the shared `httpClient` (`internal/fetcher/fetcher.go`), which persists the `itchio_token` cookie itch.io sets, across every request in the process.

**A 429 that does happen triggers a fleet-wide pause**, not a per-worker sleep. `rateLimiter.pause` (`internal/fetcher/fetcher.go`) sets a `pausedUntil` deadline that every worker's `acquire` call checks and blocks on (`acquire`, `fetcher.go`), so one 429 holds back the whole crawl for however long is warranted (preferring itch.io's own `Retry-After` header when present, `handleTooManyRequests`, `fetcher.go`).

**An epoch counter prevents pause pile-up.** Every request is tagged with the `epoch` it was issued under (`rateLimiter.epoch`, `fetcher.go`). `pause` only takes effect if the reporting request's epoch still matches the limiter's current epoch (`fetcher.go`) — otherwise it's an echo of an episode already being handled, not fresh cause to extend the pause further. Without this, the several requests already in flight when itch.io starts refusing would each report their own 429 moments apart, and a naive implementation would keep pushing the pause's end time further out on each one, potentially strangling the crawl.

**Refusals are surfaced, not hidden.** `Refusals()` (`fetcher.go`) is a running counter shown in the crawl's progress log, so an operator watching the log sees refusals as a visible number climbing, rather than only unexplained slow progress.

## Alternatives considered

- **Adaptive rate limiting** (halve on 429, recover slowly) — tried previously; measured to cost throughput without reducing refusals, since the refusals are cookie-driven rather than rate-driven. Removed.
- **Per-worker backoff, no shared pause** — the naive approach; does not actually reduce the aggregate request rate when other workers immediately fill the paused worker's slot.
- **No epoch guard, just a single shared `pausedUntil`** — vulnerable to pause pile-up: several near-simultaneous 429s from requests already in flight would each try to extend the pause, potentially compounding into a much longer stall than any single `Retry-After` actually called for.

## Consequences

- If itch.io ever starts refusing for a genuinely rate-driven reason (rather than cookie-driven), this design would under-react — `SCRAPE_RPS` would need to be lowered by hand, since nothing here adapts automatically. This is a deliberate trade, but worth re-measuring if refusal behavior changes.
- Anyone touching `internal/fetcher/fetcher.go`'s rate limiter should read its extensive comments before changing the epoch or pause logic — the reasoning is dense and the failure modes (pause pile-up, throughput collapse) are exactly what a plausible-looking simplification would reintroduce.
- The cookie jar (`newHTTPClient`, `fetcher.go`) is load-bearing, not incidental — removing it (for example, while refactoring the HTTP client) would silently reintroduce roughly half of all requests being refused.
