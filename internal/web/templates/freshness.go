package templates

import (
	"fmt"
	"time"
)

// staleAfter is when the index stops being described neutrally and starts
// being flagged.
//
// Deliberately generous. A crawl is triggered by hand and takes hours, so a
// two-day-old index is normal operation, not a fault - and an indicator that is
// amber most of the time teaches people to ignore it, which costs the one
// occasion it matters. A week means the crawl has actually stopped happening.
const staleAfter = 7 * 24 * time.Hour

// Freshness is how old the served index is, resolved at render time.
//
// It exists so the templates never call time.Now() themselves: the age has to
// be computed from a clock somebody can pin in a test, or the only way to check
// the wording of "3 days ago" is to wait three days.
type Freshness struct {
	// Updated is when the dataset now being served was published. Zero when
	// nothing has loaded yet, which is not the same as "published at the epoch".
	Updated time.Time
	Age     time.Duration
}

// NewFreshness measures an index published at updated against the clock now.
func NewFreshness(updated, now time.Time) Freshness {
	if updated.IsZero() {
		return Freshness{}
	}
	age := now.Sub(updated)
	// A published timestamp slightly in the future is a clock skew between
	// whoever wrote the file and whoever is serving it, not a prediction.
	// Clamping keeps it out of the wording rather than rendering "-1 hours".
	if age < 0 {
		age = 0
	}
	return Freshness{Updated: updated, Age: age}
}

// Known reports whether there is a published dataset to describe. False while
// the server is still loading one, when saying anything about freshness would
// be inventing it.
func (f Freshness) Known() bool { return !f.Updated.IsZero() }

// Stale reports whether the index is old enough to be worth flagging.
func (f Freshness) Stale() bool { return f.Known() && f.Age >= staleAfter }

// Label is the human reading of the age.
//
// Coarse on purpose. These pages are served with max-age=300, so a shared cache
// can hand out this string for five minutes after it was rendered; minute-level
// precision would be precision the response cannot honour. Hours and days
// survive that window intact.
func (f Freshness) Label() string {
	switch {
	case !f.Known():
		return "unknown"
	case f.Age < time.Hour:
		return "less than an hour ago"
	case f.Age < 2*time.Hour:
		return "1 hour ago"
	case f.Age < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(f.Age.Hours()))
	case f.Age < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(f.Age.Hours()/24))
	}
}

// Absolute is the exact publication time, shown on hover and to screen readers
// so the rounded label above is never the only thing on offer.
func (f Freshness) Absolute() string {
	if !f.Known() {
		return ""
	}
	return f.Updated.UTC().Format("2 January 2006, 15:04 UTC")
}

// DateTime is the machine-readable form for the <time> element's attribute.
func (f Freshness) DateTime() string {
	if !f.Known() {
		return ""
	}
	return f.Updated.UTC().Format(time.RFC3339)
}
