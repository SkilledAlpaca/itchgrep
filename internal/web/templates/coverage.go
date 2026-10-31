package templates

import (
	"fmt"
	"math"

	"itchgrep/pkg/models"
)

// Coverage is how much of itch.io's catalog the served index holds.
//
// It is shown because the alternative is worse than saying nothing: a site
// reporting "96,903 assets" with no denominator invites the reading that this
// is the whole catalog, and a search that finds nothing then looks like proof
// the asset does not exist rather than that it was never indexed.
type Coverage struct {
	Indexed int64
	Catalog int64
}

// NewCoverage takes the figures a crawl recorded.
func NewCoverage(s models.Stats) Coverage {
	return Coverage{Indexed: s.Indexed, Catalog: s.Catalog}
}

// Known reports whether there is anything to state. False for an index built
// before crawls recorded their completeness, and false for a catalog total of
// zero, which would otherwise divide into an infinite percentage.
func (c Coverage) Known() bool {
	return c.Indexed > 0 && c.Catalog > 0
}

// Percent is the covered fraction, rounded to whole points.
//
// Clamped at 100 because the two numbers are measured at different moments: the
// catalog total is read when the crawl starts and the indexed count when it
// finishes, so a catalog that shrinks mid-crawl can produce more than 100%.
// That is an artifact of the measurement, not a discovery.
func (c Coverage) Percent() int {
	if !c.Known() {
		return 0
	}
	pct := int(math.Round(float64(c.Indexed) / float64(c.Catalog) * 100))
	if pct > 100 {
		return 100
	}
	return pct
}

// Label is the whole of what the masthead says. Deliberately one short clause:
// this is a caveat sitting under the index age, not a paragraph.
func (c Coverage) Label() string {
	if !c.Known() {
		return ""
	}
	return fmt.Sprintf("%d%% of catalog indexable", c.Percent())
}

// Detail spells the rounded percentage out on hover, so the figure is checkable
// without spending a line on it.
func (c Coverage) Detail() string {
	if !c.Known() {
		return ""
	}
	return fmt.Sprintf("%s of %s assets on itch.io were reachable and indexed",
		humanizeCount(c.Indexed), humanizeCount(c.Catalog))
}

// humanizeCount groups thousands with commas. It is the one count formatter in
// this package - the sidebar facet counts, /stats' traffic figures and the
// history table all go through it, so a bare "96903" never turns up beside a
// "96,903" formatted a different way. Generic over the two integer widths the
// callers hold (the index's int64 counts, the metrics' uint64 counters) so none
// of them needs a conversion wrapper.
func humanizeCount[N ~int | ~int64 | ~uint64](n N) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, digit := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digit)
	}
	return string(out)
}
