package models

import "fmt"

// Asset represents a game asset.
// Assets are serialised to JSON and stored as a single object in the
// Google Cloud Storage bucket named by storage.BucketName.
type Asset struct {
	GameId        string
	Title         string
	Author        string
	Description   string
	Link          string
	ThumbUrl      string
	InvPopularity int64 // inverse popularity, derived from page number of the asset
}

func (a Asset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, Link: %s, ThumbUrl: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.Link, a.ThumbUrl, a.InvPopularity)
}

// Tag is an itch.io asset facet and the number of assets carrying it.
// It lives here rather than in internal/fetcher because internal/storage
// caches the discovered tag universe and must not depend on the fetcher.
type Tag struct {
	Slug  string // e.g. "pixel-art", without the "tag-" prefix
	Count int64
}

// IndexedAsset is a smaller version of Asset, used for indexing.
type IndexedAsset struct {
	GameId        string
	Title         string
	Author        string
	Description   string
	InvPopularity int64
}

func (a IndexedAsset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.InvPopularity)
}
