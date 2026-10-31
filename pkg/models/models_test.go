package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Catalog fields are persisted as JSON under the key "Catalogue", the
// spelling they were first written with. Renaming the Go field must not orphan
// the files already on the data volume.
func TestCatalogKeepsItsPersistedJSONKey(t *testing.T) {
	var s Stats
	require.NoError(t, json.Unmarshal([]byte(`{"Indexed":5,"Catalogue":9}`), &s))
	assert.Equal(t, int64(9), s.Catalog)

	var r CrawlRecord
	require.NoError(t, json.Unmarshal([]byte(`{"Indexed":5,"Catalogue":9}`), &r))
	assert.Equal(t, int64(9), r.Catalog)

	out, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"Catalogue":9`)
}
