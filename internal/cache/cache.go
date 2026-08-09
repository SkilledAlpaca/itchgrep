package cache

import (
	"errors"
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"slices"
	"sync"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/search/query"
)

// ErrNotReady is returned by Find when no data/index has ever been
// successfully loaded yet (e.g. the server just started and the initial load is
// still retrying in the background).
var ErrNotReady = errors.New("cache: not ready")

// expiryCheckTTL is the minimum interval between live storage.GetAssetsUpdateTime
// checks in IsCacheExpired. The underlying data only changes when the
// dataservice finishes a scrape (at most once a day), so checking on every
// single HTTP request is wasted work.
//
// It used to be 60s, when the check was a GCS round-trip. Against a local
// directory it is an os.Stat, so the throttle exists only to keep the syscall
// off the hot path - and a shorter one means a freshly published index is
// picked up in seconds instead of up to a minute.
const expiryCheckTTL = 5 * time.Second

type Cache struct {
	cacheLock sync.RWMutex

	dataMap map[string]models.Asset
	data    []models.Asset
	index   bleve.Index

	// tagCounts is how many assets carry each tag, across the whole dataset.
	// Computed once per refresh rather than faceted per request: the unfiltered
	// browse is the most-requested view there is and its facet never varies, so
	// asking bleve to count 600 terms over 86,000 documents on every homepage
	// load would be paying repeatedly for a constant.
	tagCounts []models.Tag

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

func NewCache(pageSize int64) *Cache {
	return &Cache{
		dataMap:         make(map[string]models.Asset),
		cacheLock:       sync.RWMutex{},
		pageSize:        pageSize,
		dataUpdatedTime: time.Time{},
	}
}

// PageSize is how many assets one page of results holds. Handlers need it to
// work out whether a further page exists.
func (c *Cache) PageSize() int64 { return c.pageSize }

// DataUpdatedTime is when the currently-loaded dataset was published. Handlers
// use it as Last-Modified so a shared cache can revalidate rather than refetch,
// and so a newly published index invalidates what the edge is holding.
func (c *Cache) DataUpdatedTime() time.Time {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()
	return c.dataUpdatedTime
}

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
	// underlying data only changes at most once a day, so we only need to
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

	err := c.doRefresh()

	c.refreshMu.Lock()
	c.refreshErr = err
	c.refreshing = false
	c.refreshMu.Unlock()
	close(done)

	return err
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
	indexPath := storage.IndexPath()
	newIndex, err := bleve.Open(indexPath)
	if err != nil {
		return fmt.Errorf("bleve.Open %s: %w", indexPath, err)
	}
	fetchTime = time.Since(preFetchTime)
	logging.Info("Opened index %s in %v", indexPath, fetchTime)

	// sort newData by popularity (smaller numbers first)
	slices.SortFunc(newData, func(i, j models.Asset) int {
		return int(i.InvPopularity - j.InvPopularity)
	})

	newDataMap := make(map[string]models.Asset, len(newData)) // we also save it as a map, so we can easily match searches from the index
	for _, asset := range newData {
		newDataMap[asset.GameId] = asset
	}

	// swap the new index/data in, holding the write lock only for the swap
	// itself. The old index is intentionally not closed yet: if anything
	// above failed we must keep serving it.
	c.cacheLock.Lock()
	oldIndex := c.index
	c.index = newIndex
	c.data = newData
	c.dataMap = newDataMap
	c.tagCounts = countTags(newData)
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

func buildFuzzyQuery(queryString string, fuzzyness int, prefixLen int) *query.DisjunctionQuery {
	titleQuery := bleve.NewMatchQuery(queryString)
	titleQuery.SetField("Title")
	titleQuery.SetBoost(3)
	titleQuery.SetPrefix(prefixLen)
	titleQuery.SetFuzziness(fuzzyness)
	descriptionQuery := bleve.NewMatchQuery(queryString)
	descriptionQuery.SetField("Description")
	descriptionQuery.SetBoost(2)
	descriptionQuery.SetPrefix(prefixLen)
	descriptionQuery.SetFuzziness(fuzzyness)
	authorQuery := bleve.NewMatchQuery(queryString)
	authorQuery.SetField("Author")
	authorQuery.SetBoost(1)
	authorQuery.SetPrefix(prefixLen)
	authorQuery.SetFuzziness(fuzzyness)
	// Tags are curated classification rather than prose, so a hit here is a
	// stronger signal than one in a description that merely mentions the word.
	// Slugs are hyphenated ("pixel-art"), which bleve's standard analyser
	// splits into terms, so a "pixel art" query matches without special casing.
	tagQuery := bleve.NewMatchQuery(queryString)
	tagQuery.SetField("Tags")
	tagQuery.SetBoost(4)
	tagQuery.SetPrefix(prefixLen)
	tagQuery.SetFuzziness(fuzzyness)

	// Combine queries with a disjunction (OR) query
	query := bleve.NewDisjunctionQuery(titleQuery, descriptionQuery, authorQuery, tagQuery)
	return query
}

func buildExactQuery(queryString string) *query.DisjunctionQuery {
	titleQuery := bleve.NewMatchQuery(queryString)
	titleQuery.SetField("Title")
	titleQuery.SetBoost(3)
	descriptionQuery := bleve.NewMatchQuery(queryString)
	descriptionQuery.SetField("Description")
	descriptionQuery.SetBoost(2)
	authorQuery := bleve.NewMatchQuery(queryString)
	authorQuery.SetField("Author")
	authorQuery.SetBoost(1)
	tagQuery := bleve.NewMatchQuery(queryString)
	tagQuery.SetField("Tags")
	tagQuery.SetBoost(4)

	// Combine queries with a disjunction (OR) query
	query := bleve.NewDisjunctionQuery(titleQuery, descriptionQuery, authorQuery, tagQuery)
	return query
}

// Both entry points this file used to expose - QueryCache and Page - are now
// one Find in search.go, because tags, price and ordering apply equally to a
// search and to a browse.
