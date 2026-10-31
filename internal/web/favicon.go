package web

import "net/http"

// faviconSVG is the icon layout.templ inlines as a data: URI. Our own pages
// never request /favicon.ico, but anything rendered without our <head> does:
// Cloudflare challenge pages, bots, feed readers. A day-long public cache
// lets Cloudflare's edge answer those (.ico is a default-cached extension)
// instead of the origin rendering its 404 page.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" rx="3" fill="#14161a"/><text x="8" y="12" font-family="monospace" font-size="11" font-weight="bold" text-anchor="middle" fill="#7ee787">&gt;</text></svg>`

// HandleFavicon serves faviconSVG. Registered for both GET and HEAD in
// cmd/webserver/main.go; net/http strips the body of a HEAD response itself.
func HandleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(faviconSVG))
}
