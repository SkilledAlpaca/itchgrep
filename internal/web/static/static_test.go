package static

import (
	"os"
	"regexp"
	"strings"
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
		{"app.cccccccccccc.css", "app.css", false, true},
		{"app.deadbeef0000.css", "app.css", false, true},
		{"app.notahash.css", "", false, false},
		{"app.AAAAAAAAAAAA.css", "", false, false},
		{"app.aaaaaaaaaaa.css", "", false, false},
		{"app.aaaaaaaaaaaaa.css", "", false, false},
		{"app.cccccccccccc.js", "", false, false},
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

// TestAppJSDefinesEveryFunctionItsListenersCall guards against an edit that
// deletes a helper another listener still calls. The failure is silent in Go and
// shows up only as a ReferenceError in the browser, inside an htmx event handler
// that then skips everything after it.
//
// It reads app.js, drops strings and comments, collects every bare call
// (identifier followed by "(", not preceded by "."), and requires each to be a
// function the file defines, a keyword, or a browser global from the allowlist.
func TestAppJSDefinesEveryFunctionItsListenersCall(t *testing.T) {
	src, err := os.ReadFile("app.js")
	require.NoError(t, err)

	code := regexp.MustCompile(`'(?:[^'\\\n]|\\.)*'`).ReplaceAllString(string(src), "''")
	code = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(code, "")
	code = regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(code, "")

	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`function\s+([A-Za-z_$][\w$]*)\s*\(`).FindAllStringSubmatch(code, -1) {
		defined[m[1]] = true
	}
	// Parameters and local variables holding callbacks are not definitions the
	// file can lose.
	for _, m := range regexp.MustCompile(`function\s*[\w$]*\s*\(([^)]*)\)`).FindAllStringSubmatch(code, -1) {
		for _, p := range strings.Split(m[1], ",") {
			if p = strings.TrimSpace(p); p != "" {
				defined[p] = true
			}
		}
	}
	allowed := map[string]bool{}
	for _, name := range strings.Fields(`if for while switch catch function return typeof new
		Date RegExp URLSearchParams Object Array String Number Boolean JSON Math
		parseInt parseFloat isNaN encodeURIComponent decodeURIComponent
		htmx setTimeout clearTimeout setInterval requestAnimationFrame matchMedia`) {
		allowed[name] = true
	}

	var missing []string
	for _, m := range regexp.MustCompile(`(?:^|[^.\w$])([A-Za-z_$][\w$]*)\s*\(`).FindAllStringSubmatch(code, -1) {
		if name := m[1]; !defined[name] && !allowed[name] {
			missing = append(missing, name)
		}
	}
	assert.Empty(t, missing, "app.js calls functions it does not define")

	// The regex must be finding calls at all, or an empty result proves nothing.
	for _, name := range []string{"localizeTimestamps", "refreshRelativeTimes", "filterFacets", "expandFacets", "restoreFacetState"} {
		assert.True(t, defined[name], name+" should be defined")
	}
}
