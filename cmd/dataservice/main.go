package main

import (
	"fmt"
	"itchgrep/internal/fetcher"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve"
)

// scrapeInProgress guards against overlapping scrapes: /trigger-fetch spawns
// a background goroutine, and two concurrent scrapes would both write the
// same storage.IndexDirName directory and corrupt the index.
var scrapeInProgress atomic.Bool

func main() {
	logging.Init("", true)

	http.HandleFunc("/trigger-fetch", handleFetchTrigger)
	port := fmt.Sprintf(":%s", os.Getenv("PORT")) // as per cloud run standard
	if port == ":" {
		port = ":8080"
	}
	logging.Info("Server listening on port %s", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		logging.Fatal("Server failed to start: %v", err)
	}
}

func handleFetchTrigger(w http.ResponseWriter, r *http.Request) {
	// Ensure that we only accept GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !scrapeInProgress.CompareAndSwap(false, true) {
		http.Error(w, "A scrape is already in progress", http.StatusConflict)
		return
	}

	go func() {
		// Released on every exit path of fetchAndStoreAssets, including its
		// early returns on error.
		defer scrapeInProgress.Store(false)
		fetchAndStoreAssets()
	}()

	// Respond to indicate the process has started
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Asset fetch and store initiated")
}

func fetchAndStoreAssets() {
	cfg := loadCrawlConfig()

	// FETCHING ASSETS
	totalAssets, err := fetcher.GetAssetCount()
	if err != nil {
		logging.Error("Failed to get asset count: %v", err)
		return
	}

	// The root view's first page also tells us how many items a page holds,
	// which sets the ceiling on what any single view can reach.
	root := fetcher.Slice{}
	firstPage, outcome := fetcher.FetchAssetPage(root, 1)
	if outcome != fetcher.FetchOK {
		logging.Error("Failed to fetch first page, terminating.")
		return
	}
	itemsPerPage := firstPage.NumItems
	if itemsPerPage <= 0 {
		logging.Error("First page reported %d items per page, terminating.", itemsPerPage)
		return
	}

	tags, err := loadOrDiscoverTags(cfg)
	if err != nil {
		logging.Error("Failed to obtain tag universe: %v", err)
		return
	}

	slices := fetcher.PlanSlices(tags, itemsPerPage)
	logging.Info("Planned %d slices from %d tags; %d assets to cover, %d reachable per slice",
		len(slices), len(tags), totalAssets, fetcher.MaxPagesPerView*itemsPerPage)

	c := &crawler{
		cfg:          cfg,
		itemsPerPage: itemsPerPage,
		totalAssets:  totalAssets,
		seen:         make(map[string]struct{}, totalAssets),
	}
	c.run(slices)

	assets := c.assets
	coverage := c.coverage()
	logging.Info("Successfully fetched %d assets (%.1f%% of %d) using %d pages across %d slices, 429s: %d",
		len(assets), coverage*100, totalAssets, c.pagesFetched.Load(), c.slicesDone.Load(), fetcher.Refusals())

	// A run that stalled must not replace a good index with a worse one.
	if coverage < cfg.coverageFloor {
		logging.Error("Coverage %.1f%% is below COVERAGE_FLOOR %.1f%%; refusing to publish. The previous index is left in place.",
			coverage*100, cfg.coverageFloor*100)
		return
	}

	// CREATING INDEX
	logging.Info("Creating index...")
	newIndexMapping := bleve.NewIndexMapping() // TODO: customize as needed
	newIndex, err := bleve.New(storage.IndexDirName, newIndexMapping)
	defer os.RemoveAll(storage.IndexDirName) // After we are done, no matter if clean or with error, we remove the index, since it is uploaded to storage.
	if err != nil {
		logging.Error("Failed to create index: %v", err)
		return
	}

	logging.Info("Created new empty index at %s", storage.IndexDirName)

	// first, convert the assets to IndexedAssets, which are smaller and used for indexing
	var smolAssets []models.IndexedAsset = make([]models.IndexedAsset, len(assets))
	for i, asset := range assets {
		smolAssets[i] = models.IndexedAsset{
			GameId:        asset.GameId,
			Title:         asset.Title,
			Author:        asset.Author,
			Description:   asset.Description,
			InvPopularity: asset.InvPopularity,
		}
	}

	// indexing the assets in batches
	b := newIndex.NewBatch()
	assetsIndexed := 0
	for i, asset := range smolAssets {
		assetsIndexed += 1
		err := b.Index(asset.GameId, asset)
		if err != nil {
			newIndex.Close() // clean up the failed new index
			logging.Error("Failed to index asset, cancelling indexing: %v", err)
			return
		}
		if i%1500 == 0 && i != 0 { // we index in batches of 1500
			logging.Info("Batching assets: %d/%d", i, len(smolAssets))
			newIndex.Batch(b)
			b.Reset()
		}
	}
	newIndex.Batch(b) // batch the remaining assets into the index
	newIndex.Close()  // close the index
	logging.Info("Successfully indexed %d assets", assetsIndexed)

	// STORING INDEX
	logging.Info("Storing index in cloud storage file")
	dir, err := os.ReadDir(storage.IndexDirName)
	if err != nil {
		logging.Error("Failed to read dir: %v", err)
	}
	for _, entry := range dir {
		logging.Info("DEBUG: entry: %s", entry.Name())
	}

	err = storage.PutFS(storage.IndexDirName, storage.IndexArchiveName)
	if err != nil {
		logging.Error("Failed to put index: %v", err)
		return
	}
	logging.Info("Successfully stored index")

	// STORING ASSETS
	logging.Info("Storing assets in cloud storage file")
	err = storage.PutAssets(assets)
	if err != nil {
		logging.Error("Failed to put assets, stopping here and not putting index: %v", err)
		return
	}
	logging.Info("Successfully stored assets")

}

// crawlConfig is the tuning for one crawl. Every field has a working default,
// so the dataservice runs with no configuration at all.
type crawlConfig struct {
	coverageTarget float64       // stop once this fraction of the catalogue is collected
	coverageFloor  float64       // below this, refuse to publish
	minYield       float64       // abandon a slice yielding fewer new assets than this, as a fraction of a page
	tagCacheMaxAge time.Duration // rediscover the tag universe when the cache is older than this
	maxPages       int64         // total page budget across all slices; 0 means unlimited
	maxTags        int           // how many tags discovery may visit; 0 means the package default
}

func loadCrawlConfig() crawlConfig {
	cfg := crawlConfig{
		coverageTarget: envFloat("COVERAGE_TARGET", 0.95),
		coverageFloor:  envFloat("COVERAGE_FLOOR", 0.90),
		minYield:       envFloat("SLICE_MIN_YIELD", 0.05),
		tagCacheMaxAge: envDuration("TAG_CACHE_MAX_AGE", 168*time.Hour),
		maxTags:        int(envInt("MAX_TAGS", fetcher.DefaultMaxTags)),
	}

	// SCRAPE_MAX_PAGES caps how many pages are fetched in total, across every
	// slice. A full crawl is thousands of pages and takes the better part of
	// an hour at a polite request rate, which makes it a painful way to
	// smoke-test the rest of the pipeline (indexing, archiving, upload,
	// webserver startup). Setting this small exercises every one of those
	// stages in a couple of minutes. Leave it unset in production.
	if v := os.Getenv("SCRAPE_MAX_PAGES"); v != "" {
		if maxPages, err := strconv.ParseInt(v, 10, 64); err == nil && maxPages > 0 {
			logging.Warning("SCRAPE_MAX_PAGES=%d set: fetching at most %d pages in total. This produces a PARTIAL index and is intended for testing only.", maxPages, maxPages)
			cfg.maxPages = maxPages
			// A deliberately truncated run would otherwise trip the publish
			// floor and refuse to store anything, which defeats the purpose.
			cfg.coverageFloor = 0
			// Discovery costs one request per tag and ignores the page budget,
			// so without this a "couple of minutes" smoke test still spends
			// ~17 minutes walking the tag universe before fetching anything.
			if os.Getenv("MAX_TAGS") == "" {
				cfg.maxTags = 25
			}
		} else {
			logging.Warning("Ignoring invalid SCRAPE_MAX_PAGES=%q", v)
		}
	}
	return cfg
}

func envFloat(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil || parsed < 0 {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

func envInt(name string, def int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseInt(v, 10, 64)
	if err != nil || parsed <= 0 {
		logging.Warning("Ignoring invalid %s=%q, using %d", name, v, def)
		return def
	}
	return parsed
}

func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := time.ParseDuration(v)
	if err != nil || parsed <= 0 {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

// loadOrDiscoverTags returns the tag universe, preferring the cached copy.
// Discovery costs several hundred requests to itch.io and the tag set barely
// moves between runs, so it is only rebuilt when the cache is missing or stale.
func loadOrDiscoverTags(cfg crawlConfig) ([]models.Tag, error) {
	tags, updated, err := storage.GetTags()
	switch {
	case err != nil:
		// Not fatal - discovery below rebuilds it. But say why, or a cache
		// that silently never loads is indistinguishable from a cold start
		// and quietly costs a few hundred requests on every single run.
		logging.Info("No usable tag cache (%v), discovering", err)
	case len(tags) == 0:
		logging.Info("Tag cache is empty, discovering")
	default:
		if age := time.Since(updated); age < cfg.tagCacheMaxAge {
			logging.Info("Using cached tag universe: %d tags, %s old", len(tags), age.Round(time.Minute))
			return tags, nil
		}
		logging.Info("Cached tag universe is stale, rediscovering")
	}

	tags, err = fetcher.DiscoverTags(nil, cfg.maxTags)
	if err != nil {
		return nil, err
	}
	if err := storage.PutTags(tags); err != nil {
		// A cache we failed to write is not a reason to abandon a crawl we
		// already paid for.
		logging.Warning("Failed to cache tag universe: %v", err)
	}
	return tags, nil
}

// crawler walks the planned slices, collecting assets and deduplicating them
// by GameId. Assets appear in roughly nine tag views each, so without the
// dedup set the same asset would be fetched and stored many times over.
type crawler struct {
	cfg          crawlConfig
	itemsPerPage int64
	totalAssets  int64

	mu     sync.Mutex
	seen   map[string]struct{}
	assets []models.Asset

	// maxRootRank is how deep the root view was crawled. Root page numbers are
	// a true global popularity rank; page numbers within any other slice are
	// not comparable to them, so slice-only assets are ranked after this.
	maxRootRank int64

	pagesFetched atomic.Int64
	slicesDone   atomic.Int64
}

// maxConcurrentRequests bounds how many fetches are in flight. Request pacing
// itself lives in internal/fetcher, which rate-limits every outbound request
// (including internal retries), so this only affects how many pages can be
// waiting on a response at once - it cannot make the crawl hit itch.io harder.
const maxConcurrentRequests = 8

func (c *crawler) coverage() float64 {
	if c.totalAssets <= 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return float64(len(c.assets)) / float64(c.totalAssets)
}

func (c *crawler) done() bool {
	if c.cfg.maxPages > 0 && c.pagesFetched.Load() >= c.cfg.maxPages {
		return true
	}
	return c.coverage() >= c.cfg.coverageTarget
}

// record adds a page's assets, returning how many were new. Assets already
// seen in an earlier slice keep the rank they were first given.
func (c *crawler) record(assets []models.Asset) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	added := 0
	for _, a := range assets {
		if a.GameId == "" {
			continue
		}
		if _, ok := c.seen[a.GameId]; ok {
			continue
		}
		c.seen[a.GameId] = struct{}{}
		c.assets = append(c.assets, a)
		added++
	}
	return added
}

func (c *crawler) run(slices []fetcher.Slice) {
	stopProgress := make(chan struct{})
	go c.logProgress(stopProgress, len(slices))
	defer close(stopProgress)

	for _, s := range slices {
		if c.done() {
			logging.Info("Coverage target reached; skipping %d remaining slices", len(slices)-int(c.slicesDone.Load()))
			return
		}
		if !s.Valid() {
			// itch.io answers a malformed slice with 403, so this is a planner
			// bug rather than something to retry.
			logging.Error("Skipping invalid slice %q: violates the browse URL grammar", s.Label())
			continue
		}
		c.runSlice(s)
		c.slicesDone.Add(1)
	}
}

// runSlice pages through one view, stopping when the view is exhausted, when
// it stops yielding anything new, or when the crawl as a whole is done.
func (c *crawler) runSlice(s fetcher.Slice) {
	isRoot := s.Sort == fetcher.SortDefault && len(s.Tags) == 0
	maxPages := s.PagesToFetch(c.itemsPerPage)

	// Trailing window of per-page yields. A single page is too noisy to judge
	// a slice on: one dense page among barren ones would keep a spent slice
	// alive, and one sparse page would abandon a productive one.
	const yieldWindow = 5
	var recentYield []int
	threshold := c.cfg.minYield * float64(c.itemsPerPage)

	for start := int64(1); start <= maxPages; start += maxConcurrentRequests {
		if c.done() {
			return
		}

		end := start + maxConcurrentRequests - 1
		if end > maxPages {
			end = maxPages
		}

		newAssets, exhausted := c.fetchPages(s, start, end, isRoot)
		if isRoot {
			c.mu.Lock()
			if end > c.maxRootRank {
				c.maxRootRank = end
			}
			c.mu.Unlock()
		}

		recentYield = append(recentYield, newAssets...)
		if len(recentYield) > yieldWindow {
			recentYield = recentYield[len(recentYield)-yieldWindow:]
		}

		if exhausted {
			return
		}

		// The root view is never abandoned early: it is the only source of
		// global popularity rank, and truncating it would leave later slices
		// ranking against an incomplete baseline.
		if isRoot {
			continue
		}
		if sliceSpent(recentYield, yieldWindow, threshold) {
			logging.Debug("Abandoning slice %s at page %d: yield below threshold", s.Label(), end)
			return
		}
	}
}

// sliceSpent reports whether a slice has stopped paying for itself: its
// trailing window is full and averages fewer new assets per page than the
// threshold. An incomplete window is never spent - judging a slice before it
// has been given a fair sample abandons productive views on one sparse page.
func sliceSpent(recentYield []int, window int, threshold float64) bool {
	if len(recentYield) < window {
		return false
	}
	total := 0
	for _, y := range recentYield {
		total += y
	}
	return float64(total)/float64(len(recentYield)) < threshold
}

// fetchPages fetches pages [start, end] of a slice concurrently, returning the
// per-page counts of newly seen assets and whether the view ran out of pages.
func (c *crawler) fetchPages(s fetcher.Slice, start, end int64, isRoot bool) ([]int, bool) {
	n := int(end - start + 1)
	perPage := make([]int, n)
	exhausted := make([]bool, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pageNum := start + int64(i)

			data, outcome := fetcher.FetchAssetPage(s, pageNum)
			c.pagesFetched.Add(1)
			switch outcome {
			case fetcher.FetchExhausted:
				exhausted[i] = true
				return
			case fetcher.FetchFailed:
				return
			}

			assets, err := fetcher.ParseAssetPage(data, c.rankFor(isRoot, pageNum))
			if err != nil {
				logging.Error("Failed to parse %s page %d: %v", s.Label(), pageNum, err)
				return
			}
			perPage[i] = c.record(assets)
		}(i)
	}
	wg.Wait()

	for _, e := range exhausted {
		if e {
			return perPage, true
		}
	}
	return perPage, false
}

// rankFor produces the InvPopularity to stamp on assets from a given page.
//
// In the root view the page number is a genuine global popularity rank. In any
// other slice it is only a rank within that slice - page 3 of tag-fonts and
// page 3 of tag-pixel-art mean entirely different things - so slice-only
// assets are pushed behind everything the root view ranked, preserving the
// ordering search relevance depends on.
func (c *crawler) rankFor(isRoot bool, pageNum int64) int64 {
	if isRoot {
		return pageNum
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxRootRank + pageNum
}

func (c *crawler) logProgress(stop <-chan struct{}, totalSlices int) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			found := len(c.assets)
			c.mu.Unlock()

			pct := 0.0
			if c.totalAssets > 0 {
				pct = float64(found) / float64(c.totalAssets) * 100
			}
			// Refusals are included because they are otherwise invisible: a
			// crawl being refused half the time and one running clean produce
			// identical progress lines, differing only in how slowly the count
			// climbs.
			logging.Info("assets: %d/%d (%.1f%%), slices: %d/%d, pages: %d, 429s: %d",
				found, c.totalAssets, pct, c.slicesDone.Load(), totalSlices,
				c.pagesFetched.Load(), fetcher.Refusals())
		}
	}
}
