package web

import (
	"net/http"
	"time"

	"itchgrep/internal/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"itchgrep/internal/logging"
)

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

			// Called directly here, not from a helper: internal/logging
			// attributes every line via runtime.Caller(2), so a helper in
			// between would make every request line point at the helper
			// instead of at this call site.
			logging.Info("%s %s %s %d %s",
				ClientIP(r, trustProxy), r.Method, r.URL, status, time.Since(start))
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
