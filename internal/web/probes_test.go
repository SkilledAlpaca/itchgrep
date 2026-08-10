package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"itchgrep/internal/metrics"

	"github.com/stretchr/testify/assert"
)

func TestLiveRoutesAreNeverProbes(t *testing.T) {
	for _, path := range []string{
		"/", "/results", "/about", "/stats", "/robots.txt", "/favicon.ico",
		"/static/app.css",
	} {
		assert.False(t, isProbe(path), "%s must never be filtered", path)
	}
}

func TestCapturedScannerPathsAreProbes(t *testing.T) {
	for _, path := range []string{
		"/wp-login.php",
		"/xmlrpc.php",
		"/alfa.php",
		"/wp-includes/js/",
		"/wp-content/plugins/hellopress/wp_filemanager.php",
		"/cgi-bin/",
		"/vendor/",
	} {
		assert.True(t, isProbe(path), "%s must be filtered", path)
	}
}

func TestProbeMatchingIsCaseInsensitive(t *testing.T) {
	assert.True(t, isProbe("/WP-LOGIN.PHP"))
}

func TestOrdinaryUnknownPathsAreNotProbes(t *testing.T) {
	// A 404 for a path that is merely unknown, rather than shaped like a
	// scanner sweep, must still reach the styled Handle404 - not this filter.
	assert.False(t, isProbe("/no-such-page"))
	assert.False(t, isProbe("/results/extra"))
}

func TestProbeFilterAnswersBareTextPlain404(t *testing.T) {
	m := metrics.New()
	handler := ProbeFilter(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach the wrapped handler")
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/wp-login.php", nil))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	// RecordProbe is internal-only and has no public getter (see
	// internal/metrics); what matters here is that ProbeFilter does not touch
	// the request counters that do feed the public page.
	assert.EqualValues(t, 0, m.Snapshot().Total)
}

func TestProbeFilterLetsRealRoutesThrough(t *testing.T) {
	reached := false
	handler := ProbeFilter(metrics.New())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/results?q=pixel", nil))

	assert.True(t, reached)
}
