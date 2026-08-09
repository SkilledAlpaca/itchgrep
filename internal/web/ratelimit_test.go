package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLimiter(rate, burst float64, trustProxy bool) *Limiter {
	return &Limiter{
		buckets:    make(map[string]*bucket),
		rate:       rate,
		burst:      burst,
		trustProxy: trustProxy,
	}
}

func TestLimiterAllowsTheBurstThenRefuses(t *testing.T) {
	l := testLimiter(1, 3, false)

	for i := 0; i < 3; i++ {
		assert.True(t, l.allow("1.2.3.4"), "request %d is within the burst", i+1)
	}
	assert.False(t, l.allow("1.2.3.4"), "the burst is spent")
}

func TestLimiterRefillsOverTime(t *testing.T) {
	l := testLimiter(10, 1, false)
	require.True(t, l.allow("1.2.3.4"))
	require.False(t, l.allow("1.2.3.4"))

	// Rewind the bucket rather than sleeping: at 10/s a single token is 100ms,
	// and a test that sleeps for real is a test that flakes on a loaded machine.
	l.buckets["1.2.3.4"].last = time.Now().Add(-200 * time.Millisecond)
	assert.True(t, l.allow("1.2.3.4"), "tokens must refill with elapsed time")
}

func TestLimiterIsPerClient(t *testing.T) {
	l := testLimiter(1, 1, false)
	require.True(t, l.allow("1.1.1.1"))
	require.False(t, l.allow("1.1.1.1"))
	assert.True(t, l.allow("2.2.2.2"), "one noisy client must not lock out another")
}

func TestLimiterDisabledWhenRateIsZero(t *testing.T) {
	l := testLimiter(0, 0, false)
	for i := 0; i < 50; i++ {
		require.True(t, l.allow("1.2.3.4"))
	}
}

func TestClientIdentityIgnoresProxyHeadersByDefault(t *testing.T) {
	// The headers are attacker-controlled on a directly-exposed server, so
	// honouring them by default would let anyone mint a new identity per
	// request and bypass the limit entirely.
	l := testLimiter(1, 1, false)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	r.Header.Set("X-Forwarded-For", "8.8.8.8")

	assert.Equal(t, "10.0.0.1", l.client(r))
}

func TestClientIdentityUsesProxyHeadersWhenTrusted(t *testing.T) {
	// Behind Cloudflare every request arrives from the tunnel, so without this
	// the entire internet shares one bucket.
	l := testLimiter(1, 1, true)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	assert.Equal(t, "9.9.9.9", l.client(r))

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "10.0.0.1:5000"
	r2.Header.Set("X-Forwarded-For", " 8.8.8.8 , 10.0.0.5")
	assert.Equal(t, "8.8.8.8", l.client(r2), "the left-most entry is the origin client")
}

func TestMiddlewareRefusesWithRetryAfterAndNoStore(t *testing.T) {
	l := testLimiter(1, 1, false)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/search?q=x", nil)
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
