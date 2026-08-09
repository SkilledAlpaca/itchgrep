package web

import (
	"errors"
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/web/templates"
	"itchgrep/pkg/models"
	"net/http"
	"strconv"
	"strings"
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

// maxTagLen bounds one tag slug. itch.io's longest is well under this.
const maxTagLen = 64

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

// HandleIndex serves the full page, results included. Rendering the results
// server-side rather than leaving htmx to fetch them is what makes every
// filtered URL shareable: the recipient gets the page they were sent, not an
// empty one that then populates.
func (h *handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)

	results, err := h.find(filters, 1)
	if err != nil {
		// The page itself still renders - masthead, search box, filters - with
		// the problem shown where the results would be. A visitor arriving
		// while the index loads gets something they can retry from.
		logging.Error("Error rendering index: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusFor(err))
		templates.Layout(pageTitle(filters), templates.Index(filters, templates.Results{Page: 1})).Render(r.Context(), w)
		return
	}

	h.cacheable(w)
	templates.Layout(pageTitle(filters), templates.Index(filters, results)).Render(r.Context(), w)
}

// HandleResults serves one page of results as a fragment.
//
// It is a GET rather than the POST the search used to be, for three reasons: a
// POST is never cached by any shared cache, so every search reached the origin;
// a POST cannot be linked, bookmarked or reloaded; and the back button did not
// work. The response carries HX-Push-Url so the address bar shows the shareable
// /?... form rather than this endpoint.
func (h *handler) HandleResults(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r)

	page := int64(1)
	if p := r.URL.Query().Get("page"); p != "" {
		parsed, err := strconv.ParseInt(p, 10, 64)
		if err != nil || parsed < 1 {
			http.Error(w, "Invalid page", http.StatusBadRequest)
			return
		}
		page = parsed
	}

	// Pushed before the lookup, and regardless of how it goes: the address bar
	// should reflect what was asked for, so that a search which failed because
	// the index was still loading can be retried by reloading the page.
	//
	// Set unconditionally rather than only for htmx requests. It is inert for
	// anything else, and making the response vary by request header would force
	// a Vary that fragments the edge cache.
	//
	// Only for the first page: infinite scroll must not rewrite the address bar
	// on every batch it pulls in.
	if page == 1 {
		w.Header().Set("HX-Push-Url", filters.ShareURL())
	}

	results, err := h.find(filters, page)
	if err != nil {
		logging.Error("Error fetching results: %s", err)
		renderProblem(w, r, err)
		return
	}

	h.cacheable(w)
	// Page 1 replaces the whole region, because changing a filter changes the
	// sidebar counts and the controls as well as the cards. Later pages are
	// appended to the existing list, so they are cards alone.
	if page == 1 {
		templates.ResultsRegion(filters, results).Render(r.Context(), w)
		return
	}
	templates.AssetPage(filters, results).Render(r.Context(), w)
}

// find runs the query and shapes the answer for the templates.
func (h *handler) find(filters templates.Filters, page int64) (templates.Results, error) {
	found, err := h.cache.Find(cache.SearchOptions{
		Query: filters.Query,
		Tags:  filters.Tags,
		Price: filters.Price,
		Sort:  filters.Sort,
		Page:  page,
	})
	if err != nil {
		return templates.Results{}, err
	}
	return templates.Results{
		Assets: found.Assets,
		Total:  found.Total,
		Tags:   found.Tags,
		Page:   page,
		// Computed from the totals rather than from whether this page came back
		// full, so the last page of results does not trigger one more request
		// that can only return nothing.
		HasMore: page*h.cache.PageSize() < found.Total,
	}, nil
}

// parseFilters reads the filter state out of a request.
//
// Unrecognised values for the enumerated parameters are dropped rather than
// rejected. They can only come from a hand-edited or stale URL, and answering
// those with a 400 would put an error page behind a link that has an obvious,
// harmless reading - whereas a bad ?page= is structural and does get rejected.
func parseFilters(r *http.Request) templates.Filters {
	q := r.URL.Query()

	f := templates.Filters{Query: clampQuery(q.Get("q"))}

	// Both encodings are accepted: ?tags=a,b is what the links generate, while
	// ?tags=a&tags=b is what a plain HTML form submission produces from the
	// hidden fields that carry the filters through a search.
	for _, raw := range q["tags"] {
		for _, tag := range strings.Split(raw, ",") {
			if tag = normaliseTag(tag); tag != "" {
				f = f.WithTag(tag)
			}
		}
	}

	switch q.Get("price") {
	case models.PricingFree:
		f.Price = models.PricingFree
	case models.PricingPaid:
		f.Price = models.PricingPaid
	}

	switch q.Get("sort") {
	case models.SortRelevance:
		f.Sort = models.SortRelevance
	case models.SortPopular:
		f.Sort = models.SortPopular
	case models.SortTitle:
		f.Sort = models.SortTitle
	}

	return f
}

// normaliseTag reduces a tag parameter to a slug, or to "" if it is not one.
// itch.io slugs are lowercase alphanumerics and hyphens; anything else is a
// typo or an injection attempt, and either way matches no document.
func normaliseTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" || len(tag) > maxTagLen {
		return ""
	}
	for _, c := range tag {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return ""
		}
	}
	return tag
}

// clampQuery bounds an incoming query so a pathological one cannot be turned
// into an expensive index pass.
func clampQuery(q string) string {
	q = strings.TrimSpace(q)
	if len(q) > maxQueryLen {
		return q[:maxQueryLen]
	}
	return q
}

func pageTitle(f templates.Filters) string {
	switch {
	case f.Query != "":
		return f.Query + " — ITCHGREP"
	case len(f.Tags) > 0:
		return strings.Join(f.Tags, " + ") + " — ITCHGREP"
	default:
		return "ITCHGREP"
	}
}

func statusFor(err error) int {
	if errors.Is(err, cache.ErrNotReady) {
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
}

// renderProblem answers a failed fetch with a rendered notice rather than a
// bare error string. The status code stays honest - htmx is configured in the
// layout to swap 503 responses, which it otherwise discards, so the page can
// say what is wrong instead of silently rendering nothing.
func renderProblem(w http.ResponseWriter, r *http.Request, err error) {
	// A transient failure must not be cached, least of all by a shared cache.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusFor(err))
	if errors.Is(err, cache.ErrNotReady) {
		templates.NotReady().Render(r.Context(), w)
		return
	}
	templates.SearchFailed().Render(r.Context(), w)
}

func (h *handler) HandleAbout(w http.ResponseWriter, r *http.Request) {
	h.cacheable(w)
	component := templates.About()
	component.Render(r.Context(), w)
}
