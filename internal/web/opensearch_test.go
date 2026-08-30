package web

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openSearchXML is an independent decode target, the same reasoning
// atomFeedXML and sitemapXML use.
type openSearchXML struct {
	XMLName     xml.Name `xml:"OpenSearchDescription"`
	ShortName   string   `xml:"ShortName"`
	Description string   `xml:"Description"`
	URLs        []struct {
		Type     string `xml:"type,attr"`
		Template string `xml:"template,attr"`
		Rel      string `xml:"rel,attr"`
	} `xml:"Url"`
}

func TestOpenSearchDescriptorIsValidXMLWithTheRightSearchTemplate(t *testing.T) {
	w := httptest.NewRecorder()
	HandleOpenSearch(w, httptest.NewRequest(http.MethodGet, "/opensearch.xml", nil))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/opensearchdescription+xml; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))

	var doc openSearchXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &doc), "the rendered body must be parseable XML")
	assert.Equal(t, "ITCHGREP", doc.ShortName)
	assert.NotEmpty(t, doc.Description)

	var sawResultsTemplate bool
	for _, u := range doc.URLs {
		if u.Type == "text/html" {
			sawResultsTemplate = true
			// Has to match the search form's actual field name (index.templ's
			// SearchBar, name="q") or a browser offering this site as a search
			// engine would submit a query nothing on the other end reads.
			assert.Equal(t, "https://itchgrep.com/?q={searchTerms}", u.Template)
		}
	}
	assert.True(t, sawResultsTemplate, "descriptor must carry the html search template")
}

func TestOpenSearchIsDiscoverableFromEveryPage(t *testing.T) {
	r, _ := newTestRouter()
	body := get(t, r, "/about").Body.String()
	assert.Contains(t, body, `<link rel="search" type="application/opensearchdescription+xml"`)
	assert.Contains(t, body, `href="/opensearch.xml"`)
}
