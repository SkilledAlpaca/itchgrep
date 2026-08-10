package web

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP identifies the caller. Split out of Limiter.client so the request
// logger can name the same client the rate limiter keys on, rather than a
// second, subtly different derivation of "who sent this."
//
// trustProxy makes the identity come from CF-Connecting-IP or
// X-Forwarded-For instead of the socket address.
//
// Off by default, and that default matters: those headers are attacker
// controlled unless something upstream overwrites them, so trusting them on
// a directly-exposed server lets any client mint a fresh identity per request
// and bypass the limit entirely. Behind Cloudflare the opposite is true -
// every request arrives from the tunnel, so without this the whole internet
// shares one bucket.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
			return ip
		}
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// Left-most entry is the original client; the rest are hops.
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
