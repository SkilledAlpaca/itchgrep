package cache

import (
	"container/list"
	"itchgrep/pkg/models"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// hitCacheSize is how many distinct text searches the hit cache holds. A hit
// list is a few hundred int32s and the sidebar a few hundred tags, so 256
// entries is well under a few megabytes; the number is about how many
// different searches are in flight over a few minutes, not about memory.
const hitCacheSize = 256

// hitCacheDepth is how many pages of hits an entry holds. A visitor who scrolls
// ten pages deep has read 360 results; beyond that the request goes to the
// index, which is also the answer for the rare deep link.
const hitCacheDepth = 10

// hitEntry is one cached text search: the hits in order, as positions in
// Cache.data, plus what is the same for every page of them. The sidebar is
// ranked for the whole result set, so it is computed once here rather than
// per page.
type hitEntry struct {
	pos   []int32
	total int64
	tags  []models.Tag
	// anyWord marks a result set from the any-word fallback, so every page
	// of it reports the same thing.
	anyWord bool
}

// hitCacheable reports whether a request belongs in the hit cache: a text
// query, which is the one request that costs a pass through the index every
// time. Capped requests (/feed) are not paged and are not repeated by a person
// scrolling.
func hitCacheable(opts SearchOptions) bool {
	return opts.Query != "" && opts.Limit == 0
}

// hitKey names a search by everything that shapes its hits except the page:
// the page is a slice of the entry. NoFacets is left out on purpose. A request
// for page 2 asks for no sidebar, but the entry it fills is the one page 1
// shares, so it carries the sidebar either way.
func hitKey(opts SearchOptions) string {
	var b strings.Builder
	field := func(s string) {
		b.WriteString(s)
		b.WriteByte(0)
	}
	field(opts.Query)
	field(opts.resolvedSort())
	field(opts.Price)
	field(opts.Author)
	field(opts.Like)
	field(strconv.FormatInt(opts.MinFirstSeen, 10))
	// Order within a list is not meaningful to the search, and a differently
	// ordered URL must not be a second entry.
	field(joinSorted(opts.Tags))
	field(joinSorted(opts.ExcludeTags))
	return b.String()
}

func joinSorted(list []string) string {
	if len(list) < 2 {
		return strings.Join(list, ",")
	}
	cp := slices.Clone(list)
	slices.Sort(cp)
	return strings.Join(cp, ",")
}

// hitCache is a least-recently-used cache of hitEntry with single-flight fill:
// concurrent requests for the same key share one pass through the index.
// Every entry belongs to a generation; reset moves to the next one and drops
// everything, so a refresh can never be followed by an answer computed from
// the dataset it replaced.
type hitCache struct {
	max int

	mu      sync.Mutex
	gen     uint64
	ll      *list.List // front is most recently used; elements hold *hitItem
	items   map[string]*list.Element
	flights map[string]*hitFlight

	// Counters for tests and for the log line a refresh writes.
	hits, misses atomic.Uint64
}

type hitItem struct {
	key   string
	entry *hitEntry
}

type hitFlight struct {
	done  chan struct{}
	entry *hitEntry
	err   error
}

func newHitCache(max int) *hitCache {
	return &hitCache{
		max:     max,
		ll:      list.New(),
		items:   make(map[string]*list.Element),
		flights: make(map[string]*hitFlight),
	}
}

// get returns the entry for key, computing it with fill on a miss. Callers that
// arrive while a fill is running wait for it and share its result, error
// included; an error is not cached, so the next request tries again.
func (h *hitCache) get(key string, fill func() (*hitEntry, error)) (*hitEntry, error) {
	h.mu.Lock()
	if el, ok := h.items[key]; ok {
		h.ll.MoveToFront(el)
		entry := el.Value.(*hitItem).entry
		h.mu.Unlock()
		h.hits.Add(1)
		return entry, nil
	}
	if fl, ok := h.flights[key]; ok {
		h.mu.Unlock()
		<-fl.done
		h.hits.Add(1)
		return fl.entry, fl.err
	}
	fl := &hitFlight{done: make(chan struct{})}
	h.flights[key] = fl
	gen := h.gen
	h.mu.Unlock()
	h.misses.Add(1)

	// Released in a defer so that a panic in fill cannot leave waiters blocked
	// on a flight nobody will finish.
	defer func() {
		h.mu.Lock()
		if h.flights[key] == fl {
			delete(h.flights, key)
		}
		// Stored only if no refresh happened while it ran. The caller holds the
		// cache's read lock for the whole fill, so a swap cannot actually
		// interleave; the generation makes that not depend on it.
		if fl.err == nil && fl.entry != nil && h.gen == gen {
			h.items[key] = h.ll.PushFront(&hitItem{key: key, entry: fl.entry})
			for h.ll.Len() > h.max {
				oldest := h.ll.Back()
				h.ll.Remove(oldest)
				delete(h.items, oldest.Value.(*hitItem).key)
			}
		}
		h.mu.Unlock()
		close(fl.done)
	}()
	fl.entry, fl.err = fill()
	return fl.entry, fl.err
}

// reset drops every entry and starts a new generation. Called with the cache's
// write lock held, at the moment a refreshed dataset is swapped in.
func (h *hitCache) reset() {
	h.mu.Lock()
	h.gen++
	h.ll.Init()
	h.items = make(map[string]*list.Element)
	h.mu.Unlock()
}

// len is the number of cached entries.
func (h *hitCache) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ll.Len()
}
