package web

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"itchgrep/internal/metrics"

	"itchgrep/internal/logging"
)

// probeRecord is the JSON shape of one probe line. A value type, not
// map[string]any, for the same field-order-stability reason as accessRecord.
// No status or duration: a probe is always answered with a bare 404,
// immediately, so those fields would be constant.
type probeRecord struct {
	Ts     string `json:"ts"`
	Kind   string `json:"kind"`
	IP     string `json:"ip"`
	Method string `json:"method"`
	Path   string `json:"path"`
	UA     string `json:"ua"`
}

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
	"/":               true,
	"/results":        true,
	"/about":          true,
	"/stats":          true,
	"/new":            true,
	"/feed":           true,
	"/saved":          true,
	"/sitemap.xml":    true,
	"/opensearch.xml": true,
	"/robots.txt":     true,
	"/favicon.ico":    true,
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
// No templ render, and it still does get an aggregate count via
// m.RecordProbe() rather than a per-request one from RecordRequest - the
// public /stats page must not turn into a scanner-sweep counter. It does now
// get its own per-request JSON line, on a "probe" kind the host-side splitter
// routes to a separate file from the access stream, so the access log stays
// clean while the probe traffic itself remains fully recorded.
//
// trustProxy and trustedCIDRs are threaded through to ClientIP for the same
// reason as RequestLogger: the limiter, the access log, and this filter must
// all agree on where a client's identity comes from.
func ProbeFilter(m *metrics.Counters, trustProxy bool, trustedCIDRs []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				m.RecordProbe()

				record := probeRecord{
					Ts:     time.Now().UTC().Format(time.RFC3339Nano),
					Kind:   "probe",
					IP:     AnonymizeIP(ClientIP(r, trustProxy, trustedCIDRs)),
					Method: r.Method,
					Path:   truncate(r.URL.Path, maxLoggedPath),
					UA:     truncateUA(r.UserAgent()),
				}
				line, err := json.Marshal(record)
				if err != nil {
					logging.Warning("failed to marshal probe record: %s", err)
				} else {
					eventFunc(string(line))
				}

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
