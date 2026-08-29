package web

import (
	"encoding/json"
	"net/http"
	"time"

	"itchgrep/internal/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"itchgrep/internal/logging"
)

// eventFunc indirects every JSON record through logging.Event. A var rather
// than a direct call so tests can substitute a capturing function without
// reaching into internal/logging's file/stdout target - test-only seam,
// overridden and restored within the test itself.
var eventFunc = logging.Event

// accessRecord is the JSON shape of one access line. A value type, not
// map[string]any, so field order in the marshaled output is stable and the
// log diffs cleanly.
type accessRecord struct {
	Ts     string `json:"ts"`
	Kind   string `json:"kind"`
	IP     string `json:"ip"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  bool   `json:"query"`
	Route  string `json:"route"`
	Status int    `json:"status"`
	Bytes  int    `json:"bytes"`
	DurMs  int64  `json:"dur_ms"`
	UA     string `json:"ua"`
}

// maxLoggedUA caps the User-Agent field: it is attacker-controlled and
// otherwise unbounded.
const maxLoggedUA = 256

// truncateUA bounds an attacker-controlled header to maxLoggedUA bytes.
// Shared by the access and probe records so both stay bounded the same way.
func truncateUA(ua string) string {
	if len(ua) <= maxLoggedUA {
		return ua
	}
	return ua[:maxLoggedUA]
}

// RequestLogger logs one line per request that reaches routing - which,
// mounted where main.go mounts it, excludes anything ProbeFilter already
// turned away - and feeds the same request into m.RecordRequest so the public
// /stats page and the log are built from a single pass over the request.
//
// Status is captured with chi's middleware.NewWrapResponseWriter rather than
// a hand-rolled wrapper, because it preserves http.Flusher - which templ's
// streaming render depends on - across HTTP/1.1 and HTTP/2, and chi is
// already a dependency.
//
// trustProxy controls where the logged client identity comes from; see
// ClientIP. It is passed in rather than read from the environment here, so
// the limiter and the logger are guaranteed to agree - both read
// TrustProxyHeaders(), sync.OnceValue over the same env var.
func RequestLogger(trustProxy bool, m *metrics.Counters) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, r)

			// Read only after next.ServeHTTP returns: chi populates the route
			// pattern as part of dispatch, which happens inside that call. Empty
			// means the path matched nothing - r.NotFound's handler ran.
			routePattern := chi.RouteContext(r.Context()).RoutePattern()

			status := statusOr200(ww.Status())
			m.RecordRequest(routePattern, r.URL.RawQuery != "", status)

			// Path only, never r.URL - the raw query may carry a search term
			// the visitor typed, and that must never reach the log. The
			// constraint noted above on logging.Info (runtime.Caller(2)
			// attribution) does not apply to logging.Event: it carries no
			// caller tag, so this could be pulled into a helper freely - it
			// stays inline because there is only one call site.
			record := accessRecord{
				Ts:     start.UTC().Format(time.RFC3339Nano),
				Kind:   "access",
				IP:     AnonymizeIP(ClientIP(r, trustProxy)),
				Method: r.Method,
				Path:   r.URL.Path,
				Query:  r.URL.RawQuery != "",
				Route:  routePattern,
				Status: status,
				Bytes:  ww.BytesWritten(),
				DurMs:  time.Since(start).Milliseconds(),
				UA:     truncateUA(r.UserAgent()),
			}
			line, err := json.Marshal(record)
			if err != nil {
				// Never let a marshaling failure take the request down; fall
				// back to the plain logger so the failure is at least visible.
				logging.Warning("failed to marshal access record: %s", err)
			} else {
				eventFunc(string(line))
			}
		})
	}
}

// statusOr200 maps WrapResponseWriter's "nothing sent yet" zero value to the
// status net/http treats a response as carrying once the first byte goes out
// without an explicit WriteHeader.
func statusOr200(status int) int {
	if status == 0 {
		return http.StatusOK
	}
	return status
}
