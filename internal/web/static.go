package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS holds the assets the page needs at runtime. They are embedded rather
// than fetched from a CDN: this is a self-hosted tool whose whole point is a
// local index, so it has no business failing to render because unpkg.com is
// unreachable. It also removes three cross-origin requests from every page load
// and any chance of an SRI mismatch when a CDN re-publishes a version.
//
//go:embed static
var staticFS embed.FS

// StaticHandler serves the embedded assets. Mount it at /static/.
//
// The embed rooted at "static" would otherwise serve them as /static/static/x,
// so the directory is stripped from both ends: fs.Sub off the front of the
// filesystem, StripPrefix off the front of the URL.
//
// The files only change when the binary does, so they are cached for an hour
// and busted by rebuilding. Deliberately not "immutable": the paths carry no
// version, so a rebuild has to be able to win.
func StaticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is
		// a build-time mistake rather than a runtime condition.
		panic("web: embedded static directory missing: " + err.Error())
	}
	server := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		server.ServeHTTP(w, r)
	})
}
