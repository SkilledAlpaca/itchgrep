package static

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashesAreStableAndTrackContent(t *testing.T) {
	a := fstest.MapFS{"x.css": {Data: []byte("one")}, "x.js": {Data: []byte("two")}}
	h1, err := Hashes(a)
	require.NoError(t, err)
	h2, err := Hashes(a)
	require.NoError(t, err)
	assert.Equal(t, h1, h2, "same bytes, same hash")
	assert.Len(t, h1["x.css"], hashLen)
	assert.NotEqual(t, h1["x.css"], h1["x.js"])

	b := fstest.MapFS{"x.css": {Data: []byte("one!")}}
	h3, err := Hashes(b)
	require.NoError(t, err)
	assert.NotEqual(t, h1["x.css"], h3["x.css"], "changed bytes, changed hash")
}

func TestURLPutsTheHashBeforeTheLastExtension(t *testing.T) {
	h := map[string]string{"app.css": "aaaaaaaaaaaa", "htmx.min.js": "bbbbbbbbbbbb"}
	assert.Equal(t, "/static/app.aaaaaaaaaaaa.css", urlFor(h, "app.css"))
	assert.Equal(t, "/static/htmx.min.bbbbbbbbbbbb.js", urlFor(h, "htmx.min.js"))
	assert.Equal(t, "/static/nope.css", urlFor(h, "nope.css"))
}

func TestResolve(t *testing.T) {
	h := map[string]string{"app.css": "aaaaaaaaaaaa", "htmx.min.js": "bbbbbbbbbbbb"}
	cases := []struct {
		rel       string
		name      string
		immutable bool
		ok        bool
	}{
		{"app.css", "app.css", false, true},
		{"app.aaaaaaaaaaaa.css", "app.css", true, true},
		{"htmx.min.js", "htmx.min.js", false, true},
		{"htmx.min.bbbbbbbbbbbb.js", "htmx.min.js", true, true},
		{"app.cccccccccccc.css", "", false, false},
		{"app.aaaaaaaaaaaa.js", "", false, false},
		{"missing.css", "", false, false},
		{"", "", false, false},
		{"../app.css", "", false, false},
	}
	for _, c := range cases {
		name, imm, ok := resolve(h, c.rel)
		assert.Equal(t, c.ok, ok, c.rel)
		assert.Equal(t, c.name, name, c.rel)
		assert.Equal(t, c.immutable, imm, c.rel)
	}
}

func TestEmbeddedFilesAreOnlyTheServedAssets(t *testing.T) {
	for _, name := range []string{"app.css", "app.js", "htmx.min.js"} {
		assert.Len(t, Hash(name), hashLen, name)
	}
	assert.Len(t, hashes, 3, "no Go source or VENDOR.md may be embedded")
}
