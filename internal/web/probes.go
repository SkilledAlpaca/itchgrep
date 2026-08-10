package web

import (
	"net/http"
	"strings"

	"itchgrep/internal/metrics"
)

// isProbeSuffixes and isProbePrefixes are what a commodity PHP/WordPress
// shell scanner asks for - captured from real sweeps against this origin.
// Mirrors the Cloudflare WAF custom rule that blocks the same traffic at the
// edge; this is the backstop for whatever reaches the origin directly, such
// as the LAN-published port. Two plain slices and HasPrefix/HasSuffix,
// deliberately no regexp, so the list stays short enough to audit against the
// routing table by eye.
var (
	isProbeSuffixes = []string{".php", ".phtml", ".asp", ".aspx", ".jsp", ".cgi"}
	isProbePrefixes = []string{
		"/wp-", "/wordpress", "/cgi-bin/", "/vendor/", "/uploads/",
		"/phpmyadmin", "/phpinfo", "/admin", "/administrator", "/xmlrpc",
		"/autodiscover", "/.env", "/.git", "/.aws", "/.ssh",
	}
)

// liveRoutes is checked first and wins outright, so "does this ever block a
// real route" is a three-line test rather than an argument: none of the rules
// below can match these anyway, but the guard makes that a guarantee instead
// of an inference.
var liveRoutes = map[string]bool{
	"/":            true,
	"/results":     true,
	"/about":       true,
	"/stats":       true,
	"/robots.txt":  true,
	"/favicon.ico": true,
}

// isProbe reports whether path matches the shape of the scanner sweep.
func isProbe(path string) bool {
	if liveRoutes[path] || strings.HasPrefix(path, "/static/") {
		return false
	}
	path = strings.ToLower(path)
	for _, suffix := range isProbeSuffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	for _, prefix := range isProbePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// ProbeFilter answers a probe with a bare 404 before it reaches routing, the
// logger, or the rate limiter.
//
// Deliberately not a tarpit and does not charge a rate-limiter token. A
// tarpit inverts the server's own WriteTimeout/IdleTimeout protections into
// an attacker-controlled FD-exhaustion vector; charging a token means taking
// the limiter's mutex for a request that now costs nothing to answer.
//
// No templ render and no per-request log line - the log would drown in it,
// which is the problem this exists to solve. m.RecordProbe() feeds the
// aggregate line the snapshot ticker emits instead.
func ProbeFilter(m *metrics.Counters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				m.RecordProbe()
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("404 page not found"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
