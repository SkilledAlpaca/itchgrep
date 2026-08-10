package web

import "net/http"

// contentSecurityPolicy is deliberately narrow: every source it allows is
// justified against what the templates actually load, not left open "in
// case". img.itch.zone is where asset thumbnails are hotlinked from (see
// asset_page.templ, asset.ThumbUrl) - itch.io's own CDN, never mirrored here.
// data: is for the inline SVG favicon in layout.templ. Everything else -
// scripts, styles, fetches, forms - is same-origin, because the page ships
// its own JS and CSS rather than pulling from a CDN. frame-ancestors 'none'
// and base-uri 'none' close off clickjacking and base-tag injection with
// nothing legitimate depending on either being open.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' https://img.itch.zone data:; script-src 'self'; style-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// SecurityHeaders sets response headers that cost nothing to apply broadly
// and nothing to get wrong in the direction of "too strict" - none of them
// depend on the route, so they are set once for every response rather than
// duplicated per-handler.
//
// No X-Frame-Options: frame-ancestors in the CSP above already forbids
// framing, and every browser that honours it ignores the older header
// anyway, so setting both would just be a second thing to keep in sync.
//
// No HSTS here. Cloudflare terminates TLS and can set it at the edge; doing
// it again at the origin would apply to the LAN-published plain-HTTP port
// too, and a client that pins HTTPS there would be locking itself out of a
// port that was never meant to serve it.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
