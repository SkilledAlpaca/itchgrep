package main

import (
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/config"
	"itchgrep/internal/logging"
	"itchgrep/internal/metrics"
	"itchgrep/internal/web"
	"net/http"
	"os"
	"strconv"
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
// disk. There is no graceful shutdown today, so a restart loses at most this
// much - worth noting rather than adding signal handling for this alone.
const trafficSnapshotInterval = 5 * time.Minute

func initializeCache() *cache.Cache {
	pageSizeStr := os.Getenv("PAGE_SIZE")
	pageSize, err := strconv.ParseInt(pageSizeStr, 10, 64)
	logging.Info("PAGE_SIZE: %v", pageSize)
	if err != nil {
		logging.Error("Invalid PAGE_SIZE, defaulting to 36: %s", pageSizeStr)
		pageSize = 36
	}
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
	// LOGGING
	logging.Init("", true)

	// CACHE INIT
	cache := initializeCache()

	// TRAFFIC COUNTERS
	// Restored from disk so the totals on /stats survive a restart; see
	// internal/metrics for why this is counters rather than log parsing.
	m := metrics.Restore()
	m.StartSnapshotter(trafficSnapshotInterval)

	// HANDLERS
	r := chi.NewRouter()
	r.Use(middleware.Recoverer) // outermost: a panic anywhere below still answers something
	// Just inside Recoverer, so it covers everything else in the chain too -
	// probe 404s, static assets, the rate limiter's own 429s - rather than
	// only the routes that happen to render a template.
	r.Use(web.SecurityHeaders)
	// Ahead of routing and ahead of the logger, so a scanner sweep never
	// reaches either: no log line per probe, and no entry in chi's route
	// table to match against. See internal/web/probes.go for why this exists
	// alongside the Cloudflare WAF rule rather than instead of it - it is the
	// backstop for whatever reaches the origin directly.
	r.Use(web.ProbeFilter(m))
	r.Use(web.RequestLogger(web.TrustProxyHeaders(), m))

	// The same variable the dataservice schedules from, so the masthead can say
	// when the next rebuild is due. Read here rather than guessed from the gap
	// between publications: a hand-triggered crawl would otherwise teach the
	// page a schedule nothing is running on.
	h := web.NewHandler(cache, config.CrawlInterval(), m)
	// The stylesheet and htmx are embedded in the binary rather than pulled
	// from a CDN, so the page renders with no outbound network access. Mounted
	// before the rate limiter: these are static, cheap, and cached at the edge,
	// so counting them against a visitor's budget would only punish first loads.
	r.Handle("/static/*", web.StaticHandler())

	// Outside the rate-limited group like /static/*: a crawler fetching this
	// once per visit is not the traffic the limiter exists to shed, and
	// internal/web/probes.go already allowlists the path so it never gets
	// mistaken for scanner noise.
	r.Get("/robots.txt", web.HandleRobots)
	r.Head("/robots.txt", web.HandleRobots)
	r.Get("/.well-known/security.txt", web.HandleSecurityTxt)
	r.Head("/.well-known/security.txt", web.HandleSecurityTxt)

	r.Group(func(r chi.Router) {
		r.Use(web.NewLimiter().Middleware)
		r.Get("/", h.HandleIndex)
		r.Get("/results", h.HandleResults)
		r.Get("/about", h.HandleAbout)
		r.Get("/stats", h.HandleStats)
		// chi routes by method, so without this a HEAD is a 405 - and HEAD is
		// what uptime monitors, link checkers and preview fetchers send first.
		// net/http discards the body of a HEAD response itself, so the same
		// handler serves both and the headers stay identical.
		r.Head("/", h.HandleIndex)
		r.Head("/about", h.HandleAbout)
		r.Head("/stats", h.HandleStats)
	})
	r.NotFound(web.Handle404)

	// SERVER
	port := fmt.Sprintf(":%s", os.Getenv("PORT"))
	if port == ":" {
		port = ":8080" // Default port to listen on
	}
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
	}
	if err := srv.ListenAndServe(); err != nil {
		logging.Fatal("Server failed: %v", err)
	}
}
