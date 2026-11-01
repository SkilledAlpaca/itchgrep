// Package storage persists everything the crawl produces: the asset list, the
// tag universe, an in-progress crawl checkpoint, the bleve search index, and
// the small JSON files for exchange rates, crawl stats, traffic, and history.
//
// It is a plain directory on disk, shared between the dataservice and the
// webserver. This used to be Google Cloud Storage (with fake-gcs-server
// standing in locally), which bought nothing the workload actually needs: no
// ACLs, no signed URLs, no lifecycle rules, no listing, and only ever one
// writer. What it cost was a container, a network hop, and a tar+gzip
// round-trip on every publish purely to move the index across a boundary that
// does not exist when both processes share a volume.
//
// The one property GCS did provide is that a reader never observes a
// half-written object. That is preserved here by writing to a temporary file in
// the same directory and renaming it into place: rename is atomic within a
// filesystem, so a reader sees either the whole previous file or the whole new
// one.
//
// That reader-visible atomicity is a different guarantee from crash
// durability. The rename makes the new name appear all-or-nothing to a
// concurrent reader the instant it happens, in memory; whether the rename
// itself has reached disk yet is a separate question, answered only by
// fsyncing the directory it happened in. writeAtomic does both: the reader
// guarantee always holds, and the durability guarantee holds unless the
// platform cannot sync a directory at all, which is logged rather than
// treated as a write failure.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"itchgrep/pkg/models"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"itchgrep/internal/logging"
	"itchgrep/pkg/money"
)

// File names for the objects this package persists, each written with
// writeAtomic and living directly under Dir().
const (
	DataFileName       = "assets.json"
	TagsFileName       = "tags.json"
	CheckpointFileName = "checkpoint.json"
	RatesFileName      = "rates.json"
	StatsFileName      = "stats.json"
	// TrafficFileName is deliberately not "stats.json" - that name is already
	// the crawl-coverage file, and the two mean entirely different things.
	TrafficFileName = "traffic.json"
	HistoryFileName = "history.json"
	// FirstSeenFileName is the persistent id -> first-seen map; see
	// FirstSeenMap.
	FirstSeenFileName = "firstseen.json"

	// IndexDirName is the published bleve index. stagingIndexDirName is where a
	// new one is built; it lives alongside so that publishing is a rename
	// rather than a copy.
	IndexDirName        = "index.bleve"
	stagingIndexDirName = "index.bleve.staging"
	previousIndexSuffix = ".previous"
)

// DefaultDir is used when DATA_DIR is unset. Relative, so a bare `go run`
// keeps its data in the working directory; the containers set DATA_DIR to the
// shared volume.
const DefaultDir = "data"

// Dir is the directory every object lives in.
func Dir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return d
	}
	return DefaultDir
}

func pathTo(name string) string { return filepath.Join(Dir(), name) }

// EnsureWritable verifies this process can actually write to Dir(), by
// creating and removing a small probe file. Meant to be called before any
// expensive work starts - a crawl that discovers an unwritable /data only at
// buildIndex, after fetching for six hours, has wasted the entire run on a
// fault that a one-file probe would have caught in milliseconds. See
// cmd/dataservice's fetchAndStoreAssets, which calls this first.
//
// The error names the uid this process is actually running as, since the
// most likely cause in production is exactly that: a bind-mounted DATA_PATH
// that was never chown'd to the nonroot uid both images run as (see
// docker-compose.yml's volumes: comments).
func EnsureWritable() error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("data directory %s not writable by this process (uid %d): os.MkdirAll: %w", dir, os.Getuid(), err)
	}
	probe, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("data directory %s not writable by this process (uid %d): %w", dir, os.Getuid(), err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("data directory %s not writable by this process (uid %d): closing probe file: %w", dir, os.Getuid(), err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("data directory %s not writable by this process (uid %d): removing probe file %s: %w", dir, os.Getuid(), name, err)
	}
	return nil
}

// IndexPath is the published search index. The webserver opens this directly;
// there is no download step.
func IndexPath() string { return pathTo(IndexDirName) }

// StagingIndexPath is where the dataservice builds a new index. It is
// deliberately inside Dir() rather than a temp dir: PublishIndex renames it
// into place, and rename only works within one filesystem.
func StagingIndexPath() string { return pathTo(stagingIndexDirName) }

// orphanTempFileAge is how old one of writeAtomic's own *.tmp-* files has to
// be before cleanupOrphanedTempFiles treats it as abandoned rather than a
// write genuinely still in flight.
const orphanTempFileAge = time.Hour

// cleanupOrphanedTempFilesOnce makes the sweep below run at most once per
// process - it exists to catch what a previous, differently-killed process
// left behind, not to re-scan Dir() on every single write.
var cleanupOrphanedTempFilesOnce sync.Once

// cleanupOrphanedTempFiles removes writeAtomic's own leftover "*.tmp-*"
// files from Dir(), the ones older than orphanTempFileAge. A process killed
// between os.CreateTemp and the os.Rename below leaves one behind
// permanently - nothing else in this codebase ever globs for that pattern -
// and on the crawl checkpoint in particular, at up to ~26 MB per write,
// those accumulate on the same filesystem the search index lives on. Age is
// what tells an orphan apart from a write genuinely in progress right now,
// whose rename has simply not landed yet.
//
// Best-effort throughout: a directory that cannot be listed or a file that
// cannot be removed is logged and skipped, never returned as an error - this
// is tidying up, not a precondition for the write that triggered it.
func cleanupOrphanedTempFiles() {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Nothing to sweep if the directory does not exist yet or cannot be
		// read - the write this is running ahead of will surface that on its
		// own, with a clearer error than this function could add.
		return
	}

	cutoff := time.Now().Add(-orphanTempFileAge)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), ".tmp-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			logging.Warning("Failed to remove orphaned temp file %s: %v", path, err)
			continue
		}
		logging.Info("Removed orphaned temp file %s (%s old, left behind by a process that never renamed it into place)",
			path, time.Since(info.ModTime()).Round(time.Minute))
		removed++
	}
	if removed > 0 {
		logging.Info("Cleaned up %d orphaned temp file(s) in %s", removed, dir)
	}
}

// writeAtomic writes data to name in Dir() such that a concurrent reader sees
// either the old contents or the new ones, never a partial write.
func writeAtomic(name string, data []byte) error {
	cleanupOrphanedTempFilesOnce.Do(cleanupOrphanedTempFiles)

	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("os.MkdirAll %s: %w", dir, err)
	}

	// Same directory as the target, so the rename below stays within one
	// filesystem. os.TempDir() would not guarantee that.
	tmp, err := os.CreateTemp(dir, name+".tmp-*")
	if err != nil {
		return fmt.Errorf("os.CreateTemp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("Write %s: %w", tmpName, err)
	}
	// Flush to disk before publishing the name. Without this a crash between
	// rename and writeback can leave a file that exists but is empty - which
	// reads back as valid JSON "null" rather than as an error.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("Sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("Close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("os.Rename: %w", err)
	}
	// The rename is now visible to concurrent readers - that guarantee never
	// depended on this. This closes the separate crash window: without it,
	// the rename can still be sitting in the directory's own write-back cache
	// when the machine loses power, and a reboot can find the old name back
	// in its place.
	syncDir(dir)
	return nil
}

// dirSyncUnsupportedOnce keeps syncDirFailed from logging on every single
// write - some platforms and filesystems cannot fsync a directory at all, and
// on those this would otherwise fire on every PutAssets, PutTraffic, etc.
var dirSyncUnsupportedOnce sync.Once

// syncDir fsyncs dir so that a rename already completed inside it is durable
// across a crash, not just visible to concurrent readers.
//
// Best-effort: a platform or filesystem that cannot sync a directory is
// logged once and otherwise ignored, never returned as a write failure. The
// concurrent-reader guarantee writeAtomic exists for does not depend on this
// succeeding; only the crash-durability guarantee does, and there is nothing
// a caller could do differently in response short of refusing to persist
// anything at all.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		logDirSyncUnsupported(err)
		return
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		logDirSyncUnsupported(err)
	}
}

func logDirSyncUnsupported(err error) {
	dirSyncUnsupportedOnce.Do(func() {
		logging.Warning("Directory sync unavailable (%v); writes remain atomic for concurrent readers but are not guaranteed durable across a crash", err)
	})
}

func writeJSON(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("json.Marshal: %w", err)
	}
	return writeAtomic(name, data)
}

// readJSON decodes name into v and reports when it was last written. A missing
// file surfaces as an error, which every caller treats as "nothing stored yet".
func readJSON(name string, v any) (time.Time, error) {
	p := pathTo(name)
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, fmt.Errorf("os.Stat %s: %w", p, err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, fmt.Errorf("os.ReadFile %s: %w", p, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return time.Time{}, fmt.Errorf("json.Unmarshal %s: %w", p, err)
	}
	return fi.ModTime(), nil
}

// renameCorruptAside moves a file that exists but failed to parse out of the
// way, tagged with the moment it was noticed, and logs loudly that it did -
// see GetHistory and GetTraffic, the two callers. Neither permanent failure
// (refusing every future write, which PutHistory used to do once
// history.json turned unreadable) nor silent zeroing (what a corrupt
// traffic.json used to do, with nothing in any log to explain why /stats
// reset to zero) is an acceptable response to data that is already lost -
// this preserves the evidence for a curious operator and lets the caller
// start fresh rather than staying wedged.
func renameCorruptAside(name string) {
	src := pathTo(name)
	dst := fmt.Sprintf("%s.corrupt-%d", src, time.Now().Unix())
	if err := os.Rename(src, dst); err != nil {
		logging.Error("%s exists but did not parse, and could not be moved aside to %s: %v - starting fresh anyway", src, dst, err)
		return
	}
	logging.Error("%s exists but did not parse; moved aside to %s for inspection and starting fresh", src, dst)
}

// PutAssets writes the asset list. The dataservice writes this last, after the
// index is published, so its timestamp is what the webserver watches to decide
// a new dataset is ready.
func PutAssets(assets []models.Asset) error { return writeJSON(DataFileName, assets) }

// GetAssets returns the published asset list. A missing file means nothing
// has ever published on this volume - see the callers in cmd/dataservice and
// internal/cache, which both treat that as "start from nothing" rather than
// as a fault.
func GetAssets() ([]models.Asset, error) {
	var assets []models.Asset
	if _, err := readJSON(DataFileName, &assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// GetAssetsUpdateTime is when the asset list was last written. Since
// PutAssets is always the last write of a publish (see PutAssets' own doc
// comment and publishCrawl in cmd/dataservice), this is the moment a
// complete, self-consistent dataset became available - which is what
// internal/cache watches to decide a refresh is due.
func GetAssetsUpdateTime() (time.Time, error) {
	fi, err := os.Stat(pathTo(DataFileName))
	if err != nil {
		return time.Time{}, fmt.Errorf("os.Stat: %w", err)
	}
	return fi.ModTime(), nil
}

// PutTags caches the discovered tag universe. Discovery costs several hundred
// requests to itch.io and the tag set barely moves between scrapes, so it is
// persisted rather than rebuilt every run.
func PutTags(tags []models.Tag) error { return writeJSON(TagsFileName, tags) }

// GetTags returns the cached tag universe and when it was written. A missing
// cache is not a failure the caller should abort on - it just means discovery
// has to run.
func GetTags() ([]models.Tag, time.Time, error) {
	var tags []models.Tag
	updated, err := readJSON(TagsFileName, &tags)
	if err != nil {
		return nil, time.Time{}, err
	}
	return tags, updated, nil
}

// PutRates stores the exchange-rate snapshot the crawl fetched, so the
// webserver converts prices with the same numbers on every restart rather than
// with whatever the baked-in fallback happens to say.
func PutRates(r money.Rates) error { return writeJSON(RatesFileName, r) }

// GetRates returns the stored snapshot. A missing file is not a failure: the
// caller falls back to the baked-in table, which is stale but dated and says so.
func GetRates() (money.Rates, error) {
	var r money.Rates
	if _, err := readJSON(RatesFileName, &r); err != nil {
		return money.Rates{}, err
	}
	if !r.Valid() {
		return money.Rates{}, errors.New("storage: stored rates carry no usable table")
	}
	return r, nil
}

// PutStats records how much of the catalog a crawl actually collected.
func PutStats(s models.Stats) error { return writeJSON(StatsFileName, s) }

// GetStats returns the completeness of the published dataset. A missing file is
// normal on an index published before these were recorded, and the caller shows
// no coverage at all rather than guessing at one.
func GetStats() (models.Stats, error) {
	var s models.Stats
	if _, err := readJSON(StatsFileName, &s); err != nil {
		return models.Stats{}, err
	}
	return s, nil
}

// PutTraffic persists the public traffic counters. Called on a timer from
// internal/metrics rather than on every request, since a rename-into-place
// write on every hit would put disk I/O in the request path for no benefit.
// A clean stop now also calls this directly, once, on the way out - see
// metrics.Counters.PersistNow and cmd/webserver/main.go's SIGTERM path - so
// a graceful shutdown loses nothing; only a hard kill (docker kill -9, an
// OOM) still loses up to the interval between two timer ticks.
func PutTraffic(t models.Traffic) error { return writeJSON(TrafficFileName, t) }

// GetTraffic returns the counters as of the last snapshot. A missing file is
// normal on first boot and on any deploy before this existed, and the caller
// (metrics.Restore) starts counting from zero rather than treating it as an
// error. A file that exists but fails to parse is moved aside - see
// renameCorruptAside - so that same zero-and-continue outcome is logged
// loudly instead of happening in total silence, which is what let a
// corrupted traffic.json reset the public /stats page to zero with nothing
// in any log to explain why.
func GetTraffic() (models.Traffic, error) {
	var t models.Traffic
	if _, err := readJSON(TrafficFileName, &t); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			renameCorruptAside(TrafficFileName)
		}
		return models.Traffic{}, err
	}
	return t, nil
}

// FirstSeenRecord is what is remembered about one asset id between crawls.
type FirstSeenRecord struct {
	// FirstSeen is when the id first appeared. The zero time means it predates
	// the feature, and must stay zero: old stock is never relabeled as new.
	FirstSeen time.Time `json:"firstSeen"`
	// Missed counts the consecutive published crawls that did not contain the
	// id. A crawl that misses an asset (a rate-limited page, a transient
	// error) must not make it look new when a later crawl sees it again, so an
	// id is forgotten only after it has been absent for several in a row.
	Missed int `json:"missed,omitempty"`
}

// FirstSeenMap is the id -> FirstSeenRecord map kept in firstseen.json. It
// outlives any one assets.json: the previous publish alone cannot tell an
// asset that was missed once from one that is genuinely new.
type FirstSeenMap map[string]FirstSeenRecord

// PutFirstSeen persists the map atomically, like every other data file.
func PutFirstSeen(m FirstSeenMap) error { return writeJSON(FirstSeenFileName, m) }

// GetFirstSeen returns the stored map. A missing file is normal (first run, or
// a volume from before the file existed) and is reported as an error wrapping
// os.ErrNotExist. A file that exists but does not parse is moved aside, as
// GetTraffic does, and reported as an ordinary error; the caller falls back to
// assets.json, whose FirstSeen values it also carries.
func GetFirstSeen() (FirstSeenMap, error) {
	var m FirstSeenMap
	if _, err := readJSON(FirstSeenFileName, &m); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			renameCorruptAside(FirstSeenFileName)
		}
		return nil, err
	}
	return m, nil
}

// maxHistoryEntries caps history.json at roughly two years of weekly crawls.
// Unbounded, the file would grow forever for a service meant to run for years
// - and nothing on /stats needs more than a couple of years of context.
const maxHistoryEntries = 104

// PutHistory appends one crawl's record to the published history, oldest
// first on disk, trimmed to the newest maxHistoryEntries. Read-modify-write
// rather than a single append: JSON has no append-in-place format, and the
// whole file is small enough that rewriting it is cheap even at the cap.
func PutHistory(rec models.CrawlRecord) error {
	history, err := GetHistory()
	if err != nil {
		return err
	}
	history = append(history, rec)
	if len(history) > maxHistoryEntries {
		history = history[len(history)-maxHistoryEntries:]
	}
	return writeJSON(HistoryFileName, history)
}

// GetHistory returns the published crawl history, oldest first. A missing
// file is normal on any deploy before this existed, or before the first crawl
// since - the caller shows no history at all rather than treating it as an
// error. A file that exists but fails to parse is moved aside - see
// renameCorruptAside - and also treated as empty, rather than returned as an
// error: PutHistory calls GetHistory before every write (see its own doc
// comment), so returning an error here used to mean a corrupted
// history.json permanently refused every future crawl's history entry too -
// the original corruption was already unrecoverable, and refusing to record
// anything new on top of it only compounded the loss.
func GetHistory() ([]models.CrawlRecord, error) {
	var history []models.CrawlRecord
	if _, err := readJSON(HistoryFileName, &history); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		renameCorruptAside(HistoryFileName)
		return nil, nil
	}
	return history, nil
}

// Checkpoint is a partially-complete crawl, written periodically so that a run
// killed part way through is resumable instead of worthless. A full crawl is
// ~6 hours and lives entirely in memory until it publishes.
type Checkpoint struct {
	Version     int // CheckpointVersion at the time of writing
	Assets      []models.Asset
	DoneSlices  []string // Slice.Label() values already finished
	MaxRootRank int64    // so slice-only assets keep ranking behind root-ranked ones
	TotalAssets int64    // catalog size when the checkpoint was taken

	// Recency is the newest-view rank per game id, held separately because it
	// is collected across slices and only stamped onto the assets once the
	// crawl finishes. Kept in the checkpoint rather than recomputed, since the
	// slice that produced it will be marked done and never revisited.
	Recency map[string]int64
}

// CheckpointVersion is the schema of the assets a checkpoint carries. Bump it
// whenever the crawl starts populating a new field on models.Asset.
//
// Without this, resuming across such a change is silently lossy in a way
// nothing downstream can detect: the assets restored from the checkpoint have
// the new field empty, and every slice that collected them is marked done, so
// they are never re-fetched. An empty field is indistinguishable from a
// legitimately empty value - Price being the case in point, where it would have
// meant "free" - so the resulting index would be confidently wrong.
//
// 2: added Asset.Price.
// 3: added Asset.PayWhatYouWant and Asset.InvRecency.
const CheckpointVersion = 3

// PutCheckpoint writes cp as the in-progress crawl's resumable state,
// stamping it with the current CheckpointVersion.
func PutCheckpoint(cp Checkpoint) error {
	// Stamped here rather than by the caller, which could forget.
	cp.Version = CheckpointVersion
	return writeJSON(CheckpointFileName, cp)
}

// GetCheckpoint returns the stored checkpoint and when it was written. A
// missing or outdated checkpoint is not a failure - it just means the crawl
// starts fresh - so callers treat the error as exactly that.
func GetCheckpoint() (Checkpoint, time.Time, error) {
	var cp Checkpoint
	updated, err := readJSON(CheckpointFileName, &cp)
	if err != nil {
		return Checkpoint{}, time.Time{}, err
	}
	if cp.Version != CheckpointVersion {
		return Checkpoint{}, time.Time{}, fmt.Errorf(
			"checkpoint is schema version %d, this build writes %d", cp.Version, CheckpointVersion)
	}
	return cp, updated, nil
}

// DeleteCheckpoint removes the checkpoint after a crawl publishes. A stale
// checkpoint left behind would make the next run resume a crawl that already
// completed, so failing to delete it is worth logging but not fatal.
func DeleteCheckpoint() error {
	if err := os.Remove(pathTo(CheckpointFileName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PublishIndex moves a freshly built index from StagingIndexPath into place.
//
// Three renames rather than one: os.Rename onto an existing non-empty directory
// fails with ENOTEMPTY, so the current index is moved aside first and only
// deleted once the new one is live. If the second rename fails the previous
// index is restored, because serving a stale index beats serving none.
//
// A webserver with the old index open is unaffected. Its file handles follow
// the inode, not the path, so the directory can be renamed and unlinked out
// from under it and it keeps serving until it reopens the new path.
func PublishIndex() error {
	staging := StagingIndexPath()
	if _, err := os.Stat(staging); err != nil {
		return fmt.Errorf("no staged index at %s: %w", staging, err)
	}

	published := IndexPath()
	previous := published + previousIndexSuffix

	// Recover a crash between the two renames below, from a previous
	// PublishIndex call: published moved to previous, but staging never made
	// it into published's place before the process died (OOM, host power
	// loss). Left alone, that state is indistinguishable from a corrupt or
	// missing index to anything that only inspects published -
	// selfHealIndexFormat's indexNeedsRebuild would see "unopenable" and
	// spend minutes rebuilding from scratch, with the site fully down the
	// whole time, when the complete previous index is sitting right there
	// under a different name. A single rename restores service immediately;
	// only after that does this function go on to publish what it was
	// actually called to publish.
	if _, err := os.Stat(published); os.IsNotExist(err) {
		if _, perr := os.Stat(previous); perr == nil {
			logging.Info("Recovering an interrupted publish: %s is missing but %s exists; restoring it before proceeding", published, previous)
			if rerr := os.Rename(previous, published); rerr != nil {
				return fmt.Errorf("recovering interrupted publish: os.Rename %s -> %s: %w", previous, published, rerr)
			}
		}
	}

	// A leftover from a run that died mid-publish would block the rename below.
	if err := os.RemoveAll(previous); err != nil {
		return fmt.Errorf("os.RemoveAll %s: %w", previous, err)
	}

	hadPrevious := false
	if _, err := os.Stat(published); err == nil {
		if err := os.Rename(published, previous); err != nil {
			return fmt.Errorf("os.Rename %s -> %s: %w", published, previous, err)
		}
		hadPrevious = true
	}

	if err := os.Rename(staging, published); err != nil {
		if hadPrevious {
			if rerr := os.Rename(previous, published); rerr != nil {
				logging.Error("Failed to restore the previous index after a failed publish: %v", rerr)
			}
		}
		return fmt.Errorf("os.Rename %s -> %s: %w", staging, published, err)
	}

	if hadPrevious {
		if err := os.RemoveAll(previous); err != nil {
			// The new index is already live, so this is untidy rather than wrong.
			logging.Warning("Failed to remove the previous index at %s: %v", previous, err)
		}
	}
	return nil
}
