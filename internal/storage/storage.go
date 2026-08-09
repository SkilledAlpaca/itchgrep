// Package storage persists everything the crawl produces: the asset list, the
// tag universe, an in-progress crawl checkpoint, and the bleve search index.
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
package storage

import (
	"encoding/json"
	"fmt"
	"itchgrep/pkg/models"
	"os"
	"path/filepath"
	"time"

	"itchgrep/internal/logging"
)

const (
	DataFileName       = "assets.json"
	TagsFileName       = "tags.json"
	CheckpointFileName = "checkpoint.json"

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

// IndexPath is the published search index. The webserver opens this directly;
// there is no download step.
func IndexPath() string { return pathTo(IndexDirName) }

// StagingIndexPath is where the dataservice builds a new index. It is
// deliberately inside Dir() rather than a temp dir: PublishIndex renames it
// into place, and rename only works within one filesystem.
func StagingIndexPath() string { return pathTo(stagingIndexDirName) }

// writeAtomic writes data to name in Dir() such that a concurrent reader sees
// either the old contents or the new ones, never a partial write.
func writeAtomic(name string, data []byte) error {
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
	return nil
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

// PutAssets writes the asset list. The dataservice writes this last, after the
// index is published, so its timestamp is what the webserver watches to decide
// a new dataset is ready.
func PutAssets(assets []models.Asset) error { return writeJSON(DataFileName, assets) }

func GetAssets() ([]models.Asset, error) {
	var assets []models.Asset
	if _, err := readJSON(DataFileName, &assets); err != nil {
		return nil, err
	}
	return assets, nil
}

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

// Checkpoint is a partially-complete crawl, written periodically so that a run
// killed part way through is resumable instead of worthless. A full crawl is
// ~75 minutes and lives entirely in memory until it publishes.
type Checkpoint struct {
	Assets      []models.Asset
	DoneSlices  []string // Slice.Label() values already finished
	MaxRootRank int64    // so slice-only assets keep ranking behind root-ranked ones
	TotalAssets int64    // catalogue size when the checkpoint was taken
}

func PutCheckpoint(cp Checkpoint) error { return writeJSON(CheckpointFileName, cp) }

// GetCheckpoint returns the stored checkpoint and when it was written. A
// missing checkpoint is the normal case - it just means the last crawl
// finished cleanly - so callers treat the error as "start fresh".
func GetCheckpoint() (Checkpoint, time.Time, error) {
	var cp Checkpoint
	updated, err := readJSON(CheckpointFileName, &cp)
	if err != nil {
		return Checkpoint{}, time.Time{}, err
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
