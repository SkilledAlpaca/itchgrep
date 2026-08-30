package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
