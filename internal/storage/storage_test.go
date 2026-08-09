package storage

import (
	"itchgrep/pkg/models"
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
	assert.Equal(t, cp, got)
	assert.WithinDuration(t, time.Now(), updated, 10*time.Second)

	require.NoError(t, DeleteCheckpoint())
	_, _, err = GetCheckpoint()
	assert.Error(t, err, "a deleted checkpoint must read back as missing")
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

func TestDirDefaultsWhenUnset(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	assert.Equal(t, DefaultDir, Dir())

	t.Setenv("DATA_DIR", "/var/lib/itchgrep")
	assert.Equal(t, "/var/lib/itchgrep", Dir())
	assert.Equal(t, filepath.Join("/var/lib/itchgrep", IndexDirName), IndexPath())
}
