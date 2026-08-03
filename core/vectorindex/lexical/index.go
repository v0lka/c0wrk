package lexical

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"
)

// Field names used for the document mapping. Exported so callers can build
// queries against them if needed.
const (
	FieldContent  = "content"
	FieldFilePath = "file_path"
	FieldLanguage = "language"

	docType = "doc"
)

// Doc is an input document for the lexical index.
//
// Content is analyzed with the c0wrk_code analyzer. FilePath and Language
// are stored as keyword fields for exact-match post-filtering.
type Doc struct {
	ID       string
	FilePath string
	Language string
	Content  string
}

// Hit is a single query result from the lexical index.
type Hit struct {
	ID    string
	Score float32
	Rank  int
}

// Index is the minimal surface of a BM25-based lexical index.
type Index interface {
	Upsert(ctx context.Context, docs []Doc) error
	Delete(ctx context.Context, ids []string) error
	Query(ctx context.Context, q string, topK int) ([]Hit, error)
	Count() (uint64, error)
	Close() error
}

// Open returns an Index backed by bleve at the given directory. The directory
// is created (with the index) if it does not already exist.
func Open(dir string) (Index, error) {
	if dir == "" {
		return nil, errors.New("lexical index dir must not be empty")
	}
	if _, err := os.Stat(dir); err == nil {
		idx, err := bleve.Open(dir)
		if err != nil {
			return nil, fmt.Errorf("opening lexical index at %s: %w", dir, err)
		}
		return &bleveIndex{idx: idx, dir: dir}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat lexical index dir %s: %w", dir, err)
	}

	idxMapping, err := buildMapping()
	if err != nil {
		return nil, fmt.Errorf("building bleve mapping: %w", err)
	}
	idx, err := bleve.New(dir, idxMapping)
	if err != nil {
		return nil, fmt.Errorf("creating lexical index at %s: %w", dir, err)
	}
	return &bleveIndex{idx: idx, dir: dir}, nil
}

// buildMapping constructs an IndexMapping with the c0wrk_code analyzer and
// field mappings for content (analyzed), file_path (keyword), and language
// (keyword).
func buildMapping() (mapping.IndexMapping, error) {
	im := bleve.NewIndexMapping()

	err := im.AddCustomAnalyzer(AnalyzerName, map[string]interface{}{
		"type":      custom.Name,
		"tokenizer": unicode.Name,
		"token_filters": []string{
			CamelCaseFilterName,
			lowercase.Name,
			en.StopName,
		},
	})
	if err != nil {
		return nil, err
	}

	contentField := bleve.NewTextFieldMapping()
	contentField.Analyzer = AnalyzerName
	contentField.Store = false
	contentField.IncludeInAll = false
	contentField.IncludeTermVectors = false

	pathField := bleve.NewKeywordFieldMapping()
	pathField.Store = true
	pathField.IncludeInAll = false

	langField := bleve.NewKeywordFieldMapping()
	langField.Store = true
	langField.IncludeInAll = false

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt(FieldContent, contentField)
	docMapping.AddFieldMappingsAt(FieldFilePath, pathField)
	docMapping.AddFieldMappingsAt(FieldLanguage, langField)

	im.AddDocumentMapping(docType, docMapping)
	im.DefaultMapping = docMapping
	im.DefaultType = docType
	im.DefaultAnalyzer = AnalyzerName

	return im, nil
}

// bleveIndex is a thin wrapper around bleve.Index implementing Index.
type bleveIndex struct {
	idx bleve.Index
	dir string
}

func (b *bleveIndex) Upsert(ctx context.Context, docs []Doc) error {
	if len(docs) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lexical upsert cancelled: %w", err)
	}
	batch := b.idx.NewBatch()
	for _, d := range docs {
		if d.ID == "" {
			continue
		}
		payload := map[string]interface{}{
			FieldContent:  d.Content,
			FieldFilePath: d.FilePath,
			FieldLanguage: d.Language,
		}
		if err := batch.Index(d.ID, payload); err != nil {
			return fmt.Errorf("batching lexical doc %s: %w", d.ID, err)
		}
	}
	if err := b.idx.Batch(batch); err != nil {
		return fmt.Errorf("committing lexical batch (%d docs): %w", len(docs), err)
	}
	return nil
}

func (b *bleveIndex) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("lexical delete cancelled: %w", err)
	}
	batch := b.idx.NewBatch()
	for _, id := range ids {
		if id == "" {
			continue
		}
		batch.Delete(id)
	}
	if err := b.idx.Batch(batch); err != nil {
		return fmt.Errorf("committing lexical delete batch (%d ids): %w", len(ids), err)
	}
	return nil
}

func (b *bleveIndex) Query(ctx context.Context, q string, topK int) ([]Hit, error) {
	if topK <= 0 {
		return nil, nil
	}
	if q == "" {
		return nil, nil
	}
	mq := bleve.NewMatchQuery(q)
	mq.SetField(FieldContent)
	mq.Analyzer = AnalyzerName
	// Default operator is OR already; keep it explicit for clarity.

	req := bleve.NewSearchRequest(mq)
	req.Size = topK
	req.From = 0
	req.IncludeLocations = false
	req.Fields = nil

	res, err := b.idx.SearchInContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}

	hits := make([]Hit, 0, len(res.Hits))
	for i, h := range res.Hits {
		hits = append(hits, Hit{
			ID:    h.ID,
			Score: float32(h.Score),
			Rank:  i + 1,
		})
	}
	return hits, nil
}

func (b *bleveIndex) Count() (uint64, error) {
	n, err := b.idx.DocCount()
	if err != nil {
		return 0, fmt.Errorf("lexical doc count: %w", err)
	}
	return n, nil
}

// Close releases the underlying bleve index. The caller must serialize access
// to this method — it is not safe for concurrent use. After Close returns
// successfully, the index is nil and further operations must not be called.
func (b *bleveIndex) Close() error {
	if b.idx == nil {
		return nil
	}
	err := b.idx.Close()
	b.idx = nil
	if err != nil {
		return fmt.Errorf("closing lexical index: %w", err)
	}
	return nil
}
