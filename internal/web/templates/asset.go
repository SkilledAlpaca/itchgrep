package templates

import "itchgrep/internal/web/static"

// Asset returns the fingerprinted URL for an embedded static file, e.g.
// /static/app.3f2a9c1b7d04.css. The hash changes whenever the file's bytes do,
// so a deploy that changes HTML and CSS together always points the new HTML at
// a URL no cache has seen. The handler is StaticHandler in internal/web.
func Asset(name string) string { return static.URL(name) }
