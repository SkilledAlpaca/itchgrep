package web

import (
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// HandleTag serves /tag/{slug}: the index page pre-filtered to one tag, so
// crawlers have a clean, indexable path per tag now that query-string URLs
// are disallowed (see robots.go). It renders exactly what HandleIndex would
// for ?tags={slug} - same masthead, same sidebar, same everything - because
// a tag page is that filter, just reached by a path a crawler is allowed to
// follow instead of a query string one is not.
//
// In-page filter links (the sidebar, the removable chips) keep their normal
// /?tags=... form; this route exists for the inbound link, not the outbound
// ones.
func (h *handler) HandleTag(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "slug")
	// normaliseTag both validates (charset, length) and lowercases in one
	// pass, so an unknown-shaped slug and an unknown-cased one are told apart
	// the same way parseFilters already tells a bad ?tags= value from a good
	// one.
	slug := normaliseTag(raw)
	if slug == "" || !h.cache.HasTag(slug) {
		// Covers both a typo/probe (normaliseTag empties it or HasTag misses)
		// and a syntactically valid slug this dataset has simply never heard
		// of - neither is a page worth a 301 to.
		Handle404(w, r)
		return
	}
	if raw != slug {
		// A real tag, just not in the one form worth indexing or bookmarking -
		// canonicalise rather than serve two URLs for the same page.
		http.Redirect(w, r, "/tag/"+slug, http.StatusMovedPermanently)
		return
	}

	filters := templates.Filters{Tags: []string{slug}}
	page, ok := pageParam(r)
	if !ok {
		http.Error(w, "Invalid page", http.StatusBadRequest)
		return
	}

	canonical := templates.BaseURL + "/tag/" + slug
	results, err := h.find(filters, page)
	if err != nil {
		logging.Error("Error rendering /tag/%s: %s", slug, err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusFor(err))
		templates.LayoutHead(pageTitle(filters), templates.CanonicalLink(canonical),
			templates.Index(filters, templates.Results{Page: page}, h.freshness(), h.coverage())).Render(r.Context(), w)
		return
	}

	h.cacheable(w)
	templates.LayoutHead(pageTitle(filters), templates.CanonicalLink(canonical),
		templates.Index(filters, results, h.freshness(), h.coverage())).Render(r.Context(), w)
}
