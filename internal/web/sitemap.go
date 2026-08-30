package web

import (
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"net/http"
)

// sitemapMaxAge matches robots.txt's: the set of pages only changes when a
// route does or the tag universe is rediscovered, both rarer than a crawl.
const sitemapMaxAge = 86400

// HandleSitemap serves /sitemap.xml, built per request from the cache's
// bounded tag universe rather than the live index - no bleve query involved,
// so this needs neither the rate-limited group nor an ErrNotReady path: a
// cache that has not loaded yet just has an empty tag list to add to the
// fixed pages.
func (h *handler) HandleSitemap(w http.ResponseWriter, r *http.Request) {
	body, err := templates.NewSitemap(h.cache.TagSlugs()).Render()
	if err != nil {
		// xml.Marshal on this struct only fails on a value it cannot encode,
		// which nothing here supplies - but the response has already promised
		// XML, so a failure here must not fall through to writing one anyway.
		logging.Error("Error encoding /sitemap.xml: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Sitemap temporarily unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", sitemapMaxAge))
	w.Write(body)
}
