package cache

import (
	"errors"
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/search/query"
)

// ErrNotReady is returned by Page and QueryCache when no data/index has ever
// been successfully loaded yet (e.g. the server just started and the initial
// load is still retrying in the background).
var ErrNotReady = errors.New("cache: not ready")

// expiryCheckTTL is the minimum interval between live storage.GetAssetsUpdateTime
// checks in IsCacheExpired. The underlying data only changes when the
// dataservice finishes a scrape (at most once a day), so checking on every
// single HTTP request is pointless network overhead.
const expiryCheckTTL = 60 * time.Second

type Cache struct {
	cacheLock sync.RWMutex

	dataMap map[string]models.Asset
	data    []models.Asset
	index   bleve.Index

	// indexTempDir is the os.MkdirTemp directory backing the currently open
	// index. It is removed once a subsequent successful refresh replaces it.
	indexTempDir string

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

	// fetch index data into a unique temp dir so concurrent/successive
	// refreshes never extract on top of each other or the live index.
	tempDir, err := os.MkdirTemp("", "itchgrep-index-*")
	if err != nil {
		return fmt.Errorf("os.MkdirTemp: %w", err)
	}

	preFetchTime = time.Now()
	indexPath, err := storage.GetFS(storage.IndexArchiveName, tempDir)
	if err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("storage.GetFS: %w", err)
	}

	newIndex, err := bleve.Open(indexPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return fmt.Errorf("bleve.Open: %w", err)
	}
	fetchTime = time.Since(preFetchTime)
	logging.Info("Fetched and opened index in %v", fetchTime)

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
	oldTempDir := c.indexTempDir
	c.index = newIndex
	c.indexTempDir = tempDir
	c.data = newData
	c.dataMap = newDataMap
	c.dataUpdatedTime = newServerUpdateTime
	c.cacheLock.Unlock()

	// only now that the new index is live do we tear down the old one.
	if oldIndex != nil {
		if err := oldIndex.Close(); err != nil {
			logging.Error("Failed to close previous index: %v", err)
		}
	}
	if oldTempDir != "" {
		if err := os.RemoveAll(oldTempDir); err != nil {
			logging.Error("Failed to remove previous index temp dir %s: %v", oldTempDir, err)
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

	// Combine queries with a disjunction (OR) query
	query := bleve.NewDisjunctionQuery(titleQuery, descriptionQuery, authorQuery)
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

	// Combine queries with a disjunction (OR) query
	query := bleve.NewDisjunctionQuery(titleQuery, descriptionQuery, authorQuery)
	return query
}

func (c *Cache) QueryCache(queryString string, pageIndex int64) ([]models.Asset, error) {
	if pageIndex < 1 {
		return nil, fmt.Errorf("cache: pageIndex must be >= 1, got %d", pageIndex)
	}

	// check for stale cache, refresh if needed. If the refresh fails we
	// still fall through and serve whatever was previously loaded (if
	// anything) rather than failing the request.
	if c.IsCacheExpired() {
		if err := c.RefreshDataCache(); err != nil {
			logging.Error("Failed to refresh cache, serving previous data if available: %v", err)
		}
	}

	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if c.index == nil {
		return nil, ErrNotReady
	}

	veryFuzzyQuery := buildFuzzyQuery(queryString, 1, 2)
	veryFuzzyQuery.SetBoost(2)
	fuzzyQuery := buildFuzzyQuery(queryString, 1, 4)
	fuzzyQuery.SetBoost(4)
	exactQuery := buildExactQuery(queryString)
	exactQuery.SetBoost(6)
	query := bleve.NewDisjunctionQuery(veryFuzzyQuery, fuzzyQuery, exactQuery)

	from := (int(pageIndex) - 1) * int(c.pageSize)
	searchRequest := bleve.NewSearchRequestOptions(query, int(c.pageSize), from, false)

	//searchRequest.Highlight = bleve.NewHighlight()
	searchRequest.Fields = []string{"Title", "Author", "Description"}
	searchRequest.SortBy([]string{"-_score", "InvPopularity"})

	searchResult, err := c.index.Search(searchRequest)
	if err != nil {
		return nil, err
	}

	logging.Info("Got %d hits for query \"%s\"", searchResult.Total, queryString)

	var matchedAssets []models.Asset
	for _, hit := range searchResult.Hits {
		matchedAssets = append(matchedAssets, c.dataMap[hit.ID])
	}

	return matchedAssets, nil
}

func (c *Cache) Page(pageNum int64) ([]models.Asset, error) {
	if pageNum < 1 {
		return nil, fmt.Errorf("cache: pageNum must be >= 1, got %d", pageNum)
	}

	// check for stale cache, refresh if needed. If the refresh fails we
	// still fall through and serve whatever was previously loaded (if
	// anything) rather than failing the request.
	if c.IsCacheExpired() {
		if err := c.RefreshDataCache(); err != nil {
			logging.Error("Failed to refresh cache, serving previous data if available: %v", err)
		}
	}

	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if c.data == nil {
		return nil, ErrNotReady
	}

	start := (pageNum - 1) * c.pageSize
	end := start + c.pageSize
	if start >= int64(len(c.data)) {
		// out of range: an empty page (not an error) is how infinite scroll
		// on the client knows to stop requesting further pages.
		return []models.Asset{}, nil
	}
	if end > int64(len(c.data)) {
		end = int64(len(c.data))
	}
	return c.data[start:end], nil
}
