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
// In-page filter links (the removable chips, the card tags) keep their normal
// /?tags=... form and are rel="nofollow". The one exception is the sidebar on
// an unfiltered page, whose tag links point here so a crawler has a clean path
// to follow instead of the query-string space.
func (h *handler) HandleTag(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "slug")
	// normalizeTag both validates (charset, length) and lowercases in one
	// pass, so an unknown-shaped slug and an unknown-cased one are told apart
	// the same way parseFilters already tells a bad ?tags= value from a good
	// one.
	slug := normalizeTag(raw)
	if slug == "" || !h.cache.HasTag(slug) {
		// Covers both a typo/probe (normalizeTag empties it or HasTag misses)
		// and a syntactically valid slug this dataset has simply never heard
		// of - neither is a page worth a 301 to.
		Handle404(w, r)
		return
	}
	if raw != slug {
		// A real tag, just not in the one form worth indexing or bookmarking -
		// canonicalize rather than serve two URLs for the same page.
		http.Redirect(w, r, "/tag/"+slug, http.StatusMovedPermanently)
		return
	}

	filters := templates.Filters{Tags: []string{slug}}
	page, ok := pageParam(r)
	if !ok {
		h.badPage(w, r, "/tag/"+slug)
		return
	}

	canonical := templates.BaseURL + "/tag/" + slug
	results, err := h.find(filters, page, true)
	if err != nil {
		logging.Error("Error rendering /tag/%s: %s", slug, err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusFor(err))
		templates.LayoutHead(templates.PageTitle(filters, ""), templates.CanonicalLink(canonical), h.searchShell(),
			templates.Index(filters, templates.Results{Page: page})).Render(r.Context(), w)
		return
	}

	h.cacheableFor(w, page, results)
	// Past the last page, "back to the first page" is this tag page, not the
	// equivalent /?tags= query-string form.
	results.FirstPageURL = "/tag/" + slug
	templates.LayoutHead(templates.PageTitle(filters, ""), templates.CanonicalLink(canonical), h.searchShell(),
		templates.Index(filters, results)).Render(r.Context(), w)
}
