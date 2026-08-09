package fetcher

import (
	"fmt"
	"itchgrep/pkg/models"
	"sort"
	"strings"
)

// MaxPagesPerView is the hard pagination cap itch.io applies to every browse
// view. Page 200 serves data; page 202 returns 404. The total asset count
// implies ~3000 pages, so deriving a page count from it alone produces ~2800
// requests that can only 404.
const MaxPagesPerView = 200

// Sort orders. The zero value (SortDefault) is the ordering itch.io serves
// when no sort segment is present, and is distinct from all three named ones.
const (
	SortDefault       = ""
	SortNewest        = "newest"
	SortNewAndPopular = "new-and-popular"
	SortTopRated      = "top-rated"
)

// allSorts is every ordering a single-tag or root view can be fetched under.
// Each is a different window onto the same result set, so a tag too large to
// page through under one ordering yields different assets under another.
var allSorts = []string{SortDefault, SortNewest, SortNewAndPopular, SortTopRated}

// Slice is one browsable view of the catalogue: a URL that can be paged
// through up to MaxPagesPerView times.
//
// The URL grammar itch.io accepts is narrow, and violating it fails loudly:
//
//	/game-assets                    root
//	/game-assets/<sort>             sort only
//	/game-assets/tag-A              one tag
//	/game-assets/<sort>/tag-A       sort plus one tag
//	/game-assets/tag-A/tag-B        two tags, default sort only, A < B
//
// Three tags is a 403. A sort together with two tags is a 403. Tags out of
// lexicographic order is a 301. There is no deeper subdivision available,
// which is why PlanSlices cannot simply recurse until every view fits.
type Slice struct {
	Sort  string   // SortDefault, or one of the named orderings
	Tags  []string // canonical (lexicographic) order; empty means the root view
	Count int64    // reported result count, for ordering the crawl
}

// Valid reports whether this slice satisfies the URL grammar above. A slice
// that fails this is a planner bug: itch.io answers it with 403 or 301, not
// with data.
func (s Slice) Valid() bool {
	if len(s.Tags) > 2 {
		return false
	}
	if len(s.Tags) == 2 && s.Sort != SortDefault {
		return false
	}
	if len(s.Tags) == 2 && s.Tags[0] >= s.Tags[1] {
		return false
	}
	switch s.Sort {
	case SortDefault, SortNewest, SortNewAndPopular, SortTopRated:
		return true
	default:
		return false
	}
}

// Path returns the URL path for this slice, in canonical form.
func (s Slice) Path() string {
	parts := []string{"/game-assets"}
	if s.Sort != SortDefault {
		parts = append(parts, s.Sort)
	}
	for _, t := range s.Tags {
		parts = append(parts, "tag-"+t)
	}
	return strings.Join(parts, "/")
}

// baseURL is the origin every browse request is made against. A package-level
// var so tests can point it at an httptest.Server.
var baseURL = "https://itch.io"

// PageURL returns the JSON endpoint for one page of this slice.
func (s Slice) PageURL(pageNum int64) string {
	return fmt.Sprintf("%s%s?page=%d&format=json", baseURL, s.Path(), pageNum)
}

// Label is a short human-readable name for logs.
func (s Slice) Label() string {
	if s.Sort == SortDefault && len(s.Tags) == 0 {
		return "root"
	}
	return strings.TrimPrefix(s.Path(), "/game-assets/")
}

// PagesToFetch is how many pages of this slice are worth requesting: the
// number its count implies, capped at the view limit.
func (s Slice) PagesToFetch(itemsPerPage int64) int64 {
	if itemsPerPage <= 0 {
		return 0
	}
	pages := (s.Count + itemsPerPage - 1) / itemsPerPage
	if pages > MaxPagesPerView {
		return MaxPagesPerView
	}
	if pages < 1 {
		return 1
	}
	return pages
}

// PlanSlices turns a tag universe into the list of views to crawl.
//
// The plan rests on one observation: an asset is fully reachable if it carries
// at least one tag whose total count fits within a single view. Assets carry
// roughly nine tags each and most tags are small, so a slice per small tag
// covers the large majority of the catalogue. What remains - assets whose
// every tag is too big to page through - is reached by pairing big tags with
// each other.
//
// Pure function: no I/O, no network. All the crawl's cost is decided here.
func PlanSlices(tags []models.Tag, itemsPerPage int64) []Slice {
	ceiling := MaxPagesPerView * itemsPerPage

	var small, big []models.Tag
	for _, t := range tags {
		if t.Slug == "" {
			continue
		}
		if t.Count > ceiling {
			big = append(big, t)
		} else {
			small = append(small, t)
		}
	}

	// Deterministic order regardless of discovery order, so a re-plan of the
	// same tag set produces the same crawl.
	sort.Slice(big, func(i, j int) bool { return big[i].Slug < big[j].Slug })
	sort.Slice(small, func(i, j int) bool { return small[i].Slug < small[j].Slug })

	var out []Slice

	// A small tag fits in one view, so one slice covers it completely.
	for _, t := range small {
		out = append(out, Slice{Tags: []string{t.Slug}, Count: t.Count})
	}

	// A big tag cannot be paged through, but each ordering exposes a different
	// window onto it, so take all four.
	for _, t := range big {
		for _, srt := range allSorts {
			out = append(out, Slice{Sort: srt, Tags: []string{t.Slug}, Count: t.Count})
		}
	}

	// The residual: assets every one of whose tags is big. Pairing two big
	// tags narrows the result set enough to page through. Pairs involving a
	// small tag are deliberately not generated - those assets are already
	// covered above, and including them would turn a few hundred slices into
	// hundreds of thousands.
	for i := 0; i < len(big); i++ {
		for j := i + 1; j < len(big); j++ {
			a, b := big[i].Slug, big[j].Slug
			if a > b {
				a, b = b, a
			}
			// The true intersection size is unknown without asking itch.io.
			// The smaller of the two is its upper bound, which is all the
			// ordering below needs.
			count := big[i].Count
			if big[j].Count < count {
				count = big[j].Count
			}
			out = append(out, Slice{Tags: []string{a, b}, Count: count})
		}
	}

	// Largest first, so the coverage target is approached as fast as possible
	// and the cheap tail is what gets dropped when the crawl stops early.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })

	// The root view leads, unconditionally: it is the only view whose page
	// numbers are a global popularity rank (see InvPopularity handling in the
	// dataservice), so it has to be crawled before anything else assigns one.
	return append([]Slice{{Count: 0}}, out...)
}
