package templates

import (
	"fmt"
	"math"

	"itchgrep/pkg/models"
)

// Coverage is how much of itch.io's catalogue the served index holds.
//
// It is shown because the alternative is worse than saying nothing: a site
// reporting "96,903 assets" with no denominator invites the reading that this
// is the whole catalogue, and a search that finds nothing then looks like proof
// the asset does not exist rather than that it was never indexed.
type Coverage struct {
	Indexed   int64
	Catalogue int64
}

// NewCoverage takes the figures a crawl recorded.
func NewCoverage(s models.Stats) Coverage {
	return Coverage{Indexed: s.Indexed, Catalogue: s.Catalogue}
}

// Known reports whether there is anything to state. False for an index built
// before crawls recorded their completeness, and false for a catalogue total of
// zero, which would otherwise divide into an infinite percentage.
func (c Coverage) Known() bool {
	return c.Indexed > 0 && c.Catalogue > 0
}

// Percent is the covered fraction, rounded to whole points.
//
// Clamped at 100 because the two numbers are measured at different moments: the
// catalogue total is read when the crawl starts and the indexed count when it
// finishes, so a catalogue that shrinks mid-crawl can produce more than 100%.
// That is an artefact of the measurement, not a discovery.
func (c Coverage) Percent() int {
	if !c.Known() {
		return 0
	}
	pct := int(math.Round(float64(c.Indexed) / float64(c.Catalogue) * 100))
	if pct > 100 {
		return 100
	}
	return pct
}

// Label is the short form shown in the masthead.
func (c Coverage) Label() string {
	if !c.Known() {
		return ""
	}
	return fmt.Sprintf("%d%% of the catalogue", c.Percent())
}

// Detail spells out the rounded percentage for a tooltip and for screen
// readers, so the figure is checkable rather than merely asserted.
func (c Coverage) Detail() string {
	if !c.Known() {
		return ""
	}
	return fmt.Sprintf("%s of %s assets on itch.io were reachable and indexed",
		humaniseCount(c.Indexed), humaniseCount(c.Catalogue))
}

// humaniseCount groups thousands with commas. The sidebar counts are formatted
// the same way, and a bare "96903" beside them would read as a different kind
// of number.
func humaniseCount(n int64) string {
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
