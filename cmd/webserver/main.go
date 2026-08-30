// Command webserver is the public-facing binary: it loads the dataset
// cmd/dataservice publishes (see internal/cache), serves the search UI and
// its supporting pages over HTTP (see internal/web), and tracks the
// no-PII traffic counters shown on /stats (see internal/metrics). It never
// crawls itch.io itself - that is cmd/dataservice's job - and reads and
// writes only the shared data directory the two binaries agree on (see
// internal/storage).
package main

import (
	"context"
	"flag"
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/config"
	"itchgrep/internal/logging"
	"itchgrep/internal/metrics"
	"itchgrep/internal/web"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// initialCacheLoadRetryInterval is how often we retry the initial cache load in
// the background if it fails at startup. On a first run there is nothing to load
// until the crawl publishes hours later, so startup must not depend on it: the
// server comes up, answers 503, and starts serving the moment the data lands.
const initialCacheLoadRetryInterval = 15 * time.Second

// trafficSnapshotInterval is how often the traffic counters are written to
// disk on the normal timer. A clean stop (SIGTERM, SIGINT) now writes one
// final snapshot on the way out and loses nothing; only a hard kill (docker
// kill -9, an OOM) still loses up to this much.
const trafficSnapshotInterval = 5 * time.Minute

// resolvePort reads PORT the same way the server and the -healthcheck flag
// both need to, so the two can never drift onto different ports.
func resolvePort() string {
	port := fmt.Sprintf(":%s", os.Getenv("PORT"))
	if port == ":" {
		port = ":8080" // Default port to listen on
	}
	return port
}

// checkHealthz is what the "-healthcheck" flag runs: a GET against the
// server's own /healthz, since the distroless runtime image has no shell and
// no curl for docker-compose.yml's healthcheck stanza to exec instead. A
// short client timeout rather than context.Background() - a healthcheck that
// can hang as long as the server does defeats the point of having one.
func checkHealthz(port string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1%s/healthz", port))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

// pageHandler is the subset of *web.NewHandler's return value newRouter
// needs. Declared here, rather than naming the concrete type directly,
// because that type is unexported (see web.NewHandler) - this interface is
// what lets router_test.go call newRouter with the exact same real handler
// main() uses, real cache included, without cmd/webserver ever needing to
// spell web's internal type.
type pageHandler interface {
	HandleIndex(w http.ResponseWriter, r *http.Request)
	HandleResults(w http.ResponseWriter, r *http.Request)
	HandleAbout(w http.ResponseWriter, r *http.Request)
	HandleStats(w http.ResponseWriter, r *http.Request)
	HandleNew(w http.ResponseWriter, r *http.Request)
	HandleFeed(w http.ResponseWriter, r *http.Request)
	HandleTag(w http.ResponseWriter, r *http.Request)
	HandleSaved(w http.ResponseWriter, r *http.Request)
	HandleSitemap(w http.ResponseWriter, r *http.Request)
	HandleReadyz(w http.ResponseWriter, r *http.Request)
}

// newRouter builds the full route table and middleware stack, exactly as
// main() used to build it inline. Pulled out into its own function so a test
// can walk the real router chi.Walk sees in production - see
// router_test.go's TestEveryRateLimitedRouteHasABreakdownClassification,
// which is the reason this exists: a hand-copied route list beside
// internal/metrics' own constants could drift from this file silently, and
// did exactly that kind of drift risk before this refactor.
func newRouter(h pageHandler, m *metrics.Counters, limiter *web.Limiter) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // outermost: a panic anywhere below still answers something
	// Just inside Recoverer, so it covers everything else in the chain too -
	// probe 404s, static assets, the rate limiter's own 429s - rather than
	// only the routes that happen to render a template.
	r.Use(web.SecurityHeaders)
	// Ahead of routing and ahead of the logger, so a scanner sweep never
	// reaches either: no entry in chi's route table to match against, and no
	// "access" log line - only its own "probe" one (see below). It still
	// draws on limiter, though: an unmatched path can never be a member of
	// the r.Group below (chi never runs a group's middleware for a path that
	// matches nothing in it), so the only way to charge a probe the same
	// token a real request pays is here, directly. See internal/web/probes.go
	// for why this exists alongside the Cloudflare WAF rule rather than
	// instead of it - it is the backstop for whatever reaches the origin
	// directly.
	r.Use(web.ProbeFilter(m, web.TrustProxyHeaders(), web.TrustedProxyCIDRs(), limiter))
	r.Use(web.RequestLogger(web.TrustProxyHeaders(), web.TrustedProxyCIDRs(), m))

	// Outside the rate-limited group like every route above: a liveness
	// probe must never come back 429, or Docker reads a busy server as a dead
	// one and restarts it into the exact traffic spike that made it busy.
	r.Get("/healthz", web.HandleHealthz)
	r.Head("/healthz", web.HandleHealthz)
	// Readiness, not liveness - see HandleReadyz's own doc comment for why
	// this is a separate endpoint from /healthz and deliberately NOT wired
	// into docker-compose.yml's healthcheck: stanza. Outside the rate-limited
	// group for the same reason /healthz is: a monitoring probe must never
	// compete with a real visitor for budget.
	r.Get("/readyz", h.HandleReadyz)
	r.Head("/readyz", h.HandleReadyz)

	// Everything else is rate-limited - /healthz and /readyz above are the
	// only two exceptions in the whole router. This used to also exclude
	// /static/*, /sitemap.xml and the 404 handler as individually
	// "cheap enough not to matter"; sitemap.xml turned out not to be (a
	// hand-built XML document, one entry per tag, with no edge-cache
	// protection against a query-string cache-buster) and the 404 handler
	// renders a full templ page for literally any unmatched path - both
	// unauthenticated and unbounded. Rather than re-litigate "which route is
	// cheap enough to skip" every time a new one is added, everything short
	// of a liveness/readiness probe now shares the same budget.
	r.Group(func(r chi.Router) {
		r.Use(limiter.Middleware)

		// The stylesheet and htmx are embedded in the binary rather than
		// pulled from a CDN, so the page renders with no outbound network
		// access. Not tagged with RateLimitedGroupMarker below: it is rate
		// limited like everything else in this group, but
		// internal/metrics deliberately buckets it under its own routeStatic
		// constant rather than the page-view set router_test.go checks.
		r.Handle("/static/*", web.StaticHandler())
		r.Get("/robots.txt", web.HandleRobots)
		r.Head("/robots.txt", web.HandleRobots)
		r.Get("/.well-known/security.txt", web.HandleSecurityTxt)
		r.Head("/.well-known/security.txt", web.HandleSecurityTxt)
		r.Get("/sitemap.xml", h.HandleSitemap)
		r.Head("/sitemap.xml", h.HandleSitemap)
		r.Get("/opensearch.xml", web.HandleOpenSearch)
		r.Head("/opensearch.xml", web.HandleOpenSearch)
		// NotFound set from inside this group, not at the root: chi.Mux.NotFound
		// wraps the handler in whichever Mux's own middleware chain it is
		// called from (see chi's mux.go), so calling it here - rather than on
		// the outer r after this Group returns - is what actually puts the
		// limiter ahead of every unmatched path, not just the routes above.
		r.NotFound(web.Handle404)

		// Page views proper: rate-limited like everything else in this
		// group, and additionally tagged with RateLimitedGroupMarker, which
		// changes nothing about how a request is handled (see its own doc
		// comment) - it exists only so router_test.go's
		// TestEveryRateLimitedRouteHasABreakdownClassification can find
		// exactly these routes, the ones internal/metrics gives a named
		// /stats bucket to, and fail if a new one is added without one.
		r.Group(func(r chi.Router) {
			r.Use(web.RateLimitedGroupMarker)
			r.Get("/", h.HandleIndex)
			r.Get("/results", h.HandleResults)
			r.Get("/about", h.HandleAbout)
			r.Get("/stats", h.HandleStats)
			r.Get("/new", h.HandleNew)
			r.Get("/feed", h.HandleFeed)
			r.Get("/tag/{slug}", h.HandleTag)
			// Rate-limited like every other page even though it renders no
			// query: the saved list itself never reaches the server, but the
			// request for the page shell does, and it costs a template render
			// like any other route in this group.
			r.Get("/saved", h.HandleSaved)
			// chi routes by method, so without this a HEAD is a 405 - and HEAD is
			// what uptime monitors, link checkers and preview fetchers send first.
			// net/http discards the body of a HEAD response itself, so the same
			// handler serves both and the headers stay identical.
			r.Head("/", h.HandleIndex)
			r.Head("/about", h.HandleAbout)
			r.Head("/stats", h.HandleStats)
			r.Head("/new", h.HandleNew)
			r.Head("/feed", h.HandleFeed)
			r.Head("/tag/{slug}", h.HandleTag)
			r.Head("/saved", h.HandleSaved)
		})
	})

	return r
}

// initializeCache builds the cache and attempts one synchronous load before
// main starts the router. If that load fails - the normal case on a brand
// new deployment, where nothing has crawled yet - it does not block startup:
// it hands back an empty cache and keeps retrying in the background, so the
// server comes up and answers 503 (cache.ErrNotReady) rather than hanging
// until the first crawl finishes hours later.
func initializeCache() *cache.Cache {
	pageSizeStr := os.Getenv("PAGE_SIZE")
	pageSize, err := strconv.ParseInt(pageSizeStr, 10, 64)
	if err != nil {
		logging.Error("Invalid PAGE_SIZE, defaulting to 36: %s", pageSizeStr)
		pageSize = 36
	}
	// Logged after the parse/fallback above, not before: the point of this
	// line is to record the value the cache is actually built with, and
	// logging the raw (possibly failed) parse result first meant the one
	// line an operator greps for could read "PAGE_SIZE: 0" immediately above
	// "defaulting to 36" - reporting a number the server never used.
	logging.Info("PAGE_SIZE: %v", pageSize)
	c := cache.NewCache(pageSize)

	if err := c.RefreshDataCache(); err != nil {
		logging.Error("Initial cache load failed, will retry in background every %v: %v", initialCacheLoadRetryInterval, err)
		// Do not block startup / ListenAndServe on this — Cloud Run enforces
		// a startup deadline. Requests get 503 (cache.ErrNotReady) until a
		// retry succeeds.
		go func() {
			ticker := time.NewTicker(initialCacheLoadRetryInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := c.RefreshDataCache(); err != nil {
					logging.Error("Retrying initial cache load failed: %v", err)
					continue
				}
				logging.Info("Initial cache load succeeded")
				return
			}
		}()
	}

	return c
}

func main() {
	// -healthcheck is what docker-compose.yml's healthcheck stanza execs
	// (distroless has no shell or curl to do this from outside the binary).
	// Checked before anything else in main - logging, cache load, the real
	// listener - so it never pays for any of that and never touches the
	// logging package's global state that the real server run below does.
	healthcheck := flag.Bool("healthcheck", false, "check this process's own /healthz over HTTP and exit 0 or 1, instead of starting the server")
	flag.Parse()
	if *healthcheck {
		if err := checkHealthz(resolvePort(), 3*time.Second); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	// LOGGING
	logging.Init()

	// CACHE INIT
	cache := initializeCache()

	// TRAFFIC COUNTERS
	// Restored from disk so the totals on /stats survive a restart; see
	// internal/metrics for why this is counters rather than log parsing.
	m := metrics.Restore()
	m.StartSnapshotter(trafficSnapshotInterval)

	// HANDLERS
	// Built once and shared between ProbeFilter and the rate-limited group
	// inside newRouter, so a scanner sweep and a legitimate visitor from the
	// same client draw against one budget rather than two independent ones.
	limiter := web.NewLimiter()
	// The same variable the dataservice schedules from, so the masthead can say
	// when the next rebuild is due. Read here rather than guessed from the gap
	// between publications: a hand-triggered crawl would otherwise teach the
	// page a schedule nothing is running on.
	h := web.NewHandler(cache, config.CrawlInterval(), m)
	r := newRouter(h, m, limiter)

	// SERVER
	port := resolvePort()
	logging.Info("Server started at port %s", port)

	// Explicit timeouts rather than http.ListenAndServe's defaults, which are
	// none at all: a client that opens a connection and then sends its request
	// one byte at a time holds a goroutine and a file descriptor indefinitely.
	// A reverse proxy absorbs most of that, but the origin should not depend on
	// one being there.
	//
	// WriteTimeout is generous because a result page renders 36 cards and the
	// first request after a new index publishes also waits on the cache reload.
	srv := &http.Server{
		Addr:              port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// net/http defaults to 1MB of request headers, which no legitimate
		// request here needs - a search query, a tag slug and a handful of
		// cookies fit comfortably under this, and a smaller ceiling is that
		// much less an attacker-controlled request line can turn into before
		// truncation even gets a chance to bound what reaches the log (see
		// maxLoggedPath, maxLoggedUA in internal/web/logmiddleware.go).
		MaxHeaderBytes: 64 << 10,
	}

	// Cancelled on SIGINT (Ctrl-C locally) or SIGTERM (what `docker stop` and
	// `docker compose down` actually send) so a stop asks for a graceful
	// shutdown instead of every in-flight request being cut off mid-response.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		// The listener itself failed (e.g. the port is already taken) - never
		// reached on a normal shutdown, since Shutdown below makes
		// ListenAndServe return http.ErrServerClosed instead.
		if err != nil && err != http.ErrServerClosed {
			logging.Fatal("Server failed: %v", err)
		}
	case <-ctx.Done():
		logging.Info("Shutdown signal received, draining connections...")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Best-effort past this point - Shutdown only fails if the
			// deadline hit before every connection finished draining, and the
			// process is exiting either way.
			logging.Error("Graceful shutdown did not finish cleanly: %v", err)
		}

		// Written once, on the way out, so a clean stop never loses a count -
		// see trafficSnapshotInterval above.
		if err := m.PersistNow(); err != nil {
			logging.Error("Failed to persist traffic counters on shutdown: %v", err)
		}
		logging.Info("Shutdown complete")
	}
}
