package templates

import "strings"

// PageTitle is the document title for a set of filters. It is the single
// source for the title of the full page, for the <title> in the fragment a
// filter swap returns (see ResultsFragment), and, through filterSummary, for
// the live region's announcement - so the three cannot drift apart.
//
// likeTitle is the title of the ?like= source asset, which only the search
// layer can supply; pass "" when it is not known.
func PageTitle(f Filters, likeTitle string) string {
	if summary := filterSummary(f, likeTitle); summary != "" {
		return summary + " — ITCHGREP"
	}
	return "ITCHGREP"
}

// filterSummary names everything that narrows a result set, in the order the
// visitor reads the chips: "knight, pixel-art, not 3d, by LimeZu, Free".
// Empty when nothing narrows it.
func filterSummary(f Filters, likeTitle string) string {
	var parts []string
	if f.Query != "" {
		parts = append(parts, f.Query)
	}
	parts = append(parts, f.Tags...)
	for _, tag := range f.NotTags {
		parts = append(parts, "not "+tag)
	}
	if f.Like != "" {
		if likeTitle != "" {
			parts = append(parts, "similar to "+likeTitle)
		} else {
			// An id that matched nothing still changes the page, so it still
			// belongs in the title - see feedTitle for the same fallback.
			parts = append(parts, "similar to game "+f.Like)
		}
	}
	if f.Author != "" {
		parts = append(parts, "by "+f.Author)
	}
	if f.Price != "" {
		parts = append(parts, priceFilterLabel(f.Price))
	}
	return strings.Join(parts, ", ")
}
