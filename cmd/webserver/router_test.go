package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/internal/metrics"
	"itchgrep/internal/web"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rateLimitedGroupMarkerPointer identifies web.RateLimitedGroupMarker among
// a route's middleware chain. Comparing func values directly is not legal in
// Go (func is not a comparable type); comparing their reflect.Value.Pointer
// is, and is reliable specifically because RateLimitedGroupMarker is a plain
// top-level function, not a method value or a closure - see its own doc
// comment in internal/web/ratelimit.go for why that distinction matters
// here.
func rateLimitedGroupMarkerPointer() uintptr {
	return reflect.ValueOf(web.RateLimitedGroupMarker).Pointer()
}

// TestEveryRateLimitedRouteHasABreakdownClassification is what
// docs/patterns.md promises a maintainer: adding a rate-limited route
// without a matching internal/metrics classification fails CI, not
// silently under-counts on /stats. The previous version of this test (see
// internal/metrics/metrics_test.go's git history) could not actually keep
// that promise - it asserted a hand-copied slice of route constants against
// itself, so it passed no matter what cmd/webserver/main.go's real router
// contained.
//
// This version builds the actual router newRouter produces - the same
// function main() calls - and walks it with chi.Walk, which is the one
// thing internal/metrics can never do: that package deliberately has no
// dependency on internal/web or chi (see its own package doc, "the
// dependency stays one-directional"), so a router-walking test cannot live
// there. It belongs here, one layer up, where chi and metrics are both
// already in scope.
func TestEveryRateLimitedRouteHasABreakdownClassification(t *testing.T) {
	h := web.NewHandler(cache.NewCache(36), 0, nil)
	limiter := web.NewLimiter()
	r := newRouter(h, metrics.New(), limiter)

	var rateLimitedRoutes []string
	err := chi.Walk(r, func(method, route string, handler http.Handler, mws ...func(http.Handler) http.Handler) error {
		for _, mw := range mws {
			if reflect.ValueOf(mw).Pointer() == rateLimitedGroupMarkerPointer() {
				rateLimitedRoutes = append(rateLimitedRoutes, route)
				break
			}
		}
		return nil
	})
	require.NoError(t, err)
	// A regression here means the marker itself stopped showing up in the
	// walk - chi.Walk's own contract changed, or RateLimitedGroupMarker was
	// dropped from the r.Group in newRouter - either way this test would
	// otherwise pass vacuously by finding nothing to check.
	require.NotEmpty(t, rateLimitedRoutes, "chi.Walk must find the rate-limited group's own routes")

	for _, route := range rateLimitedRoutes {
		c := metrics.New()
		c.RecordRequest(route, false, 200)
		snap := c.Snapshot()
		assert.EqualValues(t, 0, snap.Other, "%s is rate-limited in newRouter but has no metrics.go classification - it will silently under-count on /stats", route)
	}
}

// TestInfraAndNotFoundRoutesAreRateLimited is the regression test for idx
// 31: /sitemap.xml (a hand-built XML document with no edge-cache protection
// against a query-string cache-buster) and the 404 handler (a full templ
// render for literally any unmatched path) used to sit outside the
// rate-limited group entirely, alongside /robots.txt and /opensearch.xml.
// This drives real requests through the actual router and checks the
// limiter's own observable effect - a 429 on the second hit - rather than
// inspecting the middleware chain, so it is independent of
// TestEveryRateLimitedRouteHasABreakdownClassification's reflection-based
// approach above.
func TestInfraAndNotFoundRoutesAreRateLimited(t *testing.T) {
	t.Setenv("RATE_LIMIT_RPS", "1")
	t.Setenv("RATE_LIMIT_BURST", "1")
	h := web.NewHandler(cache.NewCache(36), 0, nil)
	limiter := web.NewLimiter()
	r := newRouter(h, metrics.New(), limiter)

	for i, path := range []string{"/robots.txt", "/opensearch.xml", "/sitemap.xml", "/no-such-page"} {
		// A distinct client per path: the limiter keys on the caller, not the
		// path, so reusing one address across iterations would have the
		// second path's very first request arrive against an already-spent
		// budget from the path before it.
		addr := fmt.Sprintf("10.0.0.%d:1111", i+1)

		first := httptest.NewRecorder()
		req1 := httptest.NewRequest(http.MethodGet, path, nil)
		req1.RemoteAddr = addr
		r.ServeHTTP(first, req1)
		require.NotEqual(t, http.StatusTooManyRequests, first.Code, "%s: the first request must not be limited", path)

		second := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, path, nil)
		req2.RemoteAddr = addr
		r.ServeHTTP(second, req2)
		assert.Equal(t, http.StatusTooManyRequests, second.Code, "%s: a second request from the same client must now be rate limited", path)
	}
}
