// Package static owns the embedded CSS and JS and the content fingerprints
// that make their URLs safe to cache for a year.
//
// It is a package of its own, rather than a file in internal/web, because both
// internal/web (the HTTP handler) and internal/web/templates (the <link> and
// <script> tags) need the hashes and web already imports templates. It sits
// beside the files because //go:embed can only reach at or below its own
// directory.
//
// The embed lists the served files by name instead of embedding the directory.
// That keeps this Go source and VENDOR.md out of the FS, so no handler built on
// it can serve them.
//
// Fingerprints go in the path (app.<hash>.css), not a ?v= query string. The
// zone has WAF challenge rules keyed on query strings and an edge cache level
// that may or may not key on them; a path fingerprint depends on neither, so
// asset caching cannot be broken by a dashboard setting.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"path"
	"strings"
)

// FS holds the files served under /static/, at its root.
//
//go:embed app.css app.js htmx.min.js
var FS embed.FS

// hashLen is the number of hex characters kept from the SHA-256. 12 characters
// is 48 bits: ample to tell two builds of one file apart, short enough for a URL.
const hashLen = 12

var hashes = mustHashes(FS)

// Hashes returns the short content hash of every regular file in fsys, keyed by
// its name. It is exported for tests, which run it on a synthetic FS.
func Hashes(fsys fs.FS) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[p] = hex.EncodeToString(sum[:])[:hashLen]
		return nil
	})
	return out, err
}

func mustHashes(fsys fs.FS) map[string]string {
	h, err := Hashes(fsys)
	if err != nil {
		// An embedded FS cannot fail to read; this is a build-time mistake.
		panic("static: hashing embedded files: " + err.Error())
	}
	return h
}

// Hash returns the current content hash of an embedded file, or "" if there is
// no such file.
func Hash(name string) string { return hashes[name] }

// URL returns the fingerprinted URL for an embedded file: app.css becomes
// /static/app.<hash>.css. An unknown name yields the bare path, which the
// handler 404s, so a typo in a template is visible rather than silently stale.
func URL(name string) string { return urlFor(hashes, name) }

func urlFor(h map[string]string, name string) string {
	hash, ok := h[name]
	if !ok {
		return "/static/" + name
	}
	ext := path.Ext(name)
	return "/static/" + strings.TrimSuffix(name, ext) + "." + hash + ext
}

// Resolve maps a path relative to /static/ onto an embedded file. name is the
// real file, and immutable is true only when the path carries that file's
// current hash.
//
// A bare name and a well-formed but stale hash (exactly hashLen lowercase hex
// characters on a known file) both resolve with immutable false. The stale case
// is the window after a deploy: edge-cached and back/forward-cached HTML still
// names the previous hashes, and a 404 there would leave the page unstyled.
// Callers must give those responses a short cache, never a long one. A hash
// segment that is not hashLen lowercase hex characters, an unknown file, or the
// wrong extension does not resolve.
func Resolve(rel string) (name string, immutable, ok bool) { return resolve(hashes, rel) }

func resolve(h map[string]string, rel string) (name string, immutable, ok bool) {
	if _, bare := h[rel]; bare {
		return rel, false, true
	}
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	i := strings.LastIndex(stem, ".")
	if i < 0 {
		return "", false, false
	}
	base := stem[:i] + ext
	want, known := h[base]
	if !known || !isHash(stem[i+1:]) {
		return "", false, false
	}
	return base, stem[i+1:] == want, true
}

func isHash(s string) bool {
	if len(s) != hashLen {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
