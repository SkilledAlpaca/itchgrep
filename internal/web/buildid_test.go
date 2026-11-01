package web

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var buildIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{12}|t\d+)$`)

func TestBuildIDIsStableAcrossHandlers(t *testing.T) {
	// A restart of the same binary must not look like a new release, or every
	// open tab would reload.
	_, a := newTestRouter()
	_, b := newTestRouter()
	require.Regexp(t, buildIDPattern, a.build)
	assert.Equal(t, a.build, b.build)
	assert.Equal(t, a.build, BuildID())
}

func TestEveryFragmentResponseCarriesTheBuild(t *testing.T) {
	r, h := newTestRouter()
	// The test router's cache never loaded, so these are the 503 problem
	// fragments app.js swaps; the 400 is the bad-page fragment. Both are
	// swapped by htmx and so must name their release too.
	for _, target := range []string{
		"/results?page=1",
		"/results?page=0",
		"/results?page=2&tags=a",
		"/results/tags?tags=a&v=1",
	} {
		w := get(t, r, target)
		assert.Equal(t, h.build, w.Header().Get("X-Itchgrep-Build"), target+" status "+http.StatusText(w.Code))
	}
	assert.Empty(t, get(t, r, "/results").Header().Values("Vary"), "the build must not fragment the edge cache")
}

func TestLayoutCarriesTheBuildMeta(t *testing.T) {
	r, h := newTestRouter()
	for _, target := range []string{"/", "/about", "/stats", "/new", "/nope"} {
		body := get(t, r, target).Body.String()
		assert.Contains(t, body, `<meta name="itchgrep-build" content="`+h.build+`"`, target)
	}
}
