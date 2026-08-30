package main

import (
	"fmt"
	"math"
	"time"
)

// fuzzyETA turns a remaining-time estimate into the wording the progress log
// prints. Fuzzy on purpose: the estimate is built from an average pace over a
// crawl whose per-page cost varies with rate-limit pauses and slice
// abandonment, so a precise "left" figure would claim a confidence the input
// does not have.
//
// The 45m-90m band is deliberately its own bucket rather than a rounding
// boundary between the minutes and hours wording: nearest-10-minutes rounding
// would turn 55 minutes into "60 minutes left", and nearest-whole-hour
// rounding would turn 65 minutes into "1 hours left" - both readable as bugs.
// Naming the whole band "an hour" avoids either.
func fuzzyETA(remaining time.Duration) string {
	switch {
	case remaining < 5*time.Minute:
		return "almost done"
	case remaining < 45*time.Minute:
		minutes := int(math.Round(remaining.Minutes()/10)) * 10
		if minutes < 10 {
			minutes = 10
		}
		return fmt.Sprintf("roughly %d minutes left", minutes)
	case remaining <= 90*time.Minute:
		return "roughly an hour left"
	default:
		hours := int(math.Round(remaining.Hours()))
		return fmt.Sprintf("roughly %d hours left", hours)
	}
}
