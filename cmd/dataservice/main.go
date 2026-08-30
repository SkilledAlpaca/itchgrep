// Command dataservice is the crawler binary: it periodically walks itch.io's
// asset catalogue (see internal/fetcher), builds a fresh search index and
// asset list from what it finds, and publishes both to the shared data
// directory cmd/webserver reads from (see internal/storage). It also
// exposes POST /trigger-fetch to start a crawl on demand, and self-heals an
// unopenable published index at startup (see selfheal.go) without waiting
// for the next scheduled crawl.
package main

import (
	"context"
	"fmt"
	"itchgrep/internal/config"
	"itchgrep/internal/fetcher"
	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// scrapeInProgress guards against overlapping scrapes: /trigger-fetch spawns
// a background goroutine, and two concurrent scrapes would both write the
// same storage.IndexDirName directory and corrupt the index.
var scrapeInProgress atomic.Bool

// activeCrawler points at the crawler currently running, if any, so a
// shutdown signal can ask it for one last checkpoint before the process
// exits. Set only inside fetchAndStoreAssets, on every exit path via defer;
// scrapeInProgress already guarantees at most one crawl runs at a time, so
// there is never more than one crawler to point at.
var activeCrawler atomic.Pointer[crawler]

func main() {
	logging.Init()

	// Runs synchronously, before scheduleCrawls: a rebuild here takes minutes,
	// and scheduleCrawls would otherwise see a fresh assets.json and wait up to
	// CRAWL_INTERVAL - as long as a week - before an unopenable index got
	// fixed by the normal crawl cycle. See selfheal.go.
	if err := selfHealIndexFormat(); err != nil {
		logging.Error("Self-heal index rebuild failed, the previous index (if any) is left in place: %v", err)
	}

	go scheduleCrawls(config.CrawlInterval())

	// An explicit mux rather than http.HandleFunc against DefaultServeMux: the
	// default mux is package-global, so anything any imported package
	// registers on it becomes reachable here too. An explicit mux is the only
	// way to know the full set of routes this process serves.
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger-fetch", handleFetchTrigger)

	port := fmt.Sprintf(":%s", os.Getenv("PORT")) // as per cloud run standard
	if port == ":" {
		port = ":8080"
	}
	logging.Info("Server listening on port %s", port)

	// Explicit timeouts rather than http.ListenAndServe's defaults, which are
	// none at all: a client that opens a connection and then sends its
	// request one byte at a time holds a goroutine and a file descriptor
	// indefinitely (Slowloris). This process has no reverse proxy of its own
	// in front of it on the compose network - webserver and cloudflared both
	// talk to it directly - so it cannot assume one absorbs that for it.
	srv := &http.Server{
		Addr:              port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Cancelled on SIGINT (Ctrl-C locally) or SIGTERM (what `docker stop` and
	// `docker compose down` actually send), mirroring cmd/webserver/main.go.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			logging.Fatal("Server failed to start: %v", err)
		}
	case <-ctx.Done():
		logging.Info("Shutdown signal received")

		// The HTTP server only ever serves /trigger-fetch, which returns as
		// soon as it has spawned the crawl goroutine - so there is nothing
		// long-running to wait out here, and a short deadline is enough.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logging.Error("HTTP server shutdown did not finish cleanly: %v", err)
		}
		cancel()

		// A crawl itself is NOT waited on here - it can run for six hours, and
		// this process has ~10s to exit. Instead, if one is running, ask it
		// for one last checkpoint so a resume picks up from close to where
		// this run actually stopped rather than from whatever
		// checkpointInterval last wrote on its own schedule. checkpoint()
		// takes the crawler's own mutex for the duration, so this is safe
		// alongside the crawl loop's own checkpoint(false) calls - both
		// serialise on the same lock - rather than a race that would need
		// restructuring the crawler to avoid.
		//
		// Best-effort, not airtight: activeCrawler is cleared by a defer right
		// at the end of fetchAndStoreAssets, after DeleteCheckpoint has already
		// run on a successful publish. A signal landing in that narrow,
		// few-instructions-wide window would rewrite a checkpoint file for a
		// crawl that just finished and already published - harmless (the next
		// run would just resume a "complete" checkpoint and republish
		// immediately) but worth naming rather than assuming away.
		if c := activeCrawler.Load(); c != nil {
			logging.Info("Crawl in progress; writing a final checkpoint before exit")
			c.checkpoint(true)
		}
	}
}

// crawlAction is what decideCrawlAction tells scheduleCrawls to do on one
// tick.
type crawlAction int

const (
	actionNone crawlAction = iota
	actionCrawl
)

// decideCrawlAction is the pure per-tick decision scheduleCrawls acts on,
// pulled out of the loop so it can be tested without a filesystem or a real
// crawl. hasData is false exactly when publishedAge found nothing to read -
// no crawl has ever published on this volume.
func decideCrawlAction(hasData bool, age, interval time.Duration) crawlAction {
	if !hasData {
		// startScrape's CompareAndSwap guard already prevents two crawls
		// racing for the same index directory, so there is no reason to wait
		// for anything else to start the first one.
		return actionCrawl
	}
	if age >= interval {
		return actionCrawl
	}
	return actionNone
}

// scheduleCrawls rebuilds the index whenever the published one is older than
// interval, and is what stops a long-running deployment freezing at whatever
// the first crawl produced. It also runs the very first crawl on a fresh
// volume - there is no separate bootstrap step.
//
// The decision is made from the age of the data on disk rather than from a
// ticker started at boot. A ticker would restart its count on every container
// restart, so a service that gets restarted more often than the interval - a
// nightly appdata backup, an update, a reboot - would never crawl at all, and
// would do it silently.
func scheduleCrawls(interval time.Duration) {
	if interval == 0 {
		logging.Info("CRAWL_INTERVAL=0: the index will only rebuild when /trigger-fetch is called")
		return
	}
	logging.Info("Rebuilding the index whenever it is older than %v", interval)

	// Checked far more often than the interval so that a due crawl starts
	// promptly after a restart, and so the log says something on a cadence a
	// human watching it can make sense of.
	const checkEvery = 15 * time.Minute
	// consecutiveFailures drives the backoff below: a crawl that keeps
	// failing (itch.io markup change, a persistent COVERAGE_FLOOR trip) must
	// not be retried at the normal cadence, or it becomes continuous crawling
	// against itch.io until they block the origin.
	consecutiveFailures := 0
	for {
		age, err := publishedAge()
		hasData := err == nil

		if decideCrawlAction(hasData, age, interval) == actionCrawl {
			if !hasData {
				logging.Info("No published index yet; starting the first crawl")
			} else {
				logging.Info("Index is %v old, past the %v limit; rebuilding", age.Truncate(time.Minute), interval)
			}

			// Waited on synchronously, unlike the HTTP trigger, so this loop
			// knows whether the crawl it just started succeeded and can back
			// off before trying again.
			done := make(chan error, 1)
			if startScrape(func(err error) { done <- err }) {
				if crawlErr := <-done; crawlErr != nil {
					consecutiveFailures++
					delay := backoffDelay(consecutiveFailures, interval)
					logging.Error("crawl failed (attempt %d, next retry in %s): %v", consecutiveFailures, delay, crawlErr)
					time.Sleep(delay)
					continue
				}
				consecutiveFailures = 0
			}
			// startScrape returning false means a /trigger-fetch call beat
			// the scheduler to it; that run's outcome is not this loop's to
			// track, so fall through to the normal check interval.
		}
		time.Sleep(checkEvery)
	}
}

// backoffDelay is how long scheduleCrawls waits before retrying after
// consecutiveFailures scheduled crawls in a row have failed. Progression: 15
// minutes, 1 hour, 6 hours, then the crawl interval itself - a failure that
// persists past three attempts is not a transient one (a stalled run, a
// dropped connection), it is something like a markup change that needs a
// human, and hammering itch.io every few hours while that gets fixed is worse
// than waiting for the normal cadence.
//
// Never returns more than interval: a short CRAWL_INTERVAL configured for
// testing must not be defeated by a backoff step longer than the interval
// itself.
func backoffDelay(consecutiveFailures int, interval time.Duration) time.Duration {
	steps := [...]time.Duration{15 * time.Minute, time.Hour, 6 * time.Hour}

	delay := interval
	if consecutiveFailures >= 1 && consecutiveFailures <= len(steps) {
		delay = steps[consecutiveFailures-1]
	}
	if interval > 0 && delay > interval {
		delay = interval
	}
	return delay
}

// publishedAge is how long ago the current dataset was published. It reads the
// asset file rather than the index because the dataservice writes that one last,
// so its timestamp is the moment a crawl actually finished.
func publishedAge() (time.Duration, error) {
	updated, err := storage.GetAssetsUpdateTime()
	if err != nil {
		return 0, err
	}
	return time.Since(updated), nil
}

// startScrape begins a crawl unless one is already running, reporting whether
// it did. Shared by the HTTP trigger and the scheduler so that both honour the
// same guard - two crawls would write the same index directory and corrupt it.
//
// onDone, if non-nil, receives the crawl's outcome once it finishes. The HTTP
// trigger has already responded to its caller before the crawl even starts
// and passes nil; the scheduler passes a callback and blocks on it so it can
// drive the failure backoff in scheduleCrawls.
func startScrape(onDone func(error)) bool {
	if !scrapeInProgress.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		// Released on every exit path of fetchAndStoreAssets, including its
		// early returns on error.
		defer scrapeInProgress.Store(false)
		err := fetchAndStoreAssets()
		if onDone != nil {
			onDone(err)
		}
	}()
	return true
}

// handleFetchTrigger is the /trigger-fetch endpoint: it starts a crawl in
// the background and returns immediately, without waiting for the crawl
// itself to finish.
func handleFetchTrigger(w http.ResponseWriter, r *http.Request) {
	// POST only. This is a state-changing action - it starts a crawl that can
	// run for hours - and GET must stay side-effect-free: a GET is what any
	// <img src=...> on any web page, or any scanner sweeping a LAN, sends
	// with no user intent behind it at all.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !startScrape(nil) {
		http.Error(w, "A scrape is already in progress", http.StatusConflict)
		return
	}

	// Respond to indicate the process has started
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Asset fetch and store initiated")
}

// fetchAndStoreAssets runs one crawl end to end: fetch, index, publish. It
// returns nil only once a new index and assets.json have actually been
// published; every other path - a fetch failure, a coverage-floor trip, an
// indexing error, a publish failure - returns a non-nil error so the caller
// (scheduleCrawls) can back off instead of retrying immediately.
//
// The body is a fixed sequence of stages, each depending on the previous
// one's output, and none of them reorderable: crawlAndCollect must finish
// before there is a coverage figure to gate on; the gate must pass before an
// index is built from a possibly-stalled crawl; publishCrawl must finish -
// index published, then assets.json written - before recordCrawlCompletion's
// bookkeeping describes a dataset that is actually live.
func fetchAndStoreAssets() error {
	// Checked before anything else, including cfg or the crawl itself: an
	// unwritable data directory (the classic cause is a bind-mounted
	// DATA_PATH never chown'd to the nonroot uid both images run as - see
	// docker-compose.yml's volumes: comments) previously surfaced only after
	// ~17 minutes of tag discovery and then a full ~6-hour crawl, at
	// buildIndex's own bleve.New call. This probe costs milliseconds and
	// fails the same way scheduleCrawls' normal backoff already handles, so
	// the operator sees the real cause immediately instead of last.
	if err := storage.EnsureWritable(); err != nil {
		logging.Error("Refusing to start a crawl: %v", err)
		return fmt.Errorf("storage.EnsureWritable: %w", err)
	}

	cfg := loadCrawlConfig()

	// activeCrawler is set inside crawlAndCollect, as soon as the crawler
	// exists, so a shutdown signal arriving mid-crawl can still find it and
	// force a checkpoint - see activeCrawler's own doc comment. Cleared here,
	// unconditionally, only once fetchAndStoreAssets is about to return: on a
	// successful run that is after recordCrawlCompletion has already deleted
	// the checkpoint. Storing nil over an unset pointer (a fetch failure
	// before the crawler was ever constructed) is a harmless no-op.
	defer activeCrawler.Store(nil)

	result, err := crawlAndCollect(cfg)
	if err != nil {
		return err
	}
	c := result.crawler

	// A run that stalled must not replace a good index with a worse one.
	if err := checkCoverageFloor(result.coverage, cfg.coverageFloor); err != nil {
		logging.Error("Coverage %.1f%% is below COVERAGE_FLOOR %.1f%%; refusing to publish. The previous index is left in place.",
			result.coverage*100, cfg.coverageFloor*100)
		return err
	}

	// Read before this run's index is built, not just before PutAssets
	// overwrites assets.json: stampFirstSeen (inside publishCrawl) has to see
	// it before NewIndexedAsset converts this run's assets, or the index
	// just about to be published would carry no FirstSeen at all and /new
	// would stay empty for one full cycle after every genuine arrival.
	previousAssets, err := storage.GetAssets()
	if err != nil {
		// First ever publish, or an index from before this file existed.
		// Diffing against nothing makes every asset "new", which is the
		// honest answer for a dataset that never had a predecessor.
		previousAssets = nil
	}

	if err := publishCrawl(result.assets, previousAssets); err != nil {
		return err
	}

	recordCrawlCompletion(result, previousAssets, c)

	return nil
}

// crawlResult is what crawlAndCollect produces: the deduplicated assets a
// crawl gathered, how much of the catalogue they cover, and the bookkeeping
// (crawler included) later stages need to gate on, index, and record.
type crawlResult struct {
	assets         []models.Asset
	coverage       float64
	totalAssets    int64
	tagsDiscovered int64
	slicesPlanned  int64
	crawler        *crawler
}

// crawlAndCollect runs one full crawl against itch.io: it sizes the
// catalogue, discovers (or reuses) the tag universe, plans the slices needed
// to cover it, walks every slice, and attaches the tags and recency ranks
// gathered along the way. It is the first stage of fetchAndStoreAssets
// because every later stage - the coverage gate, the index build, the
// publish - depends on the asset list and coverage figure only a completed
// crawl can produce; nothing downstream can run first.
func crawlAndCollect(cfg crawlConfig) (crawlResult, error) {
	totalAssets, err := fetcher.GetAssetCount()
	if err != nil {
		logging.Error("Failed to get asset count: %v", err)
		return crawlResult{}, fmt.Errorf("fetcher.GetAssetCount: %w", err)
	}

	// The root view's first page also tells us how many items a page holds,
	// which sets the ceiling on what any single view can reach.
	root := fetcher.Slice{}
	firstPage, outcome := fetcher.FetchAssetPage(root, 1)
	if outcome != fetcher.FetchOK {
		logging.Error("Failed to fetch first page, terminating.")
		return crawlResult{}, fmt.Errorf("fetching first page: outcome %v", outcome)
	}
	itemsPerPage := firstPage.NumItems
	if itemsPerPage <= 0 {
		logging.Error("First page reported %d items per page, terminating.", itemsPerPage)
		return crawlResult{}, fmt.Errorf("first page reported %d items per page", itemsPerPage)
	}

	tags, err := loadOrDiscoverTags(cfg)
	if err != nil {
		logging.Error("Failed to obtain tag universe: %v", err)
		return crawlResult{}, fmt.Errorf("loadOrDiscoverTags: %w", err)
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
		recency:      make(map[string]int64),
		doneSlices:   make(map[string]struct{}, len(slices)),
		runStart:     time.Now(),
	}
	// Published for the shutdown handler in main to find, and cleared by
	// fetchAndStoreAssets on every exit path via defer - see activeCrawler's
	// own doc comment.
	activeCrawler.Store(c)
	c.resume(cfg.checkpointMaxAge)
	// Computed after resume, not at PlanSlices: on a resumed run doneSlices is
	// now populated, and pages already accounted for by the checkpoint must
	// not inflate the ETA the way they would inflate a plain slice count.
	c.plannedPages = plannedPages(slices, c.doneSlices, itemsPerPage)
	c.run(slices)
	c.attachTags()
	c.attachRecency()

	assets := c.assets
	coverage := c.coverage()
	logging.Info("Successfully fetched %d assets (%.1f%% of %d) using %d pages across %d slices, 429s: %d",
		len(assets), coverage*100, totalAssets, c.pagesFetched.Load(), c.slicesDone.Load(), fetcher.Refusals())

	return crawlResult{
		assets:         assets,
		coverage:       coverage,
		totalAssets:    totalAssets,
		tagsDiscovered: int64(len(tags)),
		slicesPlanned:  int64(len(slices)),
		crawler:        c,
	}, nil
}

// checkCoverageFloor reports whether coverage clears floor, and if not, the
// error fetchAndStoreAssets returns instead of publishing. It is pure -
// nothing about it depends on a network or a real crawl - so the boundary
// itself (below floor errors and leaves the previous index untouched; at or
// above floor proceeds) can be tested directly.
func checkCoverageFloor(coverage, floor float64) error {
	if coverage < floor {
		return fmt.Errorf("coverage %.1f%% below COVERAGE_FLOOR %.1f%%", coverage*100, floor*100)
	}
	return nil
}

// publishCrawl builds the search index from assets and publishes it, then
// writes assets.json - in that order, and only that order. The webserver
// decides a new dataset is ready by watching assets.json's timestamp, so
// publishing the index first guarantees that by the time a webserver notices
// a fresh asset list, the index it will open already matches it; reversed,
// a webserver could open a fresh asset list against a stale or missing
// index.
//
// previousAssets is read by the caller before this runs (see
// fetchAndStoreAssets), since stampFirstSeen must see the prior publish's
// FirstSeen values before NewIndexedAsset - inside buildIndex - converts
// this run's assets into index documents.
func publishCrawl(assets, previousAssets []models.Asset) error {
	stampFirstSeen(previousAssets, assets, time.Now().UTC())

	// Rates are refreshed here, at the end of a crawl, because that is the only
	// moment the index is rebuilt - and PriceUSD is baked into the documents,
	// so the snapshot the index was built with is the snapshot it must be
	// queried with. Fetching them on a separate schedule would leave the stored
	// dollar values and the published rates describing different days.
	logging.Info("Creating index...")
	rates := currentRates()
	// buildIndex is shared with rebuildIndexFromPublishedData (selfheal.go),
	// which needs the exact same document construction and batching for a
	// rebuild that never recrawls to still produce an indistinguishable index.
	if _, err := buildIndex(assets, rates); err != nil {
		logging.Error("Failed to build index: %v", err)
		return fmt.Errorf("buildIndex: %w", err)
	}

	logging.Info("Publishing index to %s", storage.IndexPath())
	if err := storage.PublishIndex(); err != nil {
		logging.Error("Failed to publish index: %v", err)
		return fmt.Errorf("storage.PublishIndex: %w", err)
	}
	logging.Info("Successfully published index")

	logging.Info("Storing assets")
	if err := storage.PutAssets(assets); err != nil {
		logging.Error("Failed to put assets: %v", err)
		return fmt.Errorf("storage.PutAssets: %w", err)
	}
	logging.Info("Successfully stored assets")

	return nil
}

// recordCrawlCompletion writes the crawl's bookkeeping - coverage stats,
// history, and the now-stale checkpoint's removal - after publishCrawl has
// already made the new index and asset list live. It runs last, and every
// write in it is best-effort: a publish that already succeeded must not be
// reported as failed just because one of these follow-up writes was, so
// failures here are logged and swallowed rather than returned.
func recordCrawlCompletion(result crawlResult, previousAssets []models.Asset, c *crawler) {
	assets := result.assets

	// Written only on a run that published, so the figure the site quotes
	// always describes the dataset actually being served.
	if err := storage.PutStats(models.Stats{
		Indexed:   int64(len(assets)),
		Catalogue: result.totalAssets,
	}); err != nil {
		logging.Error("Failed to store crawl stats, coverage will not be shown: %v", err)
	}

	publishedAt := time.Now().UTC()
	newCount, removedCount := diffAssetIDs(previousAssets, assets)
	var freeCount int64
	for _, a := range assets {
		if a.Free() {
			freeCount++
		}
	}
	if err := storage.PutHistory(models.CrawlRecord{
		When:            publishedAt,
		DurationSeconds: int64(publishedAt.Sub(c.runStart).Seconds()),
		Indexed:         int64(len(assets)),
		Catalogue:       result.totalAssets,
		NewAssets:       newCount,
		RemovedAssets:   removedCount,
		FreeAssets:      freeCount,
		Pages:           c.pagesFetched.Load(),
		SlicesDone:      c.slicesDone.Load(),
		SlicesPlanned:   result.slicesPlanned,
		Refusals:        fetcher.Refusals(),
		Tags:            result.tagsDiscovered,
		Resumed:         c.resumed,
	}); err != nil {
		logging.Warning("Failed to record crawl history: %v", err)
	}

	// The crawl completed and published, so the checkpoint is now stale. Left
	// behind, the next run would resume a finished crawl and skip every slice.
	if err := storage.DeleteCheckpoint(); err != nil {
		logging.Warning("Failed to delete crawl checkpoint: %v", err)
	}
}

// stampFirstSeen carries FirstSeen forward from the previous publish, id by
// id, and marks genuine newcomers with publishedAt.
//
// An id present in both publishes keeps whatever FirstSeen it already had,
// including zero: old stock that predates this feature, or that arrived
// before the previous publish ever recorded one, must never be relabelled as
// new just because this happens to be the run that looked. Only an id absent
// from the previous publish is a newcomer, so only those get stamped.
func stampFirstSeen(previous, current []models.Asset, publishedAt time.Time) {
	prevFirstSeen := make(map[string]time.Time, len(previous))
	for _, a := range previous {
		prevFirstSeen[a.GameId] = a.FirstSeen
	}
	for i := range current {
		if fs, ok := prevFirstSeen[current[i].GameId]; ok {
			current[i].FirstSeen = fs
			continue
		}
		current[i].FirstSeen = publishedAt
	}
}

// diffAssetIDs compares two publishes by game id, since that is the only
// stable identity an asset carries across crawls - titles and descriptions
// can change without the asset itself being new or gone.
func diffAssetIDs(previous, current []models.Asset) (newCount, removedCount int64) {
	prevIDs := make(map[string]struct{}, len(previous))
	for _, a := range previous {
		prevIDs[a.GameId] = struct{}{}
	}
	curIDs := make(map[string]struct{}, len(current))
	for _, a := range current {
		curIDs[a.GameId] = struct{}{}
	}
	for id := range curIDs {
		if _, ok := prevIDs[id]; !ok {
			newCount++
		}
	}
	for id := range prevIDs {
		if _, ok := curIDs[id]; !ok {
			removedCount++
		}
	}
	return newCount, removedCount
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
		coverageTarget: config.EnvNonNegativeFloat("COVERAGE_TARGET", 0.95),
		// Below what a full crawl actually reaches (~89%), or the run that
		// proves the crawler works is the run that refuses to publish. Above
		// what a stalled or truncated one produces, so a bad run still cannot
		// replace a good index.
		coverageFloor:    config.EnvNonNegativeFloat("COVERAGE_FLOOR", 0.75),
		minYield:         config.EnvNonNegativeFloat("SLICE_MIN_YIELD", 0.05),
		tagCacheMaxAge:   config.EnvDuration("TAG_CACHE_MAX_AGE", 168*time.Hour),
		checkpointMaxAge: config.EnvDuration("CHECKPOINT_MAX_AGE", 24*time.Hour),
		maxTags:          int(config.EnvInt("MAX_TAGS", fetcher.DefaultMaxTags)),
	}

	// SCRAPE_MAX_PAGES caps how many pages are fetched in total, across every
	// slice. A full crawl is thousands of pages and takes about six hours at
	// a polite request rate, which makes it a painful way to smoke-test the
	// rest of the pipeline (indexing, archiving, upload, webserver startup).
	// Setting this small exercises every one of those stages in a couple of
	// minutes. Leave it unset in production.
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

	// recency is the page an asset appeared on in the root newest view, keyed
	// by GameId. Collected exactly like tagSets and for the same reason: the
	// crawl already fetches those pages as one of four orderings, and their
	// sequence was previously thrown away.
	//
	// Only the root newest view contributes. Within a tag's newest view a page
	// number means "newest of the 400 assets tagged fonts", which is not
	// comparable to any other slice's - the same trap InvPopularity avoids by
	// only trusting the root view.
	recency map[string]int64

	// maxRootRank is how deep the root view was crawled. Root page numbers are
	// a true global popularity rank; page numbers within any other slice are
	// not comparable to them, so slice-only assets are ranked after this.
	maxRootRank int64

	// doneSlices are slice labels already finished, whether in this run or in
	// the checkpointed run being resumed.
	doneSlices map[string]struct{}

	// resumed is true once resume actually restores a checkpoint, as opposed
	// to finding none, a stale one, or one for a different catalogue size.
	// Recorded on CrawlRecord because it explains a low NewAssets/RemovedAssets
	// count that would otherwise look like the diff logic was broken.
	resumed bool

	lastCheckpoint time.Time

	// runStart is when this process instance began crawling - not when a
	// resumed checkpoint's original run began. It is the numerator's clock for
	// the ETA (see etaLabel) and the DurationSeconds recorded on CrawlRecord;
	// both describe what this run has actually spent, not work inherited from
	// an earlier process.
	runStart time.Time

	// plannedPages is the page budget this run expects to spend: the planned
	// slices' PagesToFetch, minus whatever a resumed checkpoint already marked
	// done. It only ever overestimates, because SLICE_MIN_YIELD abandons slices
	// before they reach it - and an overestimate that shrinks as the crawl
	// progresses is the safe direction for an ETA to be wrong in.
	plannedPages int64

	pagesFetched atomic.Int64
	slicesDone   atomic.Int64
}

// plannedPages sums PagesToFetch across the slices this run will actually
// crawl. Asset coverage is deliberately not the basis for an ETA anywhere in
// this file: coverage saturates near coverageTarget by design (see done), so
// an estimate built on it would stall well short of finishing. Pages are what
// the rate limiter actually paces, so they are what remaining time is
// measured against.
func plannedPages(slices []fetcher.Slice, done map[string]struct{}, itemsPerPage int64) int64 {
	var total int64
	for _, s := range slices {
		if !s.Valid() {
			continue
		}
		if _, ok := done[s.Label()]; ok {
			continue
		}
		total += s.PagesToFetch(itemsPerPage)
	}
	return total
}

// checkpointInterval is the minimum wall-clock gap between checkpoint writes.
// A checkpoint serialises every asset collected so far (~26 MB at full
// coverage), so writing one per slice would spend more time uploading than
// crawling; writing one per hour would make the feature pointless.
//
// At 3 minutes, a ~6 hour crawl wrote roughly 120 checkpoints: 3-4 GB of
// fsync'd full rewrites, each one taken with the crawler mutex held. 10
// minutes cuts that to ~36 checkpoints - about a third of the write volume -
// while only widening the worst case (everything since the last checkpoint,
// lost on a kill) from 3 minutes of re-crawling to 10.
const checkpointInterval = 10 * time.Minute

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
	// Restored rather than recomputed: the newest root view may well be among
	// the slices already marked done, in which case nothing will visit it again
	// and every rank it produced would otherwise be lost.
	for id, rank := range cp.Recency {
		c.recency[id] = rank
	}
	c.resumed = true

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

	// Copied rather than shared: the crawl keeps writing to c.recency while the
	// checkpoint is being serialised outside the lock, and marshalling a map
	// under concurrent write is a panic, not a race detector's opinion.
	recency := make(map[string]int64, len(c.recency))
	for id, rank := range c.recency {
		recency[id] = rank
	}

	cp := storage.Checkpoint{
		Assets:      assets,
		DoneSlices:  make([]string, 0, len(c.doneSlices)),
		MaxRootRank: c.maxRootRank,
		TotalAssets: c.totalAssets,
		Recency:     recency,
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
func (c *crawler) record(assets []models.Asset, sliceTags []string, recencyPage int64) int {
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

		// Outside the new-asset branch for the same reason tags are: an asset
		// first collected from tag-fonts still learns its recency when the
		// newest view reaches it. Lowest page wins, so a re-crawl that sees it
		// earlier moves it forward rather than leaving the first sighting to
		// stand.
		if recencyPage > 0 {
			if prev, ok := c.recency[a.GameId]; !ok || recencyPage < prev {
				c.recency[a.GameId] = recencyPage
			}
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

// attachRecency stamps the newest-view ranks onto the collected assets, for the
// same reason and at the same point as attachTags.
//
// The count it logs is worth watching. It cannot exceed what itch.io's 200-page
// ceiling exposes - about 7,200 assets - so a much smaller number means the
// newest root view was abandoned early or never scheduled, and the recency
// ordering is correspondingly partial.
func (c *crawler) attachRecency() {
	c.mu.Lock()
	defer c.mu.Unlock()

	ranked := 0
	for i := range c.assets {
		if rank, ok := c.recency[c.assets[i].GameId]; ok {
			c.assets[i].InvRecency = rank
			ranked++
		}
	}
	logging.Info("Attached recency ranks to %d/%d assets", ranked, len(c.assets))
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
			// Zero for every other slice, which record reads as "this view
			// says nothing about recency".
			recencyPage := int64(0)
			if s.IsNewestRoot() {
				recencyPage = pageNum
			}
			perPage[i] = c.record(assets, s.Tags, recencyPage)
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
			logging.Info("assets: %d/%d (%.1f%%), slices: %d/%d, pages: %d, 429s: %d, %s",
				found, c.totalAssets, pct, c.slicesDone.Load(), totalSlices,
				c.pagesFetched.Load(), fetcher.Refusals(), c.etaLabel())
		}
	}
}

// etaLabel is the fuzzy time-to-completion appended to the progress line.
//
// Pages, not the asset count above, are the basis: a page costs the same rate
// limiter delay whichever slice it comes from, so remaining time is remaining
// pages times observed pace. Observed pace already absorbs 429 pauses, since
// they show up as elapsed wall-clock time without a matching page fetched.
func (c *crawler) etaLabel() string {
	pagesFetched := c.pagesFetched.Load()
	if pagesFetched == 0 {
		return "estimating"
	}

	pace := time.Since(c.runStart) / time.Duration(pagesFetched)

	remaining := c.plannedPages - pagesFetched
	if remaining < 0 {
		// plannedPages is a ceiling that only ever overestimates (slices
		// abandoned early via SLICE_MIN_YIELD never reach it) - never a reason
		// to report a negative time left.
		remaining = 0
	}

	return fuzzyETA(pace * time.Duration(remaining))
}
