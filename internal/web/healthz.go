package web

import "net/http"

// HandleHealthz answers a container/orchestrator liveness probe: 200 the
// moment the router is up, with no dependency on the cache having loaded.
// The cache can legitimately stay unready for hours on a first run (see
// initializeCache in cmd/webserver/main.go) and /results already reports that
// with its own 503 - a liveness check that failed on the same condition would
// have Docker restart-loop a webserver that is working exactly as designed.
//
// Cache-Control: no-store because a health check answer must never be served
// stale from anything in front of this origin.
func HandleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok"))
}

// HandleReadyz is HandleHealthz's opposite number: it DOES depend on the
// cache, answering 200 "ready" only once a published dataset has actually
// loaded, and 503 "no data" for as long as it has not (see
// cache.ErrNotReady, and initializeCache in cmd/webserver/main.go, for why
// that can legitimately last hours on a first run).
//
// Deliberately not wired into docker-compose.yml's healthcheck: that stays
// /healthz precisely so a webserver working as designed - up, but still
// waiting on the first crawl - is never restart-looped, and so a freshly
// started `--profile public` stack's cloudflared (gated on
// service_healthy) still comes up while the crawl is in progress. /readyz
// exists for an operator, or an external uptime monitor, that wants to tell
// "the process is up" apart from "the site actually has something to serve".
//
// Mounted beside /healthz, outside the rate-limited group and inside
// probes.go's liveRoutes (so it is never mistaken for a scanner probe) - the
// same treatment /healthz gets, for the same reason: a monitoring probe must
// never compete with real visitors for rate-limit budget.
func (h *handler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if h.cache.DataUpdatedTime().IsZero() || !h.cache.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("no data"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
