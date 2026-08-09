package web

import (
	"errors"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type handler struct {
	cache *cache.Cache
}

func NewHandler(cache *cache.Cache) *handler {
	return &handler{
		cache: cache,
	}
}

// Handle404 renders the site's styled 404 page with an HTTP 404 status
// code. It is exported so cmd/webserver can wire it up as chi's NotFound
// handler.
func Handle404(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	component := templates.Layout("ITCHGREP", templates.Error404())
	component.Render(r.Context(), w)
}

func (h *handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	component := templates.Layout("ITCHGREP", templates.Index())
	component.Render(r.Context(), w)
}

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

	component := templates.AssetPage(pageNum, assets, false, "")
	component.Render(r.Context(), w)
}

func (h *handler) HandleQuery(w http.ResponseWriter, r *http.Request) {
	query := r.FormValue("query")
	if query == "" {
		// this shouldn't happen as long as the form is set up correctly
		http.Error(w, "Empty Query", http.StatusBadRequest)
		return
	}

	pageNum, err := strconv.ParseInt(chi.URLParam(r, "page"), 10, 64)
	if err != nil {
		logging.Error("Error parsing page: %s", err)
		http.Error(w, "Invalid request, page number not found", http.StatusBadRequest)
		return
	}

	assets, err := h.cache.QueryCache(query, pageNum)
	if err != nil {
		logging.Error("Error searching: %s", err)
		renderProblem(w, r, err)
		return
	}

	component := templates.AssetPage(pageNum, assets, true, query)
	component.Render(r.Context(), w)
}

// renderProblem answers a failed fetch with a rendered notice rather than a
// bare error string. The status code stays honest - htmx is configured in the
// layout to swap 503 responses, which it otherwise discards, so the page can
// say what is wrong instead of silently rendering nothing.
func renderProblem(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, cache.ErrNotReady) {
		w.WriteHeader(http.StatusServiceUnavailable)
		templates.NotReady().Render(r.Context(), w)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	templates.SearchFailed().Render(r.Context(), w)
}

func (h *handler) HandleAbout(w http.ResponseWriter, r *http.Request) {
	component := templates.About()
	component.Render(r.Context(), w)
}
