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
	Price string
	Sort  string
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
	if f.Price != "" {
		v.Set("price", f.Price)
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

// FragmentURL is the endpoint htmx fetches one page of results from.
func (f Filters) FragmentURL(page int64) string {
	v := f.Values()
	v.Set("page", fmt.Sprint(page))
	return "/results?" + v.Encode()
}

// Any reports whether anything is filtering the catalogue, which is what
// decides whether the "clear all" control is worth rendering.
func (f Filters) Any() bool {
	return f.Query != "" || len(f.Tags) > 0 || f.Price != ""
}

// HasTag reports whether a tag is already applied.
func (f Filters) HasTag(tag string) bool {
	for _, t := range f.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// WithTag returns these filters plus one tag, or unchanged if it is already
// applied or the limit is reached. Tags stay sorted so that arriving at the
// same set by different routes produces the same URL, and therefore the same
// cache entry.
func (f Filters) WithTag(tag string) Filters {
	if f.HasTag(tag) || len(f.Tags) >= MaxTags {
		return f
	}
	next := f
	next.Tags = insertSorted(f.Tags, tag)
	return next
}

// WithoutTag returns these filters minus one tag.
func (f Filters) WithoutTag(tag string) Filters {
	next := f
	next.Tags = make([]string, 0, len(f.Tags))
	for _, t := range f.Tags {
		if t != tag {
			next.Tags = append(next.Tags, t)
		}
	}
	return next
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
	return Filters{Query: f.Query, Sort: f.Sort}
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
