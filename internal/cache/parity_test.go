package cache

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests hold the in-memory engine to the search index it replaces for
// requests with no text query. The index is the reference: built from the same
// assets with models.NewIndexedAsset and storage.IndexMapping, the code the
// dataservice indexes with, and queried through the same buildQuery. Any
// difference in the ids, their order, the total or the sidebar is a bug in the
// engine, not a difference of opinion.

// parityAssets generates a catalog shaped like the real one where it matters
// for ordering: many ties in InvPopularity (a page number, 36 to a page), a
// recency rank on a minority, first-seen stamps shared by whole publishes,
// titles that collide once folded, authors that collide once folded, prices in
// several currencies and at the band edges, some unreadable, and assets with no
// tags. The file order is shuffled, because the order assets.json lists them in
// is what breaks a tie.
//
// uniquePopularity gives every asset its own InvPopularity, which makes the
// whole ordering deterministic. Without it the order inside a tie is up to the
// index: bleve numbers documents in the order it analyzed a batch, which is a
// map iteration, so no two builds of the same index agree on it and there is
// nothing for the engine to match. Real data has the ties (a page of 36 shares
// one number), so the tie tests compare sort keys rather than ids.
func parityAssets(seed int64, n int, uniquePopularity bool) []models.Asset {
	rng := rand.New(rand.NewSource(seed))

	vocab := make([]string, 70)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("tag-%02d", i)
	}
	words := []string{"Pixel", "Sword", "Forest", "Tile", "ui", "Icons", "Robot", "Castle", "Zebra", "alpha", "Music", "Loop"}
	authors := make([]string, 90)
	for i := range authors {
		authors[i] = fmt.Sprintf("Maker %d", i)
	}
	prices := []string{"$0.99", "$4.99", "$5", "$5.00", "$5.01", "$19.99", "$20", "$20.00", "$49.00", "4.50€", "17.99€", "£3.00", "¥500", "$0", "weird", "CHF 5"}
	stamps := []time.Time{time.Unix(1_700_000_000, 0), time.Unix(1_710_000_000, 0), time.Unix(1_720_000_000, 0), time.Unix(1_730_000_000, 0)}

	assets := make([]models.Asset, n)
	for i := range assets {
		a := &assets[i]
		a.GameId = fmt.Sprint(100000 + i)
		a.Title = words[rng.Intn(len(words))] + " " + words[rng.Intn(len(words))]
		switch rng.Intn(4) {
		case 0:
			a.Title = "  " + a.Title
		case 1:
			a.Title = " " + a.Title + " "
		}
		a.Author = authors[rng.Intn(len(authors))]
		if rng.Intn(5) == 0 {
			// Folds to the same AuthorKey as the plain form.
			a.Author = "  " + a.Author[:3] + "  " + a.Author[4:] + " "
		}
		a.Description = "d"
		// Skewed toward the head of the vocabulary, like real tags.
		seen := map[string]bool{}
		for k := rng.Intn(9); k > 0; k-- {
			t := vocab[int(rng.ExpFloat64()*14)%len(vocab)]
			if !seen[t] {
				seen[t] = true
				a.Tags = append(a.Tags, t)
			}
		}
		slices.Sort(a.Tags)
		if rng.Intn(10) >= 6 {
			a.Price = prices[rng.Intn(len(prices))]
		}
		a.InvPopularity = int64(i/36 + 1)
		if uniquePopularity {
			a.InvPopularity = int64(i + 1)
		}
		if rng.Intn(10) < 3 {
			a.InvRecency = int64(1 + rng.Intn(400))
		}
		if rng.Intn(10) < 5 {
			a.FirstSeen = stamps[rng.Intn(len(stamps))]
		}
	}
	rng.Shuffle(len(assets), func(i, j int) { assets[i], assets[j] = assets[j], assets[i] })
	return assets
}

type parityRequest struct {
	opts SearchOptions
}

// randomRequest is one filter combination. Tags and exclusions come from the
// head of the vocabulary mostly (so combinations still match something), with
// the occasional unknown tag, an overlap between the two lists, and the edge
// cases the handlers can send: an unknown author, an unknown or tagless
// ?like=, a first-seen floor, a Limit, and later pages.
func randomRequest(rng *rand.Rand, assets []models.Asset) SearchOptions {
	pick := func() string {
		if rng.Intn(40) == 0 {
			return "no-such-tag"
		}
		return fmt.Sprintf("tag-%02d", int(rng.ExpFloat64()*6)%70)
	}
	var o SearchOptions
	for k := rng.Intn(4); k > 0; k-- {
		o.Tags = append(o.Tags, pick())
	}
	for k := rng.Intn(3); k > 0; k-- {
		o.ExcludeTags = append(o.ExcludeTags, pick())
	}
	if rng.Intn(5) == 0 {
		switch rng.Intn(4) {
		case 0:
			o.Author = "Nobody Atall"
		case 1:
			o.Author = "MAKER   7"
		default:
			o.Author = assets[rng.Intn(len(assets))].Author
		}
	}
	if rng.Intn(2) == 0 {
		o.Price = []string{models.PricingFree, models.PricingPaid, models.PricingUnder5, models.PricingUnder20}[rng.Intn(4)]
	}
	o.Sort = []string{"", models.SortPopular, models.SortTitle, models.SortPrice, models.SortRecent, models.SortNew}[rng.Intn(6)]
	if rng.Intn(6) == 0 {
		o.MinFirstSeen = []int64{1, 1_710_000_000, 1_720_000_000, 1_730_000_000, 1_740_000_000}[rng.Intn(5)]
	}
	if rng.Intn(10) == 0 {
		switch rng.Intn(3) {
		case 0:
			o.Like = "999999999"
		default:
			o.Like = assets[rng.Intn(len(assets))].GameId
		}
	}
	o.Page = int64(1 + rng.Intn(3))
	if o.MinFirstSeen > 0 && rng.Intn(2) == 0 {
		// Only /new and /feed cap a page, and both are first-seen listings.
		o.Page = 1
		o.Limit = int64(1 + rng.Intn(80))
	}
	o.NoFacets = rng.Intn(4) == 0
	return o
}

func tagList(r Results) []models.Tag {
	if len(r.Tags) == 0 {
		return nil
	}
	return r.Tags
}

// sortKeys is the tuple an ordering sorts by, for comparing two orderings of
// assets that tie.
func sortKeys(r Results, sortKey string) []string {
	out := make([]string, len(r.Assets))
	for i, a := range r.Assets {
		usd, _ := a.PriceUSD(storedRates)
		switch sortKey {
		case models.SortTitle:
			out[i] = fmt.Sprintf("%s|%d", strings.ToLower(strings.TrimSpace(a.Title)), a.InvPopularity)
		case models.SortPrice:
			out[i] = fmt.Sprintf("%v|%d", usd, a.InvPopularity)
		case models.SortRecent:
			out[i] = fmt.Sprintf("%d|%d", a.RecencyRank(), a.InvPopularity)
		case models.SortNew:
			out[i] = fmt.Sprintf("%d|%d", a.FirstSeenUnix(), a.InvPopularity)
		default:
			out[i] = fmt.Sprint(a.InvPopularity)
		}
	}
	return out
}

// compareWithSearchIndex runs combinations random requests through Find (the
// engine, or browse) and through the search index directly, and compares what a
// visitor sees. With exact set the ids must match in order; otherwise the sort
// keys must (see parityAssets for why).
func compareWithSearchIndex(t *testing.T, c *Cache, assets []models.Asset, seed int64, combinations int, exact bool) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	var nonEmpty, withTags, sorted int
	for n := 0; n < combinations; n++ {
		opts := randomRequest(rng, assets)
		desc := fmt.Sprintf("#%d %+v", n, opts)

		// Find applies the normalization the handlers rely on (a recency sort
		// without ranks, a price band without converted prices); this cache has
		// both, so opts reach both sides unchanged.
		got, err := c.Find(opts)
		require.NoError(t, err, desc)
		want, err := c.searchIndex(opts, false)
		require.NoError(t, err, desc)

		if exact {
			assert.Equal(t, ids(want), ids(got), desc)
		} else {
			assert.Equal(t, sortKeys(want, opts.resolvedSort()), sortKeys(got, opts.resolvedSort()), desc)
			assert.Len(t, got.Assets, len(want.Assets), desc)
		}
		assert.Equal(t, want.Total, got.Total, desc)
		assert.Equal(t, want.LikeTitle, got.LikeTitle, desc)
		if !opts.NoFacets {
			assert.Equal(t, tagList(want), tagList(got), "sidebar: "+desc)
			if len(got.Tags) > 0 {
				withTags++
			}
		}
		if len(got.Assets) > 0 {
			nonEmpty++
		}
		if opts.resolvedSort() != models.SortPopular {
			sorted++
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	// A parity test that mostly compared empty answers would prove nothing.
	assert.Greater(t, nonEmpty, combinations/2, "most combinations must return assets")
	assert.Greater(t, withTags, combinations/4, "many combinations must compare a non-empty sidebar")
	assert.Greater(t, sorted, combinations/2, "most combinations must use a non-default sort")
	t.Logf("%d combinations, %d with assets, %d with a sidebar, %d sorted", combinations, nonEmpty, withTags, sorted)
}

// TestEngineMatchesSearchIndex is the parity test: 800 random filter
// combinations on a generated catalog, ids in order, totals, sidebar and all.
func TestEngineMatchesSearchIndex(t *testing.T) {
	assets := parityAssets(20261031, 3000, true)
	c := newLoadedCacheFromAssets(t, 36, assets)
	require.NotNil(t, c.eng)
	compareWithSearchIndex(t, c, assets, 7, 800, true)
}

// TestEngineMatchesSearchIndexWithTies is the same on a catalog where
// popularity ties, as it does in real data: the order inside a tie is the
// index's own accident, so what must agree is the keys, the totals and the
// sidebar.
func TestEngineMatchesSearchIndexWithTies(t *testing.T) {
	assets := parityAssets(99, 3000, false)
	c := newLoadedCacheFromAssets(t, 36, assets)
	compareWithSearchIndex(t, c, assets, 13, 500, false)
}

// TestEngineMatchesSearchIndexUnfiltered walks every sort with no filter at all
// and several pages, the shapes served from the precomputed orderings.
func TestEngineMatchesSearchIndexUnfiltered(t *testing.T) {
	assets := parityAssets(11, 1500, true)
	c := newLoadedCacheFromAssets(t, 36, assets)
	for _, sortKey := range []string{"", models.SortPopular, models.SortTitle, models.SortPrice, models.SortRecent, models.SortNew} {
		for _, page := range []int64{1, 2, 7, 42, 43, 44} {
			for _, nofacets := range []bool{false, true} {
				opts := SearchOptions{Sort: sortKey, Page: page, NoFacets: nofacets}
				got, err := c.Find(opts)
				require.NoError(t, err)
				want, err := c.searchIndex(opts, false)
				require.NoError(t, err)
				desc := fmt.Sprintf("%+v", opts)
				assert.Equal(t, ids(want), ids(got), desc)
				assert.Equal(t, want.Total, got.Total, desc)
				if !nofacets {
					assert.Equal(t, tagList(want), tagList(got), desc)
				}
			}
		}
	}
}

// TestEngineNewMatchesSearchIndex is /new as the handler asks for it.
func TestEngineNewMatchesSearchIndex(t *testing.T) {
	assets := parityAssets(5, 2000, true)
	c := newLoadedCacheFromAssets(t, 36, assets)
	opts := SearchOptions{MinFirstSeen: 1, Sort: models.SortNew, Limit: 72, Page: 1, NoFacets: true}
	got, err := c.Find(opts)
	require.NoError(t, err)
	want, err := c.searchIndex(opts, false)
	require.NoError(t, err)
	assert.Len(t, got.Assets, 72)
	assert.Equal(t, ids(want), ids(got))
	assert.Equal(t, want.Total, got.Total)
}

func TestEngineHandlesPageNumberOverflow(t *testing.T) {
	c := newLoadedCache(t, 36)
	got, err := c.Find(SearchOptions{Tags: []string{"2d"}, Page: 1 << 62})
	require.NoError(t, err)
	assert.Empty(t, got.Assets)
	assert.EqualValues(t, 0, got.Total, "an out-of-range page is the empty page, as the index path gives")
}
