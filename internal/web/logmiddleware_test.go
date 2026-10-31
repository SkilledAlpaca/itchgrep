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
)

// captureEvents swaps eventFunc for one that appends every line to a slice
// instead of reaching logging.Event's real file/stdout target, and restores
// the original on cleanup. Test-only seam - see the eventFunc doc comment.
func captureEvents(t *testing.T) *[]string {
	t.Helper()
	lines := &[]string{}
	original := eventFunc
	eventFunc = func(line string) {
		*lines = append(*lines, line)
	}
	t.Cleanup(func() { eventFunc = original })
	return lines
}

func TestAnonymizeIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string
	}{
		{"ipv4", "203.0.113.42", "203.0.113.0"},
		{"ipv6", "2001:db8:1234:5678::1", "2001:db8:1234::"},
		{"ipv4-mapped ipv6", "::ffff:203.0.113.42", "203.0.113.0"},
		{"garbage", "not-an-ip", "unknown"},
		{"empty", "", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AnonymizeIP(tt.ip))
		})
	}
}

func TestTruncateBoundsAnAttackerControlledString(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 256))
	assert.Equal(t, strings.Repeat("a", 10), truncate(strings.Repeat("a", 500), 10))
	assert.Equal(t, "", truncate("anything", 0))
}

func TestTruncateUAKeepsItsOwnLimit(t *testing.T) {
	long := strings.Repeat("a", maxLoggedUA+50)
	assert.Len(t, truncateUA(long), maxLoggedUA)
	assert.Equal(t, "Mozilla/5.0", truncateUA("Mozilla/5.0"))
}

// TestRequestLoggerBoundsAPathologicallyLongPath is the point of
// maxLoggedPath: the UA was already bounded, but the path was not, so a near-
// 1MB request line used to become a near-1MB log line.
func TestRequestLoggerBoundsAPathologicallyLongPath(t *testing.T) {
	lines := captureEvents(t)
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, nil, m))
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	longPath := "/" + strings.Repeat("a", maxLoggedPath+1000)
	req := httptest.NewRequest(http.MethodGet, longPath, nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if assert.Len(t, *lines, 1) {
		var rec accessRecord
		assert.NoError(t, json.Unmarshal([]byte((*lines)[0]), &rec))
		assert.Len(t, rec.Path, maxLoggedPath)
	}
}

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
// mock, because that dispatch behavior is chi's, not something a mock could
// stand in for.
func TestRoutePatternIsOnlyPopulatedAfterDispatch(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, nil, m))
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
	r.Use(RequestLogger(false, nil, m))
	r.Get("/results", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/results?q=pixel", nil))

	snap := m.Snapshot()
	assert.EqualValues(t, 1, snap.Searches, "a query on /results is a search")
}

// TestRequestLoggerEmitsAccessRecordWithoutTheQueryValue is the point of the
// whole redesign: query is a boolean, not the raw string, so a search term
// can never leak into the durable export even though the log still records
// that a search happened.
func TestRequestLoggerEmitsAccessRecordWithoutTheQueryValue(t *testing.T) {
	lines := captureEvents(t)
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, nil, m))
	r.Get("/results", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/results?q=zebrafish", nil)
	req.RemoteAddr = "1.2.3.4:54321"
	r.ServeHTTP(httptest.NewRecorder(), req)

	if assert.Len(t, *lines, 1) {
		var rec accessRecord
		assert.NoError(t, json.Unmarshal([]byte((*lines)[0]), &rec))
		assert.Equal(t, "access", rec.Kind)
		assert.True(t, rec.Query, "the query boolean must still be true")
		assert.Equal(t, "/results", rec.Path)
		assert.Equal(t, "1.2.3.0", rec.IP, "anonymized to the /24")

		out := (*lines)[0]
		assert.False(t, strings.Contains(out, "zebrafish"), "the search term must never reach the log")
		assert.False(t, strings.Contains(out, "1.2.3.4"), "the full client IP must never reach the log")
	}
}
