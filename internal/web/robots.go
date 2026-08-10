package web

import "net/http"

// robotsTxt disallows /results: that endpoint renders an htmx fragment with
// no layout around it, so a crawler indexing it would show a page fragment
// with nothing to click through to. Worse, every hit runs the expensive
// fuzzy search path (see ratelimit.go), and a crawler has no rate limit of
// its own conscience - it will happily walk every query string it can find.
// Everything else is a normal page and stays allowed.
const robotsTxt = `User-agent: *
Disallow: /results
Allow: /
`

// HandleRobots serves the crawl policy above. Registered for both GET and
// HEAD in cmd/webserver/main.go; net/http strips the body of a HEAD
// response itself, so one handler covers both.
func HandleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// A day is generous for a file that only changes when a route does, and
	// short enough that a route change does not linger stale for long.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(robotsTxt))
}

// securityTxt tells anyone who finds a fault where to report it, per RFC 9116.
// Contact is the issue tracker rather than an address: this is a public file
// that spam harvesters read too, and the repository is public anyway.
//
// Expires is mandatory and the file is invalid once it passes, so this needs
// bumping annually - see the README's security section, which says so in the
// one place someone would look.
const securityTxt = `Contact: https://github.com/SkilledAlpaca/itchgrep/issues
Expires: 2027-08-10T00:00:00Z
Preferred-Languages: en
Canonical: https://itchgrep.com/.well-known/security.txt
`

// HandleSecurityTxt serves the above at the well-known location RFC 9116
// requires. Note the path is deliberately not caught by the scanner filter in
// probes.go: /.well-known/ has honest uses - this and ACME renewals - so it
// falls through to routing like any other path.
func HandleSecurityTxt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(securityTxt))
}
