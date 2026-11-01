package web

import (
	"container/list"
	"net"
	"net/http"
	"sync"
	"time"

	"itchgrep/internal/config"
	"itchgrep/internal/logging"
)

// TrustProxyHeaders is read once and shared by everything that needs to agree
// on where a client's identity comes from - the limiter and the request
// logger both derive it from the same env var, and a sync.OnceValue is cheaper
// insurance against them drifting than two independent config.EnvBool calls.
var TrustProxyHeaders = sync.OnceValue(func() bool {
	return config.EnvBool("TRUST_PROXY_HEADERS", false)
})

// Rate limiting exists because a search is not a cheap request. Each one runs
// three disjunctions - exact, fuzzy with a 4-character prefix, and fuzzy with a
// 2-character prefix - across four fields over ~86,000 documents. The
// short-prefix fuzzy pass is the expensive one, and nothing else in the request
// path pushes back, so a handful of concurrent queries can saturate a CPU.

// bucket is one client's token bucket, refilled lazily rather than by a timer:
// with one bucket per client IP, a ticker per bucket would cost more than the
// requests it guards.
type bucket struct {
	tokens float64
	last   time.Time
	// elem is this bucket's place in Limiter.order, for O(1) recency updates
	// and eviction.
	elem *list.Element
}

// maxBuckets caps how many distinct identities Limiter tracks at once.
// evictLoop already reaps quiet buckets every 5 minutes, but that is not
// enough on its own against a caller minting a fresh identity every request -
// a spoofed header (before ClientIP validated one), a large IPv6 allocation,
// or simply many distinct real visitors within one eviction window can all
// grow the map faster than eviction shrinks it. 50,000 entries is comfortably
// above any real traffic this site has ever seen and small in memory (a
// *bucket plus a string key, well under a few MB total) - the ceiling exists
// to bound an attacker, not to constrain legitimate use.
const maxBuckets = 50_000

// overflowLogEvery bounds how often a full bucket table is reported. A flood
// that fills the table keeps it full, so every new identity would otherwise
// write a line.
const overflowLogEvery = time.Minute

// Limiter allows burst requests immediately and rate per second thereafter,
// counted per client.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	// order holds the keys of buckets, most recently used first. When the
	// table is full the back one is evicted: O(1), no scan of the map.
	order *list.List

	rate  float64
	burst float64

	// trustProxy makes the client identity come from CF-Connecting-IP or
	// X-Forwarded-For instead of the socket address, when the request also
	// arrived from one of trustedCIDRs - see ClientIP.
	//
	// Off by default, and that default matters: those headers are attacker
	// controlled unless something upstream overwrites them, so trusting them on
	// a directly-exposed server lets any client mint a fresh identity per
	// request and bypass the limit entirely. Behind Cloudflare the opposite is
	// true - every request arrives from the tunnel, so without this the whole
	// internet shares one bucket.
	trustProxy   bool
	trustedCIDRs []*net.IPNet

	// evicted counts buckets dropped to make room since the table filled up,
	// and lastFullLog is when that was last reported. Both are guarded by mu.
	evicted     uint64
	lastFullLog time.Time
}

// NewLimiter reads its configuration from the environment: RATE_LIMIT_RPS
// (default 5), RATE_LIMIT_BURST (default 20), TRUST_PROXY_HEADERS (default
// false) and TRUSTED_PROXY_CIDRS (see ClientIP; required once
// TRUST_PROXY_HEADERS is true). A non-positive rate disables limiting
// entirely.
func NewLimiter() *Limiter {
	l := &Limiter{
		buckets:      make(map[string]*bucket),
		order:        list.New(),
		rate:         config.EnvFloat("RATE_LIMIT_RPS", 5),
		burst:        config.EnvFloat("RATE_LIMIT_BURST", 20),
		trustProxy:   TrustProxyHeaders(),
		trustedCIDRs: TrustedProxyCIDRs(),
	}
	if l.rate <= 0 {
		logging.Warning("RATE_LIMIT_RPS is %v: rate limiting is disabled", l.rate)
		return l
	}
	logging.Info("Rate limit: %.1f req/s, burst %.0f, trusting proxy headers: %v (%d trusted network(s))",
		l.rate, l.burst, l.trustProxy, len(l.trustedCIDRs))
	go l.evictLoop()
	return l
}

// evictLoop drops buckets for clients that have gone quiet, so the map cannot
// grow without bound on a public site.
func (l *Limiter) evictLoop() {
	for range time.Tick(5 * time.Minute) {
		cutoff := time.Now().Add(-10 * time.Minute)
		l.mu.Lock()
		for k, b := range l.buckets {
			if b.last.Before(cutoff) {
				l.drop(k, b)
			}
		}
		l.mu.Unlock()
	}
}

// client identifies the caller for limiting purposes. Kept as a method - the
// derivation itself lives in ClientIP - so ratelimit_test.go keeps working
// unchanged.
func (l *Limiter) client(r *http.Request) string {
	return limiterKey(ClientIP(r, l.trustProxy, l.trustedCIDRs))
}

// limiterKey is the bucket an address draws from. An IPv4 address is its own
// bucket. An IPv6 address is reduced to its /64: a single subscriber is
// normally handed at least that much and can vary the low 64 bits at will, so
// keying on the full address would let one machine mint a fresh bucket per
// request - which is how the table gets flooded in the first place. The access
// log anonymizes separately (AnonymizeIP) and is unaffected.
func limiterKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// allow reports whether this client may make a request now.
func (l *Limiter) allow(key string) bool {
	if l.rate <= 0 {
		return true
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.order == nil {
		l.order = list.New()
	}
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxBuckets {
			// The table is full: make room by evicting the bucket used least
			// recently, and give this identity a fresh one. Not a shared
			// overflow bucket, which a flood of minted identities could drain
			// to lock real newcomers out; and not failing open, which would
			// let those identities through unlimited.
			//
			// An evicted identity that comes back gets a fresh full bucket,
			// which is the price of a bounded table. It cannot be exploited
			// to exceed the rate: to be evicted an identity must go
			// unused while maxBuckets others are touched, and each of those
			// is itself limited at the same rate.
			if oldest := l.order.Back(); oldest != nil {
				k := oldest.Value.(string)
				l.drop(k, l.buckets[k])
			}
			l.noteFull(now)
		}
		b = &bucket{tokens: l.burst - 1, last: now}
		b.elem = l.order.PushFront(key)
		l.buckets[key] = b
		return true
	}
	l.order.MoveToFront(b.elem)

	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// drop removes one bucket from the table. Caller holds l.mu.
func (l *Limiter) drop(key string, b *bucket) {
	delete(l.buckets, key)
	if b != nil && b.elem != nil {
		l.order.Remove(b.elem)
	}
}

// noteFull records an eviction forced by a full table and, at most once per
// overflowLogEvery, says so. Caller holds l.mu.
func (l *Limiter) noteFull(now time.Time) {
	l.evicted++
	if now.Sub(l.lastFullLog) < overflowLogEvery {
		return
	}
	l.lastFullLog = now
	logging.Warning("Rate limiter table is full (%d identities): evicting the least recently used bucket for each new identity (%d evictions so far)", maxBuckets, l.evicted)
}

// RateLimitedGroupMarker is a no-op middleware - it changes nothing about
// how a request is handled. Its only purpose is to sit alongside
// Limiter.Middleware in cmd/webserver's rate-limited route group, as a
// landmark a test can look for: chi.Walk exposes each route's middleware
// chain, but a *Limiter.Middleware method value is a closure with no
// pointer identity reliable enough to compare across two otherwise-equal
// receivers, whereas a plain top-level function like this one has exactly
// one address for the whole program. See cmd/webserver's router_test.go,
// which walks the real router built by newRouter and uses this marker to
// find every route that must carry a metrics classification.
func RateLimitedGroupMarker(next http.Handler) http.Handler {
	return next
}

// Middleware rejects requests over the limit with 429 and a Retry-After.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(l.client(r)) {
			w.Header().Set("Retry-After", "1")
			// Never cache a rejection: an edge cache that stored this would
			// serve it to everyone until it expired.
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
