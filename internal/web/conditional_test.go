package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getWith(t *testing.T, r http.Handler, target string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestConditionalRequestsAreAnsweredBeforeAnySearch(t *testing.T) {
	r, _ := perfRouter(t)
	for _, target := range []string{"/", "/?q=pixel", "/?sort=title&tags=tag-00", "/results?q=pixel&page=2", "/tag/tag-00", "/new", "/about", "/saved"} {
		full := get(t, r, target)
		require.Equal(t, http.StatusOK, full.Code, target)
		lm := full.Header().Get("Last-Modified")
		require.NotEmpty(t, lm, target)

		w := getWith(t, r, target, map[string]string{"If-Modified-Since": lm})
		assert.Equal(t, http.StatusNotModified, w.Code, target)
		assert.Empty(t, w.Body.String(), target)
		if strings.Contains(target, "page=") {
			assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"), target)
		} else {
			assert.Equal(t, "public, max-age=300, s-maxage=3600", w.Header().Get("Cache-Control"), target)
		}
		assert.Equal(t, lm, w.Header().Get("Last-Modified"), target)

		// A copy newer than the data is current as well.
		later := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
		assert.Equal(t, http.StatusNotModified, getWith(t, r, target, map[string]string{"If-Modified-Since": later}).Code, target)
	}
}

func TestConditionalRequestsThatCannotBeAnsweredGet200(t *testing.T) {
	r, _ := perfRouter(t)
	full := get(t, r, "/?q=pixel")
	lm := full.Header().Get("Last-Modified")
	parsed, err := http.ParseTime(lm)
	require.NoError(t, err)

	// A copy older than the data.
	old := parsed.Add(-time.Hour).UTC().Format(http.TimeFormat)
	w := getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": old})
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Body.String())

	// No validator, or one that is not a date.
	assert.Equal(t, http.StatusOK, get(t, r, "/?q=pixel").Code)
	assert.Equal(t, http.StatusOK, getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": "yesterday-ish"}).Code)

	// An If-None-Match makes If-Modified-Since moot, and this server has no ETag.
	assert.Equal(t, http.StatusOK, getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": lm, "If-None-Match": `"x"`}).Code)

	// A different URL is a different page: a client with no copy of it sends no
	// validator, and gets the page.
	other := get(t, r, "/?q=asset")
	assert.Equal(t, http.StatusOK, other.Code)
	assert.NotEqual(t, full.Body.String(), other.Body.String())

	// Validators are for reading. A HEAD is answered; a POST is not a page.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("If-Modified-Since", lm)
	rec := httptest.NewRecorder()
	h := NewHandler(manyTagsCache(t), 0, nil)
	assert.False(t, h.notModified(rec, req))
}

func TestConditionalRequestAfterNewDataGets200(t *testing.T) {
	r, c := perfRouter(t)
	first := get(t, r, "/")
	lm := first.Header().Get("Last-Modified")
	require.NotEmpty(t, lm)
	require.Equal(t, http.StatusNotModified, getWith(t, r, "/", map[string]string{"If-Modified-Since": lm}).Code)

	// A crawl publishes: assets.json is rewritten and the cache reloads.
	newer := time.Now().Add(2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(os.Getenv("DATA_DIR"), "assets.json"), newer, newer))
	require.NoError(t, c.RefreshDataCache())

	w := getWith(t, r, "/", map[string]string{"If-Modified-Since": lm})
	assert.Equal(t, http.StatusOK, w.Code, "the client's copy predates the data")
	assert.NotEqual(t, lm, w.Header().Get("Last-Modified"))
	assert.NotEmpty(t, w.Body.String())
}

func TestHeadRequestsAreConditionalToo(t *testing.T) {
	_, c := perfRouter(t)
	h := NewHandler(c, 0, nil)
	lm := c.DataUpdatedTime().UTC().Format(http.TimeFormat)
	req := httptest.NewRequest(http.MethodHead, "/about", nil)
	req.Header.Set("If-Modified-Since", lm)
	w := httptest.NewRecorder()
	h.HandleAbout(w, req)
	assert.Equal(t, http.StatusNotModified, w.Code)
}

func TestACodeOnlyDeployInvalidatesEveryClientCopy(t *testing.T) {
	// The data did not change, but the process now serving is newer than the
	// copy the client holds: that copy was rendered by the previous release,
	// and a 304 would keep its old markup and script names alive.
	r, _, h := perfRouterHandler(t)
	full := get(t, r, "/?q=pixel")
	lm := full.Header().Get("Last-Modified")
	require.Equal(t, http.StatusNotModified, getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": lm}).Code)

	h.started = h.started.Add(3 * time.Hour)
	w := getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": lm})
	assert.Equal(t, http.StatusOK, w.Code, "a client older than the process gets a fresh page")
	newLM := w.Header().Get("Last-Modified")
	assert.NotEqual(t, lm, newLM)
	parsed, err := http.ParseTime(newLM)
	require.NoError(t, err)
	assert.Equal(t, h.started.UTC().Truncate(time.Second), parsed.UTC(), "Last-Modified is the later of data time and process start")

	// A client that fetched after the deploy is current again.
	assert.Equal(t, http.StatusNotModified, getWith(t, r, "/?q=pixel", map[string]string{"If-Modified-Since": newLM}).Code)
}

func TestLaterPagesRevalidateUnderTheShortPolicy(t *testing.T) {
	// A 304 cannot know whether a later page is past the end of the results, and
	// a page past the end is cached briefly, so every later page's 304 is too.
	r, _ := perfRouter(t)
	for _, target := range []string{"/?page=2", "/results?page=2", "/tag/tag-00?page=2", "/results?q=pixel&page=3"} {
		lm := get(t, r, target).Header().Get("Last-Modified")
		require.NotEmpty(t, lm, target)
		w := getWith(t, r, target, map[string]string{"If-Modified-Since": lm})
		require.Equal(t, http.StatusNotModified, w.Code, target)
		assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"), target)
	}
	lm := get(t, r, "/").Header().Get("Last-Modified")
	w := getWith(t, r, "/", map[string]string{"If-Modified-Since": lm})
	assert.Equal(t, "public, max-age=300, s-maxage=3600", w.Header().Get("Cache-Control"), "page one keeps the long edge policy")
}

func TestHeadOfResultsIsConditionalWithoutABody(t *testing.T) {
	r, _ := perfRouter(t)
	assertHeadIsConditional(t, r, "/results?q=pixel")
}

// assertHeadIsConditional drives a real server, because a recorder keeps a body
// that net/http would drop from a HEAD response.
func assertHeadIsConditional(t *testing.T, h http.Handler, target string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	head := func(since string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodHead, srv.URL+target, nil)
		require.NoError(t, err)
		if since != "" {
			req.Header.Set("If-Modified-Since", since)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	resp, body := head("")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, body)
	lm := resp.Header.Get("Last-Modified")
	require.NotEmpty(t, lm)

	resp, body = head(lm)
	assert.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.Empty(t, body)
}
