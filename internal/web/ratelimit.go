package web

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"itchgrep/internal/logging"
)

// TrustProxyHeaders is read once and shared by everything that needs to agree
// on where a client's identity comes from - the limiter and the request
// logger both derive it from the same env var, and a sync.OnceValue is cheaper
// insurance against them drifting than two independent envBool calls.
var TrustProxyHeaders = sync.OnceValue(func() bool {
	return envBool("TRUST_PROXY_HEADERS", false)
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
}

// Limiter allows burst requests immediately and rate per second thereafter,
// counted per client.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rate  float64
	burst float64

	// trustProxy makes the client identity come from CF-Connecting-IP or
	// X-Forwarded-For instead of the socket address.
	//
	// Off by default, and that default matters: those headers are attacker
	// controlled unless something upstream overwrites them, so trusting them on
	// a directly-exposed server lets any client mint a fresh identity per
	// request and bypass the limit entirely. Behind Cloudflare the opposite is
	// true - every request arrives from the tunnel, so without this the whole
	// internet shares one bucket.
	trustProxy bool
}

// NewLimiter reads its configuration from the environment: RATE_LIMIT_RPS
// (default 5), RATE_LIMIT_BURST (default 20) and TRUST_PROXY_HEADERS (default
// false). A non-positive rate disables limiting entirely.
func NewLimiter() *Limiter {
	l := &Limiter{
		buckets:    make(map[string]*bucket),
		rate:       envFloat("RATE_LIMIT_RPS", 5),
		burst:      envFloat("RATE_LIMIT_BURST", 20),
		trustProxy: TrustProxyHeaders(),
	}
	if l.rate <= 0 {
		logging.Warning("RATE_LIMIT_RPS is %v: rate limiting is disabled", l.rate)
		return l
	}
	logging.Info("Rate limit: %.1f req/s, burst %.0f, trusting proxy headers: %v",
		l.rate, l.burst, l.trustProxy)
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
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}

// client identifies the caller for limiting purposes. Kept as a method - the
// derivation itself lives in ClientIP - so ratelimit_test.go keeps working
// unchanged.
func (l *Limiter) client(r *http.Request) string {
	return ClientIP(r, l.trustProxy)
}

// allow reports whether this client may make a request now.
func (l *Limiter) allow(key string) bool {
	if l.rate <= 0 {
		return true
	}

	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

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

func envFloat(name string, def float64) float64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}
