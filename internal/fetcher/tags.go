package fetcher

import (
	"fmt"
	"io"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// tagDirectoryURL is itch.io's own directory of asset tags, and the best seed
// available: it lists the whole asset tag vocabulary in one document (~607
// slugs, and ?page= does not extend it - later pages repeat the same set).
//
// It matters because the alternatives undercount badly. The browse sitemap
// carries only ~39 asset facets, and a breadth-first walk of co-tag links from
// those converges at ~135 tags - a clique of popular tags that link to each
// other. The long tail is invisible from there, and the long tail is exactly
// what makes assets reachable: an asset can only be paged through completely
// if it carries a tag small enough to fit under the 200-page cap.
//
// robots.txt disallows /search but not /tags.
var tagDirectoryURL = "https://itch.io/tags/assets"

// browseSitemapURL is the sitemap itch.io advertises in robots.txt. It is the
// fallback seed if the tag directory ever moves or stops parsing.
//
// Package-level vars so tests can point them at an httptest.Server.
var browseSitemapURL = "https://itch.io/sitemaps/browse.xml"

// DefaultMaxTags bounds discovery. itch.io's tag vocabulary has a long tail of
// near-empty tags that cost a request each and cover nothing, and without a
// bound a BFS over co-tag links has no natural stopping point.
//
// The bound matters for wall-clock, not just politeness: discovery costs one
// request per tag, so at one request per second a full 1000-tag pass takes
// ~17 minutes on its own. That is why the result is cached (see
// storage.PutTags) and why the limit is configurable.
const DefaultMaxTags = 1000

// tagLinkPattern matches the asset-facet links that appear both in the browse
// sitemap and in the co-tag lists on each facet page.
var tagLinkPattern = regexp.MustCompile(`/game-assets/tag-([a-z0-9][a-z0-9-]*)`)

// gameCountPattern extracts the result count from a facet page. It renders
// two ways on the same page - `<div class="game_count">36,324 results</div>`
// and `<nobr class="game_count"> (36,324 results)</nobr>` - so the leading
// space and parenthesis both have to be optional.
var gameCountPattern = regexp.MustCompile(`game_count[^>]*>\s*\(?([\d,]+)`)

// DiscoverTags walks itch.io's asset tag universe, returning each tag with the
// number of assets carrying it.
//
// It breadth-first searches from the facets listed in the browse sitemap,
// harvesting the co-tag links every facet page offers (~31 per page). Passing
// a non-nil seed skips the sitemap fetch entirely, which is how tests stay off
// the network.
//
// Every request goes through doGet, so discovery is paced by the same limiter
// as the scrape and carries the session cookie.
func DiscoverTags(seed []string, maxTags int) ([]models.Tag, error) {
	if maxTags <= 0 {
		maxTags = DefaultMaxTags
	}
	if seed == nil {
		var err error
		seed, err = discoverySeed()
		if err != nil {
			return nil, fmt.Errorf("seeding tag discovery: %w", err)
		}
	}
	if len(seed) == 0 {
		return nil, fmt.Errorf("no seed tags found")
	}

	seen := make(map[string]bool, len(seed))
	queue := make([]string, 0, len(seed))
	for _, s := range seed {
		if !seen[s] {
			seen[s] = true
			queue = append(queue, s)
		}
	}

	tags := make([]models.Tag, 0, len(queue))
	for len(queue) > 0 && len(tags) < maxTags {
		slug := queue[0]
		queue = queue[1:]

		count, coTags, err := fetchTagPage(slug)
		if err != nil {
			// One unreachable facet must not abort a discovery pass that has
			// already cost hundreds of requests.
			logging.Warning("Tag discovery: skipping %q: %v", slug, err)
			continue
		}
		tags = append(tags, models.Tag{Slug: slug, Count: count})

		for _, co := range coTags {
			if !seen[co] && len(seen) < maxTags {
				seen[co] = true
				queue = append(queue, co)
			}
		}
	}

	sort.Slice(tags, func(i, j int) bool { return tags[i].Count > tags[j].Count })
	logging.Info("Tag discovery: %d tags", len(tags))
	return tags, nil
}

// discoverySeed returns the slugs to start the walk from, preferring the tag
// directory and falling back to the browse sitemap.
//
// The fallback is deliberately quiet about which one it used only in the happy
// path: a silent drop from ~607 seeds to ~39 would cost most of the catalogue's
// reachability while still producing a plausible-looking crawl.
func discoverySeed() ([]string, error) {
	slugs, err := fetchTagLinks(tagDirectoryURL)
	switch {
	case err != nil:
		logging.Warning("Tag directory %s unavailable (%v), falling back to the browse sitemap; expect far fewer tags and lower coverage", tagDirectoryURL, err)
	case len(slugs) == 0:
		logging.Warning("Tag directory %s parsed to zero tags, falling back to the browse sitemap; expect far fewer tags and lower coverage", tagDirectoryURL)
	default:
		logging.Info("Tag directory: %d asset tags", len(slugs))
		return slugs, nil
	}
	return fetchTagLinks(browseSitemapURL)
}

// fetchTagLinks pulls every asset facet slug referenced by a document. It
// serves both seed sources, which happen to need identical handling: the
// sitemap is XML and the directory is HTML, but the slugs are in <loc> and
// href attributes that tagLinkPattern matches the same way.
func fetchTagLinks(url string) ([]string, error) {
	resp, _, err := doGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return uniqueTagSlugs(string(body)), nil
}

// fetchTagPage returns a facet's asset count and the co-tags it links to.
func fetchTagPage(slug string) (int64, []string, error) {
	s := Slice{Tags: []string{slug}}
	resp, _, err := doGet(baseURL + s.Path())
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	html := string(body)

	count, err := parseGameCount(html)
	if err != nil {
		return 0, nil, err
	}

	coTags := uniqueTagSlugs(html)
	// A page always links to itself; that is not a new tag.
	filtered := coTags[:0]
	for _, c := range coTags {
		if c != slug {
			filtered = append(filtered, c)
		}
	}
	return count, filtered, nil
}

// parseGameCount reads the "(36,324 results)" figure off a browse page.
func parseGameCount(html string) (int64, error) {
	m := gameCountPattern.FindStringSubmatch(html)
	if m == nil {
		return 0, fmt.Errorf("no game_count found")
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing game_count %q: %w", m[1], err)
	}
	return n, nil
}

// uniqueTagSlugs extracts every distinct asset tag slug referenced in a
// document, in first-seen order.
func uniqueTagSlugs(doc string) []string {
	matches := tagLinkPattern.FindAllStringSubmatch(doc, -1)
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
