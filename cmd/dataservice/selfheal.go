package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"itchgrep/internal/logging"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/blevesearch/bleve/v2"
)

// selfHealIndexFormat is run once at startup, before scheduleCrawls starts
// its own loop. It exists for one deploy in particular: the move from bleve
// v1 (upsidedown/boltdb) to v2 (scorch) leaves production with an index on
// disk that this binary's bleve.Open cannot read at all - v2 registers no
// upsidedown reader - while assets.json is still fresh. Left alone,
// scheduleCrawls would see a recent publish and wait up to CRAWL_INTERVAL
// (as long as a week) before the next crawl fixed it, and the webserver would
// 503 the entire time. This closes that gap by rebuilding the index from
// what is already published, in minutes, without waiting for or triggering a
// fresh crawl.
//
// It doubles as corruption recovery: bleve.Open fails the same way for a
// half-written index directory (killed mid-publish, a disk fault) as it does
// for a wrong-format one, and the fix is identical either way - rebuild from
// the last known-good assets.json.
func selfHealIndexFormat() error {
	if _, err := storage.GetAssetsUpdateTime(); err != nil {
		// Nothing has ever published on this volume. That is scheduleCrawls'
		// job - it starts the first crawl unconditionally - and there is no
		// assets.json here to rebuild an index from even if we wanted to.
		return nil
	}

	needsRebuild, reason := indexNeedsRebuild(storage.IndexPath())
	if !needsRebuild {
		return nil
	}

	logging.Info("Published index is unusable (%s); rebuilding as scorch from assets.json - this takes minutes, the webserver keeps serving the old data meanwhile", reason)
	if err := rebuildIndexFromPublishedData(); err != nil {
		return fmt.Errorf("rebuildIndexFromPublishedData: %w", err)
	}
	logging.Info("Self-heal rebuild complete")
	return nil
}

// indexNeedsRebuild reports whether the index at dir is something other than
// an openable scorch index, and why. It is the pure decision selfHealIndexFormat
// acts on, pulled out so it can be tested against a handful of directory
// fixtures without running bleve.New over real data.
//
// bleve.Open is tried first because it is authoritative - the same open the
// webserver performs - and already covers every failure this needs to catch:
// a missing directory, a v1 upsidedown/boltdb index (no reader registered in
// this binary at all), or a corrupted one. The index_meta.json check below it
// is defence in depth for the one case a successful Open cannot rule out on
// its own: a future index type this binary's registry does understand, but
// which is not the scorch this mapping and query layer are built for.
func indexNeedsRebuild(dir string) (needsRebuild bool, reason string) {
	idx, err := bleve.Open(dir)
	if err != nil {
		return true, fmt.Sprintf("bleve.Open %s: %v", dir, err)
	}
	defer idx.Close()

	indexType, err := readIndexMetaType(dir)
	if err != nil {
		return true, fmt.Sprintf("reading %s/index_meta.json: %v", dir, err)
	}
	if indexType != "scorch" {
		return true, fmt.Sprintf("index_meta.json reports index_type=%q, not scorch", indexType)
	}
	return false, ""
}

// readIndexMetaType reads the "index_type" bleve records in index_meta.json
// at the root of an index directory. Read directly off disk rather than
// through an opened Index, because bleve.Index exposes no accessor for its
// own on-disk format - the metadata file is the only place that says so.
func readIndexMetaType(dir string) (string, error) {
	data, err := os.ReadFile(dir + "/index_meta.json")
	if err != nil {
		return "", err
	}
	// boltdb preallocates the file and bleve writes JSON into the front of
	// it, leaving the remainder NUL-padded; json.Unmarshal rejects that
	// trailing padding as trailing garbage if it is not trimmed first.
	data = bytes.TrimRight(data, "\x00")

	var meta struct {
		IndexType string `json:"index_type"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	return meta.IndexType, nil
}

// rebuildIndexFromPublishedData rebuilds the search index from the assets and
// exchange rates the last successful crawl already published, without
// recrawling itch.io. Every input buildIndex needs - the asset list and the
// rate snapshot PriceUSD was converted with - is already on disk; nothing
// this function does depends on state that lives only inside a running
// crawl, which is what makes rebuilding in minutes possible instead of
// needing a fresh six-hour run.
func rebuildIndexFromPublishedData() error {
	assets, err := storage.GetAssets()
	if err != nil {
		return fmt.Errorf("storage.GetAssets: %w", err)
	}

	// A missing rates file only happens on an index published before rates
	// were collected at all; falling back keeps the rebuild from failing over
	// a snapshot that is merely stale rather than absent - see doRefresh in
	// internal/cache, which makes the same trade for the same reason.
	rates, err := storage.GetRates()
	if err != nil {
		logging.Info("No stored exchange rates (%v); using the built-in snapshot from %s", err, money.Fallback().Date)
		rates = money.Fallback()
	}

	indexed, err := buildIndex(assets, rates)
	if err != nil {
		return fmt.Errorf("buildIndex: %w", err)
	}

	logging.Info("Publishing rebuilt index to %s", storage.IndexPath())
	if err := storage.PublishIndex(); err != nil {
		return fmt.Errorf("storage.PublishIndex: %w", err)
	}
	// assets.json is not rewritten: it is exactly what was just read back out
	// and reindexed, so touching it again would only cost a write for no
	// change in content.
	logging.Info("Rebuilt and published a scorch index for %d assets from existing data", indexed)
	return nil
}

// buildIndex builds a fresh index at storage.StagingIndexPath from assets and
// rates and returns how many documents it wrote. Shared by
// fetchAndStoreAssets (assets fresh off a crawl) and
// rebuildIndexFromPublishedData (assets already on disk) - the two disagree
// about where the assets and rates come from and what to do once the index
// is built (a crawl also writes assets.json, stats and history; a rebuild
// does not), but never about how a document is derived or batched, so that
// part exists exactly once.
//
// The caller owns storage.PublishIndex(): building and publishing are kept
// separate so a caller that wants to inspect or discard a build without
// publishing it - a test, primarily - can.
// indexBatchSize is how many documents buildIndex accumulates in one
// bleve.Batch before flushing it to disk.
const indexBatchSize = 1500

func buildIndex(assets []models.Asset, rates money.Rates) (count int, err error) {
	// Built at the staging path, which sits beside the published index so
	// that publishing is a rename rather than a copy. A previous run killed
	// between creating this and publishing it would leave the directory
	// behind, and bleve.New refuses to open a path that already exists.
	staging := storage.StagingIndexPath()
	if err := os.RemoveAll(staging); err != nil {
		return 0, fmt.Errorf("clearing stale staging index at %s: %w", staging, err)
	}
	// bleve.New defaults to the scorch backend (index_meta.json records
	// "index_type":"scorch") since bleve v2; that default is relied on here
	// rather than pinned explicitly, because scorch is also the only backend
	// v2 ships a writer for. The legacy upsidedown/boltdb format this
	// replaced stored three copies of every field nobody read - see
	// internal/storage's IndexMapping for what changed and why.
	newIndex, err := bleve.New(staging, storage.IndexMapping())
	// On any failure below, the half-built index must not be left where the
	// next run would trip over it - but on success it has to survive this
	// function returning, for the caller to publish. Named return plus a
	// defer that checks it (rather than an unconditional one, which the
	// staging directory this function built would not survive) is what makes
	// that distinction; PublishIndex renames the directory away on success,
	// so a defer that fired unconditionally would be racing it to delete the
	// very thing about to be published.
	defer func() {
		if err != nil {
			os.RemoveAll(staging)
		}
	}()
	if err != nil {
		return 0, fmt.Errorf("bleve.New: %w", err)
	}
	logging.Info("Created new empty index at %s", staging)

	// convert the assets to IndexedAssets, which are smaller and used for indexing
	unpriced := 0
	smolAssets := make([]models.IndexedAsset, len(assets))
	for i, asset := range assets {
		priceUSD := 0.0
		if !asset.Free() {
			m, ok := money.Parse(asset.Price)
			if ok {
				priceUSD, ok = rates.USD(m)
			}
			if !ok {
				// Not zero: an unreadable price must not look like a free asset
				// to a ceiling filter or a cheapest-first sort.
				priceUSD = models.UnknownPrice
				unpriced++
			}
		}
		smolAssets[i] = models.NewIndexedAsset(asset, priceUSD)
	}
	if unpriced > 0 {
		// Worth logging rather than swallowing: a jump here means itch.io is
		// showing a currency format money.Parse does not know, and those assets
		// are silently absent from every price filter until it does.
		logging.Warning("Could not price %d/%d assets in dollars; they will not match price filters", unpriced, len(assets))
	}

	// indexing the assets in batches
	//
	// Every write is checked, including the batch flushes and the close. They
	// are what actually put documents on disk, so a failure ignored here does
	// not produce a visible error - it produces a smaller index that publishes
	// successfully and replaces a complete one, and the only symptom is assets
	// that stop being findable.
	b := newIndex.NewBatch()
	assetsIndexed := 0
	for i, asset := range smolAssets {
		if err := b.Index(asset.GameId, asset); err != nil {
			newIndex.Close() // clean up the failed new index
			return 0, fmt.Errorf("indexing asset %s: %w", asset.GameId, err)
		}
		assetsIndexed++
		// Flushed in batches rather than one bleve.Batch per document (too
		// many small writes to be efficient) or one for the whole run (holds
		// every document in memory at once). indexBatchSize is a round
		// number chosen as a middle ground between the two, not a tuned one.
		if i%indexBatchSize == 0 && i != 0 {
			logging.Info("Batching assets: %d/%d", i, len(smolAssets))
			if err := newIndex.Batch(b); err != nil {
				newIndex.Close()
				return 0, fmt.Errorf("writing batch at asset %d: %w", i, err)
			}
			b.Reset()
		}
	}
	if err := newIndex.Batch(b); err != nil { // the remainder
		newIndex.Close()
		return 0, fmt.Errorf("writing final batch: %w", err)
	}
	if err := newIndex.Close(); err != nil {
		return 0, fmt.Errorf("closing new index: %w", err)
	}
	logging.Info("Successfully indexed %d assets", assetsIndexed)
	return assetsIndexed, nil
}
