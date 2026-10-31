package templates

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssetIsFingerprinted(t *testing.T) {
	assert.Regexp(t, `^/static/app\.[0-9a-f]{12}\.css$`, Asset("app.css"))
	assert.Regexp(t, `^/static/app\.[0-9a-f]{12}\.js$`, Asset("app.js"))
	assert.Regexp(t, `^/static/htmx\.min\.[0-9a-f]{12}\.js$`, Asset("htmx.min.js"))
}

func TestLayoutLinksTheFingerprintedAssets(t *testing.T) {
	html := render(t, Layout("T", Shell{}, About()))
	for _, name := range []string{"app.css", "app.js", "htmx.min.js"} {
		assert.Contains(t, html, Asset(name))
	}
	assert.NotRegexp(t, regexp.MustCompile(`(?:href|src)="/static/(?:app|htmx\.min)\.(?:css|js)"`), html,
		"no bare, unversioned asset URL may remain in the layout")
	require.Contains(t, html, `rel="stylesheet"`)
}
