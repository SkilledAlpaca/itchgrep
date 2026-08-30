package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cidrs parses a handful of CIDR literals for a test's trustedCIDRs, failing
// the test immediately on a typo rather than silently running against an
// empty list.
func cidrs(t *testing.T, literals ...string) []*net.IPNet {
	t.Helper()
	var nets []*net.IPNet
	for _, l := range literals {
		_, n, err := net.ParseCIDR(l)
		if err != nil {
			t.Fatalf("bad CIDR literal %q in test setup: %v", l, err)
		}
		nets = append(nets, n)
	}
	return nets
}

func TestClientIPHonorsTheHeaderFromInsideATrustedCIDR(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")

	got := ClientIP(r, true, cidrs(t, "10.0.0.0/24"))
	assert.Equal(t, "9.9.9.9", got, "the socket address is inside the trusted network")
}

func TestClientIPIgnoresTheHeaderFromOutsideEveryTrustedCIDR(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")

	got := ClientIP(r, true, cidrs(t, "10.0.0.0/24"))
	assert.Equal(t, "203.0.113.9", got, "outside the trusted network, so the socket address stands")
}

func TestClientIPIgnoresTheHeaderWhenTheTrustedListIsEmpty(t *testing.T) {
	// Secure by default: TRUST_PROXY_HEADERS=true with nothing configured in
	// TRUSTED_PROXY_CIDRS must trust nobody, not everybody.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")

	got := ClientIP(r, true, nil)
	assert.Equal(t, "10.0.0.1", got)
}

func TestClientIPIgnoresTheHeaderWhenProxyTrustIsOff(t *testing.T) {
	// Even from inside a trusted network, the header is only honored once
	// TRUST_PROXY_HEADERS itself is on.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")

	got := ClientIP(r, false, cidrs(t, "10.0.0.0/24"))
	assert.Equal(t, "10.0.0.1", got)
}

func TestClientIPFallsBackToXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", " 8.8.8.8 , 10.0.0.5")

	got := ClientIP(r, true, cidrs(t, "10.0.0.0/24"))
	assert.Equal(t, "8.8.8.8", got, "the left-most entry is the original client")
}

func TestClientIPPrefersCFConnectingIPOverXForwardedFor(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	r.Header.Set("X-Forwarded-For", "8.8.8.8")

	got := ClientIP(r, true, cidrs(t, "10.0.0.0/24"))
	assert.Equal(t, "9.9.9.9", got)
}

func TestParseTrustedCIDRsSkipsMalformedEntries(t *testing.T) {
	// One typo in the list must not take down every entry beside it - the
	// same tolerance envBool and envFloat give a bad env var elsewhere.
	got := parseTrustedCIDRs("10.0.0.0/24, not-a-cidr ,192.168.1.0/24,")

	require.Len(t, got, 2)
	assert.Equal(t, "10.0.0.0/24", got[0].String())
	assert.Equal(t, "192.168.1.0/24", got[1].String())
}

func TestParseTrustedCIDRsOfAnEmptyStringIsEmpty(t *testing.T) {
	assert.Empty(t, parseTrustedCIDRs(""))
}
