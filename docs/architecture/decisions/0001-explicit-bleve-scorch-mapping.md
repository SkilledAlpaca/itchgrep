# 0001: Explicit bleve field mapping, not dynamic defaults

## Context

bleve, the full-text search library this project indexes into, will happily index a document with no mapping at all: it discovers fields dynamically and, for each one, stores three things by default — a stored copy of the raw value, a "doc values" column (needed for sorting/faceting), and a copy of the field's terms folded into a composite `_all` field that a query naming no specific field can search against.

None of that is free. `internal/storage/index.go`'s own comment records the measured cost: applied to every field of a document repeated across roughly 97,000 assets, those three defaults were most of a ~740MB index. Every query this project actually issues (`internal/cache/search.go`) names its own field explicitly — nothing ever searches `_all` — and a hit is resolved back to a `models.Asset` through an in-memory map keyed by game ID (`internal/cache/cache.go`'s `dataMap`), never by reading the stored copy back out of the index.

## Decision

`internal/storage/index.go`'s `IndexMapping` turns both global defaults off (`m.StoreDynamic = false`, `m.DocValuesDynamic = false`) and disables the `_all` composite field outright. Every field that needs to be searchable, sortable, or facetable at all gets an explicit mapping entry, built from one of three small constructors (`text()`, `kw()`, `num()`), each one already defaulting `Store` and `DocValues` off. A field turns a property back on only when a comment on that exact line names the specific query in `internal/cache/search.go` that needs it — for example, `TagSlugs` gets `DocValues = true` because the sidebar's tag facet reads it, and `PriceUSD` gets both `DocValues` and indexing because it is both range-filtered and sorted by.

`IndexDynamic` is deliberately left at its default (on): a field that falls through with no explicit entry is still findable via the dynamic mapping, rather than silently invisible.

## Alternatives considered

- **Leave the dynamic defaults on.** Simpler to write, but pays the ~740MB cost for data nothing reads, and — worse — makes "does this field need doc values" an implicit question nobody has to answer, so a later regression (a doc-values column added by accident) would be invisible in the index mapping and only show up as index bloat someone has to notice and diagnose.
- **Turn dynamic defaults off but map every field with everything enabled**, rather than deriving each field's flags from the query that needs it. This would still leave `_all`, stored copies, and unused doc-values columns wherever someone forgot to think about it, since "off unless I remember to turn it on for a reason" is exactly the property that keeps the index small.

## Consequences

- A field added to `models.IndexedAsset` (`pkg/models/models.go`) without a matching entry in `IndexMapping` still gets indexed — via `IndexDynamic`'s default — but with none of the three optimizations, and it is unsearchable-with-the-usual-query-shape until someone writes the entry. This is a deliberate fail-safe: **unsearchable is a bug someone notices quickly; a silently reintroduced stored copy is not.**
- Anyone adding a field to the index has to know *which* mapping constructor to start from and *why* — see [patterns.md#add-a-field-to-the-index](../../patterns.md#add-a-field-to-the-index) for the recipe, and follow the existing comments' habit of naming the specific query that justifies each flag.
- The mapping is a contract between two processes that never talk to each other directly: `dataservice` writes documents shaped by it, `webserver` queries fields that only behave correctly because of it. It is deliberately owned by `internal/storage` (imported by both) rather than duplicated, so the two processes cannot drift apart on what a field means.
