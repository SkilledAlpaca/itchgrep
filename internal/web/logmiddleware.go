package web

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"itchgrep/internal/metrics"
	"itchgrep/pkg/money"

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

// maxLoggedPath caps the Path field the same way: it too is attacker
// controlled, and MaxHeaderBytes on the server (see cmd/webserver) bounds the
// headers but not the request line's path, so without this a near-1MB
// request line becomes a near-1MB log line.
const maxLoggedPath = 512

// truncate bounds an attacker-controlled string to max bytes. Shared by the
// UA and path fields of both the access and probe records so all four stay
// bounded the same way.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// truncateUA bounds an attacker-controlled header to maxLoggedUA bytes.
func truncateUA(ua string) string {
	return truncate(ua, maxLoggedUA)
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
// trustProxy and trustedCIDRs control where the logged client identity comes
// from; see ClientIP. Both are passed in rather than read from the
// environment here, so the limiter and the logger are guaranteed to agree -
// all three read TrustProxyHeaders() and TrustedProxyCIDRs(), sync.OnceValues
// over the same env vars.
func RequestLogger(trustProxy bool, trustedCIDRs []*net.IPNet, m *metrics.Counters) func(http.Handler) http.Handler {
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
			m.RecordRequest(routePattern, isSearchRequest(r, routePattern), status)

			// Path only, never r.URL - the raw query may carry a search term
			// the visitor typed, and that must never reach the log. The
			// constraint noted above on logging.Info (runtime.Caller(2)
			// attribution) does not apply to logging.Event: it carries no
			// caller tag, so this could be pulled into a helper freely - it
			// stays inline because there is only one call site.
			record := accessRecord{
				Ts:     start.UTC().Format(time.RFC3339Nano),
				Kind:   "access",
				IP:     AnonymizeIP(ClientIP(r, trustProxy, trustedCIDRs)),
				Method: r.Method,
				Path:   truncate(r.URL.Path, maxLoggedPath),
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

// isSearchRequest reports whether r asks for the first page of results narrowed
// by something the visitor chose: a query or at least one filter (tags,
// exclusions, similar-to, author, price). Later pages are the same search
// continued, and a request carrying only a currency or a sort is the same
// results re-displayed, so neither counts. Only the presence of a value is
// read; it is not stored.
//
// Two refinements. A /tag/{slug} page is a filter in its path, so it counts
// whatever its query string says (HandleTag ignores q). And an htmx request
// whose narrowing parameters equal those of the page it was made from
// (HX-Current-URL) is that page re-displayed with another currency or sort,
// not a new search; the comparison goes through the handlers' own parsing and
// ignores order, so it agrees with what they would show. A full-page load
// carries no such header and keeps the plain rule.
func isSearchRequest(r *http.Request, routePattern string) bool {
	q := r.URL.Query()
	if p := q.Get("page"); p != "" && p != "1" {
		return false
	}
	if routePattern == "/tag/{slug}" {
		return true
	}
	key := narrowingKey(q)
	if key == "" {
		return false
	}
	if r.Header.Get("HX-Request") == "true" {
		if cur, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && cur.RawQuery != "" && narrowingKey(cur.Query()) == key {
			return false
		}
	}
	return true
}

// narrowingKey reduces a query string to what narrows the catalog - the query,
// tags, exclusions, similar-to, author and price - parsed exactly as the
// handlers parse them and with the lists sorted, so two spellings of the same
// narrowing compare equal. Empty means nothing narrows.
func narrowingKey(v url.Values) string {
	f := parseFilterValues(v, money.Fallback())
	tags := append([]string(nil), f.Tags...)
	not := append([]string(nil), f.NotTags...)
	sort.Strings(tags)
	sort.Strings(not)
	key := strings.Join([]string{f.Query, strings.Join(tags, ","), strings.Join(not, ","), f.Like, f.Author, f.Price}, "\x00")
	if key == strings.Repeat("\x00", 5) {
		return ""
	}
	return key
}
