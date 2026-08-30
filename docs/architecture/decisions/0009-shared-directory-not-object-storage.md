# 0009: A plain shared directory, not an object store

## Context

`internal/storage` used to sit on top of Google Cloud Storage (GCS), with `fake-gcs-server` standing in for it in local development. The package's own header comment (`internal/storage/storage.go`) records exactly what that bought and what it cost: GCS's actual features — ACLs, signed URLs, lifecycle rules, listing — are things this workload never uses, because there is exactly one writer (`dataservice`) and a small, fixed set of named objects nothing ever needs to enumerate. What GCS *did* cost was a container (`fake-gcs-server`) purely to emulate it locally, a network hop on every read and write even in production, and a tar+gzip round-trip on every index publish solely to move a directory across a process boundary that, once both binaries run in the same compose stack sharing a volume, does not actually exist.

The one property GCS genuinely provided — a reader never observes a half-written object — is not unique to object storage. A local filesystem gives the same guarantee for free, via `rename`'s atomicity within one filesystem.

## Decision

`internal/storage` is a plain directory on disk (`DATA_DIR`, default `./data`, `/data` in both containers), shared between `dataservice` and `webserver` via one Docker volume (`docker-compose.yml`'s `itchgrep-data`). Every write goes through `writeAtomic` (`internal/storage/storage.go`): write to a temporary file in the same directory, `fsync` it, then `os.Rename` it into place. This reproduces GCS's one load-bearing guarantee — a concurrent reader sees either the whole old file or the whole new one, never a partial write — using nothing but a filesystem rename, which is atomic within one filesystem by construction. The package's own comment distinguishes this from *crash* durability (surviving a power loss, not just concurrent-reader visibility) and explains why `writeAtomic` also `fsync`s the containing directory, best-effort, to close that separate window.

The published search index — a directory tree, not a single file — gets the equivalent treatment one level up: `storage.PublishIndex` (`storage.go`) renames a whole staging directory into place, using a three-step rename sequence (move the old index aside, move the new one in, delete the old one) specifically because `os.Rename` onto a non-empty existing directory fails outright, and because a failure partway through must leave the *previous* index restored and serving rather than nothing at all.

## Alternatives considered

- **Keep GCS (or an S3-compatible equivalent) for production, plain filesystem only in tests.** This is close to what the previous design already was (production GCS, `fake-gcs-server` for local/tests) — and is exactly what was removed. Maintaining two storage backends means either testing against a fake that can drift from the real service's behavior, or accepting that CI never exercises the real path at all. A single filesystem-backed implementation is what both environments actually run.
- **A real embedded database (SQLite, BoltDB) instead of a directory of files.** Would add a dependency and a schema for data that is already well-served by a handful of independently-readable JSON files and one bleve index directory — none of which benefit from being inside a single database file, and several of which (the index especially) are more naturally their own directory tree anyway.

## Consequences

- This design assumes `dataservice` and `webserver` share one filesystem (one Docker volume, or the same host path) — it does not work across machines without a shared network filesystem, and nothing here provides multi-writer coordination beyond `dataservice`'s own single-crawl-at-a-time guard (`scrapeInProgress`, `cmd/dataservice/main.go`). Scaling to a second `dataservice` instance, or to `webserver` and `dataservice` on separate hosts, would need this decision revisited.
- Backup and restore is "copy the directory" rather than anything object-store-specific — see [disaster-recovery.md](../../disaster-recovery.md) for what in that directory is safe to lose (regenerable) versus not (see [Data lifecycle](../../architecture.md#data-lifecycle)).
- A future maintainer reaching for "we should put this in S3 for durability/scalability" should read this ADR first: the workload this serves has one writer and no need for the properties object storage adds, and reintroducing one would bring back the network hop and archive/extract cost this design removed, for a durability property a nightly volume backup already provides just as well for this workload's actual failure modes.
