# htmx.min.js provenance

This directory's `htmx.min.js` is a vendored, third-party file - it is not
written or maintained by this project. It is embedded into the webserver
binary via `//go:embed static` in `internal/web/static.go` and served at
`/static/htmx.min.js`.

## Version

**1.9.10.** Verified directly from the bundle itself: the minified source
carries a `version:"1.9.10"` field a short way into the file (search for
`version:"` near the top of `htmx.min.js`). Trust this over any version
number written down elsewhere, including in this file - if the two ever
disagree, the string inside the file is the one that is actually running.

## Source

Fetched from [unpkg](https://unpkg.com/), per the commit that vendored it
(`85581c1`, "Refresh the webserver UI and show the tags we index"): "normalize.css,
concrete.css and htmx came from unpkg on every page load" before being
embedded instead. The likely exact URL, matching unpkg's standard
`package@version/path` scheme and the version confirmed above, is:

```
https://unpkg.com/htmx.org@1.9.10/dist/htmx.min.js
```

**UNKNOWN - REQUIRES HUMAN CONFIRMATION:** the exact URL is a reconstruction,
not a recorded fact - the commit that vendored the file did not note the
precise path fetched. Confirm against unpkg (or https://htmx.org/docs/#installing)
before treating it as authoritative.

## Why vendored

This is a self-hosted tool built around a local search index; it has no
business failing to render because a third-party CDN is unreachable. Serving
`htmx.min.js` from `/static/` instead of `https://unpkg.com/...`:

- removes a cross-origin request and its associated DNS/TLS/latency cost from
  every page load;
- keeps the page working with no outbound network access at all, which
  matters for a tool meant to run fully offline against local data;
- removes any risk of the CDN silently serving a different build than the one
  this project tested against (no SRI hash to keep in sync either).

## How to refresh deliberately

htmx is never auto-updated - a new version has to be a deliberate,
reviewed change, since the whole UI's client-side behaviour depends on it.
To pick up a new release:

1. Download the new version's `dist/htmx.min.js`, e.g.
   `https://unpkg.com/htmx.org@<version>/dist/htmx.min.js`.
2. Replace `internal/web/static/htmx.min.js` with the downloaded file.
3. Confirm the new file's own `version:"..."` string (see "Version" above)
   matches the release you meant to fetch - unpkg has been known to redirect
   an unpinned request to a different version than expected.
4. Update the version number in this file to match.
5. Run the full test suite (`go test ./...`) and manually exercise the
   search UI - htmx's breaking changes, when they happen, tend to show up as
   silent swap failures rather than build errors.
