package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLimiter(rate, burst float64, trustProxy bool, trustedCIDRs []*net.IPNet) *Limiter {
	return &Limiter{
		buckets:      make(map[string]*bucket),
		rate:         rate,
		burst:        burst,
		trustProxy:   trustProxy,
		trustedCIDRs: trustedCIDRs,
	}
}

func TestLimiterAllowsTheBurstThenRefuses(t *testing.T) {
	l := testLimiter(1, 3, false, nil)

	for i := 0; i < 3; i++ {
		assert.True(t, l.allow("1.2.3.4"), "request %d is within the burst", i+1)
	}
	assert.False(t, l.allow("1.2.3.4"), "the burst is spent")
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l := testLimiter(10, 1, false, nil)
	require.True(t, l.allow("1.2.3.4"))
	require.False(t, l.allow("1.2.3.4"))

	// Rewind the bucket rather than sleeping: at 10/s a single token is 100ms,
	// and a test that sleeps for real is a test that flakes on a loaded machine.
	l.buckets["1.2.3.4"].last = time.Now().Add(-200 * time.Millisecond)
	assert.True(t, l.allow("1.2.3.4"), "tokens must refill with elapsed time")
}

func TestLimiterIsPerClient(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	require.True(t, l.allow("1.1.1.1"))
	require.False(t, l.allow("1.1.1.1"))
	assert.True(t, l.allow("2.2.2.2"), "one noisy client must not lock out another")
}

func TestLimiterDisabledWhenRateIsZero(t *testing.T) {
	l := testLimiter(0, 0, false, nil)
	for i := 0; i < 50; i++ {
		require.True(t, l.allow("1.2.3.4"))
	}
}

// TestLimiterCapsTheBucketMapAtMaxBuckets is the regression test for the bounded-map contract:
// evictLoop alone cannot bound the map against an attacker minting a fresh
// identity every request faster than the 5-minute eviction sweep can reap
// them. Once the map is already at maxBuckets, a brand new identity must
// share the single overflow bucket rather than growing the map further.
func TestLimiterCapsTheBucketMapAtMaxBuckets(t *testing.T) {
	// burst 2, not 1: the first overflow identity's request leaves the shared
	// bucket with one token still in it (burst-1), so a second identity's
	// allow() outcome here is about map growth, not about also racing the
	// budget the sibling test below already covers.
	l := testLimiter(1, 2, false, nil)
	for i := 0; i < maxBuckets; i++ {
		l.buckets[strconv.Itoa(i)] = &bucket{tokens: 1, last: time.Now()}
	}
	require.Len(t, l.buckets, maxBuckets)

	assert.True(t, l.allow("new-identity-1"))
	assert.Len(t, l.buckets, maxBuckets+1, "only the shared overflow bucket should have been added")

	l.allow("new-identity-2")
	assert.Len(t, l.buckets, maxBuckets+1, "a second identity past the cap must not add a second bucket, whatever allow() answers")
}

// TestLimiterOverflowIdentitiesShareOneBudget proves the overflow bucket is
// actually shared, not merely map-bounded: two distinct identities that both
// arrive after the cap draw on the same token, so one draining it also
// limits the other - the correct direction to fail in when the map is this
// close to capacity.
func TestLimiterOverflowIdentitiesShareOneBudget(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	for i := 0; i < maxBuckets; i++ {
		l.buckets[strconv.Itoa(i)] = &bucket{tokens: 1, last: time.Now()}
	}

	assert.True(t, l.allow("overflow-a"), "the shared bucket's only token")
	assert.False(t, l.allow("overflow-b"), "a different identity past the cap draws on the same, now-spent, bucket")
}

func TestClientIdentityIgnoresProxyHeadersByDefault(t *testing.T) {
	// The headers are attacker-controlled on a directly-exposed server, so
	// honoring them by default would let anyone mint a new identity per
	// request and bypass the limit entirely.
	l := testLimiter(1, 1, false, cidrs(t, "10.0.0.0/24"))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	r.Header.Set("X-Forwarded-For", "8.8.8.8")

	assert.Equal(t, "10.0.0.1", l.client(r))
}

func TestClientIdentityUsesProxyHeadersWhenTrusted(t *testing.T) {
	// Behind Cloudflare every request arrives from the tunnel, so without this
	// the entire internet shares one bucket.
	l := testLimiter(1, 1, true, cidrs(t, "10.0.0.0/24"))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	assert.Equal(t, "9.9.9.9", l.client(r))

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:5000"
	r2.Header.Set("X-Forwarded-For", " 8.8.8.8 , 10.0.0.5")
	assert.Equal(t, "8.8.8.8", l.client(r2), "the left-most entry is the origin client")
}

func TestClientIdentityIgnoresProxyHeadersFromAnUntrustedPeer(t *testing.T) {
	// Trusting the header is not enough on its own: the socket address it
	// arrived over also has to be inside a configured network, or a LAN
	// client hitting the published port directly could still mint an
	// identity by simply sending the header itself.
	l := testLimiter(1, 1, true, cidrs(t, "10.0.0.0/24"))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")

	assert.Equal(t, "203.0.113.9", l.client(r), "outside every trusted network, so the header is not honored")
}

func TestMiddlewareRefusesWithRetryAfterAndNoStore(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/results?q=x", nil)
	r.RemoteAddr = "1.2.3.4:1111"
	h.ServeHTTP(first, r)
	require.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	h.ServeHTTP(second, r)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Equal(t, "1", second.Header().Get("Retry-After"))
	// A cached 429 would be served to every other visitor until it expired.
	assert.Equal(t, "no-store", second.Header().Get("Cache-Control"))
}
