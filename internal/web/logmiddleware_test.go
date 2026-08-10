package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"itchgrep/internal/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
)

func TestStatusZeroIsTreatedAsOK(t *testing.T) {
	// A handler that never calls WriteHeader still answers 200 once bytes go
	// out - WrapResponseWriter's Status() stays 0 in that case, and the log
	// line must not claim a status nothing ever sent.
	assert.Equal(t, http.StatusOK, statusOr200(0))
	assert.Equal(t, http.StatusNotFound, statusOr200(http.StatusNotFound))
}

// TestRoutePatternIsOnlyPopulatedAfterDispatch is the one non-obvious
// assumption the design leans on: chi.RouteContext(r.Context()).RoutePattern()
// reads as "" until routing has actually matched a handler, which happens
// inside next.ServeHTTP - so reading it after that call returns, as
// RequestLogger does, sees the real pattern for a matched route and "" for
// one that fell through to NotFound. Exercised against a real chi.Mux, not a
// mock, because that dispatch behaviour is chi's, not something a mock could
// stand in for.
func TestRoutePatternIsOnlyPopulatedAfterDispatch(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, m))
	r.Get("/results", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/results", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-such-path", nil))

	snap := m.Snapshot()
	assert.EqualValues(t, 1, snap.Results, "the matched route classifies by its own pattern")
	assert.EqualValues(t, 2, snap.Total, "an unmatched path is still counted, just not by route")
}

func TestRequestLoggerRecordsAgainstTheSharedCounters(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, m))
	r.Get("/results", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/results?q=pixel", nil))

	snap := m.Snapshot()
	assert.EqualValues(t, 1, snap.Searches, "a query on /results is a search")
}
