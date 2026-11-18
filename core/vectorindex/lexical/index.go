package lexical

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/index/scorch"
	"github.com/blevesearch/bleve/v2/index/upsidedown/store/boltdb"
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

// Doc is an input document for the lexical index: Content is analyzed with the
// c0wrk_code analyzer; FilePath and Language are keyword fields for exact-match
// post-filtering. See specs/domains/workspace.md (Vector Index).
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

// scorchAsyncErrorCallbackName keys this package's entry in scorch's
// process-global async-error callback registry; every writer open below
// passes it so scorch routes background errors to reportScorchAsyncError.
const scorchAsyncErrorCallbackName = "c0wrk-lexical"

// init registers the async-error sink before any open: scorch's registry
// contract requires init-time registration (the name lookup at construction
// silently yields nil otherwise).
func init() {
	scorch.RegistryAsyncErrorCallbacks[scorchAsyncErrorCallbackName] = reportScorchAsyncError
}

// asyncErrMu guards asyncErrSink: scorch fires the callback from its own
// background goroutines, so the test-installed swap must synchronize with
// them. A nil sink means the default slog reporting.
var (
	asyncErrMu   sync.RWMutex
	asyncErrSink func(err error, path string)
)

// reportScorchAsyncError is the sink scorch background loops report through:
// without a registered callback their panics and persistent errors vanish
// into stats counters, and a dead background loop is undiagnosable from a
// dump alone (specs/decisions/130-lexical-scorch-persister-deadlock.md).
func reportScorchAsyncError(err error, path string) {
	asyncErrMu.RLock()
	sink := asyncErrSink
	asyncErrMu.RUnlock()
	if sink != nil {
		sink(err, path)
		return
	}
	logScorchAsyncError(slog.Default(), err, path)
}

// logScorchAsyncError is the default reporting: a background-loop panic kills
// its goroutine outright, so it logs at Error; other background errors
// (persist retries, cleanup failures) log at Warn.
func logScorchAsyncError(log *slog.Logger, err error, path string) {
	level := slog.LevelWarn
	if errors.Is(err, scorch.ErrAsyncPanic) {
		level = slog.LevelError
	}
	log.Log(context.Background(), level, "scorch background error",
		"path", path, "error", err)
}

// scorchOptions returns the runtime options every writer open carries. The
// persister options disable scorch's merger-catch-up pause entirely — the
// pause is the one wait in scorch with no termination guarantee, and a dead
// mergerLoop goroutine turns it into a permanent write hang
// (specs/decisions/130-lexical-scorch-persister-deadlock.md). Keys must match
// scorch's persisterOptions field names: bleve JSON-round-trips the value.
func scorchOptions() map[string]interface{} {
	return map[string]interface{}{
		"scorchPersisterOptions": map[string]interface{}{
			"PersisterNapTimeMSec":      0,
			"PersisterNapUnderNumFiles": math.MaxInt,
		},
		"asyncErrorCallbackName": scorchAsyncErrorCallbackName,
	}
}

// Open returns an Index backed by bleve at the given directory. The directory
// is created (with the index) if it does not already exist. The open runs
// under the directory's lock gate (exclusive): a busy gate — another live
// opener on this index, in this or another process — refuses with
// ErrLockBusy within gateWait instead of hanging on bbolt's unbounded file
// lock. The gate stays held until Close.
func Open(dir string) (Index, error) {
	if dir == "" {
		return nil, errors.New("lexical index dir must not be empty")
	}
	if _, err := os.Stat(dir); err == nil {
		gate, gateErr := acquireGate(dir, true)
		if gateErr != nil {
			return nil, fmt.Errorf("opening lexical index at %s: %w", dir, gateErr)
		}
		idx, err := bleve.OpenUsing(dir, scorchOptions())
		if err != nil {
			_ = gate.release()
			return nil, fmt.Errorf("opening lexical index at %s: %w", dir, err)
		}
		return &bleveIndex{idx: idx, dir: dir, gate: gate}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat lexical index dir %s: %w", dir, err)
	}

	// The gate sentinel lives inside the directory, so the directory must
	// exist before the gate is taken (bleve.New would create it later).
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating lexical index dir %s: %w", dir, err)
	}
	gate, gateErr := acquireGate(dir, true)
	if gateErr != nil {
		return nil, fmt.Errorf("creating lexical index at %s: %w", dir, gateErr)
	}
	idxMapping, err := buildMapping()
	if err != nil {
		_ = gate.release()
		return nil, fmt.Errorf("building bleve mapping: %w", err)
	}
	// kvstore is scorch-ignored bookkeeping, but it lands in the persisted
	// index meta — keep boltdb.Name so the meta stays identical to what
	// bleve.New wrote for every pre-existing index. The meta carries NO
	// persister options: every open re-reads them through a float64
	// round-trip that cannot carry math.MaxInt, so scorchOptions() is
	// applied per writer open instead (the runtime merge always wins).
	idx, err := bleve.NewUsing(dir, idxMapping, scorch.Name, boltdb.Name, nil)
	if err != nil {
		_ = gate.release()
		return nil, fmt.Errorf("creating lexical index at %s: %w", dir, err)
	}
	return &bleveIndex{idx: idx, dir: dir, gate: gate}, nil
}

// OpenReadOnly returns an Index over an EXISTING directory opened
// bleve-read-only (scorch config read_only: no persister, no merger, no
// writes). This is the shared-corpus reader mode (ADR-104): N attached
// workspace roots of one published snapshot corpus read the same on-disk
// lexical index concurrently while exactly zero of them may write it. The
// open runs under the directory's lock gate (SHARED — readers coexist with
// readers, as the concurrent-corpus-readers contract requires): a gate held
// exclusively by a writer — or stuck — refuses with ErrLockBusy within
// gateWait instead of hanging on bbolt. A missing directory is an error —
// published corpora always carry one.
func OpenReadOnly(dir string) (Index, error) {
	if dir == "" {
		return nil, errors.New("lexical index dir must not be empty")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("opening read-only lexical index at %s: %w", dir, err)
	}
	gate, gateErr := acquireGate(dir, false)
	if gateErr != nil {
		return nil, fmt.Errorf("opening read-only lexical index at %s: %w", dir, gateErr)
	}
	idx, err := bleve.OpenUsing(dir, map[string]interface{}{"read_only": true})
	if err != nil {
		_ = gate.release()
		return nil, fmt.Errorf("opening read-only lexical index at %s: %w", dir, err)
	}
	return &bleveIndex{idx: idx, dir: dir, gate: gate}, nil
}

// buildMapping constructs the c0wrk_code analyzer mapping: content analyzed,
// file_path and language keyword fields.
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

// bleveIndex is a thin wrapper around bleve.Index implementing Index. The
// gate stays held from the successful open until Close — the gate is the
// bounded replica of bbolt's own lock, so its lifetime must match the
// index's.
type bleveIndex struct {
	idx  bleve.Index
	dir  string
	gate *lockGate
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

// Close releases the underlying bleve index and the lock gate. Not safe for
// concurrent use; after it returns, b.idx is nil and further operations must
// not be called.
func (b *bleveIndex) Close() error {
	if b.idx == nil {
		return nil
	}
	err := b.idx.Close()
	b.idx = nil
	// The gate is released even when the bleve close failed: a half-closed
	// index must not keep the directory locked forever.
	gateErr := b.gate.release()
	b.gate = nil
	if err != nil {
		return fmt.Errorf("closing lexical index: %w", err)
	}
	return gateErr
}
