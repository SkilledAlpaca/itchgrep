package fetcher

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The markup below is trimmed from real /game-assets responses. Attribute
// order differs between views - the root view emits data-game_id before dir,
// the free view after it - which is why the parser goes through goquery rather
// than a regex.
const (
	paidCell = `<div data-game_id="2476825" dir="auto" class="game_cell has_cover lazy_images">` +
		`<div class="game_thumb"><a class="thumb_link game_link" href="https://x.itch.io/kit">` +
		`<img data-lazy_src="https://img.itch.zone/a.png"/></a></div>` +
		`<div class="game_cell_data"><div class="game_title">` +
		`<a class="title game_link" href="https://x.itch.io/kit">Echoes Audio Kit</a>` +
		`<a class="price_tag meta_tag"><div class="price_value">9.97&euro;</div></a></div>` +
		`<div class="game_text">Sounds.</div>` +
		`<div class="game_author"><a href="https://x.itch.io">pizzadoggy</a></div></div></div>`

	// A pay-what-you-want asset from the free view: it carries a price_tag and
	// a sale_tag, but no price_value at all.
	pwywCell = `<div dir="auto" data-game_id="569368" class="game_cell has_cover lazy_images">` +
		`<div class="game_thumb"><a class="thumb_link game_link" href="https://v.itch.io/lines">` +
		`<img data-lazy_src="https://img.itch.zone/b.png"/></a></div>` +
		`<div class="game_cell_data"><div class="game_title">` +
		`<a class="title game_link" href="https://v.itch.io/lines">Retro Lines</a>` +
		`<a class="price_tag meta_tag sale" title="Pay $0 or more for this asset pack">` +
		`<div class="sale_tag">-35%</div></a></div>` +
		`<div class="game_text">Tiles.</div>` +
		`<div class="game_author"><a href="https://v.itch.io">VEXED</a></div></div></div>`

	// No price markup whatsoever, which is the ordinary free asset.
	freeCell = `<div dir="auto" data-game_id="671751" class="game_cell has_cover">` +
		`<div class="game_thumb"><a class="thumb_link game_link" href="https://w.itch.io/pack">` +
		`<img data-lazy_src="https://img.itch.zone/c.png"/></a></div>` +
		`<div class="game_cell_data"><div class="game_title">` +
		`<a class="title game_link" href="https://w.itch.io/pack">Icon Pack</a></div>` +
		`<div class="game_text">Icons.</div>` +
		`<div class="game_author"><a href="https://w.itch.io">someone</a></div></div></div>`
)

func TestParseAssetPageReadsThePrice(t *testing.T) {
	assets, err := ParseAssetPage(itchResponse{Content: paidCell + pwywCell + freeCell}, 7)
	require.NoError(t, err)
	require.Len(t, assets, 3)

	assert.Equal(t, "9.97€", assets[0].Price)
	assert.False(t, assets[0].Free())

	// The whole point of parsing price_value rather than price_tag: a discount
	// badge on a pay-what-you-want asset must not read as a price.
	assert.Empty(t, assets[1].Price, "a sale badge with no price_value is not a price")
	assert.True(t, assets[1].Free(), "itch.io counts pay-what-you-want at $0 as free")

	assert.Empty(t, assets[2].Price)
	assert.True(t, assets[2].Free())
}

func TestParseAssetPageStillReadsEverythingElse(t *testing.T) {
	assets, err := ParseAssetPage(itchResponse{Content: paidCell}, 7)
	require.NoError(t, err)
	require.Len(t, assets, 1)

	a := assets[0]
	assert.Equal(t, "2476825", a.GameId)
	assert.Equal(t, "Echoes Audio Kit", a.Title)
	assert.Equal(t, "pizzadoggy", a.Author)
	assert.Equal(t, "Sounds.", a.Description)
	assert.Equal(t, "https://x.itch.io/kit", a.Link)
	assert.Equal(t, "https://img.itch.zone/a.png", a.ThumbUrl)
	assert.Equal(t, int64(7), a.InvPopularity)
}
