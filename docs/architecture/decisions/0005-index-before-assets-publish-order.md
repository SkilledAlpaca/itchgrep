# 0005: Publish the index before `assets.json`, always in that order

Status: Accepted (implemented)

## Context

The webserver has no signal, webhook, or IPC channel telling it a new crawl has published — it simply notices that `assets.json`'s modification time is newer than what it currently has loaded (`Cache.IsCacheExpired`, `internal/cache/cache.go`), checked at most once every 5 seconds, and then reloads (`Cache.doRefresh`, `cache.go`). `doRefresh` reads `assets.json` and opens `index.bleve/` as two separate operations, not one atomic snapshot of "the dataset as of one instant."

If the two files could become visible to a reader in either order, there is a real window where the webserver could observe a *freshly written* `assets.json` alongside a *stale or entirely missing* index (or vice versa) — the two would describe different datasets, and search results would silently disagree with the asset list they're supposedly serving.

## Decision

`publishCrawl` (`cmd/dataservice/main.go`) writes the two artifacts in a fixed, non-negotiable order: `storage.PublishIndex()` first, `storage.PutAssets()` second. The doc comment on `publishCrawl` states the invariant this guarantees plainly: *"by the time a webserver notices a fresh asset list, the index it will open already matches it."* Reversed, a webserver could open a fresh asset list against a stale or missing index — the failure this ordering exists to rule out.

This composes with two other decisions that make the ordering actually safe under concurrent access:
- `storage.PublishIndex` (`internal/storage/storage.go`) itself publishes the index via atomic directory renames, so a reader never observes a half-written index directory.
- `storage.PutAssets` goes through `writeAtomic` (`storage.go`) — write to a temp file, `fsync`, then `os.Rename` into place — so a reader never observes a half-written `assets.json` either.

Given both individual writes are atomic, ordering them index-then-assets is what makes the *pair* consistent from any reader's point of view: a reader that sees the new `assets.json` is guaranteed the new index was already fully visible before that moment.

## Alternatives considered

- **Write `assets.json` first, then publish the index.** Would create exactly the inverted failure: a webserver polling `assets.json`'s timestamp could reload against a stale or (worse, on a first-ever publish) nonexistent index.
- **A single atomic transaction covering both.** Not available — they are two different kinds of on-disk artifact (a directory tree vs. a single file), and there's no cross-artifact atomic rename primitive on a normal filesystem. Sequencing single-artifact atomic writes in the right order is the achievable substitute.
- **A separate "ready" marker file, written last, that the webserver waits for instead of `assets.json`'s own timestamp.** Would work, but adds a third artifact and a second thing that has to stay synchronized with the two real ones, for no benefit over simply fixing the order of the two writes that already have to happen.

## Consequences

- Anyone editing `publishCrawl` (or writing a similar publish sequence elsewhere) must preserve this order. A refactor that reorders these two lines "for readability" would silently reintroduce the race this ADR exists to prevent — there is no test that would catch it directly (the webserver's refresh path and the dataservice's publish path are tested separately), so this has to be preserved by whoever reads the comment, not caught by CI.
- `selfHealIndexFormat`'s rebuild path (`cmd/dataservice/selfheal.go`, see [ADR 0008](0008-self-healing-index-rebuild.md)) follows the same order for the same reason, even though it never touches `assets.json` at all (it only republishes the index) — see its own comment for why that's safe.
