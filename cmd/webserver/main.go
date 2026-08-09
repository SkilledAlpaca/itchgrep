package main

import (
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/web"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// initialCacheLoadRetryInterval is how often we retry the initial cache
// load in the background if it fails at startup, so a transient/unreachable
// bucket doesn't permanently degrade the server (it just serves 503 until
// the load succeeds).
const initialCacheLoadRetryInterval = 15 * time.Second

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logging.Info("%s %s %s", r.RemoteAddr, r.Method, r.URL)
		next.ServeHTTP(w, r)
	})
}

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

	// HANDLERS
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(logMiddleware)

	h := web.NewHandler(cache)
	// The stylesheet and htmx are embedded in the binary rather than pulled
	// from a CDN, so the page renders with no outbound network access.
	r.Handle("/static/*", web.StaticHandler())
	r.Get("/", h.HandleIndex)
	r.Get("/assets/{page}", h.HandleGetAssetPage)
	r.Post("/query/{page}", h.HandleQuery)
	r.Get("/about", h.HandleAbout)
	r.NotFound(web.Handle404)

	// SERVER
	port := fmt.Sprintf(":%s", os.Getenv("PORT"))
	if port == ":" {
		port = ":8080" // Default port to listen on
	}
	logging.Info("Server started at port %s", port)

	http.ListenAndServe(port, r)
}
