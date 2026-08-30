# 0007: Visitor query text is never logged; IP addresses are truncated, not hashed

## Context

itchgrep is a public search engine — every request potentially carries a visitor-typed search query, and every request carries a client IP address. Both are the kind of data that is easy to log "just in case it's useful for debugging" and easy to regret having logged later. This project draws an explicit, narrow line about what is retained, stated precisely so it can be checked against the code rather than taken on faith.

## Decision

**Query text and other free-text filter values are never logged, anywhere, in any form.** `internal/cache/search.go`'s `describe` function — the one thing that turns a search into a log line (`internal/cache/search.go:586-612`) — records only *whether* a query was present (`"query present"`) and *whether* an author filter was set (`"author filter present"`), never the actual text of either. Everything else `describe` logs (tags, price, sort, page) comes from a fixed vocabulary or the site's own UI controls, not free-typed text, so logging those values as-is carries no comparable risk. `internal/web/logmiddleware.go`'s access log follows the identical rule at the HTTP layer: it logs `r.URL.Path` and a boolean `Query bool` (whether `r.URL.RawQuery` was non-empty), and explicitly never logs `r.URL` itself or the raw query string — see the comment directly on that field in `RequestLogger` (`logmiddleware.go:96-101`): *"the raw query may carry a search term the visitor typed, and that must never reach the log."*

**IP addresses are truncated to a network prefix before being logged, never logged at full precision, and never hashed.** `AnonymizeIP` (`internal/web/clientip.go:109-130`) masks an IPv4 address to its `/24` and an IPv6 address to its `/48` — enough precision to recognize a repeat offender or a sweep originating from one allocation, not enough to identify an individual visitor. Every place that writes an IP to a log — `RequestLogger`'s access line and `ProbeFilter`'s probe line — calls `AnonymizeIP(ClientIP(...))`, never `ClientIP(...)` directly. `AnonymizeIP`'s own comment states the reasoning for truncation over a salted hash explicitly: *"no salt to store, rotate, or leak, and the output stays human-readable and greppable — operational simplicity as a feature."*

**The one place a *full-precision* IP is used at all is the in-memory rate limiter**, which needs it to key one bucket per client (`Limiter.client` → `ClientIP`, `internal/web/ratelimit.go:99-101`) — and that value is never persisted or logged; it lives only in the limiter's in-memory map and is evicted after 10 minutes of inactivity (`evictLoop`, `ratelimit.go:83-94`).

**The public `/stats` page is built from aggregate counters, never from parsing logs.** `internal/metrics`'s own package doc comment (`internal/metrics/metrics.go:1-17`) states the reasoning directly: counters "cannot leak what they never store" — `models.Traffic` (`pkg/models/models.go:298-315`) holds only route-level hit counts and an hourly search-count histogram, no addresses, no user agents, no query text, by construction of the struct itself rather than by discipline at the point of rendering.

## Alternatives considered

- **Hash IP addresses instead of truncating them.** Considered and rejected — see `AnonymizeIP`'s own comment. A hash needs a salt to resist a rainbow-table lookup of every possible IPv4 address (a small, fully enumerable space), and that salt then becomes its own thing to generate, store, rotate, and avoid leaking. Truncation gives a comparable "can I recognize a repeat visitor from the same network" signal with none of that operational burden.
- **Log full query text, but only for a short retention window, to help debug a bad search result.** Rejected outright — there is no code path that does this even transiently. The chosen alternative (a boolean "query present") gives enough signal to see that a slow or empty search *had* a query, and to correlate its shape (page, sort, filters) with performance, without ever holding the text itself.

## Consequences

- Debugging a specific visitor's bad search experience from the logs alone is not possible — the log will show that a query was present and roughly when and how it was filtered, but never what was typed. This is accepted as the cost of the privacy posture, not an oversight; a bug report from a visitor who shares what they searched for is the intended way to reproduce such an issue.
- Any new logging call site added in the future (a new handler, a new middleware) has to be checked against this rule by hand — there is no automated test enforcing "no raw query string reaches `logging.Event`." Follow the existing pattern (`truncate`/`truncateUA` and the explicit `Path`-only, never-`URL` comment in `logmiddleware.go`) rather than reaching for `r.URL.String()`.
- `AnonymizeIP` is a one-way, lossy transform with no way to recover the original address from a log line — this is deliberate, and means the logs themselves cannot answer "exactly who made this request," only "roughly what network."
