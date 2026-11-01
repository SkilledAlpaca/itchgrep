package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"itchgrep/internal/logging"
	"net/http"
	"os"
	"sync"
	"time"
)

// buildHeader names the response header that carries the release a fragment
// was rendered by. app.js compares it with the <meta name="itchgrep-build">
// of the page it is running in and, on a mismatch, navigates instead of
// swapping: a page and a fragment from different releases disagree about
// markup (which element carries hx-target, what the sidebar looks like), and
// swapping one into the other nested the results inside a tag link.
//
// The two can disagree because the edge caches HTML and fragments by URL for
// up to s-maxage, independently, and a deploy changes the origin immediately.
// A purge would not reach a tab left open across the deploy or a browser copy
// inside max-age, so the check runs in the browser. The header is stored with
// the cached response, which is what makes it visible on a hit the origin
// never saw. There is deliberately no Vary.
const buildHeader = "X-Itchgrep-Build"

var (
	buildOnce sync.Once
	buildVal  string
)

// BuildID identifies the running release: the first 12 hex characters of the
// SHA-256 of the executable. The binary is the release, so the ID is the same
// across restarts of one deploy (a plain restart must not reload every open
// tab) and changes whenever the code does. VCS build info is no substitute:
// the Docker build has no .git. When the executable cannot be read the process
// start time stands in, which only costs a reload per restart.
func BuildID() string {
	buildOnce.Do(func() {
		id, err := hashExecutable()
		if err != nil {
			id = fmt.Sprintf("t%d", time.Now().UnixNano())
			logging.Warning("Could not hash the executable (%v); using the start time as the build ID %s, so open tabs will reload on the next restart", err, id)
		}
		buildVal = id
	})
	return buildVal
}

func hashExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil))[:12], nil
}

// stampBuild sets the build header. Called first in every fragment handler,
// before any status is written, so 304s, errors and the 400/503 fragments app.js
// swaps all carry it.
func (h *handler) stampBuild(w http.ResponseWriter) {
	w.Header().Set(buildHeader, h.build)
}
