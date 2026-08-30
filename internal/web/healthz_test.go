package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"itchgrep/internal/cache"

	"github.com/stretchr/testify/assert"
)

func TestHandleHealthzGet(t *testing.T) {
	w := httptest.NewRecorder()
	HandleHealthz(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", w.Body.String())
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestHandleHealthzHead(t *testing.T) {
	// net/http strips the body of a HEAD response on the way out, so this just
	// confirms the handler itself doesn't special-case the method.
	w := httptest.NewRecorder()
	HandleHealthz(w, httptest.NewRequest(http.MethodHead, "/healthz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestHealthzIsNeverAProbe(t *testing.T) {
	assert.False(t, isProbe("/healthz"))
}

// TestHandleReadyzAnswersNoDataBeforeTheCacheHasLoaded is /readyz's core
// case: unlike /healthz, /readyz must actually reflect whether a dataset has
// ever loaded, so an operator (or an external monitor) can tell "the process
// is up" apart from "there is something to serve".
func TestHandleReadyzAnswersNoDataBeforeTheCacheHasLoaded(t *testing.T) {
	h := NewHandler(cache.NewCache(36), 0, nil)
	w := httptest.NewRecorder()
	h.HandleReadyz(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no data", w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestHandleReadyzAnswersReadyOnceTheCacheHasLoaded(t *testing.T) {
	h := NewHandler(loadedLikeCache(t), 0, nil)
	w := httptest.NewRecorder()
	h.HandleReadyz(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ready", w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestHandleReadyzHead(t *testing.T) {
	h := NewHandler(loadedLikeCache(t), 0, nil)
	w := httptest.NewRecorder()
	h.HandleReadyz(w, httptest.NewRequest(http.MethodHead, "/readyz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestReadyzIsNeverAProbe(t *testing.T) {
	assert.False(t, isProbe("/readyz"))
}
