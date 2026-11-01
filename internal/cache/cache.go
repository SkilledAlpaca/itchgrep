// Package cache is the webserver's in-memory view of the published dataset:
// the asset list, the bleve search index, the tag universe, and the
// exchange-rate snapshot they were built with. It is the one place that
// knows how to load that data from internal/storage, keep it fresh as the
// dataservice republishes, and answer a search or a plain browse against
// whichever copy is currently loaded (see search.go's Find).
package cache

import (
	"cmp"
	"errors"
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// ErrNotReady is returned by Find when no data/index has ever been
// successfully loaded yet (e.g. the server just started and the initial load is
// still retrying in the background).
var ErrNotReady = errors.New("cache: not ready")

// expiryCheckTTL is the minimum interval between live storage.GetAssetsUpdateTime
// checks in IsCacheExpired. The underlying data only changes when the
// dataservice finishes a scrape (weekly, per CRAWL_INTERVAL), so checking on every
// single HTTP request is wasted work.
//
// It used to be 60s, when the check was a GCS round-trip. Against a local
// directory it is an os.Stat, so the throttle exists only to keep the syscall
// off the hot path - and a shorter one means a freshly published index is
// picked up in seconds instead of up to a minute.
const expiryCheckTTL = 5 * time.Second

// Cache is the webserver's loaded copy of one published dataset: the asset
// list, the bleve search index, and the metadata (tag universe, exchange
// rates, coverage stats) they were published alongside. It is safe for
// concurrent use - every accessor takes cacheLock - and RefreshDataCache
// swaps a newly loaded dataset in atomically, so a request in flight never
// sees a mix of old and new data.
type Cache struct {
	cacheLock sync.RWMutex

	// dataMap indexes GameId to its position in data, rather than duplicating
	// each asset, so the ~97k assets in data are held in memory exactly once.
	dataMap map[string]int
	data    []models.Asset
	index   bleve.Index

	// eng answers requests with no text query from memory (see engine). Built
	// from data on every refresh, so the two are always the same dataset.
	eng *engine

	// hits remembers the answers to recent text searches (see hitCache) so that
	// paging through one costs a slice, not another pass through the index.
	// Reset in the same write lock that swaps the dataset.
	hits *hitCache

	// indexInfo is the stat of the directory index was opened from, kept to
	// recognize a refresh that points at the same directory (see doRefresh).
	indexInfo os.FileInfo

	// tagCounts is how many assets carry each tag, across the whole dataset.
	// Computed once per refresh rather than faceted per request: the unfiltered
	// browse is the most-requested view there is and its facet never varies, so
	// asking bleve to count 600 terms over 86,000 documents on every homepage
	// load would be paying repeatedly for a constant.
	tagCounts []models.Tag

	// tagCatalog is the same histogram without the cap: every tag's count over
	// the whole dataset. A narrowed search ranks its sidebar by how
	// over-represented each tag is against it (see liftTags), which needs the
	// niche tags that tagCounts leaves out. Built once per refresh, read-only
	// afterwards, and its size is the length of data.
	tagCatalog map[string]int64

	// rates is the exchange-rate snapshot the published index was built with.
	// Loaded from storage rather than fetched, so the dollar values baked into
	// the documents and the rates used to display converted prices are the same
	// numbers from the same day.
	rates money.Rates

	// hasRecency records whether the loaded dataset carries newest-view ranks
	// at all. An index built before the crawl started collecting them has none,
	// and a "recently added" control over that data would order by nothing.
	hasRecency bool

	// stats is how much of itch.io's catalog the loaded dataset covers, as
	// measured by the crawl that produced it. Zero when unrecorded.
	stats models.Stats

	// tagUniverse is the site-wide tag vocabulary the dataservice crawl
	// discovered (storage.GetTags/tags.json), not just the ones the current
	// publish happens to have an asset for. tagCounts above is capped to
	// maxFacetTags and only reflects this publish's assets - the sidebar's
	// job. /tag/{slug} validation and the sitemap need the fuller,
	// independently-bounded list instead, so a tag with no matching asset in
	// today's index still gets a page rather than a 404.
	tagUniverse []models.Tag

	// hasPriceBands records whether the loaded index carries PriceUSD. Unlike
	// hasRecency this cannot be read off the asset list, because the converted
	// dollar value exists only in the index - so it is probed at load time by
	// asking the index the same question the filter will ask.
	hasPriceBands bool

	// the time the data was last updated on the server.
	// if we check if the current time is greater than this time, we know the
	// cache is expired
	dataUpdatedTime time.Time

	// expiryCheckMu guards lastExpiryCheck. It is a dedicated mutex (rather
	// than reusing cacheLock) so IsCacheExpired can record the time of its
	// last live check while only holding cacheLock.RLock() — upgrading an
	// RLock to a Lock is not supported by sync.RWMutex and would deadlock.
	expiryCheckMu sync.Mutex
	// lastExpiryCheck is the time of the last real storage.GetAssetsUpdateTime
	// check performed by IsCacheExpired. Calls within expiryCheckTTL of this
	// time short-circuit to false without hitting storage.
	lastExpiryCheck time.Time

	// the cache can be retrieved as chunks/pages
	pageSize int64

	// single-flight bookkeeping for RefreshDataCache: collapses concurrent
	// refresh attempts into a single in-flight operation whose result is
	// shared with every caller that arrived while it was running.
	refreshMu   sync.Mutex
	refreshing  bool
	refreshDone chan struct{}
	refreshErr  error
}

// NewCache builds an empty Cache with no data loaded yet - callers must call
// RefreshDataCache (directly, or via Find/IsCacheExpired) before it can serve
// anything. pageSize is how many assets one page of results holds; it is
// fixed for the life of the Cache rather than reloaded from storage, since
// changing it mid-run would change what "page 2" means for a URL already
// shared.
func NewCache(pageSize int64) *Cache {
	return &Cache{
		dataMap:         make(map[string]int),
		cacheLock:       sync.RWMutex{},
		pageSize:        pageSize,
		hits:            newHitCache(hitCacheSize),
		dataUpdatedTime: time.Time{},
		// Seeded with the built-in table so that a cache which has not loaded
		// yet, or one whose data directory holds no rates file, still has a
		// dated snapshot to convert with rather than a nil map.
		rates: money.Fallback(),
	}
}

// Rates is the exchange-rate snapshot prices are converted with. Handlers pass
// it to the templates, which show its date and source beside every converted
// figure.
func (c *Cache) Rates() money.Rates {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.rates
}

// HasRecency reports whether the loaded dataset can be ordered by recency. The
// sort control is hidden entirely when it cannot, rather than offering an
// ordering that would silently degrade to popularity.
func (c *Cache) HasRecency() bool {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.hasRecency
}

// Stats is how complete the served dataset is. The zero value means the crawl
// that built it did not record the figure, and nothing should be claimed.
func (c *Cache) Stats() models.Stats {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.stats
}

// HasPriceBands reports whether the loaded index carries the converted dollar
// value that "under $5" and "under $20" are ranges over.
//
// Same rule as HasRecency, and it exists for the same reason: an index built
// before PriceUSD was introduced has no such field, a numeric range over a field
// no document has matches nothing, and the two buttons then sit there returning
// an empty page. Hidden is honest; present-but-always-empty reads as "there are
// no cheap assets", which is a claim about itch.io rather than about the index.
func (c *Cache) HasPriceBands() bool {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.hasPriceBands
}

// PageSize is how many assets one page of results holds. Handlers need it to
// work out whether a further page exists.
func (c *Cache) PageSize() int64 { return c.pageSize }

// TagSlugs is every slug in the discovered tag universe, alphabetized so the
// sitemap it feeds is stable across requests rather than following whatever
// order tags.json happened to store. See tagUniverse for why this is not
// tagCounts.
func (c *Cache) TagSlugs() []string {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	slugs := make([]string, len(c.tagUniverse))
	for i, t := range c.tagUniverse {
		slugs[i] = t.Slug
	}
	sort.Strings(slugs)
	return slugs
}

// HasTag reports whether slug names a tag in the discovered universe. It is
// how /tag/{slug} tells a real tag - possibly one with nothing matching it in
// today's publish - from a typo or a probe, which get a 404 instead of a page.
func (c *Cache) HasTag(slug string) bool {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	for _, t := range c.tagUniverse {
		if t.Slug == slug {
			return true
		}
	}
	return false
}

// DataUpdatedTime is when the currently-loaded dataset was published. Handlers
// use it as Last-Modified so a shared cache can revalidate rather than refetch,
// and so a newly published index invalidates what the edge is holding.
func (c *Cache) DataUpdatedTime() time.Time {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.dataUpdatedTime
}

// IsCacheExpired reports whether the loaded dataset is older than what
// storage now holds, throttling the underlying check to at most once per
// expiryCheckTTL so it costs nothing on the hot request path. A cache that
// has never loaded anything is always expired.
func (c *Cache) IsCacheExpired() bool {
	c.cacheLock.RLock()
	dataUpdatedTime := c.dataUpdatedTime
	c.cacheLock.RUnlock()

	// if we never updated the cache, it is expired. This must stay first,
	// ahead of the TTL throttle below, so a cold cache is never throttled
	// into looking valid.
	if dataUpdatedTime.IsZero() {
		return true
	}

	// Throttle the live storage.GetAssetsUpdateTime roundtrip: the
	// underlying data only changes when a crawl publishes (weekly), so we only need to
	// actually check at most once every expiryCheckTTL. expiryCheckMu is a
	// dedicated mutex (not cacheLock) so this bookkeeping never needs to
	// upgrade an RLock to a Lock (which would deadlock) and so the network
	// call below never happens while holding cacheLock.
	c.expiryCheckMu.Lock()
	now := time.Now()
	if now.Sub(c.lastExpiryCheck) < expiryCheckTTL {
		c.expiryCheckMu.Unlock()
		return false
	}
	c.lastExpiryCheck = now
	c.expiryCheckMu.Unlock()

	// otherwise, we check if the data on the server is newer than the data in the cache
	storageUpdateTime, err := storage.GetAssetsUpdateTime()
	if err != nil {
		logging.Error("Failed to get assets update time: %v", err)
		return false
	}
	return dataUpdatedTime.Before(storageUpdateTime)
}

// RefreshDataCache reloads the asset data and search index from storage and
// atomically swaps them into place. Concurrent callers collapse into a
// single in-flight refresh and all observe its result (single-flight).
func (c *Cache) RefreshDataCache() error {
	c.refreshMu.Lock()
	if c.refreshing {
		done := c.refreshDone
		c.refreshMu.Unlock()
		<-done
		c.refreshMu.Lock()
		err := c.refreshErr
		c.refreshMu.Unlock()
		return err
	}

	c.refreshing = true
	done := make(chan struct{})
	c.refreshDone = done
	c.refreshMu.Unlock()

	// Cleared in a defer so that no way out of doRefresh - an error, or a
	// panic recovered further up - leaves the flag set. A stuck flag makes
	// every later caller wait on a channel nobody will close.
	var err error
	defer func() {
		c.refreshMu.Lock()
		c.refreshErr = err
		c.refreshing = false
		c.refreshMu.Unlock()
		close(done)
	}()
	err = c.doRefresh()
	return err
}

// indexOpenTimeout bounds how long opening an index waits for its bolt file
// lock. bleve's default waits forever, so a lock that is never released would
// hang the refresh - and with it every search that triggered one. Same bound
// the dataservice's self-heal uses.
const indexOpenTimeout = "5s"

// Ready reports whether the cache can serve searches right now: an index and
// the asset list it answers about are both loaded. It is what /readyz reports.
func (c *Cache) Ready() bool {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.index != nil && c.data != nil
}

// doRefresh performs the actual reload. The multi-MB download and
// bleve.Open happen without holding cacheLock; the write lock is taken only
// to swap the pointers in. On any failure the previously-loaded index/data
// remain untouched and keep serving.
func (c *Cache) doRefresh() error {
	// we fetch this here already, since we can just stop if we fail to fetch even this
	newServerUpdateTime, err := storage.GetAssetsUpdateTime()
	if err != nil {
		return fmt.Errorf("storage.GetAssetsUpdateTime: %w", err)
	}

	// fetch asset data
	preFetchTime := time.Now()
	newData, err := storage.GetAssets()
	if err != nil {
		return fmt.Errorf("storage.GetAssets: %w", err)
	}
	if newData == nil {
		return errors.New("cache: storage.GetAssets returned no data")
	}
	fetchTime := time.Since(preFetchTime)
	logging.Info("Fetched %d assets in %v", len(newData), fetchTime)

	// Open the published index in place. There is no copy: the dataservice
	// publishes by renaming a new directory over the old one, so this open
	// resolves to the new inode while the currently-live index below keeps
	// serving from the old one, which stays valid until we close it.
	preFetchTime = time.Now()
	//
	// Unless it is the directory already open. An assets.json rewritten
	// without a new index directory would make a second open fight the first
	// for the bolt lock until the timeout, and fail the refresh for nothing:
	// the live index is already the right one, so keep it.
	indexPath := storage.IndexPath()
	info, err := os.Stat(indexPath)
	if err != nil {
		return fmt.Errorf("stat index %s: %w", indexPath, err)
	}
	c.cacheLock.RLock()
	liveIndex, liveInfo := c.index, c.indexInfo
	c.cacheLock.RUnlock()
	reuse := liveIndex != nil && liveInfo != nil && os.SameFile(liveInfo, info) && liveInfo.ModTime().Equal(info.ModTime())
	newIndex := liveIndex
	if reuse {
		logging.Info("Index %s is the one already open; keeping it", indexPath)
	} else {
		newIndex, err = bleve.OpenUsing(indexPath, map[string]interface{}{"bolt_timeout": indexOpenTimeout})
		if err != nil {
			return fmt.Errorf("bleve.OpenUsing %s: %w", indexPath, err)
		}
		fetchTime = time.Since(preFetchTime)
		logging.Info("Opened index %s in %v", indexPath, fetchTime)
	}

	// sort newData by popularity (smaller numbers first). cmp.Compare rather
	// than a subtraction cast to int: the difference of two int64s does not fit
	// an int on a 32-bit build, where it would wrap and scramble the order.
	//
	// Stable, so assets with the same InvPopularity (they share a page number)
	// keep the order assets.json lists them in, which is the order they were
	// indexed in and so the order bleve breaks the same tie in.
	slices.SortStableFunc(newData, func(i, j models.Asset) int {
		return cmp.Compare(i.InvPopularity, j.InvPopularity)
	})

	// newDataMap indexes GameId to its position in newData, so a lookup is
	// c.data[c.dataMap[id]] instead of duplicating every asset into the map.
	newDataMap := make(map[string]int, len(newData))
	hasRecency := false
	for i, asset := range newData {
		newDataMap[asset.GameId] = i
		if asset.InvRecency > 0 {
			hasRecency = true
		}
	}

	// Counted here with the other per-refresh work, outside the write lock.
	newTagCatalog := tagHistogram(newData)

	// Probed before the swap, and outside the write lock, because it is a search
	// against the index rather than a field read.
	hasPriceBands := indexHasPriceUSD(newIndex)
	if !hasPriceBands {
		logging.Info("Index carries no PriceUSD; the under-$5 and under-$20 filters will be hidden")
	}

	// A missing rates file is normal on an index published before rates were
	// collected, and is not worth failing a refresh over: the built-in snapshot
	// is stale but dated, and says so wherever it is used.
	newRates, err := storage.GetRates()
	if err != nil {
		logging.Info("No stored exchange rates (%v); using the built-in snapshot from %s", err, money.Fallback().Date)
		newRates = money.Fallback()
	}

	// Absent on an index published before crawls recorded their completeness.
	// The zero value renders no coverage at all, which is the honest reading:
	// this build does not know what fraction it holds.
	newStats, err := storage.GetStats()
	if err != nil {
		logging.Info("No stored crawl stats (%v); coverage will not be shown", err)
		newStats = models.Stats{}
	}

	// Absent on an index published before discovery ever ran, or if tags.json
	// failed to write. Not fatal, for the same reason a missing rates file
	// isn't: /tag/{slug} and the sitemap just have nothing beyond the fixed
	// pages to enumerate until a crawl produces one.
	newTagUniverse, _, err := storage.GetTags()
	if err != nil {
		logging.Info("No cached tag universe (%v); /tag pages and the sitemap will list none", err)
		newTagUniverse = nil
	}

	// Derived from the sorted list and the same rates the index was priced
	// with, outside the write lock like the rest of the per-refresh work.
	newEngine := buildEngine(newData, newRates)

	// swap the new index/data in, holding the write lock only for the swap
	// itself. The old index is intentionally not closed yet: if anything
	// above failed we must keep serving it.
	c.cacheLock.Lock()
	oldIndex := c.index
	if reuse {
		oldIndex = nil
	}
	c.index = newIndex
	c.indexInfo = info
	c.data = newData
	c.dataMap = newDataMap
	c.eng = newEngine
	c.hits.reset()
	c.tagCatalog = newTagCatalog
	c.tagCounts = topTags(newTagCatalog)
	c.rates = newRates
	c.hasRecency = hasRecency
	c.hasPriceBands = hasPriceBands
	c.stats = newStats
	c.tagUniverse = newTagUniverse
	c.dataUpdatedTime = newServerUpdateTime
	c.cacheLock.Unlock()

	// only now that the new index is live do we tear down the old one. Closing
	// it also releases the last reference to the directory the dataservice
	// already unlinked, which is what actually frees the disk space.
	if oldIndex != nil {
		if err := oldIndex.Close(); err != nil {
			logging.Error("Failed to close previous index: %v", err)
		}
	}

	return nil
}

// Field weights shared by every text query textQuery
// and phraseQuery (search.go) build. Tags outweigh the rest because they are
// curated classification rather than prose - a hit there is a stronger
// signal than one in a description that merely mentions the word - and
// title beats description beats author because, in that order, that is how
// specific a match tends to be to what someone actually typed.
const (
	titleBoost       = 3
	descriptionBoost = 2
	authorBoost      = 1
	tagBoost         = 4
)

// fuzzyPrefixLen is the number of leading characters of a term that must match
// exactly before a typo is tolerated.
const fuzzyPrefixLen = 2

// queryAnalyzer is the analyzer the index applies to the text fields (bleve's
// default, "standard"). textQuery runs terms through it to find the ones the
// index drops entirely, such as stop words.
var queryAnalyzer = func() analysis.Analyzer {
	im := mapping.NewIndexMapping()
	a := im.AnalyzerNamed(im.DefaultAnalyzer)
	if a == nil {
		panic("cache: default analyzer not found")
	}
	return a
}()

// queryTerms returns the distinct terms the analyzer makes of s, in order.
// Splitting by analyzer rather than by whitespace keeps the query in step with
// the index: "pixel-art" is two terms, like "pixel art"; stop words such as
// "of" or "the" vanish, since the index holds no token for them; and a script
// without spaces, such as Chinese, yields the per-token terms it was indexed
// under. Repeats collapse to one.
func queryTerms(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range queryAnalyzer.Analyze([]byte(s)) {
		term := string(tok.Term)
		if !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

// textQuery requires every term of s to match in at least one text field: a
// conjunction over terms, each term a disjunction of an exact match and a
// light-typo match over the four fields. With anyWord set it is a disjunction
// over terms instead (the fallback for a query nothing satisfies as a whole),
// with the same per-term clauses and boosts. If s has no terms the query
// matches nothing, as a lone stop word always has.
func textQuery(s string, anyWord bool) query.Query {
	var terms []query.Query
	for _, word := range queryTerms(s) {
		exact := buildFieldQuery(word, 0, 0)
		exact.SetBoost(10)
		fuzzy := buildFieldQuery(word, 1, fuzzyPrefixLen)
		fuzzy.SetBoost(2)
		terms = append(terms, bleve.NewDisjunctionQuery(exact, fuzzy))
	}
	switch len(terms) {
	case 0:
		return bleve.NewMatchNoneQuery()
	case 1:
		return terms[0]
	}
	if anyWord {
		return bleve.NewDisjunctionQuery(terms...)
	}
	return bleve.NewConjunctionQuery(terms...)
}

// buildFieldQuery builds a disjunction over Title, Description, Author and
// Tags for one term. fuzzyness is the edit distance bleve's MatchQuery allows
// (0 for an exact match) and prefixLen is how many leading characters must
// match exactly before fuzziness applies. Slugs in Tags are hyphenated
// ("pixel-art"), which the standard analyzer splits, so tags match by word.
func buildFieldQuery(term string, fuzzyness int, prefixLen int) *query.DisjunctionQuery {
	field := func(name string, boost float64) query.Query {
		q := bleve.NewMatchQuery(term)
		q.SetField(name)
		q.SetBoost(boost)
		if fuzzyness > 0 {
			q.SetPrefix(prefixLen)
			q.SetFuzziness(fuzzyness)
		}
		return q
	}
	return bleve.NewDisjunctionQuery(
		field("Title", titleBoost),
		field("Description", descriptionBoost),
		field("Author", authorBoost),
		// Tags are curated classification rather than prose, so a hit here
		// is a stronger signal than one in a description.
		field("Tags", tagBoost),
	)
}

// Both entry points this file used to expose - QueryCache and Page - are now
// one Find in search.go, because tags, price and ordering apply equally to a
// search and to a browse.
