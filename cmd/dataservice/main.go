package main

import (
	"fmt"
	"itchgrep/internal/fetcher"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"net/http"
	"os"
	"sort"
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
		tagSets:      make(map[string]map[string]struct{}, totalAssets),
		doneSlices:   make(map[string]struct{}, len(slices)),
	}
	c.resume(cfg.checkpointMaxAge)
	c.run(slices)
	c.attachTags()

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
	//
	// Built at the staging path, which sits beside the published index so that
	// publishing is a rename rather than a copy. A previous run killed between
	// creating this and publishing it would leave the directory behind, and
	// bleve.New refuses to open a path that already exists.
	logging.Info("Creating index...")
	staging := storage.StagingIndexPath()
	if err := os.RemoveAll(staging); err != nil {
		logging.Error("Failed to clear stale staging index at %s: %v", staging, err)
		return
	}
	newIndex, err := bleve.New(staging, storage.IndexMapping())
	// On any failure below, the half-built index must not be left where the
	// next run would trip over it. PublishIndex renames it away on success, so
	// this is a no-op then.
	defer os.RemoveAll(staging)
	if err != nil {
		logging.Error("Failed to create index: %v", err)
		return
	}

	logging.Info("Created new empty index at %s", staging)

	// first, convert the assets to IndexedAssets, which are smaller and used for indexing
	var smolAssets []models.IndexedAsset = make([]models.IndexedAsset, len(assets))
	for i, asset := range assets {
		smolAssets[i] = models.NewIndexedAsset(asset)
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

	// PUBLISHING
	//
	// Index first, assets.json second. The webserver watches assets.json's
	// timestamp to decide a new dataset is ready, so this ordering guarantees
	// the index it then opens is the one matching those assets.
	logging.Info("Publishing index to %s", storage.IndexPath())
	if err := storage.PublishIndex(); err != nil {
		logging.Error("Failed to publish index: %v", err)
		return
	}
	logging.Info("Successfully published index")

	logging.Info("Storing assets")
	if err := storage.PutAssets(assets); err != nil {
		logging.Error("Failed to put assets: %v", err)
		return
	}
	logging.Info("Successfully stored assets")

	// The crawl completed and published, so the checkpoint is now stale. Left
	// behind, the next run would resume a finished crawl and skip every slice.
	if err := storage.DeleteCheckpoint(); err != nil {
		logging.Warning("Failed to delete crawl checkpoint: %v", err)
	}
}

// crawlConfig is the tuning for one crawl. Every field has a working default,
// so the dataservice runs with no configuration at all.
type crawlConfig struct {
	coverageTarget   float64       // stop once this fraction of the catalogue is collected
	coverageFloor    float64       // below this, refuse to publish
	minYield         float64       // abandon a slice yielding fewer new assets than this, as a fraction of a page
	tagCacheMaxAge   time.Duration // rediscover the tag universe when the cache is older than this
	checkpointMaxAge time.Duration // ignore a crawl checkpoint older than this
	maxPages         int64         // total page budget across all slices; 0 means unlimited
	maxTags          int           // how many tags discovery may visit; 0 means the package default
}

func loadCrawlConfig() crawlConfig {
	cfg := crawlConfig{
		coverageTarget:   envFloat("COVERAGE_TARGET", 0.95),
		coverageFloor:    envFloat("COVERAGE_FLOOR", 0.90),
		minYield:         envFloat("SLICE_MIN_YIELD", 0.05),
		tagCacheMaxAge:   envDuration("TAG_CACHE_MAX_AGE", 168*time.Hour),
		checkpointMaxAge: envDuration("CHECKPOINT_MAX_AGE", 24*time.Hour),
		maxTags:          int(envInt("MAX_TAGS", fetcher.DefaultMaxTags)),
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

	// tagSets accumulates, per GameId, the tags of every slice an asset was
	// seen in. Repeat sightings are what make this worth keeping: the dedup
	// below discards the asset itself on a second sighting, but the fact that
	// it also appeared under another tag is exactly the information itch.io's
	// listing JSON does not give us, and it costs nothing to retain.
	tagSets map[string]map[string]struct{}

	// maxRootRank is how deep the root view was crawled. Root page numbers are
	// a true global popularity rank; page numbers within any other slice are
	// not comparable to them, so slice-only assets are ranked after this.
	maxRootRank int64

	// doneSlices are slice labels already finished, whether in this run or in
	// the checkpointed run being resumed.
	doneSlices map[string]struct{}

	lastCheckpoint time.Time

	pagesFetched atomic.Int64
	slicesDone   atomic.Int64
}

// checkpointInterval is the minimum wall-clock gap between checkpoint writes.
// A checkpoint serialises every asset collected so far (~26 MB at full
// coverage), so writing one per slice would spend more time uploading than
// crawling; writing one per hour would make the feature pointless.
const checkpointInterval = 3 * time.Minute

// resume restores a partially-complete crawl. It is best-effort: any problem
// means starting fresh, which costs time but is never wrong.
func (c *crawler) resume(maxAge time.Duration) {
	cp, updated, err := storage.GetCheckpoint()
	if err != nil {
		logging.Info("No crawl checkpoint to resume (%v), starting fresh", err)
		return
	}
	if age := time.Since(updated); age > maxAge {
		logging.Info("Ignoring crawl checkpoint: %s old, older than %s", age.Round(time.Minute), maxAge)
		return
	}
	if cp.TotalAssets != c.totalAssets {
		// The catalogue moved underneath the checkpoint. Coverage arithmetic
		// and the slice plan were both computed against the old size, so
		// resuming would silently mix two different crawls.
		logging.Info("Ignoring crawl checkpoint: catalogue was %d assets, now %d", cp.TotalAssets, c.totalAssets)
		return
	}

	for _, a := range cp.Assets {
		if a.GameId == "" {
			continue
		}
		if _, ok := c.seen[a.GameId]; ok {
			continue
		}
		c.seen[a.GameId] = struct{}{}
		c.assets = append(c.assets, a)
		if len(a.Tags) > 0 {
			set := make(map[string]struct{}, len(a.Tags))
			for _, t := range a.Tags {
				set[t] = struct{}{}
			}
			c.tagSets[a.GameId] = set
		}
	}
	for _, label := range cp.DoneSlices {
		c.doneSlices[label] = struct{}{}
	}
	c.maxRootRank = cp.MaxRootRank

	logging.Info("Resuming crawl from checkpoint %s old: %d assets, %d slices already done",
		time.Since(updated).Round(time.Minute), len(c.assets), len(c.doneSlices))
}

// checkpoint persists progress so far, at most once per checkpointInterval.
// Failure is logged and ignored: losing a checkpoint costs a restart, whereas
// aborting the crawl over one loses the crawl.
func (c *crawler) checkpoint(force bool) {
	c.mu.Lock()
	if !force && time.Since(c.lastCheckpoint) < checkpointInterval {
		c.mu.Unlock()
		return
	}
	c.lastCheckpoint = time.Now()

	// Copy under the lock; the upload below must not hold it, since the crawl
	// keeps recording assets throughout.
	//
	// Tags are materialised into the copy rather than left to attachTags at the
	// end of the crawl: a checkpoint has to be self-contained, or resuming from
	// one would silently drop every tag learned before the interruption.
	assets := make([]models.Asset, len(c.assets))
	copy(assets, c.assets)
	for i := range assets {
		set := c.tagSets[assets[i].GameId]
		if len(set) == 0 {
			continue
		}
		tags := make([]string, 0, len(set))
		for t := range set {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		assets[i].Tags = tags
	}

	cp := storage.Checkpoint{
		Assets:      assets,
		DoneSlices:  make([]string, 0, len(c.doneSlices)),
		MaxRootRank: c.maxRootRank,
		TotalAssets: c.totalAssets,
	}
	for label := range c.doneSlices {
		cp.DoneSlices = append(cp.DoneSlices, label)
	}
	c.mu.Unlock()

	if err := storage.PutCheckpoint(cp); err != nil {
		logging.Warning("Failed to write crawl checkpoint: %v", err)
		return
	}
	logging.Debug("Checkpointed %d assets, %d slices done", len(cp.Assets), len(cp.DoneSlices))
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
// seen in an earlier slice keep the rank they were first given, but still
// contribute this slice's tags.
func (c *crawler) record(assets []models.Asset, sliceTags []string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	added := 0
	for _, a := range assets {
		if a.GameId == "" {
			continue
		}
		if _, ok := c.seen[a.GameId]; !ok {
			c.seen[a.GameId] = struct{}{}
			c.assets = append(c.assets, a)
			added++
		}

		// Deliberately outside the new-asset branch: an asset already collected
		// from tag-pixel-art still teaches us something when it turns up again
		// under tag-sprites.
		for _, t := range sliceTags {
			set := c.tagSets[a.GameId]
			if set == nil {
				set = make(map[string]struct{}, len(sliceTags))
				c.tagSets[a.GameId] = set
			}
			set[t] = struct{}{}
		}
	}
	return added
}

// attachTags materialises the accumulated tag sets onto the collected assets.
// Called once after the crawl: doing it per sighting would mean rewriting a
// slice header on every duplicate, which is most of what the crawl sees.
func (c *crawler) attachTags() {
	c.mu.Lock()
	defer c.mu.Unlock()

	tagged := 0
	for i := range c.assets {
		set := c.tagSets[c.assets[i].GameId]
		if len(set) == 0 {
			continue
		}
		tags := make([]string, 0, len(set))
		for t := range set {
			tags = append(tags, t)
		}
		sort.Strings(tags) // stable output, so reruns diff cleanly
		c.assets[i].Tags = tags
		tagged++
	}
	logging.Info("Attached tags to %d/%d assets", tagged, len(c.assets))
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

		label := s.Label()
		c.mu.Lock()
		_, alreadyDone := c.doneSlices[label]
		c.mu.Unlock()
		if alreadyDone {
			c.slicesDone.Add(1)
			continue
		}

		c.runSlice(s)

		c.mu.Lock()
		c.doneSlices[label] = struct{}{}
		c.mu.Unlock()
		c.slicesDone.Add(1)

		c.checkpoint(false)
	}
}

// runSlice pages through one view, stopping when the view is exhausted, when
// it stops yielding anything new, or when the crawl as a whole is done.
func (c *crawler) runSlice(s fetcher.Slice) {
	isRoot := s.IsRoot()
	maxPages := s.PagesToFetch(c.itemsPerPage)

	// Trailing window of per-page yields. A single page is too noisy to judge
	// a slice on: one dense page among barren ones would keep a spent slice
	// alive, and one sparse page would abandon a productive one.
	//
	// Sized in whole batches. It used to be 5, but fetchPages returns
	// maxConcurrentRequests (8) counts at a time, so the window was already
	// full when the first batch landed and every slice was judged on its first
	// 8 pages - the guard against judging a slice before it had a fair sample
	// never once fired. Measured consequence: 257 of 314 abandoned slices died
	// at exactly page 8.
	const yieldWindow = 2 * maxConcurrentRequests
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
		// Nor is a view that reaches assets nothing else reaches. Browse results
		// are popularity-ordered, so a low yield near the front means "the
		// popular end of this set is already collected", not "this set holds
		// nothing new" - the unique assets are precisely the deepest ones. See
		// Slice.PageInFull.
		if s.PageInFull {
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
			perPage[i] = c.record(assets, s.Tags)
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
