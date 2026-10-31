package web

import (
	"net/http"
	"strings"

	"itchgrep/internal/web/static"
)

// The assets are embedded (package static) rather than fetched from a CDN:
// this is a self-hosted tool whose whole point is a local index, so it has no
// business failing to render because unpkg.com is unreachable. It also removes
// three cross-origin requests from every page load and any chance of an SRI
// mismatch when a CDN re-publishes a version.

const (
	// immutableCache is for fingerprinted paths: the hash is the content, so
	// the bytes behind that URL can never change.
	immutableCache = "public, max-age=31536000, immutable"
	// bareCache is for the unversioned names and stale hashes (old HTML still
	// cached somewhere, a hand-typed URL). Short, because those bytes change on every release.
	bareCache = "public, max-age=300"
)

// StaticHandler serves the embedded assets. Mount it at /static/.
//
// Templates link /static/app.<hash>.css, and a matching hash is cached for a
// year. That is what fixes a deploy changing HTML, CSS and JS together while a
// CDN or browser still holds the old CSS: new HTML names new URLs, so there is
// nothing stale to hit. The bare /static/app.css still works for HTML cached
// from before this scheme, but is never long-cached. So does a well-formed hash
// that is no longer current: Cloudflare and browsers keep serving HTML for a few
// minutes after a deploy, and that HTML names the previous hashes. They get the
// current bytes on the short cache rather than a 404 and an unstyled page, and
// never the immutable header, so the wrong hash is not pinned for a year.
func StaticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, immutable, ok := static.Resolve(strings.TrimPrefix(r.URL.Path, "/static/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if immutable {
			w.Header().Set("Cache-Control", immutableCache)
		} else {
			w.Header().Set("Cache-Control", bareCache)
		}
		http.ServeFileFS(w, r, static.FS, name)
	})
}
