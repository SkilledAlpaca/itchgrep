package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolvePortDefaultsTo8080(t *testing.T) {
	t.Setenv("PORT", "")
	assert.Equal(t, ":8080", resolvePort())
}

func TestResolvePortUsesEnv(t *testing.T) {
	t.Setenv("PORT", "9090")
	assert.Equal(t, ":9090", resolvePort())
}

// TestCheckHealthz covers what the "-healthcheck" flag actually runs: the
// same GET a container orchestrator would make, against a real listener, so
// this is what fails first if docker-compose.yml's healthcheck stanza and
// this flag ever drift apart on path or status expectations.
func TestCheckHealthzSucceedsOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	assert.NoError(t, checkHealthz(portFromTestServerURL(t, srv.URL), time.Second))
}

func TestCheckHealthzFailsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	assert.Error(t, checkHealthz(portFromTestServerURL(t, srv.URL), time.Second))
}

func TestCheckHealthzFailsWhenNothingIsListening(t *testing.T) {
	// Port 1 is a privileged port nothing in a test sandbox is listening on;
	// this exercises the connection-refused path, not just a bad status.
	assert.Error(t, checkHealthz(":1", 200*time.Millisecond))
}

// portFromTestServerURL extracts ":<port>" from an httptest.Server's URL, so
// checkHealthz - which only ever dials 127.0.0.1 - can be pointed at it.
func portFromTestServerURL(t *testing.T, url string) string {
	t.Helper()
	i := len(url) - 1
	for i >= 0 && url[i] != ':' {
		i--
	}
	return url[i:]
}
