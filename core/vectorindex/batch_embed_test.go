package vectorindex

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// fakeBatchEmbedder implements BatchEmbedder for tests. Every call is
// recorded (batch sizes and texts) and each text is embedded with the same
// deterministic algorithm as fakeEmbeddingFunc. With l2Normalize set (the
// production-realistic mode — sp4rk's ONNX sessions mean-pool AND
// L2-normalize), the vector is normalized before being returned; otherwise
// the raw fake vector is returned, like a hypothetical non-normalizing
// embedder.
type fakeBatchEmbedder struct {
	mu           sync.Mutex
	calls        [][]string
	l2Normalize  bool                              // return L2-normalized vectors (mirrors sp4rk's ONNX pooling)
	failErr      error                             // returned when failAfter is exceeded (or immediately, when failAfter == -1)
	failAfter    int                               // 0: never fail; N: succeed for the first N calls, fail afterwards; -1: fail on first call
	onSuccess    func(callIdx int, texts []string) // invoked after a call's vectors are computed
	poisonSubstr string                            // when non-empty, any call whose texts contain this substring fails — simulates a content-triggered embedder error (e.g. a tokenizer rejecting one pathological text)
}

func (f *fakeBatchEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), texts...))
	callIdx := len(f.calls)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.failAfter < 0 || (f.failAfter > 0 && callIdx > f.failAfter) {
		if f.failErr != nil {
			return nil, f.failErr
		}
		return nil, errors.New("fake batch embedder failure")
	}
	if f.poisonSubstr != "" {
		for _, text := range texts {
			if strings.Contains(text, f.poisonSubstr) {
				return nil, fmt.Errorf("fake batch embedder: poisoned text (contains %q)", f.poisonSubstr)
			}
		}
	}

	base := fakeEmbeddingFunc()
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := base(ctx, text)
		if err != nil {
			return nil, err
		}
		if f.l2Normalize {
			vec = l2NormalizeTest(vec)
		}
		vecs[i] = vec
	}
	if f.onSuccess != nil {
		f.onSuccess(callIdx, texts)
	}
	return vecs, nil
}

// l2NormalizeTest normalizes v in the test the same way chromem-go does,
// mirroring sp4rk's meanPoolAndNormalize output contract.
func l2NormalizeTest(v []float32) []float32 {
	var sq float64
	for _, val := range v {
		sq += float64(val) * float64(val)
	}
	norm := float32(math.Sqrt(sq))
	out := make([]float32, len(v))
	for i, val := range v {
		out[i] = val / norm
	}
	return out
}

// recordedBatchSizes returns the number of texts per EmbedDocuments call.
func (f *fakeBatchEmbedder) recordedBatchSizes() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	sizes := make([]int, len(f.calls))
	for i, c := range f.calls {
		sizes[i] = len(c)
	}
	return sizes
}

// recordedTexts returns the concatenation of all texts across calls, in order.
func (f *fakeBatchEmbedder) recordedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c...)
	}
	return out
}

// countingWrap wraps a chromem embedding function with an invocation counter.
func countingWrap(counter *atomic.Int64, base chromem.EmbeddingFunc) chromem.EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		counter.Add(1)
		return base(ctx, text)
	}
}

// normalizedFakeEmbeddingFunc is fakeEmbeddingFunc with L2-normalized output,
// mirroring sp4rk's production EmbeddingFunc (ONNX mean-pool + L2 normalize).
func normalizedFakeEmbeddingFunc() chromem.EmbeddingFunc {
	base := fakeEmbeddingFunc()
	return func(ctx context.Context, text string) ([]float32, error) {
		vec, err := base(ctx, text)
		if err != nil {
			return nil, err
		}
		return l2NormalizeTest(vec), nil
	}
}

// newBatchTestService creates a Service with an isolated per-test chromem
// storage directory and a switched branch, ready for AddDocuments.
// embedFn overrides the chromem-side embedding function (nil →
// fakeEmbeddingFunc); chromemCalls, when non-nil, wraps it with an
// invocation counter to observe "legacy" per-document embedding calls.
func newBatchTestService(t *testing.T, batch BatchEmbedder, embeddingBatchSize int, embedFn chromem.EmbeddingFunc, chromemCalls *atomic.Int64) *Service {
	t.Helper()
	if embedFn == nil {
		embedFn = fakeEmbeddingFunc()
	}
	if chromemCalls != nil {
		embedFn = countingWrap(chromemCalls, embedFn)
	}
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      embedFn,
		BatchEmbedder:      batch,
		EmbeddingBatchSize: embeddingBatchSize,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Register the temp dir BEFORE the svc.Close cleanup: t.Cleanup is LIFO,
	// so svc.Close (registered later) runs first and releases the lexical
	// store (.zap/.bolt) handles before TempDir's RemoveAll. Windows refuses
	// to delete files that still have open handles (EBUSY).
	projectDir := t.TempDir()
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", projectDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	return svc
}

// batchTestDocs builds n deterministic, uniquely-ID'd documents with the
// metadata fields the file-hash sidecar composes its entries from.
func batchTestDocs(n int) []chromem.Document {
	docs := make([]chromem.Document, n)
	for i := range docs {
		docs[i] = chromem.Document{
			ID:      fmt.Sprintf("doc-%04d", i),
			Content: fmt.Sprintf("content %d %s", i, string(rune('a'+i%26))),
			Metadata: map[string]string{
				"file_path":    fmt.Sprintf("/ws/file%03d.go", i),
				"content_hash": fmt.Sprintf("hash-%04d", i),
			},
		}
	}
	return docs
}

// TestAddDocuments_BatchEmbedder_ZeroChromemEmbeddingCalls is the core
// acceptance assertion of the batch path: with a BatchEmbedder configured,
// AddDocuments must populate Document.Embedding BEFORE the chromem commit so
// that chromem-go's per-document embedding function is invoked ZERO times,
// and EmbedDocuments must receive batches of at most the configured
// embedding batch size.
func TestAddDocuments_BatchEmbedder_ZeroChromemEmbeddingCalls(t *testing.T) {
	fb := &fakeBatchEmbedder{}
	var chromemCalls atomic.Int64
	svc := newBatchTestService(t, fb, 32, nil, &chromemCalls)

	docs := batchTestDocs(70)
	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}

	if got := chromemCalls.Load(); got != 0 {
		t.Errorf("chromem embedding function invoked %d times on the batch path; want 0", got)
	}

	// 70 texts with batch size 32 → 3 calls of sizes 32, 32, 6.
	sizes := fb.recordedBatchSizes()
	wantSizes := []int{32, 32, 6}
	if !reflect.DeepEqual(sizes, wantSizes) {
		t.Errorf("EmbedDocuments batch sizes = %v; want %v", sizes, wantSizes)
	}
	texts := fb.recordedTexts()
	if len(texts) != 70 {
		t.Fatalf("total texts sent to EmbedDocuments = %d; want 70", len(texts))
	}
	for i, text := range texts {
		if want := docs[i].Content; text != want {
			t.Errorf("text %d = %q; want %q (order/content mismatch)", i, text, want)
		}
	}

	if got := svc.collection.Count(); got != 70 {
		t.Errorf("collection Count = %d; want 70", got)
	}
	for _, doc := range docs {
		stored, err := svc.collection.GetByID(context.Background(), doc.ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", doc.ID, err)
		}
		if len(stored.Embedding) == 0 {
			t.Errorf("doc %s stored without an embedding", doc.ID)
		}
	}
}

// TestAddDocuments_BatchEmbedder_SubBatchChunkBounds crosses the 200-doc
// embeddingSubBatchSize boundary (250 docs): chunking must respect BOTH
// bounds — every EmbedDocuments call at most embeddingBatchSize texts, and
// the outer sub-batch semantics preserved (no call mixes documents from
// different sub-batches; the boundary falls between calls).
func TestAddDocuments_BatchEmbedder_SubBatchChunkBounds(t *testing.T) {
	fb := &fakeBatchEmbedder{}
	svc := newBatchTestService(t, fb, 32, nil, nil)

	docs := batchTestDocs(250)
	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}

	sizes := fb.recordedBatchSizes()
	// Sub-batch 1: docs 0..199 → 6×32 + 8. Sub-batch 2: docs 200..249 → 32 + 18.
	wantSizes := []int{32, 32, 32, 32, 32, 32, 8, 32, 18}
	if !reflect.DeepEqual(sizes, wantSizes) {
		t.Errorf("EmbedDocuments batch sizes = %v; want %v", sizes, wantSizes)
	}
	for _, size := range sizes {
		if size > 32 {
			t.Errorf("EmbedDocuments called with %d texts; want ≤ embedding batch size 32", size)
		}
	}
	if got := svc.collection.Count(); got != 250 {
		t.Errorf("collection Count = %d; want 250", got)
	}
}

// TestAddDocuments_BatchVsLegacy_Equivalence verifies the batch path stores
// equivalent document sets compared to the legacy per-document path: same
// content, same metadata, same file-hash sidecar, and equivalent embeddings.
//
// Two embedder shapes are covered:
//   - normalized (production-realistic: sp4rk's ONNX sessions mean-pool AND
//     L2-normalize, so both paths hand chromem unit vectors) — the stored
//     embeddings must be byte-identical;
//   - raw (fakeEmbeddingFunc, which does not normalize) — chromem v0.7.0
//     stores an embedding it created itself RAW, but normalizes a
//     pre-populated one, so the two representations differ by scale only.
//     Dot-product scoring against the (always normalized) query embedding
//     makes the batch representation the strictly-correct cosine; assert
//     exact direction parallelism instead of byte equality.
func TestAddDocuments_BatchVsLegacy_Equivalence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		l2Normalize bool
	}{
		{name: "normalized embedder (production shape)", l2Normalize: true},
		{name: "raw embedder (fake shape)", l2Normalize: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// In the production-shape subtest both sides use a NORMALIZING
			// embed func, exactly like sp4rk's Embedder; in the raw subtest
			// both sides use fakeEmbeddingFunc as-is.
			embed := fakeEmbeddingFunc()
			if tc.l2Normalize {
				embed = normalizedFakeEmbeddingFunc()
			}
			legacy := newBatchTestService(t, nil, 32, embed, nil)
			// Batch path uses a DIFFERENT batch size (16) than the default
			// (32) to prove the stored result does not depend on chunking.
			batch := newBatchTestService(t, &fakeBatchEmbedder{l2Normalize: tc.l2Normalize}, 16, embed, nil)

			docs := batchTestDocs(40)

			legacy.AcquireWriteLock()
			if err := legacy.AddDocuments(context.Background(), docs, nil); err != nil {
				legacy.ReleaseWriteLock()
				t.Fatalf("legacy AddDocuments: %v", err)
			}
			legacy.ReleaseWriteLock()

			batch.AcquireWriteLock()
			if err := batch.AddDocuments(context.Background(), docs, nil); err != nil {
				batch.ReleaseWriteLock()
				t.Fatalf("batch AddDocuments: %v", err)
			}
			batch.ReleaseWriteLock()

			if lc, bc := legacy.collection.Count(), batch.collection.Count(); lc != bc {
				t.Fatalf("collection counts differ: legacy=%d batch=%d", lc, bc)
			}

			for _, doc := range docs {
				l, err := legacy.collection.GetByID(context.Background(), doc.ID)
				if err != nil {
					t.Fatalf("legacy GetByID(%s): %v", doc.ID, err)
				}
				b, err := batch.collection.GetByID(context.Background(), doc.ID)
				if err != nil {
					t.Fatalf("batch GetByID(%s): %v", doc.ID, err)
				}
				if l.Content != b.Content {
					t.Errorf("doc %s content differs: legacy=%q batch=%q", doc.ID, l.Content, b.Content)
				}
				if !reflect.DeepEqual(l.Metadata, b.Metadata) {
					t.Errorf("doc %s metadata differs: legacy=%v batch=%v", doc.ID, l.Metadata, b.Metadata)
				}
				if len(l.Embedding) != len(b.Embedding) {
					t.Fatalf("doc %s embedding dims differ: legacy=%d batch=%d", doc.ID, len(l.Embedding), len(b.Embedding))
				}
				if tc.l2Normalize {
					// Both paths stored the same unit vector as-is.
					for i := range l.Embedding {
						if l.Embedding[i] != b.Embedding[i] {
							t.Errorf("doc %s embedding[%d] differs: legacy=%v batch=%v", doc.ID, i, l.Embedding[i], b.Embedding[i])
							break
						}
					}
					continue
				}
				var dot, ln, bn float64
				for i := range l.Embedding {
					dot += float64(l.Embedding[i]) * float64(b.Embedding[i])
					ln += float64(l.Embedding[i]) * float64(l.Embedding[i])
					bn += float64(b.Embedding[i]) * float64(b.Embedding[i])
				}
				if cos := dot / (math.Sqrt(ln) * math.Sqrt(bn)); math.Abs(cos-1) > 1e-5 {
					t.Errorf("doc %s embeddings are not parallel: cosine=%v (legacy=%v batch=%v)", doc.ID, cos, l.Embedding, b.Embedding)
				}
				if norm := math.Sqrt(bn); math.Abs(norm-1) > 1e-5 {
					t.Errorf("doc %s batch embedding is not normalized: |v|=%v", doc.ID, norm)
				}
			}

			// The file-hash sidecar must be populated identically on both paths.
			lFiles, err := legacy.GetCollectionFiles()
			if err != nil {
				t.Fatalf("legacy GetCollectionFiles: %v", err)
			}
			bFiles, err := batch.GetCollectionFiles()
			if err != nil {
				t.Fatalf("batch GetCollectionFiles: %v", err)
			}
			if !reflect.DeepEqual(lFiles, bFiles) {
				t.Errorf("file-hash sidecars differ: legacy=%v batch=%v", lFiles, bFiles)
			}
		})
	}
}

// TestAddDocuments_BatchEmbedder_SkipsPrePopulated verifies the batch path
// only sends documents with an EMPTY Embedding to the batch embedder;
// documents that already carry a vector are passed through to chromem
// untouched (and chromem still performs zero embedding calls).
func TestAddDocuments_BatchEmbedder_SkipsPrePopulated(t *testing.T) {
	fb := &fakeBatchEmbedder{}
	var chromemCalls atomic.Int64
	svc := newBatchTestService(t, fb, 32, nil, &chromemCalls)

	docs := batchTestDocs(4)
	docs[1].Embedding = []float32{1, 0, 0, 0, 0, 0, 0, 0}
	docs[3].Embedding = []float32{0, 1, 0, 0, 0, 0, 0, 0}

	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("AddDocuments: %v", err)
	}

	if got := chromemCalls.Load(); got != 0 {
		t.Errorf("chromem embedding function invoked %d times; want 0", got)
	}
	texts := fb.recordedTexts()
	wantTexts := []string{docs[0].Content, docs[2].Content}
	if !reflect.DeepEqual(texts, wantTexts) {
		t.Errorf("EmbedDocuments texts = %v; want only the docs without pre-set embeddings: %v", texts, wantTexts)
	}
	// The pre-populated vectors must survive the chromem round-trip unchanged
	// (both are already unit vectors, so chromem skips normalization). Docs
	// 0/2 were assigned raw batch vectors that chromem normalizes, so only
	// their presence + dimension is asserted above via GetByID succeeding.
	for _, i := range []int{1, 3} {
		stored, err := svc.collection.GetByID(context.Background(), docs[i].ID)
		if err != nil {
			t.Fatalf("GetByID(%s): %v", docs[i].ID, err)
		}
		if !reflect.DeepEqual(stored.Embedding, docs[i].Embedding) {
			t.Errorf("doc %s embedding = %v; want the original pre-populated %v", docs[i].ID, stored.Embedding, docs[i].Embedding)
		}
	}
}

// TestAddDocuments_BatchEmbedder_ErrorPreservesSidecarSemantics verifies the
// sidecar upsert-after-full-commit semantics survive on the batch path: when
// embedding fails for the SECOND sub-batch (the first 200-doc sub-batch
// already committed to chromem), AddDocuments must return an error and the
// file-hash sidecar must stay EMPTY — recording the first sub-batch's hashes
// would mark half-indexed files as up-to-date.
func TestAddDocuments_BatchEmbedder_ErrorPreservesSidecarSemantics(t *testing.T) {
	fb := &fakeBatchEmbedder{failAfter: 7} // sub-batch 1 (200 docs → 7 calls) succeeds; the 8th call (sub-batch 2) fails
	svc := newBatchTestService(t, fb, 32, nil, nil)

	docs := batchTestDocs(250)
	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err == nil {
		t.Fatal("AddDocuments must fail when the batch embedder fails")
	}

	if got := svc.collection.Count(); got != 200 {
		t.Errorf("collection Count = %d; want 200 (first sub-batch committed)", got)
	}
	files, ferr := svc.GetCollectionFiles()
	if ferr != nil {
		t.Fatalf("GetCollectionFiles: %v", ferr)
	}
	if len(files) != 0 {
		t.Errorf("file-hash sidecar populated after a failed batch (%d entries); upsert-after-full-commit semantics broken", len(files))
	}
}

// TestAddDocuments_BatchEmbedder_CancelledMidBatch verifies cancellation
// between embedding chunks: when the context is cancelled after the first
// successful EmbedDocuments chunk, the next chunk's pre-check aborts with a
// wrapped context error and NOTHING is committed to chromem.
func TestAddDocuments_BatchEmbedder_CancelledMidBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fb := &fakeBatchEmbedder{onSuccess: func(callIdx int, _ []string) {
		if callIdx == 1 {
			cancel()
		}
	}}
	svc := newBatchTestService(t, fb, 32, nil, nil)

	docs := batchTestDocs(70)
	svc.AcquireWriteLock()
	err := svc.AddDocuments(ctx, docs, nil)
	svc.ReleaseWriteLock()
	if err == nil {
		t.Fatal("AddDocuments must fail when the context is cancelled mid-batch")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not wrap context.Canceled: %v", err)
	}
	if got := svc.collection.Count(); got != 0 {
		t.Errorf("collection Count = %d; want 0 (cancelled before any chromem commit)", got)
	}
}

// TestAddDocuments_BatchEmbedder_PerTextFallbackDropsPoisonedDoc verifies
// the content-failure isolation path: when a chunk fails as a unit because
// of one pathological text (the production case being a tokenizer bug
// triggered by specific content), AddDocuments retries each text
// individually, drops ONLY the offending document, and still commits the
// rest — one bad file must not abort indexing of everything batched with
// it. The file-hash sidecar still records every file (the dropped doc's
// file is considered indexed minus the bad chunk; retrying a deterministic
// content failure every pass would waste embedding work), and chromem
// performs zero per-document embedding calls throughout.
func TestAddDocuments_BatchEmbedder_PerTextFallbackDropsPoisonedDoc(t *testing.T) {
	fb := &fakeBatchEmbedder{poisonSubstr: "POISON"}
	var chromemCalls atomic.Int64
	svc := newBatchTestService(t, fb, 32, nil, &chromemCalls)

	docs := batchTestDocs(40)
	docs[17].Content = "content 17 POISON \xff\xfeq"
	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("AddDocuments: %v; want success with only the poisoned doc dropped", err)
	}

	if got := svc.collection.Count(); got != 39 {
		t.Errorf("collection Count = %d; want 39 (poisoned doc dropped)", got)
	}

	// Chromem must never fall back to per-document embedding. Checked BEFORE
	// the verification Query below, which itself embeds the query text.
	if got := chromemCalls.Load(); got != 0 {
		t.Errorf("chromem embedding func invoked %d times; want 0 (dropped doc excluded from commit)", got)
	}

	// The dropped document must be the poisoned one and nothing else.
	results, qerr := svc.collection.Query(context.Background(), " ", 39, nil, nil)
	if qerr != nil {
		t.Fatalf("Query: %v", qerr)
	}
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.ID] = true
	}
	if seen["doc-0017"] {
		t.Error("poisoned doc-0017 must not be committed to the collection")
	}
	if !seen["doc-0016"] || !seen["doc-0018"] {
		t.Error("the poisoned doc's batch neighbors must still be committed")
	}

	// The fallback shape: call 1 is the 32-text chunk that fails as a unit,
	// calls 2..33 are the 32 per-text retries, call 34 is the remaining
	// 8-text chunk that succeeds as a unit.
	sizes := fb.recordedBatchSizes()
	if len(sizes) != 34 {
		t.Fatalf("EmbedDocuments calls = %d (%v); want 34 ([32, 1×32..., 8])", len(sizes), sizes)
	}
	if sizes[0] != 32 || sizes[len(sizes)-1] != 8 {
		t.Errorf("batch sizes = %v; want first 32, last 8", sizes)
	}
	for i := 1; i <= 32; i++ {
		if sizes[i] != 1 {
			t.Errorf("per-text fallback call %d has batch size %d; want 1", i+1, sizes[i])
		}
	}

	// The sidecar still records every file, including the dropped doc's:
	// the content failure is deterministic, so the file must NOT be
	// re-embedded on every subsequent pass.
	files, ferr := svc.GetCollectionFiles()
	if ferr != nil {
		t.Fatalf("GetCollectionFiles: %v", ferr)
	}
	if len(files) != 40 {
		t.Errorf("file-hash sidecar entries = %d; want 40 (all files recorded, dropped chunk included)", len(files))
	}
}

// TestAddDocuments_BatchEmbedder_DroppedDocExcludedFromLexical pins the
// dual-index divergence guard: a document dropped by the per-text fallback
// must be excluded from the lexical upsert too — otherwise the BM25 index
// would return hits for a chunk the vector store never committed (lexical
// hits enrich their content via chromem GetByID, so such a hit would be
// hollow). The poisoned doc's batch neighbors must remain searchable.
func TestAddDocuments_BatchEmbedder_DroppedDocExcludedFromLexical(t *testing.T) {
	fb := &fakeBatchEmbedder{poisonSubstr: "POISON"}
	svc := newBatchTestService(t, fb, 32, nil, nil)

	docs := batchTestDocs(40)
	docs[17].Content = "content 17 POISON \xff\xfeq"
	lexDocs := make([]lexical.Doc, 0, len(docs))
	for _, d := range docs {
		lexDocs = append(lexDocs, lexical.Doc{
			ID:       d.ID,
			FilePath: d.Metadata["file_path"],
			Language: "go",
			Content:  d.Content,
		})
	}

	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, lexDocs)
	svc.ReleaseWriteLock()
	if err != nil {
		t.Fatalf("AddDocuments: %v; want success with only the poisoned doc dropped", err)
	}

	lex := svc.GetLexical()
	if lex == nil {
		t.Fatal("lexical index must be available after SwitchBranch")
	}
	count, cerr := lex.Count()
	if cerr != nil {
		t.Fatalf("lexical Count: %v", cerr)
	}
	if count != 39 {
		t.Errorf("lexical Count = %d; want 39 (poisoned doc excluded from upsert)", count)
	}

	// The dropped doc carried the unique term POISON: it must not be
	// retrievable from the lexical index at all.
	hits, qerr := lex.Query(context.Background(), "POISON", 10)
	if qerr != nil {
		t.Fatalf("lexical Query: %v", qerr)
	}
	if len(hits) != 0 {
		t.Errorf("lexical Query(POISON) returned %d hits; want 0 (dropped doc leaked into BM25 index)", len(hits))
	}
}

// TestAddDocuments_BatchEmbedder_AllTextsFailIndividually verifies the
// systemic-failure discriminator: when a chunk fails as a unit AND every
// per-text retry fails too, the embedder itself is broken, so AddDocuments
// must abort with an error rather than silently drop the whole batch.
func TestAddDocuments_BatchEmbedder_AllTextsFailIndividually(t *testing.T) {
	fb := &fakeBatchEmbedder{failAfter: -1} // every call fails, batched or per-text
	svc := newBatchTestService(t, fb, 32, nil, nil)

	docs := batchTestDocs(40)
	svc.AcquireWriteLock()
	err := svc.AddDocuments(context.Background(), docs, nil)
	svc.ReleaseWriteLock()
	if err == nil {
		t.Fatal("AddDocuments must fail when every per-text retry fails (systemic embedder failure)")
	}
	if got := svc.collection.Count(); got != 0 {
		t.Errorf("collection Count = %d; want 0 (nothing committed on systemic failure)", got)
	}
	files, ferr := svc.GetCollectionFiles()
	if ferr != nil {
		t.Fatalf("GetCollectionFiles: %v", ferr)
	}
	if len(files) != 0 {
		t.Errorf("file-hash sidecar entries = %d; want 0 after a failed pass", len(files))
	}
}

// TestNewManager_BatchEmbedder_ForwardedToService verifies the Manager
// forwards an optional BatchEmbedder into the Service, and that a nil one
// stays nil so the legacy per-document chromem path (relied upon by the
// existing tests) is preserved.
func TestNewManager_BatchEmbedder_ForwardedToService(t *testing.T) {
	fb := &fakeBatchEmbedder{}
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc(), BatchEmbedder: fb})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })
	if mgr.service.batchEmbedder == nil {
		t.Error("ManagerConfig.BatchEmbedder not forwarded to the Service")
	}

	mgrLegacy, err := NewManager(ManagerConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewManager (legacy): %v", err)
	}
	t.Cleanup(func() { mgrLegacy.Shutdown() })
	if mgrLegacy.service.batchEmbedder != nil {
		t.Error("nil ManagerConfig.BatchEmbedder must stay nil on the Service (legacy path)")
	}
}
