package web

import (
	"errors"
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// fragmentMaxAge is how long a rendered result page may be reused.
//
// Every visitor sees the same results for the same query, and the underlying
// data only changes when a crawl publishes - at most once a day. Without this
// header a proxy in front of the site can cache nothing at all, so every
// search, including repeats of the same popular query, runs a full bleve pass
// on the origin. Five minutes is short enough that a newly published index
// reaches users promptly and long enough to absorb the bursts that matter.
const fragmentMaxAge = 300

// maxQueryLen bounds what reaches the index. The fuzzy passes cost more the
// longer the input, and no legitimate search needs more than this.
const maxQueryLen = 200

type handler struct {
	cache *cache.Cache
}

func NewHandler(cache *cache.Cache) *handler {
	return &handler{
		cache: cache,
	}
}

// cacheable marks a response as reusable by shared caches, and stamps it with
// the dataset it was rendered from so revalidation works.
func (h *handler) cacheable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", fragmentMaxAge))
	if t := h.cache.DataUpdatedTime(); !t.IsZero() {
		w.Header().Set("Last-Modified", t.UTC().Format(http.TimeFormat))
	}
}

// Handle404 renders the site's styled 404 page with an HTTP 404 status
// code. It is exported so cmd/webserver can wire it up as chi's NotFound
// handler.
func Handle404(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	component := templates.Layout("Not found — ITCHGREP", templates.Error404())
	component.Render(r.Context(), w)
}

// HandleIndex serves the full page. A query in ?q= is rendered into the search
// box and loaded as results, which is what makes a search URL shareable: the
// recipient gets the page and the results, not an empty box.
func (h *handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	query := clampQuery(r.URL.Query().Get("q"))

	title := "ITCHGREP"
	if query != "" {
		title = query + " — ITCHGREP"
	}

	h.cacheable(w)
	component := templates.Layout(title, templates.Index(query))
	component.Render(r.Context(), w)
}

// HandleGetAssetPage serves one page of the popularity-ordered browse listing.
func (h *handler) HandleGetAssetPage(w http.ResponseWriter, r *http.Request) {
	pageNum, err := strconv.ParseInt(chi.URLParam(r, "page"), 10, 64)
	if err != nil {
		logging.Error("Error parsing page: %s", err)
		http.Error(w, "Invalid request, page number not found", http.StatusBadRequest)
		return
	}
	assets, err := h.cache.Page(pageNum)
	if err != nil {
		logging.Error("Error fetching page: %s", err)
		renderProblem(w, r, err)
		return
	}

	h.cacheable(w)
	component := templates.AssetPage(pageNum, assets, false, "")
	component.Render(r.Context(), w)
}

// HandleSearch serves one page of results for ?q=.
//
// This is a GET rather than the POST it used to be, for three reasons: a POST
// is never cached by any shared cache, so every search reached the origin; a
// POST cannot be linked, bookmarked or reloaded; and the back button did not
// work. The response carries HX-Push-Url so the address bar shows the
// shareable /?q=... form rather than this endpoint.
func (h *handler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	query := clampQuery(r.URL.Query().Get("q"))
	if query == "" {
		// An empty search is the browse listing, not an error.
		http.Redirect(w, r, "/assets/1", http.StatusSeeOther)
		return
	}

	pageNum := int64(1)
	if p := r.URL.Query().Get("page"); p != "" {
		parsed, err := strconv.ParseInt(p, 10, 64)
		if err != nil || parsed < 1 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
		pageNum = parsed
	}

	// Pushed before the lookup, and regardless of how it goes: the address bar
	// should reflect what was asked for, so that a search which failed because
	// the index was still loading can be retried by reloading the page.
	//
	// Set unconditionally rather than only for htmx requests. It is inert for
	// anything else, and making the response vary by request header would force
	// a Vary that fragments the edge cache.
	if pageNum == 1 {
		w.Header().Set("HX-Push-Url", "/?q="+url.QueryEscape(query))
	}

	assets, err := h.cache.QueryCache(query, pageNum)
	if err != nil {
		logging.Error("Error searching: %s", err)
		renderProblem(w, r, err)
		return
	}

	h.cacheable(w)
	component := templates.AssetPage(pageNum, assets, true, query)
	component.Render(r.Context(), w)
}

// clampQuery bounds an incoming query so a pathological one cannot be turned
// into an expensive index pass.
func clampQuery(q string) string {
	if len(q) > maxQueryLen {
		return q[:maxQueryLen]
	}
	return q
}

// renderProblem answers a failed fetch with a rendered notice rather than a
// bare error string. The status code stays honest - htmx is configured in the
// layout to swap 503 responses, which it otherwise discards, so the page can
// say what is wrong instead of silently rendering nothing.
func renderProblem(w http.ResponseWriter, r *http.Request, err error) {
	// A transient failure must not be cached, least of all by a shared cache.
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, cache.ErrNotReady) {
		w.WriteHeader(http.StatusServiceUnavailable)
		templates.NotReady().Render(r.Context(), w)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	templates.SearchFailed().Render(r.Context(), w)
}

func (h *handler) HandleAbout(w http.ResponseWriter, r *http.Request) {
	h.cacheable(w)
	component := templates.About()
	component.Render(r.Context(), w)
}
