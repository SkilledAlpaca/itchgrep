package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func assertSecurityHeadersSet(t *testing.T, h http.Header) {
	t.Helper()
	assert.Equal(t, contentSecurityPolicy, h.Get("Content-Security-Policy"))
	assert.Equal(t, "strict-origin-when-cross-origin", h.Get("Referrer-Policy"))
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "same-origin", h.Get("Cross-Origin-Opener-Policy"))
	assert.Empty(t, h.Get("X-Frame-Options"), "frame-ancestors in the CSP supersedes it")
}

func TestSecurityHeadersSetOnOrdinaryResponse(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	assertSecurityHeadersSet(t, w.Header())
}

// SecurityHeaders is mounted just inside middleware.Recoverer in
// cmd/webserver/main.go, ahead of the probe filter and routing, precisely so
// it still applies to a probe 404 - this pins that behaviour at the unit
// level since main.go's ordering cannot be exercised directly.
func TestSecurityHeadersSetOnProbe404(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/wp-login.php", nil))

	assertSecurityHeadersSet(t, w.Header())
}

func TestSecurityHeadersSetOnStaticAsset(t *testing.T) {
	handler := SecurityHeaders(StaticHandler())

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))

	assertSecurityHeadersSet(t, w.Header())
}
