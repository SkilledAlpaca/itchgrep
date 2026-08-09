package templates

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSearchURLEscapesTheQuery(t *testing.T) {
	// An unescaped ampersand truncates the query at the & - the old POST form
	// passed it as a JSON body, so this hazard is new with the move to GET.
	assert.Equal(t, "/search?q=a%26b&page=2", searchURL("a&b", 2))
	assert.Equal(t, "/search?q=%22quoted%22&page=1", searchURL(`"quoted"`, 1))
	assert.Equal(t, "/search?q=pixel+art&page=1", searchURL("pixel art", 1))
}
