// Package templates is the rendering layer: the templ-generated markup
// (*_templ.go, built from the .templ sources) plus the plain-Go view models
// this package hand-writes for it - Filters (this file), Coverage,
// Freshness, History, Traffic, and the Atom/sitemap/OpenSearch XML
// documents. Handlers in internal/web build one of these from a request and
// the loaded cache.Results, and the templates render it; nothing in this
// package reads a request or the cache directly.
package templates

import (
	"fmt"
	"itchgrep/pkg/models"
	"net/url"
	"strings"
)

// Filters is the state of the search controls: what was typed, which tags are
// applied, whether free or paid was chosen, and how results are ordered.
//
// It lives in the templates package rather than beside the query layer because
// almost everything it does is build links. Every control on the page is a URL
// - a tag chip is "the current state plus this tag", the free toggle is "the
// current state with price=free" - so the type that renders those has to be the
// one that owns the canonical encoding.
//
// Values are treated as immutable: the With* methods return a copy, so a
// template can build a dozen variant links off one Filters without any of them
// disturbing the others.
type Filters struct {
	Query string
	Tags  []string
	// NotTags are tags an asset must not carry. Kept separate from Tags rather
	// than encoded as "-tag" inside it, so that neither list can ever contain a
	// value the other is also asserting.
	NotTags []string
	Author  string
	Price   string
	Sort    string
	// Like, when set, is the GameId of an asset the results should resemble:
	// the search layer disjuncts its tag slugs and excludes it from the
	// result set. It sits alongside the other filters rather than off to one
	// side, so a "similar to X" page is exactly as shareable, cacheable and
	// combinable with a tag or price filter as any other search.
	Like string
	// Currency, when set, is the currency prices are converted into for
	// display. It changes nothing about which assets match - only how their
	// prices are written - but it belongs in the URL all the same, so that a
	// converted page is shareable and cacheable like any other.
	Currency string

	// AllTags asks the full page to list every sidebar tag instead of the head
	// (the no-script "More tags" link). Display only: it changes no result, so it
	// is not part of Values, ShareURL or FragmentURL - only PageURL, the form a
	// full page is requested in, writes it, so that the canonical redirect
	// accepts the address instead of stripping it.
	AllTags bool
}

// MaxTags and MaxNotTags bound how many tags one request may require and how
// many it may exclude. Each becomes a conjunct in the search, and past a handful
// the result set is empty anyway. They are also the size of the crawlable
// filter space: every extra tag multiplies the distinct URLs a bot can generate
// by following sidebar links, so the cap is deliberately low. Exclusions are
// capped separately because the WAF already challenges three or more not=
// values; four leaves a person who passes the challenge a little room.
const (
	MaxTags    = 4
	MaxNotTags = 4
)

// AtTagLimit reports whether another required tag would be refused. A control
// that would lead there must not be rendered as a link, because WithTag returns
// the filters unchanged and the link would point at the page it is on.
func (f Filters) AtTagLimit() bool { return len(f.Tags) >= MaxTags }

// AtNotTagLimit is AtTagLimit for exclusions.
func (f Filters) AtNotTagLimit() bool { return len(f.NotTags) >= MaxNotTags }

// Bare reports whether the filters encode to nothing, which is the front page
// as a crawler first sees it. Only then is a sidebar tag link a plain
// "/tag/{slug}": with anything else applied, the destination has to keep it.
func (f Filters) Bare() bool { return len(f.Values()) == 0 }

// Narrowed reports whether anything besides the query is narrowing the
// catalog, which is what separates "remove a filter" advice from "search for
// something broader" advice on an empty page.
func (f Filters) Narrowed() bool {
	return len(f.Tags) > 0 || len(f.NotTags) > 0 ||
		f.Price != "" || f.Author != "" || f.Like != ""
}

// Values encodes the filters as query parameters, omitting anything at its
// default.
//
// Omitting defaults is what keeps the URL space small: /?q=x and
// /?q=x&price=&sort=relevance would otherwise be different cache keys for the
// same page, and a shared cache in front of the site would hold both.
func (f Filters) Values() url.Values {
	v := url.Values{}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if len(f.Tags) > 0 {
		v.Set("tags", strings.Join(f.Tags, ","))
	}
	if len(f.NotTags) > 0 {
		v.Set("not", strings.Join(f.NotTags, ","))
	}
	if f.Like != "" {
		v.Set("like", f.Like)
	}
	if f.Author != "" {
		v.Set("author", f.Author)
	}
	if f.Price != "" {
		v.Set("price", f.Price)
	}
	if f.Currency != "" {
		v.Set("cur", f.Currency)
	}
	// The default ordering depends on whether anything was searched for, so it
	// is only worth encoding when it differs from what the server would pick.
	if f.Sort != "" && f.Sort != f.defaultSort() {
		v.Set("sort", f.Sort)
	}
	return v
}

func (f Filters) defaultSort() string {
	if f.Query == "" {
		return models.SortPopular
	}
	return models.SortRelevance
}

// ResolvedSort is the ordering actually in force, for rendering the control.
func (f Filters) ResolvedSort() string {
	if f.Sort == "" {
		return f.defaultSort()
	}
	return f.Sort
}

// ShareURL is the page address these filters correspond to: what the address
// bar should show, and what a visitor copies to share the result.
func (f Filters) ShareURL() string {
	v := f.Values()
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// PageURL is the shareable address of one page of results.
//
// It backs the href on the load-more control, so that with scripting off the
// control is an ordinary link to the next page rather than a dead element. Page
// 1 is left implicit, keeping the canonical URL of a filter unchanged.
func (f Filters) PageURL(page int64) string {
	v := f.Values()
	if page > 1 {
		v.Set("page", fmt.Sprint(page))
	}
	if f.AllTags {
		v.Set(MoreTagsParam, "1")
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// FragmentURL is the endpoint htmx fetches one page of results from.
func (f Filters) FragmentURL(page int64) string {
	v := f.Values()
	v.Set("page", fmt.Sprint(page))
	return "/results?" + v.Encode()
}

// FeedURL is the /feed address for these filters, in the same canonical
// parameter order ShareURL uses. Sort is never carried onto it even when set:
// the feed's ordering is always newest-arrival-first, so encoding a sort that
// the feed itself ignores would only make two URLs cache-key the same feed.
//
// Like, unlike sort, is kept: "new arrivals similar to X" is exactly as
// coherent a feed as "new arrivals tagged pixel-art" - both are the same
// FirstSeen-stamped ordering, just narrowed differently.
func (f Filters) FeedURL() string {
	v := f.Values()
	v.Del("sort")
	if len(v) == 0 {
		return "/feed"
	}
	return "/feed?" + v.Encode()
}

// Any reports whether anything is filtering the catalog, which is what
// decides whether the "clear all" control is worth rendering.
//
// Currency is excluded on purpose: it changes how prices read, not which assets
// are shown, so offering to "clear filters" because someone chose to see euros
// would be describing a display preference as a constraint.
func (f Filters) Any() bool {
	return f.Query != "" || f.Narrowed()
}

// HasTag reports whether a tag is already applied.
func (f Filters) HasTag(tag string) bool {
	return contains(f.Tags, tag)
}

// HasNotTag reports whether a tag is already excluded.
func (f Filters) HasNotTag(tag string) bool {
	return contains(f.NotTags, tag)
}

func contains(list []string, want string) bool {
	for _, t := range list {
		if t == want {
			return true
		}
	}
	return false
}

// WithTag returns these filters plus one tag, or unchanged if it is already
// applied or the limit is reached. Tags stay sorted so that arriving at the
// same set by different routes produces the same URL, and therefore the same
// cache entry.
//
// Requiring a tag drops any exclusion of it. The two are contradictory, and
// resolving it in favor of the click that just happened is the only reading
// that leaves the control doing what it says.
func (f Filters) WithTag(tag string) Filters {
	if f.HasTag(tag) || f.AtTagLimit() {
		return f
	}
	next := f.WithoutNotTag(tag)
	next.Tags = insertSorted(f.Tags, tag)
	return next
}

// WithoutTag returns these filters minus one required tag.
func (f Filters) WithoutTag(tag string) Filters {
	next := f
	next.Tags = remove(f.Tags, tag)
	return next
}

// WithNotTag returns these filters with one tag excluded, dropping it from the
// required list for the same reason WithTag drops the exclusion.
func (f Filters) WithNotTag(tag string) Filters {
	if f.HasNotTag(tag) || f.AtNotTagLimit() {
		return f
	}
	next := f.WithoutTag(tag)
	next.NotTags = insertSorted(f.NotTags, tag)
	return next
}

// WithoutNotTag returns these filters minus one exclusion.
func (f Filters) WithoutNotTag(tag string) Filters {
	next := f
	next.NotTags = remove(f.NotTags, tag)
	return next
}

// WithAuthor returns these filters restricted to one creator, or unrestricted
// again if that creator was already the one selected.
func (f Filters) WithAuthor(author string) Filters {
	next := f
	if f.Author == author {
		next.Author = ""
	} else {
		next.Author = author
	}
	return next
}

// WithLike returns these filters narrowed to items similar to one asset, or
// unrestricted again if that asset was already the one selected - the same
// toggle WithAuthor and WithPrice use, so clicking "similar" twice on the
// same card returns to the unfiltered view rather than being a one-way door.
func (f Filters) WithLike(gameId string) Filters {
	next := f
	if f.Like == gameId {
		next.Like = ""
	} else {
		next.Like = gameId
	}
	return next
}

// WithoutLike returns these filters with the similarity filter removed, for
// the chip's remove control.
func (f Filters) WithoutLike() Filters {
	next := f
	next.Like = ""
	return next
}

func remove(list []string, drop string) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		if t != drop {
			out = append(out, t)
		}
	}
	return out
}

// ToggleTag adds a tag if absent and removes it if present, so one control can
// serve both directions.
func (f Filters) ToggleTag(tag string) Filters {
	if f.HasTag(tag) {
		return f.WithoutTag(tag)
	}
	return f.WithTag(tag)
}

// WithPrice returns these filters with the price selection set, or cleared if
// the same one is chosen again - the control is a toggle, not a radio group,
// so clicking "Free" twice returns to showing everything.
func (f Filters) WithPrice(price string) Filters {
	next := f
	if f.Price == price {
		next.Price = ""
	} else {
		next.Price = price
	}
	return next
}

// WithSort returns these filters ordered differently.
func (f Filters) WithSort(sort string) Filters {
	next := f
	next.Sort = sort
	return next
}

// Cleared keeps only the search text, dropping every filter. Deliberately not
// "clear everything": a person clicking "clear filters" next to their own query
// means the tags, not the search they just typed.
func (f Filters) Cleared() Filters {
	return Filters{Query: f.Query, Sort: f.Sort, Currency: f.Currency}
}

func insertSorted(tags []string, tag string) []string {
	out := make([]string, 0, len(tags)+1)
	inserted := false
	for _, t := range tags {
		if !inserted && tag < t {
			out = append(out, tag)
			inserted = true
		}
		out = append(out, t)
	}
	if !inserted {
		out = append(out, tag)
	}
	return out
}
