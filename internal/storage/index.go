package storage

import (
	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/mapping"
)

// IndexMapping is the bleve mapping the published index is built with.
//
// It lives here, beside the index's location and publishing, because it is a
// contract between two processes rather than a detail of either: the
// dataservice writes documents with it and the webserver queries fields that
// only behave as expected because of it. Held privately by the writer, a
// change to it would silently stop the reader's filters matching anything.
//
// Everything is left to bleve's dynamic defaults except the three fields that
// exist to be matched exactly rather than searched: TagSlugs, Pricing and
// SortTitle. The default analyser would fold "pixel-art" into the two terms
// "pixel" and "art", which is right for a search box and wrong for a filter -
// asking for the pixel-art tag would then also return everything merely tagged
// "art". The keyword analyser leaves each value as one indivisible term.
//
// The three are stored neither as term vectors nor as retrievable values: they
// are only ever used to filter, sort and facet, all of which read doc values.
func IndexMapping() *mapping.IndexMappingImpl {
	kw := bleve.NewTextFieldMapping()
	kw.Analyzer = keyword.Name
	kw.Store = false
	kw.IncludeTermVectors = false
	kw.IncludeInAll = false

	doc := bleve.NewDocumentMapping()
	doc.AddFieldMappingsAt("TagSlugs", kw)
	doc.AddFieldMappingsAt("Pricing", kw)
	doc.AddFieldMappingsAt("SortTitle", kw)

	m := bleve.NewIndexMapping()
	m.DefaultMapping = doc
	return m
}
