package main

import (
	"os"
	"testing"

	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/blevesearch/bleve/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIndexNeedsRebuildOnAMissingIndex covers the state a fresh volume or a
// killed-mid-publish crawl leaves behind: nothing at the path at all.
// bleve.Open must fail on it, which is exactly the failure this is standing
// in for a real v1 upsidedown/boltdb index with - v2 cannot open that either,
// for the same reason (no matching registration), even though the cause is
// different.
func TestIndexNeedsRebuildOnAMissingIndex(t *testing.T) {
	needsRebuild, reason := indexNeedsRebuild(t.TempDir() + "/does-not-exist")
	assert.True(t, needsRebuild)
	assert.NotEmpty(t, reason)
}

// TestIndexNeedsRebuildOnAMangledMeta simulates the other shape of an
// unopenable index without a v1 binary to produce a real one with: a
// directory that has an index_meta.json, but not one bleve.Open can make
// sense of. A corrupt on-disk index degrades exactly the same way a v1 index
// does from this binary's point of view - neither opens - so this doubles as
// the corruption-recovery case.
func TestIndexNeedsRebuildOnAMangledMeta(t *testing.T) {
	dir := t.TempDir() + "/idx"
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(dir+"/index_meta.json", []byte("not json"), 0o644))

	needsRebuild, reason := indexNeedsRebuild(dir)
	assert.True(t, needsRebuild)
	assert.NotEmpty(t, reason)
}

// TestIndexNeedsRebuildOnAValidScorchIndex is the negative case: an index
// built the normal way, with the current mapping, must be recognised as
// already fine so a healthy deploy never pays for a rebuild it does not need.
func TestIndexNeedsRebuildOnAValidScorchIndex(t *testing.T) {
	dir := t.TempDir() + "/idx"
	idx, err := bleve.New(dir, storage.IndexMapping())
	require.NoError(t, err)
	require.NoError(t, idx.Close())

	needsRebuild, reason := indexNeedsRebuild(dir)
	assert.False(t, needsRebuild, "reason: %s", reason)
}

// TestSelfHealDoesNothingOnAFreshVolume covers the case that must NOT trigger
// a rebuild: no crawl has ever published here, so there is no assets.json to
// rebuild from, and none is needed - scheduleCrawls' first crawl is what
// populates a fresh volume.
func TestSelfHealDoesNothingOnAFreshVolume(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	require.NoError(t, selfHealIndexFormat())
	_, err := os.Stat(storage.IndexPath())
	assert.True(t, os.IsNotExist(err), "self-heal must not create an index where no crawl ever published one")
}

// TestSelfHealRebuildsAnUnopenableIndexFromPublishedData is the end-to-end
// path: assets.json and rates.json exist (a previous crawl published), but
// the index directory does not open. selfHealIndexFormat must rebuild and
// publish a working scorch index from that data alone, without anything that
// looks like a crawl running.
func TestSelfHealRebuildsAnUnopenableIndexFromPublishedData(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	assets := []models.Asset{
		{GameId: "1", Title: "Pixel Art Tileset", Author: "ana", Tags: []string{"2d", "pixel-art"}, InvPopularity: 1},
		{GameId: "2", Title: "Ambient Music Loops", Author: "cy", Tags: []string{"music"}, Price: "$4.95", InvPopularity: 2},
	}
	require.NoError(t, storage.PutAssets(assets))
	require.NoError(t, storage.PutRates(money.Fallback()))

	// No index at all published yet - the same shape a killed-mid-publish run
	// or a not-yet-existing IndexDirName leaves.
	require.NoError(t, selfHealIndexFormat())

	idx, err := bleve.Open(storage.IndexPath())
	require.NoError(t, err, "the rebuilt index must be openable")
	defer idx.Close()

	count, err := idx.DocCount()
	require.NoError(t, err)
	assert.EqualValues(t, len(assets), count)

	// SetField, not a bare default-field query: the mapping disables "_all"
	// (see internal/storage.IndexMapping), the same as every real query in
	// internal/cache/search.go.
	titleQuery := bleve.NewMatchQuery("Pixel")
	titleQuery.SetField("Title")
	res, err := idx.Search(bleve.NewSearchRequest(titleQuery))
	require.NoError(t, err)
	require.NotZero(t, res.Total, "a rebuilt index must still answer a search")
}

// TestSelfHealLeavesAHealthyIndexAlone is the negative end-to-end case: a
// well-formed scorch index must survive selfHealIndexFormat untouched, not
// be rebuilt on every single startup.
func TestSelfHealLeavesAHealthyIndexAlone(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	assets := []models.Asset{{GameId: "1", Title: "Existing Asset", InvPopularity: 1}}
	require.NoError(t, storage.PutAssets(assets))

	idx, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	require.NoError(t, idx.Index("1", models.NewIndexedAsset(assets[0], 0)))
	require.NoError(t, idx.Close())

	fi, err := os.Stat(storage.IndexPath())
	require.NoError(t, err)
	before := fi.ModTime()

	require.NoError(t, selfHealIndexFormat())

	fi, err = os.Stat(storage.IndexPath())
	require.NoError(t, err)
	assert.Equal(t, before, fi.ModTime(), "a healthy index must not be rebuilt")
}
