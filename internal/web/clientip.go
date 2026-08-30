package web

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"itchgrep/internal/logging"
)

// TrustedProxyCIDRs is read once, alongside TrustProxyHeaders: the networks a
// proxy header is honored from once TRUST_PROXY_HEADERS is true. See
// ClientIP.
//
// Required, not merely advisory, once header trust is turned on. Without this
// gate, TRUST_PROXY_HEADERS=true honored CF-Connecting-IP from any peer that
// could reach the published port at all, including a LAN client hitting the
// origin directly past the reverse proxy - letting it mint a fresh identity
// per request to bypass the rate limiter and poison the access log. Failing
// closed - nothing trusted until a network is named - means turning on header
// trust without configuring who it is extended to does nothing, rather than
// the dangerous thing.
var TrustedProxyCIDRs = sync.OnceValue(func() []*net.IPNet {
	nets := parseTrustedCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if len(nets) == 0 && TrustProxyHeaders() {
		logging.Warning("TRUST_PROXY_HEADERS is true but TRUSTED_PROXY_CIDRS names no valid network: proxy headers will not be trusted from any peer. Set TRUSTED_PROXY_CIDRS to the CIDR(s) requests are actually proxied from.")
	}
	return nets
})

// parseTrustedCIDRs turns a comma-separated CIDR list into parsed networks. A
// malformed entry is skipped and warned about rather than failing startup
// over one typo - the same tolerance config.EnvBool and config.EnvFloat give
// a bad value elsewhere in this package.
func parseTrustedCIDRs(raw string) []*net.IPNet {
	var nets []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(part)
		if err != nil {
			logging.Warning("Ignoring invalid TRUSTED_PROXY_CIDRS entry %q: %v", part, err)
			continue
		}
		nets = append(nets, ipnet)
	}
	return nets
}

// ClientIP identifies the caller. Split out of Limiter.client so the request
// logger can name the same client the rate limiter keys on, rather than a
// second, subtly different derivation of "who sent this."
//
// trustProxy makes the identity come from CF-Connecting-IP or
// X-Forwarded-For instead of the socket address - but only when the request
// actually arrived from one of trustedCIDRs, checked against RemoteAddr,
// which a header can never spoof. Without that check, "trust the header"
// would mean trusting whatever the request itself claims to be, from anyone
// who can open a connection.
//
// Off by default, and that default matters: those headers are attacker
// controlled unless something upstream overwrites them, so trusting them on
// a directly-exposed server lets any client mint a fresh identity per request
// and bypass the limit entirely. Behind Cloudflare the opposite is true -
// every request arrives from the tunnel, so without this the whole internet
// shares one bucket.
func ClientIP(r *http.Request, trustProxy bool, trustedCIDRs []*net.IPNet) string {
	if trustProxy && fromTrustedPeer(r, trustedCIDRs) {
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

// fromTrustedPeer reports whether the request's own socket address - never a
// header, which is exactly what a spoofed identity would arrive in - falls
// inside one of trustedCIDRs.
func fromTrustedPeer(r *http.Request, trustedCIDRs []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trustedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
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
