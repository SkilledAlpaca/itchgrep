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
	// Currency, when set, is the currency prices are converted into for
	// display. It changes nothing about which assets match - only how their
	// prices are written - but it belongs in the URL all the same, so that a
	// converted page is shareable and cacheable like any other.
	Currency string
}

// MaxTags bounds how many tags one request may apply. Each becomes a conjunct
// in the search, and past a handful the result set is empty anyway, so this
// exists to stop a hand-written URL turning into an expensive query rather than
// to restrain any real use.
const MaxTags = 8

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

// Any reports whether anything is filtering the catalogue, which is what
// decides whether the "clear all" control is worth rendering.
//
// Currency is excluded on purpose: it changes how prices read, not which assets
// are shown, so offering to "clear filters" because someone chose to see euros
// would be describing a display preference as a constraint.
func (f Filters) Any() bool {
	return f.Query != "" || len(f.Tags) > 0 || len(f.NotTags) > 0 ||
		f.Price != "" || f.Author != ""
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
// resolving it in favour of the click that just happened is the only reading
// that leaves the control doing what it says.
func (f Filters) WithTag(tag string) Filters {
	if f.HasTag(tag) || len(f.Tags) >= MaxTags {
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
	if f.HasNotTag(tag) || len(f.NotTags) >= MaxTags {
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

// WithCurrency returns these filters displaying prices in another currency, or
// as listed if the same one is chosen again.
func (f Filters) WithCurrency(currency string) Filters {
	next := f
	if f.Currency == currency {
		next.Currency = ""
	} else {
		next.Currency = currency
	}
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
