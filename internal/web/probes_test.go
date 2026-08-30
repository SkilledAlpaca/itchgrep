package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"itchgrep/internal/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveRoutesAreNeverProbes(t *testing.T) {
	for _, path := range []string{
		"/", "/results", "/about", "/stats", "/new", "/feed", "/saved",
		"/sitemap.xml", "/opensearch.xml", "/robots.txt", "/favicon.ico",
		"/static/app.css",
		// Tag pages are a prefix, not one path, so they cannot sit in
		// liveRoutes - this asserts the pattern lists never grow something
		// that would swallow them.
		"/tag/pixel-art",
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
	handler := ProbeFilter(m, false, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestProbeFilterBoundsAPathologicallyLongPath(t *testing.T) {
	lines := captureEvents(t)
	handler := ProbeFilter(metrics.New(), false, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach the wrapped handler")
	}))

	longPath := "/wp-" + strings.Repeat("a", maxLoggedPath+1000) + ".php"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, longPath, nil))

	require.Len(t, *lines, 1)
	var rec probeRecord
	require.NoError(t, json.Unmarshal([]byte((*lines)[0]), &rec))
	assert.Len(t, rec.Path, maxLoggedPath)
}

func TestProbeFilterLetsRealRoutesThrough(t *testing.T) {
	reached := false
	handler := ProbeFilter(metrics.New(), false, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/results?q=pixel", nil))

	assert.True(t, reached)
}

// TestProbeFilterEmitsOneProbeRecordAndNoAccessRecord confirms the split
// promised by the revised ProbeFilter doc comment: a probe gets exactly one
// "probe" kind line from ProbeFilter itself, and never reaches RequestLogger
// - which mounts after ProbeFilter in main.go - so it produces no "access"
// line at all.
func TestProbeFilterEmitsOneProbeRecordAndNoAccessRecord(t *testing.T) {
	lines := captureEvents(t)
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(ProbeFilter(m, false, nil))
	r.Use(RequestLogger(false, nil, m))
	// chi only builds its middleware chain once a route is registered - see
	// Mux.ServeHTTP's mx.handler == nil fast path - so a live route must be
	// present even though this test never requests it.
	r.Get("/results", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/wp-login.php", nil)
	req.RemoteAddr = "1.2.3.4:54321"
	r.ServeHTTP(httptest.NewRecorder(), req)

	var probeKinds, accessKinds int
	for _, line := range *lines {
		var kind struct {
			Kind string `json:"kind"`
		}
		assert.NoError(t, json.Unmarshal([]byte(line), &kind))
		switch kind.Kind {
		case "probe":
			probeKinds++
			var rec probeRecord
			assert.NoError(t, json.Unmarshal([]byte(line), &rec))
			assert.Equal(t, "/wp-login.php", rec.Path)
			assert.Equal(t, "1.2.3.0", rec.IP)
		case "access":
			accessKinds++
		}
	}
	assert.Equal(t, 1, probeKinds, "exactly one probe record")
	assert.Equal(t, 0, accessKinds, "ProbeFilter must short-circuit before RequestLogger runs")
}
