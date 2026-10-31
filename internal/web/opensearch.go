package web

import (
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"net/http"
)

// openSearchMaxAge matches robots.txt's: this document depends on nothing
// but the site's own address, so it changes at the pace of a deploy, not a
// crawl.
const openSearchMaxAge = 86400

// HandleOpenSearch serves /opensearch.xml, the descriptor a browser uses to
// offer this site as a search engine. Static like robots.txt - it depends on
// neither the request nor the loaded dataset - so it needs no handler
// receiver.
func HandleOpenSearch(w http.ResponseWriter, r *http.Request) {
	body, err := templates.NewOpenSearch().Render()
	if err != nil {
		// See sitemap.go's HandleSitemap for why this is checked despite
		// nothing here being able to trigger it.
		logging.Error("Error encoding /opensearch.xml: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "OpenSearch descriptor temporarily unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/opensearchdescription+xml; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", openSearchMaxAge))
	w.Write(body)
}
