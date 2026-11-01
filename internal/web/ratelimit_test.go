package web

import (
	"container/list"
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
		order:        list.New(),
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

// fill puts n flood entries in the table, oldest first.
func fill(l *Limiter, n int) {
	for i := 0; i < n; i++ {
		b := &bucket{tokens: 0, last: time.Now()}
		b.elem = l.order.PushFront("flood-" + strconv.Itoa(i))
		l.buckets["flood-"+strconv.Itoa(i)] = b
	}
}

// With the table full of flood entries a real newcomer evicts the least
// recently used one and gets its own full budget, limited at its own rate.
func TestLimiterGivesANewcomerItsOwnBudgetWhenTheTableIsFull(t *testing.T) {
	l := testLimiter(1, 3, false, nil)
	fill(l, maxBuckets)

	for i := 0; i < 3; i++ {
		assert.True(t, l.allow("newcomer"), "request %d is within its own burst", i+1)
	}
	assert.False(t, l.allow("newcomer"), "and then it is limited at its own rate")
	assert.Len(t, l.buckets, maxBuckets, "memory stays bounded")
	assert.Equal(t, maxBuckets, l.order.Len())
	assert.NotContains(t, l.buckets, "flood-0", "the least recently used entry made room")
	assert.Contains(t, l.buckets, "flood-"+strconv.Itoa(maxBuckets-1))
}

// A flood of distinct keys never lets one key exceed its rate. An evicted key
// that returns gets a fresh bucket, the price of a bounded table; it cannot be
// used to go faster than the rate, because evicting a key takes maxBuckets
// other identities, each limited at the same rate.
func TestLimiterAFloodOfKeysDoesNotLetOneKeyExceedItsRate(t *testing.T) {
	l := testLimiter(1, 2, false, nil)
	fill(l, maxBuckets)
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.allow("steady") {
			allowed++
		}
		for j := 0; j < 100; j++ {
			l.allow("flood-" + strconv.Itoa(i*100+j))
		}
	}
	assert.Equal(t, 2, allowed, "the burst, and no refill within the test's instant")
	assert.LessOrEqual(t, len(l.buckets), maxBuckets)
}

func TestLimiterTableNeverExceedsMaxBuckets(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	fill(l, maxBuckets)
	for i := 0; i < 2000; i++ {
		l.allow("new-" + strconv.Itoa(i))
	}
	assert.Len(t, l.buckets, maxBuckets)
	assert.Equal(t, maxBuckets, l.order.Len(), "the recency list tracks the map exactly")
	assert.Equal(t, uint64(2000), l.evicted)
}

func TestLimiterEvictsTheLeastRecentlyUsedNotTheOldestInserted(t *testing.T) {
	l := testLimiter(1, 5, false, nil)
	fill(l, maxBuckets)
	l.allow("flood-0") // touch the oldest-inserted: now most recently used

	l.allow("newcomer")
	assert.Contains(t, l.buckets, "flood-0")
	assert.NotContains(t, l.buckets, "flood-1")
}

func TestLimiterReportsAFullTableAtMostOncePerMinute(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	now := time.Now()

	l.noteFull(now)
	first := l.lastFullLog
	require.Equal(t, now, first)

	l.noteFull(now.Add(30 * time.Second))
	assert.Equal(t, first, l.lastFullLog, "no second report inside the interval")

	l.noteFull(now.Add(61 * time.Second))
	assert.Equal(t, now.Add(61*time.Second), l.lastFullLog)
	assert.Equal(t, uint64(3), l.evicted, "every eviction is still counted")
}

func TestIPv6ClientsShareABucketPerSlash64(t *testing.T) {
	l := testLimiter(1, 1, false, nil)
	key := func(addr string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		return l.client(r)
	}

	a := key("[2001:db8:1:2::1]:5000")
	assert.Equal(t, a, key("[2001:db8:1:2:ffff:ffff:ffff:ffff]:5000"), "same /64")
	assert.NotEqual(t, a, key("[2001:db8:1:3::1]:5000"), "a different /64 is a different client")
	assert.Equal(t, "192.0.2.7", key("192.0.2.7:5000"), "IPv4 stays per address")
	assert.NotEqual(t, key("192.0.2.7:5000"), key("192.0.2.8:5000"))

	require.True(t, l.allow(a))
	assert.False(t, l.allow(key("[2001:db8:1:2::2]:1")), "the neighbor in the same /64 draws on the same budget")
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
