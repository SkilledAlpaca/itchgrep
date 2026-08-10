package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHandleRobotsGet(t *testing.T) {
	w := httptest.NewRecorder()
	HandleRobots(w, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.True(t, strings.Contains(w.Body.String(), "Disallow: /results"))
}

func TestHandleRobotsHead(t *testing.T) {
	// net/http strips the body of a HEAD response on the way out, so this
	// just confirms the handler itself doesn't special-case the method.
	w := httptest.NewRecorder()
	HandleRobots(w, httptest.NewRequest(http.MethodHead, "/robots.txt", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestHandleSecurityTxt(t *testing.T) {
	w := httptest.NewRecorder()
	HandleSecurityTxt(w, httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), "Contact: ")
}

// TestSecurityTxtHasNotExpired is the reminder to bump the date. RFC 9116
// makes the file invalid once Expires passes, and nothing else in the system
// would notice - so this fails the build instead, roughly a year out.
func TestSecurityTxtHasNotExpired(t *testing.T) {
	var expires string
	for _, line := range strings.Split(securityTxt, "\n") {
		if after, found := strings.CutPrefix(line, "Expires: "); found {
			expires = after
			break
		}
	}
	assert.NotEmpty(t, expires, "security.txt must carry an Expires field")

	when, err := time.Parse(time.RFC3339, expires)
	assert.NoError(t, err, "Expires must be an RFC 3339 timestamp")
	assert.True(t, when.After(time.Now()),
		"security.txt expired on %s - bump the Expires date in robots.go", expires)
}

// TestSecurityTxtIsNotCaughtByProbeFilter guards the interaction between two
// files that have no other reason to know about each other: probes.go blocks
// paths on shape alone, and /.well-known/ sits close to several patterns it
// does block.
func TestSecurityTxtIsNotCaughtByProbeFilter(t *testing.T) {
	assert.False(t, isProbe("/.well-known/security.txt"))
	assert.False(t, isProbe("/.well-known/acme-challenge/token"))
}
