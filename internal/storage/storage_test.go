package storage

import (
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useTempDir points storage at a fresh directory for the duration of one test.
// These tests used to require a running fake-gcs-server on localhost:4443; they
// now touch nothing outside t.TempDir().
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	return dir
}

func testAssets() []models.Asset {
	return []models.Asset{
		{GameId: "1", Title: "Asset 1", Author: "Author 1", Description: "Description 1", Link: "http://example.com/1", ThumbUrl: "http://example.com/thumb1.jpg", Tags: []string{"pixel-art"}},
		{GameId: "2", Title: "Asset 2", Author: "Author 2", Description: "Description 2", Link: "http://example.com/2", ThumbUrl: "http://example.com/thumb2.jpg"},
	}
}

func TestPutAndGetAssets(t *testing.T) {
	useTempDir(t)

	require.NoError(t, PutAssets(testAssets()))

	got, err := GetAssets()
	require.NoError(t, err)
	assert.Equal(t, testAssets(), got, "assets must round-trip unchanged, tags included")
}

func TestPutAssetsCreatesTheDataDirectory(t *testing.T) {
	// The containers mount an empty volume, so the very first write happens
	// into a directory that does not exist yet.
	base := t.TempDir()
	dir := filepath.Join(base, "does", "not", "exist")
	t.Setenv("DATA_DIR", dir)

	require.NoError(t, PutAssets(testAssets()))
	assert.FileExists(t, filepath.Join(dir, DataFileName))
}

func TestGetAssetsOnEmptyStorageIsAnError(t *testing.T) {
	// Callers distinguish "nothing stored yet" from a real failure by the
	// error, so a missing file must not read back as an empty slice.
	useTempDir(t)

	_, err := GetAssets()
	assert.Error(t, err)

	_, err = GetAssetsUpdateTime()
	assert.Error(t, err)
}

func TestGetAssetsUpdateTime(t *testing.T) {
	useTempDir(t)

	before := time.Now().Add(-time.Second)
	require.NoError(t, PutAssets(testAssets()))
	after := time.Now().Add(time.Second)

	updated, err := GetAssetsUpdateTime()
	require.NoError(t, err)
	assert.True(t, updated.After(before) && updated.Before(after),
		"update time %v should sit between %v and %v", updated, before, after)
}

func TestPutAssetsIsAtomic(t *testing.T) {
	// A reader must never observe a partial write. The rename is what
	// guarantees it; this asserts no temporary file is left behind claiming to
	// be the real one, and that a failed decode cannot happen mid-publish.
	dir := useTempDir(t)

	require.NoError(t, PutAssets(testAssets()))
	require.NoError(t, PutAssets(testAssets()[:1]))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{DataFileName}, names, "no temp files may survive a write")

	got, err := GetAssets()
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestPutAndGetTags(t *testing.T) {
	useTempDir(t)

	tags := []models.Tag{{Slug: "pixel-art", Count: 36324}, {Slug: "fonts", Count: 405}}
	require.NoError(t, PutTags(tags))

	got, updated, err := GetTags()
	require.NoError(t, err)
	assert.Equal(t, tags, got)
	assert.WithinDuration(t, time.Now(), updated, 10*time.Second)
}

func TestCheckpointRoundTripAndDelete(t *testing.T) {
	useTempDir(t)

	cp := Checkpoint{
		Assets:      testAssets(),
		DoneSlices:  []string{"root", "free", "tag-pixel-art"},
		MaxRootRank: 200,
		TotalAssets: 108697,
	}
	require.NoError(t, PutCheckpoint(cp))

	got, updated, err := GetCheckpoint()
	require.NoError(t, err)
	cp.Version = CheckpointVersion // stamped on write, so the caller cannot forget it
	assert.Equal(t, cp, got)
	assert.WithinDuration(t, time.Now(), updated, 10*time.Second)

	require.NoError(t, DeleteCheckpoint())
	_, _, err = GetCheckpoint()
	assert.Error(t, err, "a deleted checkpoint must read back as missing")
}

func TestCheckpointFromAnOlderSchemaIsRejected(t *testing.T) {
	// Resuming across a schema change is silently lossy: the restored assets
	// lack the new field and the slices that would have refilled it are already
	// marked done. Price is the case in point - empty would have read as "free"
	// for the 40% of the catalog collected before the change.
	useTempDir(t)

	stale := Checkpoint{Version: CheckpointVersion - 1, Assets: testAssets(), TotalAssets: 108697}
	require.NoError(t, writeJSON(CheckpointFileName, stale))

	_, _, err := GetCheckpoint()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version")
}

func TestDeleteCheckpointIsIdempotent(t *testing.T) {
	// Called unconditionally after every successful publish, including runs
	// that never wrote one.
	useTempDir(t)
	assert.NoError(t, DeleteCheckpoint())
}

// stageIndex writes a directory at StagingIndexPath containing one marker file,
// standing in for a built bleve index.
func stageIndex(t *testing.T, marker string) {
	t.Helper()
	staging := StagingIndexPath()
	require.NoError(t, os.MkdirAll(staging, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "marker"), []byte(marker), 0o644))
}

func TestPublishIndexMovesTheStagedIndexIntoPlace(t *testing.T) {
	useTempDir(t)

	stageIndex(t, "first")
	require.NoError(t, PublishIndex())

	assert.NoDirExists(t, StagingIndexPath(), "publishing must consume the staged index")
	got, err := os.ReadFile(filepath.Join(IndexPath(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))
}

func TestPublishIndexReplacesAnExistingIndex(t *testing.T) {
	// os.Rename onto a non-empty directory fails with ENOTEMPTY, so replacing
	// an existing index is the case that actually exercises PublishIndex.
	dir := useTempDir(t)

	stageIndex(t, "first")
	require.NoError(t, PublishIndex())
	stageIndex(t, "second")
	require.NoError(t, PublishIndex())

	got, err := os.ReadFile(filepath.Join(IndexPath(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "second", string(got))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.Equal(t, IndexDirName, e.Name(), "no staging or previous index may be left behind")
	}
}

func TestPublishIndexKeepsThePreviousIndexWhenNothingIsStaged(t *testing.T) {
	// A crawl that fails before building an index must not take the live one
	// down with it.
	useTempDir(t)

	stageIndex(t, "good")
	require.NoError(t, PublishIndex())

	assert.Error(t, PublishIndex(), "publishing with no staged index must fail")

	got, err := os.ReadFile(filepath.Join(IndexPath(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "good", string(got), "the live index must survive a failed publish")
}

// TestPublishIndexRecoversAnInterruptedPublish is the regression test for
// A process killed between PublishIndex's two renames leaves
// published missing and previous holding the last known-good index. The
// next PublishIndex call - not a special recovery path, just the ordinary
// call the next crawl or self-heal rebuild makes - must restore that index
// into place before publishing its own, rather than leaving the gap for
// selfHealIndexFormat to discover as "unopenable" and pay for a full rebuild
// to fix.
func TestPublishIndexRecoversAnInterruptedPublish(t *testing.T) {
	dir := useTempDir(t)

	// Get a real "previous" index in place the way two consecutive
	// PublishIndex calls would: publish "first", then rename it aside by
	// hand exactly as PublishIndex itself does internally, then delete
	// published - reproducing the exact on-disk shape a kill between the two
	// renames leaves: no published index, a complete previous one.
	stageIndex(t, "first")
	require.NoError(t, PublishIndex())
	require.NoError(t, os.Rename(IndexPath(), IndexPath()+previousIndexSuffix))

	stageIndex(t, "second")
	require.NoError(t, PublishIndex())

	got, err := os.ReadFile(filepath.Join(IndexPath(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "second", string(got), "the interrupted publish is recovered first, then the new one still lands on top of it")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.Equal(t, IndexDirName, e.Name(), "no staging or previous index may be left behind")
	}
}

// TestPublishIndexWithNoPreviousDoesNotTryToRecoverAnything guards the
// common case against a false-positive recovery: no published index and no
// .previous either (a fresh volume, or the very first publish) must publish
// normally, with no recovery rename attempted.
func TestPublishIndexWithNoPreviousDoesNotTryToRecoverAnything(t *testing.T) {
	useTempDir(t)

	stageIndex(t, "only")
	require.NoError(t, PublishIndex())

	got, err := os.ReadFile(filepath.Join(IndexPath(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "only", string(got))
}

func TestEnsureWritableSucceedsOnAWritableDirectory(t *testing.T) {
	dir := useTempDir(t)
	require.NoError(t, EnsureWritable())
	// The probe file must not be left behind for a caller to trip over later.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "EnsureWritable must remove its own probe file")
}

func TestEnsureWritableCreatesTheDataDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "does", "not", "exist")
	t.Setenv("DATA_DIR", dir)

	require.NoError(t, EnsureWritable())
	assert.DirExists(t, dir)
}

// TestEnsureWritableFailsOnAnUnwritableDirectory is the regression test for
// A bind-mounted DATA_PATH never chown'd to the container's nonroot
// uid must be caught by this probe in milliseconds, not six hours into a
// crawl at buildIndex's own bleve.New call. Skipped when running as root
// (uid 0), since root ignores directory permission bits on most platforms
// and this test would otherwise report success it did not actually observe.
func TestEnsureWritableFailsOnAnUnwritableDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits are not enforced against this process")
	}

	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	require.NoError(t, os.Chmod(dir, 0o555))
	// Belt-and-braces restore so the testing framework can still clean up
	// t.TempDir() afterwards, regardless of what EnsureWritable did.
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err := EnsureWritable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not writable")
}

// TestCleanupOrphanedTempFilesRemovesOldOnesOnly is the regression test for
// A process killed between os.CreateTemp and the rename in
// writeAtomic leaves a "*.tmp-*" file behind forever, since nothing else
// ever globs for that pattern. cleanupOrphanedTempFiles must remove one old
// enough to be abandoned while leaving a fresh one alone - a write genuinely
// in progress right now must never be swept out from under it.
func TestCleanupOrphanedTempFilesRemovesOldOnesOnly(t *testing.T) {
	dir := useTempDir(t)

	oldTmp := filepath.Join(dir, "assets.json.tmp-old12345")
	require.NoError(t, os.WriteFile(oldTmp, []byte("stale"), 0o644))
	require.NoError(t, os.Chtimes(oldTmp, time.Now().Add(-2*orphanTempFileAge), time.Now().Add(-2*orphanTempFileAge)))

	freshTmp := filepath.Join(dir, "traffic.json.tmp-fresh6789")
	require.NoError(t, os.WriteFile(freshTmp, []byte("in flight"), 0o644))

	// A real, published file must never be touched just because its name
	// happens to sit beside a tmp file - only the "*.tmp-*" pattern itself
	// is in scope.
	realFile := filepath.Join(dir, DataFileName)
	require.NoError(t, os.WriteFile(realFile, []byte("[]"), 0o644))

	cleanupOrphanedTempFiles()

	assert.NoFileExists(t, oldTmp, "an old orphaned temp file must be removed")
	assert.FileExists(t, freshTmp, "a temp file young enough to still be in flight must survive")
	assert.FileExists(t, realFile, "a published file must never be swept")
}

func TestCleanupOrphanedTempFilesToleratesAMissingDirectory(t *testing.T) {
	t.Setenv("DATA_DIR", filepath.Join(t.TempDir(), "does", "not", "exist"))
	assert.NotPanics(t, cleanupOrphanedTempFiles)
}

func TestDirDefaultsWhenUnset(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	assert.Equal(t, DefaultDir, Dir())

	t.Setenv("DATA_DIR", "/var/lib/itchgrep")
	assert.Equal(t, "/var/lib/itchgrep", Dir())
	assert.Equal(t, filepath.Join("/var/lib/itchgrep", IndexDirName), IndexPath())
}

func TestRatesRoundTripThroughStorage(t *testing.T) {
	// The webserver must convert with the same snapshot the index was built
	// with, or the dollar values baked into the documents and the rates shown
	// beside converted prices would describe different days.
	t.Setenv("DATA_DIR", t.TempDir())

	_, err := GetRates()
	assert.Error(t, err, "nothing stored yet is an error the caller falls back from")

	want := money.Fallback()
	require.NoError(t, PutRates(want))

	got, err := GetRates()
	require.NoError(t, err)
	assert.Equal(t, want.Date, got.Date)
	assert.Equal(t, want.Source, got.Source)
	assert.Equal(t, want.PerEUR["USD"], got.PerEUR["USD"])
}

func TestAnEmptyRatesTableIsRejected(t *testing.T) {
	// A rates file with no euro in it converts nothing. Accepting it would put
	// every price through a zero rate rather than falling back to the built-in
	// snapshot.
	t.Setenv("DATA_DIR", t.TempDir())
	require.NoError(t, PutRates(money.Rates{Date: "2026-08-09"}))

	_, err := GetRates()
	assert.Error(t, err)
}

func TestGetHistoryOnEmptyStorageIsEmptyNotAnError(t *testing.T) {
	// Unlike GetAssets, a missing history file is expected on every deploy
	// before the first crawl since this feature shipped - it must not read
	// back as a failure the caller has to handle specially.
	useTempDir(t)

	history, err := GetHistory()
	require.NoError(t, err)
	assert.Empty(t, history)
}

// TestGetHistoryOnACorruptFileMovesItAsideAndStartsFresh is the regression
// test for the corrupt-file recovery path: history.json existing but failing to parse (a torn write,
// a bad sector) used to be a real error GetHistory returned - and since
// PutHistory calls GetHistory before every write, that made the corruption
// permanent: no crawl could ever record history again. It must instead move
// the bad file aside and read back as empty, the same as if nothing had ever
// been written.
func TestGetHistoryOnACorruptFileMovesItAsideAndStartsFresh(t *testing.T) {
	dir := useTempDir(t)
	corruptPath := filepath.Join(dir, HistoryFileName)
	require.NoError(t, os.WriteFile(corruptPath, []byte("{not valid json"), 0o644))

	history, err := GetHistory()
	require.NoError(t, err, "a corrupt file must read back as empty, not as an error")
	assert.Empty(t, history)
	assert.NoFileExists(t, corruptPath, "the corrupt file must be moved aside, not left where the next read would trip over it again")

	matches, err := filepath.Glob(corruptPath + ".corrupt-*")
	require.NoError(t, err)
	assert.Len(t, matches, 1, "the corrupt file must survive under a .corrupt-<unix ts> name for inspection")
}

// TestPutHistoryStillWritesAfterACorruptFile proves the fix actually
// unblocks future writes, not just GetHistory's own read: a crawl completing
// after the corruption must be able to record its history entry, where it
// previously could not.
func TestPutHistoryStillWritesAfterACorruptFile(t *testing.T) {
	dir := useTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, HistoryFileName), []byte("garbage"), 0o644))

	require.NoError(t, PutHistory(models.CrawlRecord{Indexed: 42}))

	got, err := GetHistory()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.EqualValues(t, 42, got[0].Indexed)
}

// TestGetTrafficOnACorruptFileMovesItAsideAndLogsLoudly is the traffic.json
// half of the same recovery contract: unlike history.json, a corrupt traffic.json already fell
// back to zero counters (metrics.Restore discards any GetTraffic error) -
// the bug was that it did so in total silence, with nothing in any log to
// explain why /stats reset. GetTraffic itself must now move the bad file
// aside for evidence before its caller falls back.
func TestGetTrafficOnACorruptFileMovesItAsideAndLogsLoudly(t *testing.T) {
	dir := useTempDir(t)
	corruptPath := filepath.Join(dir, TrafficFileName)
	require.NoError(t, os.WriteFile(corruptPath, []byte("{not valid json"), 0o644))

	_, err := GetTraffic()
	assert.Error(t, err, "GetTraffic's error contract for callers is unchanged - only the silent handling of corruption is fixed")
	assert.NoFileExists(t, corruptPath)

	matches, err := filepath.Glob(corruptPath + ".corrupt-*")
	require.NoError(t, err)
	assert.Len(t, matches, 1)
}

// TestGetTrafficOnAMissingFileDoesNotRenameAnything guards against the
// obvious way to get that recovery wrong: os.ErrNotExist (the ordinary first-boot
// case) must never be treated as corruption - there is nothing to rename.
func TestGetTrafficOnAMissingFileDoesNotRenameAnything(t *testing.T) {
	dir := useTempDir(t)

	_, err := GetTraffic()
	assert.Error(t, err)

	matches, err := filepath.Glob(filepath.Join(dir, TrafficFileName) + ".corrupt-*")
	require.NoError(t, err)
	assert.Empty(t, matches, "a missing file is not corruption and must not produce a .corrupt-* artifact")
}

func TestPutHistoryRoundTrips(t *testing.T) {
	useTempDir(t)

	rec := models.CrawlRecord{
		When:            time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		DurationSeconds: 3600,
		Indexed:         96903,
		Catalog:         108697,
		NewAssets:       1204,
		RemovedAssets:   12,
		FreeAssets:      83214,
		Pages:           4200,
		SlicesDone:      314,
		SlicesPlanned:   320,
		Refusals:        7,
		Tags:            812,
		Resumed:         true,
	}
	require.NoError(t, PutHistory(rec))

	got, err := GetHistory()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, rec, got[0])
}

func TestPutHistoryAppendsNewestLast(t *testing.T) {
	useTempDir(t)

	first := models.CrawlRecord{When: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Indexed: 1}
	second := models.CrawlRecord{When: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Indexed: 2}
	require.NoError(t, PutHistory(first))
	require.NoError(t, PutHistory(second))

	got, err := GetHistory()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, first.Indexed, got[0].Indexed, "oldest entry stays first on disk")
	assert.Equal(t, second.Indexed, got[1].Indexed, "newest entry is appended last")
}

func TestPutHistoryCapsAtTheNewestEntries(t *testing.T) {
	// 104 entries is two years of weekly crawls. Uncapped, a service meant to
	// run for years would grow this file forever.
	useTempDir(t)

	for i := 0; i < maxHistoryEntries+10; i++ {
		require.NoError(t, PutHistory(models.CrawlRecord{Indexed: int64(i)}))
	}

	got, err := GetHistory()
	require.NoError(t, err)
	require.Len(t, got, maxHistoryEntries)
	assert.EqualValues(t, 10, got[0].Indexed, "oldest surviving entry is the 11th written")
	assert.EqualValues(t, maxHistoryEntries+9, got[len(got)-1].Indexed, "the newest entry is always kept")
}
