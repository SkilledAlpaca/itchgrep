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

// AnonymizeIP reduces an address to the network the caller sits on: /24 for
// IPv4, /48 for IPv6. Enough to recognize a repeat offender or a sweep from a
// single allocation, not enough to name a visitor. Truncation is chosen over
// a salted hash deliberately: no salt to store, rotate, or leak, and the
// output stays human-readable and greppable - operational simplicity as a
// feature.
//
// ClientIP itself is not changed by this function or anywhere else - the
// rate limiter keys on it and needs full precision. Only log call sites
// anonymize, by calling this on the result of ClientIP.
func AnonymizeIP(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "unknown"
	}
	// To4 first: an IPv4-mapped IPv6 address (::ffff:1.2.3.4) would otherwise
	// mask as IPv6 and lose the /24 semantics the caller expects for it.
	if v4 := parsed.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String()
	}
	return parsed.Mask(net.CIDRMask(48, 128)).String()
}
