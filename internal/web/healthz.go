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
