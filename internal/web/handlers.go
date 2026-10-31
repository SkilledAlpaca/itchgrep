// Package web is the webserver's HTTP surface: the route handlers, the
// request-scoped middleware (rate limiting, security headers, access
// logging, scanner filtering), and the small helpers that turn a request's
// query string into the internal/cache.SearchOptions a search actually
// runs. It renders through internal/web/templates and reads the loaded
// dataset through internal/cache; it does not itself know how that dataset
// gets built or persisted.
package web

import (
	"errors"
	"fmt"
	"itchgrep/internal/cache"
	"itchgrep/internal/logging"
	"itchgrep/internal/metrics"
	"itchgrep/internal/storage"
	"itchgrep/internal/web/templates"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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

// statsMaxAge is shorter than fragmentMaxAge: a stats page five minutes stale
// invites "is the site down?" bug reports in a way a five-minute-old result
// list does not.
const statsMaxAge = 60

// maxQueryLen bounds what reaches the index. The fuzzy passes cost more the
// longer the input, and no legitimate search needs more than this.
const maxQueryLen = 200

// maxNewAssets caps /new. It is a page, not a feed - there is no "load more"
// - so this has to be generous enough to feel complete on its own rather than
// a teaser for a list the page never offers the rest of.
const maxNewAssets = 72

// maxTagLen bounds one tag slug. itch.io's longest is well under this.
const maxTagLen = 64

// maxFeedEntries caps /feed for the same reason maxNewAssets caps /new: a
// feed has no pagination a reader would ever ask for a further page through,
// so the cap has to be generous enough that a normal poll interval never
// truncates it rather than a teaser for a list the feed never offers the
// rest of.
const maxFeedEntries = 50

// feedMaxAge is longer than fragmentMaxAge: a feed reader polls on a
// schedule of its own choosing rather than a person reloading a page, and the
// underlying data only changes once a crawl publishes - see fragmentMaxAge
// for the same reasoning applied to a shorter window.
const feedMaxAge = 3600

// maxAuthorLen bounds an author parameter. itch.io display names are far
// shorter; this only exists to keep a hand-written URL from becoming a long
// term lookup.
const maxAuthorLen = 120

// maxGameIdLen bounds a like= parameter. itch.io's ids are unpadded decimal
// numbers well under this (see internal/fetcher.ParseAssetPage, which reads
// data-game_id straight off the listing markup); this exists to keep a
// hand-written URL from turning into a pathologically long dataMap key
// rather than to restrain any real id.
const maxGameIdLen = 32

// maxPage bounds ?page=. No real result set is long enough to need it, and
// without a cap (page-1)*pageSize overflows int64 for a large enough input,
// wrapping negative and reaching the cache's slicing arithmetic as a negative
// start/from - a panic, not a 400, for what is otherwise an ordinary
// out-of-range request. cache.browse and cache.search each guard their own
// arithmetic too, belt-and-braces, but this is the cheap rejection that keeps
// a hostile page number from reaching the cache at all.
const maxPage = 10000

type handler struct {
	cache *cache.Cache

	// crawlInterval is how often the dataservice rebuilds the index, used only
	// to say when the next rebuild is due. Zero when rebuilds are triggered by
	// hand, in which case the page says nothing about a next one.
	crawlInterval time.Duration

	// metrics backs HandleStats. Nil-tolerant - metrics.Counters' methods are
	// all no-ops on a nil receiver - so handlers_test.go can build a handler
	// without wiring up counters it has no reason to exercise.
	metrics *metrics.Counters
}

// NewHandler builds the handler every page route in cmd/webserver/main.go is
// registered against. m may be nil - see the metrics field's own comment.
func NewHandler(cache *cache.Cache, crawlInterval time.Duration, m *metrics.Counters) *handler {
	return &handler{
		cache:         cache,
		crawlInterval: crawlInterval,
		metrics:       m,
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

// freshness measures the served dataset against the wall clock, for the age
// line in the masthead. Read per request rather than at startup, so the line
// updates itself when a crawl publishes underneath a long-running process.
func (h *handler) freshness() templates.Freshness {
	return templates.NewFreshness(h.cache.DataUpdatedTime(), time.Now(), h.crawlInterval)
}

func (h *handler) coverage() templates.Coverage {
	return templates.NewCoverage(h.cache.Stats())
}

// history reads the crawl log directly from storage rather than through the
// cache: history.json changes only once per crawl, is small even at its
// two-year cap, and /stats already carries statsMaxAge cache headers - adding
// a reload-triggered cache field for it would be a second caching layer
// solving a load problem that does not exist.
func (h *handler) history() templates.History {
	records, err := storage.GetHistory()
	if err != nil {
		logging.Warning("Failed to read crawl history, /stats will show none: %v", err)
		records = nil
	}
	return templates.NewHistory(records)
}

// pageParam reads ?page=, defaulting to the first. Unlike the filter
// parameters a bad page is rejected rather than ignored: it is structural
// rather than a preference, and quietly serving page 1 for ?page=abc would look
// like the results had simply run out.
func pageParam(r *http.Request) (int64, bool) {
	p := r.URL.Query().Get("page")
	if p == "" {
		return 1, true
	}
	parsed, err := strconv.ParseInt(p, 10, 64)
	if err != nil || parsed < 1 || parsed > maxPage {
		return 0, false
	}
	return parsed, true
}

// Handle404 renders the site's styled 404 page with an HTTP 404 status
// code. It is exported so cmd/webserver can wire it up as chi's NotFound
// handler.
func Handle404(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	component := templates.Layout("Not found — ITCHGREP", templates.Shell{}, templates.Error404())
	component.Render(r.Context(), w)
}

// HandleIndex serves the full page, results included. Rendering the results
// server-side rather than leaving htmx to fetch them is what makes every
// filtered URL shareable: the recipient gets the page they were sent, not an
// empty one that then populates.
func (h *handler) HandleIndex(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r, h.cache.Rates())

	// Honored here too, not just on the fragment endpoint, so that the
	// load-more link works with scripting off and so a deep-linked page of
	// results survives being shared.
	page, ok := pageParam(r)
	if !ok {
		h.badPage(w, r, filters.ShareURL())
		return
	}

	// One address per result set. Dropped values, an unsorted or duplicated tag
	// list, parameters at their default and unknown parameters are all different
	// URLs for the same page, which a shared cache stores separately and a
	// crawler counts separately. Only here and not on /results: that endpoint is
	// a fragment fetched by htmx, which cannot follow a redirect to a full page.
	//
	// A 302 with a short max-age rather than a 301: what counts as canonical
	// depends on the tag caps and on which exchange-rate table is loaded (a
	// currency is valid only if the table has it), so a permanent redirect would
	// bake in today's rules in every browser that saw it. robots.txt already
	// disallows query strings, so permanence bought nothing.
	//
	// It converges in one hop because the target is the output of PageURL, which
	// parses back to the same filters (TestCanonicalURLIsAFixedPoint), and the
	// comparison is against that exact string.
	if canonical := filters.PageURL(page); canonical != requestedForm(r) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", fragmentMaxAge))
		http.Redirect(w, r, canonical, http.StatusFound)
		return
	}

	results, err := h.find(filters, page)
	if err != nil {
		// The page itself still renders - masthead, search box, filters - with
		// the problem shown where the results would be. A visitor arriving
		// while the index loads gets something they can retry from.
		logging.Error("Error rendering index: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusFor(err))
		// No results reached, so nothing is known about the ?like= source's
		// title yet - the feed link falls back to describing the filter by
		// id alone, the same way it would for an unknown one.
		templates.LayoutHead(templates.PageTitle(filters, ""), templates.FeedLink(filters, ""), h.searchShell(), templates.Index(filters, templates.Results{Page: page})).Render(r.Context(), w)
		return
	}

	h.cacheable(w)
	// LayoutHead, not Layout: this is the one page a feed for the current
	// filters makes sense on, so it carries the auto-discovery link every
	// other page has no reason to.
	templates.LayoutHead(templates.PageTitle(filters, results.LikeTitle), templates.FeedLink(filters, results.LikeTitle), h.searchShell(), templates.Index(filters, results)).Render(r.Context(), w)
}

// badPage renders the styled 400 for a malformed ?page= on a full-page route.
// The status stays 400, so it still shows up as a client error, but the body is
// the site's own page with a way back instead of a bare "Invalid page".
func (h *handler) badPage(w http.ResponseWriter, r *http.Request, firstPage string) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	templates.Layout("Invalid page — ITCHGREP", templates.Shell{Age: h.freshness(), Coverage: h.coverage()}, templates.BadPage(firstPage)).Render(r.Context(), w)
}

// shell is the page frame for every route that is not a result list.
func (h *handler) shell() templates.Shell {
	return templates.Shell{Age: h.freshness(), Coverage: h.coverage()}
}

// searchShell is the page frame for the routes built around a result list.
func (h *handler) searchShell() templates.Shell {
	return templates.Shell{Age: h.freshness(), Coverage: h.coverage(), Search: true}
}

// requestedForm is the request's own address in the shape Filters.PageURL
// writes, so the two compare directly.
func requestedForm(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return r.URL.Path
	}
	return r.URL.Path + "?" + r.URL.RawQuery
}

// HandleResults serves one page of results as a fragment.
//
// It is a GET rather than the POST the search used to be, for three reasons: a
// POST is never cached by any shared cache, so every search reached the origin;
// a POST cannot be linked, bookmarked or reloaded; and the back button did not
// work. The response carries HX-Push-Url so the address bar shows the shareable
// /?... form rather than this endpoint.
func (h *handler) HandleResults(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r, h.cache.Rates())

	page, ok := pageParam(r)
	if !ok {
		// A fragment, not a page: htmx swaps this into the results region
		// (app.js lets a 400 through), where a full document would nest one.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		templates.BadPage(filters.ShareURL()).Render(r.Context(), w)
		return
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
		templates.ResultsFragment(filters, results).Render(r.Context(), w)
		return
	}
	templates.AssetPage(filters, results).Render(r.Context(), w)
}

// find runs the query and shapes the answer for the templates.
func (h *handler) find(filters templates.Filters, page int64) (templates.Results, error) {
	found, err := h.cache.Find(cache.SearchOptions{
		Query:       filters.Query,
		Tags:        filters.Tags,
		ExcludeTags: filters.NotTags,
		Author:      filters.Author,
		Price:       filters.Price,
		Sort:        filters.Sort,
		Like:        filters.Like,
		Page:        page,
	})
	if err != nil {
		return templates.Results{}, err
	}
	return templates.Results{
		Assets:        found.Assets,
		Total:         found.Total,
		Tags:          found.Tags,
		Page:          page,
		Rates:         h.cache.Rates(),
		HasRecency:    h.cache.HasRecency(),
		HasPriceBands: h.cache.HasPriceBands(),
		// Computed from the totals rather than from whether this page came back
		// full, so the last page of results does not trigger one more request
		// that can only return nothing.
		HasMore: page*h.cache.PageSize() < found.Total,
		// LikeTitle threads the ?like= source asset's title from the search
		// layer through to the chip row - see cache.Results.LikeTitle.
		LikeTitle: found.LikeTitle,
	}, nil
}

// HandleNew serves /new: the newest arrivals the crawl has stamped a
// FirstSeen for, most recent first, capped rather than paged.
//
// MinFirstSeen: 1 is "FirstSeenUnix > 0" - every genuine arrival has a real
// Unix second in it, so anything at or above 1 is stamped and anything at 0
// is the unknown old stock this page must never show. Limit bypasses the
// configured page size entirely: this page has no further page to request, so
// its cap is a property of the page, not of pagination.
func (h *handler) HandleNew(w http.ResponseWriter, r *http.Request) {
	found, err := h.cache.Find(cache.SearchOptions{
		MinFirstSeen: 1,
		Sort:         models.SortNew,
		Limit:        maxNewAssets,
		Page:         1,
		// This page has no sidebar - nothing ever reads Results.Tags here -
		// so the facet bleve would otherwise compute over the whole match
		// set is work with no reader.
		NoFacets: true,
	})
	if err != nil {
		logging.Error("Error rendering /new: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(statusFor(err))
		templates.Layout("New — ITCHGREP", h.shell(), templates.ArrivalsPage(nil, h.cache.Rates(), h.freshness())).Render(r.Context(), w)
		return
	}

	h.cacheable(w)
	templates.Layout("New — ITCHGREP", h.shell(), templates.ArrivalsPage(found.Assets, h.cache.Rates(), h.freshness())).Render(r.Context(), w)
}

// HandleFeed serves /feed: an Atom document for the current filters.
// parseFilters is reused exactly, so any share URL's query string is a valid
// feed query - the same tags, author and price narrowing the results page
// would apply. Entries are bounded to genuine arrivals the same way /new is
// (MinFirstSeen: 1), newest first, capped rather than paged, because a feed
// reader has no notion of "load more" to offer.
func (h *handler) HandleFeed(w http.ResponseWriter, r *http.Request) {
	filters := parseFilters(r, h.cache.Rates())

	found, err := h.cache.Find(cache.SearchOptions{
		Query:        filters.Query,
		Tags:         filters.Tags,
		ExcludeTags:  filters.NotTags,
		Author:       filters.Author,
		Price:        filters.Price,
		Like:         filters.Like,
		MinFirstSeen: 1,
		Sort:         models.SortNew,
		Limit:        maxFeedEntries,
		Page:         1,
		// An Atom feed has no sidebar either - see the same field on
		// HandleNew above.
		NoFacets: true,
	})
	if err != nil {
		logging.Error("Error rendering /feed: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Feed temporarily unavailable", statusFor(err))
		return
	}

	body, err := templates.NewFeed(filters, found.Assets, h.cache.Rates(), h.cache.DataUpdatedTime(), found.LikeTitle).Render()
	if err != nil {
		// xml.Marshal on this struct only fails on a value it cannot encode,
		// which nothing here supplies - but the response has already
		// promised atom+xml, so a failure here must not fall through to
		// writing one anyway.
		logging.Error("Error encoding /feed: %s", err)
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Feed temporarily unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", feedMaxAge))
	w.Write(body)
}

// parseFilters reads the filter state out of a request.
//
// Unrecognized values for the enumerated parameters are dropped rather than
// rejected. They can only come from a hand-edited or stale URL, and answering
// those with a 400 would put an error page behind a link that has an obvious,
// harmless reading - whereas a bad ?page= is structural and does get rejected.
func parseFilters(r *http.Request, rates money.Rates) templates.Filters {
	q := r.URL.Query()

	f := templates.Filters{Query: clampQuery(q.Get("q"))}

	// Both encodings are accepted: ?tags=a,b is what the links generate, while
	// ?tags=a&tags=b is what a plain HTML form submission produces from the
	// hidden fields that carry the filters through a search.
	//
	// Parsing stops at the cap rather than looping over every value and letting
	// WithTag refuse each one: an over-long URL has nothing past the cap worth
	// reading, and the caller redirects it to the canonical form anyway.
tags:
	for _, raw := range q["tags"] {
		for _, tag := range strings.Split(raw, ",") {
			if f.AtTagLimit() {
				break tags
			}
			if tag = normalizeTag(tag); tag != "" {
				f = f.WithTag(tag)
			}
		}
	}
	// Applied after the inclusions, so that a URL asserting both ends up
	// excluding: WithNotTag drops the tag from the required list, and the last
	// instruction in the URL is the one that stands.
not:
	for _, raw := range q["not"] {
		for _, tag := range strings.Split(raw, ",") {
			if f.AtNotTagLimit() {
				break not
			}
			if tag = normalizeTag(tag); tag != "" {
				f = f.WithNotTag(tag)
			}
		}
	}

	f.Like = normalizeGameId(q.Get("like"))

	f.Author = clampAuthor(q.Get("author"))

	switch q.Get("price") {
	case models.PricingFree:
		f.Price = models.PricingFree
	case models.PricingPaid:
		f.Price = models.PricingPaid
	case models.PricingUnder5:
		f.Price = models.PricingUnder5
	case models.PricingUnder20:
		f.Price = models.PricingUnder20
	}

	switch q.Get("sort") {
	case models.SortRelevance:
		f.Sort = models.SortRelevance
	case models.SortPopular:
		f.Sort = models.SortPopular
	case models.SortTitle:
		f.Sort = models.SortTitle
	case models.SortPrice:
		f.Sort = models.SortPrice
	case models.SortRecent:
		f.Sort = models.SortRecent
	}

	f.Currency = normalizeCurrency(q.Get("cur"), rates)

	return f
}

// normalizeCurrency accepts only a currency the loaded snapshot can actually
// convert to. An unknown code is dropped rather than carried, so the control
// never shows a selection that silently converts nothing.
func normalizeCurrency(cur string, rates money.Rates) string {
	cur = strings.ToUpper(strings.TrimSpace(cur))
	if cur == "" || !rates.Has(cur) {
		return ""
	}
	return cur
}

// clampAuthor bounds an author parameter. It is matched exactly against a
// keyword field, so anything longer than a plausible display name can only be
// junk - but unlike a tag it has no restricted alphabet, since itch.io names
// contain spaces, punctuation and every script there is.
func clampAuthor(author string) string {
	author = strings.TrimSpace(author)
	if len(author) > maxAuthorLen {
		return ""
	}
	return author
}

// normalizeTag reduces a tag parameter to a slug, or to "" if it is not one.
// itch.io slugs are lowercase alphanumerics and hyphens; anything else is a
// typo or an injection attempt, and either way matches no document.
func normalizeTag(tag string) string {
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

// normalizeGameId reduces a like= parameter to a GameId, or to "" if it is
// not shaped like one. itch.io ids are decimal digits only, so anything else
// cannot match a real asset and is dropped the same way an out-of-alphabet
// tag is - the search layer then treats an empty Like exactly like the
// parameter being absent.
func normalizeGameId(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxGameIdLen {
		return ""
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return id
}

// clampQuery bounds an incoming query so a pathological one cannot be turned
// into an expensive index pass.
//
// Cut on a rune boundary, not a byte one. itch.io's catalog is searched in
// every script there is, and slicing a multi-byte character in half produces
// invalid UTF-8 that then travels into the query, the page title and the value
// of the search box.
func clampQuery(q string) string {
	q = strings.TrimSpace(q)
	if len(q) <= maxQueryLen {
		return q
	}
	cut := maxQueryLen
	for cut > 0 && !utf8.RuneStart(q[cut]) {
		cut--
	}
	// Trimmed again: the cut can land just after whitespace, and a query that
	// still ends in a space is not a fixed point of this function, which would
	// make the canonical redirect take a second hop.
	return strings.TrimSpace(q[:cut])
}

// statusFor maps a cache.Find error onto a response status.
//
// Everything that is not ErrNotReady is a server fault - a corrupt segment or
// an IO failure out of bleve, not a mistake in what the visitor asked for.
// The one error that used to reach here from a bad request, an invalid page
// number, never gets this far: HandleIndex, HandleResults and HandleTag all
// call pageParam and answer 400 themselves before touching the cache, so by
// the time an error comes back from Find it is always the cache's fault, not
// the request's - and hiding that behind 400 would keep it out of 5xx
// monitoring, which is exactly the failure this exists to stop.
func statusFor(err error) int {
	if errors.Is(err, cache.ErrNotReady) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
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

// HandleAbout serves /about: the project's credits and license page.
//
// Always the full document. This used to answer HX-Request with a bare
// fragment for the masthead links to swap in place, but that left the
// visitor on the previous URL reading about-content; the links are plain
// navigations now, so nothing sends an HX-Request here any more.
func (h *handler) HandleAbout(w http.ResponseWriter, r *http.Request) {
	h.cacheable(w)
	templates.Layout("About — ITCHGREP", h.shell(), templates.About()).Render(r.Context(), w)
}

// HandleSaved serves /saved: the masthead around an empty grid that app.js
// fills from localStorage. There is no query to run and nothing per-visitor
// to look up - the saved list itself never reaches this handler, or any
// other part of the server - so the response is the same document for every
// request, cacheable exactly like the other static shells.
func (h *handler) HandleSaved(w http.ResponseWriter, r *http.Request) {
	h.cacheable(w)
	templates.Layout("Saved — ITCHGREP", h.shell(), templates.SavedPage()).Render(r.Context(), w)
}

// HandleStats serves the public traffic page. It renders inside the
// rate-limited route group like every other page, and it is cached far more
// briefly than a result fragment - see statsMaxAge.
func (h *handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", statsMaxAge))
	traffic := templates.NewTraffic(h.metrics.Snapshot(), time.Now())
	templates.Layout("Stats — ITCHGREP", h.shell(), templates.StatsPage(traffic, h.history())).Render(r.Context(), w)
}
