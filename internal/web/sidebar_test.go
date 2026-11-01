package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/internal/web/templates"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sidebarRouter is perfRouter plus the tag tail route.
func sidebarRouter(t *testing.T) (*chi.Mux, *cache.Cache) {
	t.Helper()
	r, c := perfRouter(t)
	h := NewHandler(c, 0, nil)
	r.Get("/results/tags", h.HandleResultsTags)
	r.Head("/results/tags", h.HandleResultsTags)
	return r, c
}

var gameIDs = regexp.MustCompile(`data-game-id="(\d+)"`)

func cardIDs(body string) []string {
	var out []string
	for _, m := range gameIDs.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestSidebarRendersTheHeadAndLeavesTheRestToTheTail(t *testing.T) {
	r, c := sidebarRouter(t)
	page := get(t, r, "/").Body.String()

	assert.Equal(t, 40, strings.Count(page, "<li data-facet="), "the page carries the head")
	assert.Contains(t, page, fmt.Sprintf(`data-tail-url="/results/tags?v=%d"`, c.DataUpdatedTime().Unix()),
		"the fragment is keyed by the data generation")
	assert.Contains(t, page, `href="/?alltags=1"`)
	assert.Contains(t, page, "More tags")

	list := get(t, r, fmt.Sprintf("/results/tags?v=%d", c.DataUpdatedTime().Unix()))
	require.Equal(t, http.StatusOK, list.Code)
	rows := regexp.MustCompile(`<li data-facet="([^"]+)"`).FindAllStringSubmatch(list.Body.String(), -1)
	assert.Len(t, rows, 60, "the whole ranked list: 60 tags, head and tail")
	assert.Equal(t, 20, strings.Count(list.Body.String(), "data-facet-tail hidden"))

	// The swap replaces the list, so what the visitor ends up with is exactly
	// this response: no tag appears twice, and it starts with the page's head.
	seen := map[string]bool{}
	for _, m := range rows {
		assert.False(t, seen[m[1]], "duplicate row "+m[1])
		seen[m[1]] = true
	}
	head := regexp.MustCompile(`<li data-facet="([^"]+)"`).FindAllStringSubmatch(page, -1)
	for i, m := range head {
		assert.Equal(t, m[1], rows[i][1], "same generation: the page's head leads the list")
	}
}

func TestTagTailFollowsTheFiltersAndTheirLinksKeepThem(t *testing.T) {
	r, _ := sidebarRouter(t)
	for _, target := range []string{
		"/results/tags?tags=tag-00",
		"/results/tags?q=pixel&cur=EUR&sort=title",
		"/results/tags?not=tag-00&price=free",
	} {
		w := get(t, r, target)
		require.Equal(t, http.StatusOK, w.Code, target)
		assert.Contains(t, w.Header().Get("Cache-Control"), "s-maxage=3600", target)
	}
	// The rows' links add a tag to the filters they were fetched for.
	w := get(t, r, "/results/tags?cur=EUR&sort=title")
	assert.Contains(t, w.Body.String(), `hx-get="/results?cur=EUR&amp;page=1&amp;sort=title&amp;tags=`)
}

func TestTagTailFailsEmptyWhenNotReady(t *testing.T) {
	r, _ := newTestRouter()
	w := get(t, r, "/results/tags")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestMoreTagsParameterIsCanonicalAndDoesNotChangeTheResults(t *testing.T) {
	r, _ := sidebarRouter(t)
	// More tags is written where the canonical form puts it: parameters sort by
	// name, so alltags comes first.
	for base, withMore := range map[string]string{
		"/":             "/?alltags=1",
		"/?tags=tag-00": "/?alltags=1&tags=tag-00",
		"/?q=pixel":     "/?alltags=1&q=pixel",
		"/?price=free":  "/?alltags=1&price=free",
	} {
		plain := get(t, r, base)
		more := get(t, r, withMore)
		require.Equal(t, http.StatusOK, plain.Code, base)
		require.Equal(t, http.StatusOK, more.Code, "the canonical redirect must accept alltags: "+base)

		assert.Equal(t, cardIDs(plain.Body.String()), cardIDs(more.Body.String()), "same assets, same order: "+base)
		assert.Equal(t, templatesCount(plain.Body.String()), templatesCount(more.Body.String()), base)
	}

	// The full list is every tag of the result set, with no hidden rows.
	more := get(t, r, "/?alltags=1").Body.String()
	assert.Equal(t, 60, strings.Count(more, "<li data-facet="))
	assert.NotContains(t, more, "data-facet-tail")
	assert.NotContains(t, more, "More tags")
}

// templatesCount is the result count the page states, which any change in what
// matched would move.
func templatesCount(body string) string {
	return regexp.MustCompile(`<span class="result-count">([^<]*)</span>`).FindString(body)
}

func TestMoreTagsParameterIsOnlyAcceptedAsOne(t *testing.T) {
	r, _ := sidebarRouter(t)
	for target, want := range map[string]string{
		"/?alltags=0":             "/",
		"/?alltags=yes":           "/",
		"/?alltags=1&utm=x":       "/?alltags=1",
		"/?alltags=1&tags=tag-00": "",
	} {
		w := get(t, r, target)
		if want == "" {
			assert.Equal(t, http.StatusOK, w.Code, target)
			continue
		}
		assert.Equal(t, http.StatusFound, w.Code, target)
		assert.Equal(t, want, w.Header().Get("Location"), target)
	}
}

func TestMoreTagsKeepsPagingExpanded(t *testing.T) {
	// Without scripting, the load-more link is a page load; it keeps the full
	// list the visitor asked for.
	f := templates.Filters{AllTags: true}
	assert.Equal(t, "/?alltags=1&page=2", f.PageURL(2))
	assert.Equal(t, "/results?page=2", f.FragmentURL(2), "the htmx request never carries it")
	assert.Equal(t, "/", f.ShareURL(), "nor does a filter link")
}

func TestTagPageAcceptsMoreTags(t *testing.T) {
	r, _ := sidebarRouter(t)
	plain := get(t, r, "/tag/tag-00")
	more := get(t, r, "/tag/tag-00?alltags=1")
	require.Equal(t, http.StatusOK, more.Code)
	assert.Equal(t, cardIDs(plain.Body.String()), cardIDs(more.Body.String()))
}

func TestTagTailIsConditionalAndCacheable(t *testing.T) {
	r, _ := sidebarRouter(t)
	full := get(t, r, "/results/tags")
	require.Equal(t, http.StatusOK, full.Code)
	assert.Equal(t, "public, max-age=300, s-maxage=3600", full.Header().Get("Cache-Control"))
	w := getWith(t, r, "/results/tags", map[string]string{"If-Modified-Since": full.Header().Get("Last-Modified")})
	assert.Equal(t, http.StatusNotModified, w.Code)
}

func TestHeadOfTheTagTailIsAnsweredWithoutABody(t *testing.T) {
	r, _ := sidebarRouter(t)
	assertHeadIsConditional(t, r, "/results/tags")
}
