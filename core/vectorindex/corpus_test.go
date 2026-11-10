package vectorindex

// ADR-104 stage-2 (owner/view) acceptance tests:
//
//   1. two clean roots — including different branches at one commit — reuse
//      the shared corpus with NO full embed/chunk/commit pass (bounded
//      reconciliation only);
//   2. no full copy of the unchanged collection on the second root, neither
//      on disk (one snapshots dir, empty overlay stores) nor in RAM (one
//      shared decode — handle identity);
//   3. changes/deletions/renames/untracked files in root A never change
//      root B's results (corpus immutability + per-root overlay);
//   5. a failed attach never serves another snapshot's collection
//      (fail-closed);
//   6. the status stream distinguishes preparation / migration-rebuild /
//      indexing / unavailable;
//   7. corpus builds are single-flight; concurrent attachers wait for the
//      one builder and never open a parallel store.
//
// Criterion 4 (a focus switch never cancels the other root's indexing) is a
// registry-level property and is covered by the backend worktree suite.

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// viewHarness is one shared-store fixture: a real git repo with a linked
// worktree at the SAME commit (different branch), a shared storage root, a
// corpus registry, and counting embedders per service.
type viewHarness struct {
	repo     *gittest.Repo
	rootA    string // the checkout (branch main)
	rootB    string // the linked worktree (branch topic, same commit)
	storage  string
	registry *CorpusRegistry
	t        *testing.T
}

// newViewHarness builds the two-roots-one-snapshot fixture. files are
// written into the initial commit of main; the worktree is born from main
// (same commit OID).
func newViewHarness(t *testing.T, files map[string]string) *viewHarness {
	t.Helper()
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		repo.Git(t, "add", name)
	}
	if len(files) > 0 {
		repo.Git(t, "commit", "-m", "fixture files")
	}
	wt := filepath.Join(t.TempDir(), "wt")
	repo.Git(t, "worktree", "add", "-b", "topic", wt, "main")
	h := &viewHarness{
		repo:     repo,
		rootA:    root,
		rootB:    wt,
		storage:  filepath.Join(t.TempDir(), "vector_index"),
		registry: NewCorpusRegistry(nil),
		t:        t,
	}
	// Registered AFTER the TempDir calls above, so LIFO runs it BEFORE
	// their RemoveAll: retained residency handles hold read-only bleve
	// (.zap) readers open by design, and Windows refuses to unlink a file
	// with an open handle. Idempotent — tests that Close the registry
	// themselves are unaffected.
	t.Cleanup(func() { h.registry.Close() })
	return h
}

// embedCounter wraps a service's per-text embedder with a counter.
type embedCounter struct {
	mu    sync.Mutex
	texts []string
}

func (c *embedCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.texts)
}

// newViewService builds a Service bound to the shared registry and storage.
func (h *viewHarness) newViewService(counter *embedCounter) *Service {
	h.t.Helper()
	ef := func(ctx context.Context, text string) ([]float32, error) {
		if counter != nil {
			counter.mu.Lock()
			counter.texts = append(counter.texts, text)
			counter.mu.Unlock()
		}
		return fakeEmbeddingFunc()(ctx, text)
	}
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             ef,
		EmbeddingCacheFingerprint: "fp-view",
		Corpus:                    h.registry,
	})
	if err != nil {
		h.t.Fatalf("NewService: %v", err)
	}
	h.t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// attach drives the full view attach of one root: project, root binding,
// snapshot attach (corpus ensure included) and the reconciliation pass.
func (h *viewHarness) attach(svc *Service, idx *Indexer, root, legacyBranch string) error {
	if err := svc.SetProject("view", h.storage); err != nil {
		return err
	}
	svc.SetWorkspaceRoot(root)
	if err := idx.AttachSnapshot(context.Background(), root, legacyBranch); err != nil {
		return err
	}
	return idx.IndexIncremental(context.Background(), root)
}

func (h *viewHarness) newIndexer(svc *Service, phases *[]IndexPhase, states *[]IndexState) *Indexer {
	h.t.Helper()
	cb := ProgressCallback(nil)
	if phases != nil || states != nil {
		var mu sync.Mutex
		cb = func(p IndexPhase, st IndexState, _, _ int, _ string) {
			mu.Lock()
			if phases != nil {
				*phases = append(*phases, p)
			}
			if states != nil {
				*states = append(*states, st)
			}
			mu.Unlock()
		}
	}
	return NewIndexer(IndexerConfig{
		Service:    svc,
		ChunkFn:    fakeChunkFunc,
		HashFn:     fakeHashFunc,
		OnProgress: cb,
	})
}

// snapshotDirs lists the published snapshot corpus directories.
func (h *viewHarness) snapshotDirs() []string {
	h.t.Helper()
	entries, err := os.ReadDir(filepath.Join(h.storage, branchRootsDirName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), collectionNamePrefix+snapshotKeyPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// dirBytes sums the regular-file sizes under dir (0 when absent).
func dirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr // best-effort measurement
		}
		if info, infoErr := d.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// TestSharedCorpus_TwoRootsOneCommit_ReuseWithoutFullPass pins criteria 1
// and 2: the first root builds the corpus (one full pass); the second root
// — a different branch at the same commit — attaches with a bounded
// reconciliation only: zero new embeddings, zero overlay documents, one
// snapshot corpus on disk, and ONE shared decode in RAM (handle identity).
func TestSharedCorpus_TwoRootsOneCommit_ReuseWithoutFullPass(t *testing.T) {
	files := map[string]string{
		"a.go":         "package main\n\n// alpha marker one\n",
		"b.go":         "package main\n\n// beta marker two\n",
		"sub/c.go":     "package sub\n\n// gamma marker three\n",
		"big.go":       "package main\n\n// delta marker four\n// delta more lines\n",
		"unrelated.go": "package main\n\n// epsilon marker five\n",
	}
	h := newViewHarness(t, files)

	counterA := &embedCounter{}
	svcA := h.newViewService(counterA)
	idxA := h.newIndexer(svcA, nil, nil)
	if err := h.attach(svcA, idxA, h.rootA, "main"); err != nil {
		t.Fatalf("attach A: %v", err)
	}
	embeddedByA := counterA.count()
	if embeddedByA == 0 {
		t.Fatal("fixture sanity: the corpus build must have embedded the files")
	}

	// Criterion 1: root B (branch topic, SAME commit) attaches with NO
	// embed pass at all — the corpus already holds every chunk.
	counterB := &embedCounter{}
	svcB := h.newViewService(counterB)
	idxB := h.newIndexer(svcB, nil, nil)
	started := time.Now()
	if err := h.attach(svcB, idxB, h.rootB, "topic"); err != nil {
		t.Fatalf("attach B: %v", err)
	}
	if n := counterB.count(); n != 0 {
		t.Errorf("root B re-embedded %d texts; want 0 (shared corpus, bounded reconciliation only)", n)
	}
	reconcileBudget := 30 * time.Second
	if d := time.Since(started); d > reconcileBudget {
		t.Errorf("root B reconciliation took %s; want within the %s latency budget (no full pass)", d, reconcileBudget)
	}

	// B's overlay stays empty: nothing was committed for the clean tree.
	// n = -1 keeps a nil collection (no overlay at all) reportable in the
	// failure message without dereferencing it.
	n := -1
	if col := svcB.GetCollection(); col != nil {
		n = col.Count()
	}
	if n != 0 {
		t.Errorf("root B overlay holds %d docs; want 0 (corpus serves the clean tree)", n)
	}

	// Criterion 2 (disk): ONE snapshot corpus; the per-root overlay stores
	// exist but hold no collection bytes (their dirs are ~0 bytes while the
	// corpus carries the vectors).
	snaps := h.snapshotDirs()
	if len(snaps) != 1 {
		t.Fatalf("snapshot corpora on disk: %d (%v); want exactly 1", len(snaps), snaps)
	}
	corpusBytes := dirBytes(filepath.Join(h.storage, branchRootsDirName, snaps[0]))
	rootBKey := overlayRootKey(h.rootB)
	overlayBytes := dirBytes(filepath.Join(h.storage, branchRootsDirName, collectionName(rootBKey)))
	if corpusBytes == 0 {
		t.Fatal("fixture sanity: corpus dir must hold bytes")
	}
	if overlayBytes > corpusBytes/4 {
		t.Errorf("root B overlay store is %d bytes vs corpus %d — a full copy leaked into the per-root store", overlayBytes, corpusBytes)
	}
	t.Logf("measured: corpus=%d bytes, root-B overlay manifest/delta bytes=%d, root-A embeds=%d, root-B embeds=0",
		corpusBytes, overlayBytes, embeddedByA)

	// Criterion 2 (RAM): both services share ONE decode — the registry's
	// cached handle — never a per-root copy.
	cs := h.registry.storeFor(h.storage)
	cs.mu.Lock()
	handleCount := len(cs.handles)
	cs.mu.Unlock()
	if handleCount != 1 {
		t.Errorf("registry holds %d decoded corpus handles; want 1 (shared decode)", handleCount)
	}

	// B's search serves the shared corpus through its own view: same hits,
	// hydrated against B's tree.
	for _, q := range []string{"alpha marker", "gamma marker"} {
		res, err := svcB.HybridSearch(context.Background(), SearchOptions{Query: q, TopK: 5, Mode: ModeHybrid})
		if err != nil {
			t.Fatalf("B search %q: %v", q, err)
		}
		if len(res) == 0 {
			t.Errorf("B search %q returned no hits — the shared corpus is not serving the view", q)
			continue
		}
		if !strings.HasPrefix(res[0].FilePath, h.rootB) {
			t.Errorf("B result path %q is not hydrated against root B %q", res[0].FilePath, h.rootB)
		}
	}
}

// TestHybridRRF_VectorLegDedup_CorpusWins pins the fourth merge site of the
// same-ID dedup: a stale overlay duplicate (injected directly here — the
// realistic producer is a retirement that outlived a transient failure) must
// NOT earn a second vector-side RRF contribution in hybrid mode. The corpus
// entry carries the root's current bytes and wins, mirroring
// vectorOnlySearch, lexicalOnlySearch and aggregateCorpusLexicalHits.
func TestHybridRRF_VectorLegDedup_CorpusWins(t *testing.T) {
	files := map[string]string{
		"a.go": "package main\n\n// alpha marker one\n",
		"b.go": "package main\n\n// beta marker two\n",
	}
	h := newViewHarness(t, files)

	svc := h.newViewService(nil)
	idx := h.newIndexer(svc, nil, nil)
	if err := h.attach(svc, idx, h.rootA, "main"); err != nil {
		t.Fatalf("attach A: %v", err)
	}

	// Resolve the corpus-served candidate for a.go (the must-match pin
	// already applies inside corpusVectorCandidates) and poison the overlay
	// with a document under the SAME DocumentID — the state a failed
	// retirement leaves behind: both stores visibly serve one ID.
	svc.mu.RLock()
	resolver := newContentResolver(svc.maxFileSize, svc.maxChunkSize)
	corpusHit, err := svc.corpusVectorCandidates(context.Background(), "alpha marker one", hybridFanout(10, svc.hybridConfig), "", []string{"alpha marker"}, resolver)
	svc.mu.RUnlock()
	if err != nil {
		t.Fatalf("corpus candidates: %v", err)
	}
	if len(corpusHit) == 0 {
		t.Fatal("fixture sanity: the corpus must serve a.go")
	}
	src := corpusHit[0]
	emb, embErr := fakeEmbeddingFunc()(context.Background(), src.Content)
	if embErr != nil {
		t.Fatalf("fake embed: %v", embErr)
	}
	stale := chromem.Document{ID: src.ID, Content: src.Content, Metadata: src.Metadata, Embedding: emb}

	svc.AcquireWriteLock()
	if err := svc.current.collection.AddDocuments(context.Background(), []chromem.Document{stale}, 1); err != nil {
		svc.ReleaseWriteLock()
		t.Fatalf("poisoning the overlay with the stale duplicate: %v", err)
	}
	svc.ReleaseWriteLock()

	res, err := svc.HybridSearch(context.Background(), SearchOptions{
		Query: "alpha marker one", TopK: 5, Mode: ModeHybrid, MustMatch: []string{"alpha marker"},
	})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d (%v); want exactly the one must-matched file", len(res), res)
	}
	got := res[0]
	// Without the merge dedup the same ID occupies TWO vector-rank slots and
	// the second occurrence overwrites vectorRank with the later rank.
	if got.VectorRank != 1 {
		t.Errorf("duplicated ID's vector rank = %d; want 1 (one vector-side contribution, corpus copy wins)", got.VectorRank)
	}
	// Exactly one vector contribution (rank 1) plus one corpus-lexical
	// contribution (rank 1); a second 1/(k+2) vector contribution is the
	// double-count the dedup eliminates.
	k := svc.hybridConfig.RRFK
	want := 1.0/float64(k+1) + 1.0/float64(k+1)
	if diff := got.Score - float32(want); diff < -1e-6 || diff > 1e-6 {
		t.Errorf("fused score = %v; want %v (one vector + one lexical contribution, no duplicate boost)", got.Score, want)
	}
}

// writeLegacyDocGob writes one chromem-shaped document gob (the
// chromemDocumentProbe field set is gob-compatible with chromem.Document)
// into a legacy collection directory.
func writeLegacyDocGob(t *testing.T, path string, width int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close() //nolint:errcheck // test fixture
	doc := chromemDocumentProbe{
		ID:        "a.go:0",
		Metadata:  map[string]string{"file_path": "a.go"},
		Embedding: make([]float32, width),
	}
	if err := gob.NewEncoder(f).Encode(&doc); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}

// TestPromoteLegacyStore_ManifestlessWidthGate pins the era-derivation
// posture of the manifest-less adoption: the current-era stamp fires only
// when a sampled document's embedding width MATCHES the current era (the
// one dimension of the era verifiable in place); a width mismatch — a
// dims-changing model swap between binaries — refuses with an honest
// reason, writes nothing, and leaves the legacy store in place for the
// cache-spared rebuild; an empty/undecodable store is equally unverifiable.
func TestPromoteLegacyStore_ManifestlessWidthGate(t *testing.T) {
	buildFixture := func(t *testing.T) string {
		t.Helper()
		srcRoot := t.TempDir()
		legacyRoot := branchRootPath(srcRoot, "main")
		colDir := filepath.Join(legacyRoot, "deadbeefcol")
		if err := os.MkdirAll(colDir, 0o750); err != nil {
			t.Fatalf("mkdir collection: %v", err)
		}
		writeLegacyDocGob(t, filepath.Join(colDir, "00000001.gob"), 8)
		pruneWrite(t, fileHashesPathFor(srcRoot, "main"), `{"a.go":"h1"}`)
		pruneWrite(t, contentlessMarkerPathFor(srcRoot, "main"), "")
		// A completed legacy store always has its lexical leg (the
		// branch open creates it), and promotion now requires it: a store
		// without one cannot be served by any corpus open, so the fixture
		// must model a real store rather than a half-written one.
		if err := os.MkdirAll(lexicalIndexPath(srcRoot, "main"), 0o750); err != nil {
			t.Fatalf("mkdir lexical leg: %v", err)
		}
		return srcRoot
	}
	newStore := func(t *testing.T) *corpusStore {
		t.Helper()
		reg := NewCorpusRegistry(nil)
		t.Cleanup(reg.Close)
		return reg.storeFor(t.TempDir())
	}
	const key = "snap_aaaa0000bbbb1111"

	t.Run("width mismatch refuses without stamping or moving", func(t *testing.T) {
		srcRoot := buildFixture(t)
		migrated, reason, err := newStore(t).promoteLegacyStore(srcRoot, "main", key, "fp-cur", 4)
		if err != nil {
			t.Fatalf("promoteLegacyStore: %v", err)
		}
		if migrated {
			t.Fatal("a width-mismatching store must not be promoted")
		}
		if reason != "generation manifest absent and the sampled embedding width does not match the current era" {
			t.Errorf("reason = %q; want the honest width-mismatch reason", reason)
		}
		// Nothing was written and nothing moved: no manifest, store intact.
		if m := readBranchManifest(branchRootPath(srcRoot, "main")); m != nil {
			t.Errorf("refused promotion stamped a manifest anyway: %+v", m)
		}
		if !pathExists(branchRootPath(srcRoot, "main")) {
			t.Error("refused promotion moved the legacy store away")
		}
	})

	t.Run("width match stamps and promotes by rename", func(t *testing.T) {
		srcRoot := buildFixture(t)
		cs := newStore(t)
		migrated, reason, err := cs.promoteLegacyStore(srcRoot, "main", key, "fp-cur", 8)
		if err != nil {
			t.Fatalf("promoteLegacyStore: %v", err)
		}
		if !migrated || reason != "" {
			t.Fatalf("migrated=%v reason=%q; want a clean promotion", migrated, reason)
		}
		snapRoot := branchRootPath(cs.root, key)
		if !pathExists(filepath.Join(snapRoot, corpusManifestName)) {
			t.Error("promotion did not publish corpus.json")
		}
		if m := readBranchManifest(snapRoot); m == nil || m.EmbeddingFP != "fp-cur" || m.Dims != 8 {
			t.Errorf("stamped manifest = %+v; want the current era (fp-cur/8) carried into the snapshot", m)
		}
		if pathExists(branchRootPath(srcRoot, "main")) {
			t.Error("legacy store was not renamed into the corpus")
		}
	})

	t.Run("agreeing sidecar chunker fingerprints are stamped", func(t *testing.T) {
		srcRoot := buildFixture(t)
		// 4-field entries whose 4th field is the chunker fingerprint.
		pruneWrite(t, fileHashesPathFor(srcRoot, "main"), `{"a.go":"hashA|120|1700000000|fp-chunk","b.go":"hashB|80|1700000000|fp-chunk"}`)
		cs := newStore(t)
		if migrated, _, err := cs.promoteLegacyStore(srcRoot, "main", key, "fp-cur", 8); err != nil || !migrated {
			t.Fatalf("promoteLegacyStore: migrated=%v err=%v", migrated, err)
		}
		m := readBranchManifest(branchRootPath(cs.root, key))
		if m == nil || m.ChunkerFP != "fp-chunk" {
			t.Errorf("stamped manifest ChunkerFP = %+v; want fp-chunk (the corpus-current adoption gate compares real values)", m)
		}
	})

	t.Run("disagreeing sidecar chunker fingerprints stay unrecorded", func(t *testing.T) {
		srcRoot := buildFixture(t)
		pruneWrite(t, fileHashesPathFor(srcRoot, "main"), `{"a.go":"hashA|120|1700000000|fp-one","b.go":"hashB|80|1700000000|fp-two"}`)
		cs := newStore(t)
		if migrated, _, err := cs.promoteLegacyStore(srcRoot, "main", key, "fp-cur", 8); err != nil || !migrated {
			t.Fatalf("promoteLegacyStore: migrated=%v err=%v", migrated, err)
		}
		m := readBranchManifest(branchRootPath(cs.root, key))
		if m == nil || m.ChunkerFP != "" {
			t.Errorf("stamped manifest ChunkerFP = %+v; want \"\" (disagreeing records must not be guessed)", m)
		}
	})

	t.Run("undecodable store is unverifiable and refused", func(t *testing.T) {
		srcRoot := buildFixture(t)
		if err := os.Remove(filepath.Join(branchRootPath(srcRoot, "main"), "deadbeefcol", "00000001.gob")); err != nil {
			t.Fatal(err)
		}
		migrated, reason, err := newStore(t).promoteLegacyStore(srcRoot, "main", key, "fp-cur", 8)
		if err != nil {
			t.Fatalf("promoteLegacyStore: %v", err)
		}
		if migrated || reason != "generation manifest absent and no document decodes to verify the embedding width" {
			t.Errorf("migrated=%v reason=%q; want the unverifiable-store refusal", migrated, reason)
		}
	})
}

// TestViewIsolation_RootAMutationsNeverChangeB pins criterion 3: edits,
// deletions, renames and untracked additions in root A land ONLY in A's
// overlay; root B's results — served from the immutable corpus through its
// own visibility — are byte-for-byte unchanged.
func TestViewIsolation_RootAMutationsNeverChangeB(t *testing.T) {
	files := map[string]string{
		"a.go":    "package main\n\n// original alpha content\n",
		"gone.go": "package main\n\n// doomed marker\n",
		"ren.go":  "package main\n\n// rename me marker\n",
		"keep.go": "package main\n\n// untouched marker\n",
	}
	h := newViewHarness(t, files)

	svcA := h.newViewService(nil)
	idxA := h.newIndexer(svcA, nil, nil)
	if err := h.attach(svcA, idxA, h.rootA, "main"); err != nil {
		t.Fatalf("attach A: %v", err)
	}
	svcB := h.newViewService(nil)
	idxB := h.newIndexer(svcB, nil, nil)
	if err := h.attach(svcB, idxB, h.rootB, "topic"); err != nil {
		t.Fatalf("attach B: %v", err)
	}

	// searchPinned searches with a must-match content pin: the fake test
	// embedder produces near-identical vectors for every chunk, so semantic
	// targeting is meaningless — the must-match filter (real production
	// behavior, applied pre-fusion) is what selects the file.
	searchPinned := func(svc *Service, q, must string) map[string]string {
		t.Helper()
		res, err := svc.HybridSearch(context.Background(), SearchOptions{Query: q, TopK: 10, Mode: ModeHybrid, MustMatch: []string{must}})
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		out := make(map[string]string, len(res))
		for _, r := range res {
			rel := relPathFromAbs(h.rootB, r.FilePath)
			if rel == "" {
				rel = relPathFromAbs(h.rootA, r.FilePath)
			}
			out[rel] = r.Content
		}
		return out
	}

	// Baseline B results.
	alphaB := searchPinned(svcB, "original alpha content", "original alpha")
	goneB := searchPinned(svcB, "doomed marker", "doomed")
	renB := searchPinned(svcB, "rename me marker", "rename me")
	if len(alphaB) == 0 || len(goneB) == 0 || len(renB) == 0 {
		t.Fatal("fixture sanity: B must find the committed files before A diverges")
	}

	// Mutate root A: edit, delete, rename, untracked.
	if err := os.WriteFile(filepath.Join(h.rootA, "a.go"), []byte("package main\n\n// MUTATED alpha content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.rootA, "gone.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(h.rootA, "ren.go"), filepath.Join(h.rootA, "ren2.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.rootA, "untracked.go"), []byte("package main\n\n// untracked only in A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := idxA.IndexIncremental(context.Background(), h.rootA); err != nil {
		t.Fatalf("A incremental after mutations: %v", err)
	}

	// A's own view reflects every mutation.
	mutA := searchPinned(svcA, "MUTATED alpha content", "MUTATED alpha")
	if len(mutA) == 0 {
		t.Error("A no longer finds its edited content")
	}
	if res := searchPinned(svcA, "doomed marker", "doomed"); len(res) != 0 {
		t.Errorf("A still serves deleted gone.go: %v", res)
	}
	if res := searchPinned(svcA, "untracked only in A", "untracked only in A"); len(res) == 0 {
		t.Error("A does not serve its untracked file")
	}

	// B's results are UNCHANGED: original content, deleted-in-A file still
	// present, old path still present, A's untracked file invisible.
	alphaAfter := searchPinned(svcB, "original alpha content", "original alpha")
	if len(alphaAfter) == 0 {
		t.Error("B lost the committed a.go content after A's edit — corpus immutability violated")
	}
	goneAfter := searchPinned(svcB, "doomed marker", "doomed")
	if len(goneAfter) == 0 {
		t.Error("B lost gone.go after A deleted it in A's tree — isolation violated")
	}
	renAfter := searchPinned(svcB, "rename me marker", "rename me")
	if len(renAfter) == 0 {
		t.Error("B lost ren.go after A renamed it — isolation violated")
	} else if _, also := renAfter["ren2.go"]; also {
		t.Error("B sees A's rename target ren2.go — overlay leak across roots")
	}
	if res := searchPinned(svcB, "untracked only in A", "untracked only in A"); len(res) != 0 {
		t.Errorf("B sees A's untracked file: %v — overlay leak across roots", res)
	}
	if res := searchPinned(svcB, "MUTATED alpha content", "MUTATED alpha"); len(res) != 0 {
		t.Errorf("B sees A's edited content: %v — corpus immutability violated", res)
	}
}

// TestCorpusFailClosed_ForeignEraNeverServed pins criterion 5: an attach
// whose corpus is unreadable (foreign embedding era) fails and the root NEVER
// receives any other snapshot's or root's collection — search errors out
// instead of serving foreign data.
// corruptSnapshotEraEverywhere rewrites EVERY era record of the snapshot's
// corpus to a foreign embedding era: the v1 corpus.json manifest AND the §3
// generation manifest (the canonical era record once the store adopted the
// chunk-record form — corrupting either one alone would leave the other
// form legitimately serving a corpus this helper means to make unreadable).
func corruptSnapshotEraEverywhere(t *testing.T, storage, snapKey string) {
	t.Helper()
	manifestPath := filepath.Join(branchRootPath(storage, snapKey), corpusManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m CorpusManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m.EmbeddingFP = "fp-of-another-era"
	if err := os.WriteFile(manifestPath, mustJSON(t, &m), 0o644); err != nil {
		t.Fatal(err)
	}
	if genDir, gen, ok := readCurrentGeneration(storage); ok {
		gen.EmbeddingFP = "fp-of-another-era"
		gd, err := json.MarshalIndent(gen, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(genDir, storeGenerationManifestName), gd, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorpusForeignEraRebuildsInsteadOfServingStale(t *testing.T) {
	files := map[string]string{
		"a.go": "package main\n\n// alpha marker\n",
	}
	h := newViewHarness(t, files)

	svcA := h.newViewService(nil)
	idxA := h.newIndexer(svcA, nil, nil)
	if err := h.attach(svcA, idxA, h.rootA, "main"); err != nil {
		t.Fatalf("attach A: %v", err)
	}

	// Corrupt the publication: a foreign embedding era. The stale corpus
	// must read UNPUBLISHED for the current era (era-aware Published) and
	// the next attach REBUILDS it under the current era — never serving
	// the foreign-era vectors (the fail-open hazard) and never wedging the
	// snapshot behind a permanent ErrCorpusEraMismatch (the pre-fix
	// fail-closed-forever hazard: no restart or reindex recovered it).
	snaps := h.snapshotDirs()
	if len(snaps) != 1 {
		t.Fatalf("fixture sanity: one corpus expected, got %v", snaps)
	}
	snapKey, ok := identityFromCollectionName(snaps[0])
	if !ok {
		t.Fatalf("fixture sanity: %q is not a corpus directory name", snaps[0])
	}
	corruptSnapshotEraEverywhere(t, h.storage, snapKey)
	// Drop the live attachment (and with it the registry's cached handle):
	// the next attach reads the corrupted manifest from disk.
	if err := svcA.Close(); err != nil {
		t.Fatalf("closing A: %v", err)
	}

	svcC := h.newViewService(nil)
	idxC := h.newIndexer(svcC, nil, nil)
	if err := h.attach(svcC, idxC, h.rootB, "topic"); err != nil {
		t.Fatalf("attach after an era change must rebuild, not wedge: %v", err)
	}
	// The republished manifest carries the CURRENT era.
	manifestPath := filepath.Join(branchRootPath(h.storage, snapKey), corpusManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("rereading the corpus manifest: %v", err)
	}
	var m CorpusManifest
	if jErr := json.Unmarshal(data, &m); jErr != nil || m.EmbeddingFP != "fp-view" {
		t.Fatalf("republished manifest = %+v (err %v); want the current era fp-view", m, jErr)
	}
	// The rebuilt corpus serves current-era results.
	res, err := svcC.HybridSearch(context.Background(), SearchOptions{Query: "alpha marker", TopK: 5})
	if err != nil || len(res) == 0 {
		t.Fatalf("search on the rebuilt corpus = %v (err %v); want results", res, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// gatingEmbedder blocks every embedding until released; the first call
// signals "the builder is inside the pass" so the test can start the racing
// attacher deterministically mid-build.
type gatingEmbedder struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	texts   []string
}

func (g *gatingEmbedder) embed(_ context.Context, text string) ([]float32, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	g.mu.Lock()
	g.texts = append(g.texts, text)
	g.mu.Unlock()
	return fakeEmbeddingFunc()(context.Background(), text)
}

// count reports how many texts this builder embedded.
func (g *gatingEmbedder) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.texts)
}

// TestCorpusBuild_SingleFlight_ConcurrentAttachers pins criterion 7: two
// roots racing for the same unpublished corpus produce exactly ONE build
// (one writer to the store); the second attacher waits for the lease and
// then opens the published corpus. A reopen AFTER a failed build retries
// the build instead of serving the half-built store.
func TestCorpusBuild_SingleFlight_ConcurrentAttachers(t *testing.T) {
	files := map[string]string{
		"a.go": "package main\n\n// alpha marker\n",
		"b.go": "package main\n\n// beta marker\n",
	}
	h := newViewHarness(t, files)

	gate := &gatingEmbedder{release: make(chan struct{}), started: make(chan struct{})}
	svcA := h.newViewService(nil)
	svcA.embeddingFunc = gate.embed
	idxA := h.newIndexer(svcA, nil, nil)
	svcB := h.newViewService(nil)
	counterB := &embedCounter{}
	svcB.embeddingFunc = func(ctx context.Context, text string) ([]float32, error) {
		counterB.mu.Lock()
		counterB.texts = append(counterB.texts, text)
		counterB.mu.Unlock()
		return fakeEmbeddingFunc()(ctx, text)
	}
	idxB := h.newIndexer(svcB, nil, nil)

	// Racer A starts FIRST and becomes the builder (its embedder blocks
	// inside the pass); racer B starts only once A is demonstrably inside
	// the build — B must wait on the lease, never open a parallel store.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = h.attach(svcA, idxA, h.rootA, "main") }()
	select {
	case <-gate.started:
	case <-time.After(20 * time.Second):
		t.Fatal("builder never entered its embedding pass")
	}
	go func() { defer wg.Done(); errs[1] = h.attach(svcB, idxB, h.rootB, "topic") }()
	// No settle barrier is needed before the release: every assertion below
	// is interleaving-independent — B either parks on the lease or finds the
	// published corpus, and in BOTH branches it never becomes a writer (the
	// deterministic waiter-branch coverage lives in
	// TestCorpusBuild_WaiterSettlesWithBuilder, which drives the lease
	// directly through its done channel).
	close(gate.release)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d attach: %v", i, err)
		}
	}

	// Exactly one corpus; the build's documents were embedded exactly once
	// per chunk by the SINGLE builder (svcA's counter counts the gated
	// path; svcB's counter must stay zero — it never built).
	snaps := h.snapshotDirs()
	if len(snaps) != 1 {
		t.Fatalf("racing attachers produced %d corpora (%v); want 1 (single-writer)", len(snaps), snaps)
	}
	if gate.count() == 0 {
		t.Error("the gated builder embedded nothing — the corpus was built by someone else (parallel writer)")
	}
	if n := counterB.count(); n != 0 {
		t.Errorf("racer B embedded %d texts while the lease was held — a parallel writer ran", n)
	}
	if col := svcB.GetCollection(); col == nil || col.Count() != 0 {
		t.Error("racer B wrote into its overlay during the attach — the wait-for-lease path built in parallel")
	}
	// The lease bookkeeping settles: no builds remain in flight.
	cs := h.registry.storeFor(h.storage)
	cs.mu.Lock()
	left := len(cs.builds)
	cs.mu.Unlock()
	if left != 0 {
		t.Errorf("%d corpus build leases still held after both attaches settled", left)
	}
}

// TestCorpusStatusPhases_PreparationMigrationIndexingUnavailable pins
// criterion 6: the progress stream distinguishes the preparation window, the
// migration-rebuild promotion of a legacy store (bytes moved, nothing
// re-embedded), the indexing pass of a cold build, and the terminal
// unavailable of a failed attach.
func TestCorpusStatusPhases_PreparationMigrationIndexingUnavailable(t *testing.T) {
	files := map[string]string{
		"a.go": "package main\n\n// alpha marker\n",
	}
	h := newViewHarness(t, files)

	// Phase A — cold build: preparation → indexing → open → …
	var phases []IndexPhase
	var states []IndexState
	svcA := h.newViewService(nil)
	idxA := h.newIndexer(svcA, &phases, &states)
	if err := h.attach(svcA, idxA, h.rootA, "main"); err != nil {
		t.Fatalf("attach A: %v", err)
	}
	assertSaw(t, phases, PhasePreparation, "cold attach must announce preparation")
	assertSaw(t, states, IndexStatePreparing, "cold attach must announce the preparing state")
	assertSaw(t, states, IndexStateIndexing, "cold attach must announce real indexing")
	// One writer per store: the registry guarantees a single manager per
	// root, so the migration phase's service may only open root A's overlay
	// after this one is down (its lexical handle owns the overlay's bolt
	// lock).
	if err := svcA.Close(); err != nil {
		t.Fatalf("closing phase-A service: %v", err)
	}

	// Phase B — migration: seed a LEGACY branch store for a second commit,
	// then attach under the view model; the rename promotion must announce
	// migration-rebuild and embed NOTHING.
	repo := h.repo
	if err := os.WriteFile(filepath.Join(h.rootA, "legacy.go"), []byte("package main\n\n// legacy marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.Git(t, "add", "legacy.go")
	repo.Git(t, "commit", "-m", "second commit")

	legacySvc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: "fp-view",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacySvc.Close() })
	if err := legacySvc.SetProject("legacy", h.storage); err != nil {
		t.Fatal(err)
	}
	if err := legacySvc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	legacyIdx := NewIndexer(IndexerConfig{Service: legacySvc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := legacyIdx.IndexFull(context.Background(), h.rootA); err != nil {
		t.Fatalf("legacy full pass: %v", err)
	}
	// The legacy service must be DOWN before the promotion: its read-write
	// lexical handle owns the bolt file lock that follows the directory
	// through the rename, and a read-only open under it would block forever.
	if err := legacySvc.Close(); err != nil {
		t.Fatalf("closing the legacy service: %v", err)
	}
	// Wipe the corpus published in phase A so the next attach MUST come from
	// the legacy promotion path.
	for _, s := range h.snapshotDirs() {
		if err := os.RemoveAll(filepath.Join(h.storage, branchRootsDirName, s)); err != nil {
			t.Fatal(err)
		}
	}

	counter := &embedCounter{}
	svcM := h.newViewService(counter)
	var mPhases []IndexPhase
	idxM := h.newIndexer(svcM, &mPhases, nil)
	if err := h.attach(svcM, idxM, h.rootA, "main"); err != nil {
		t.Fatalf("migrating attach: %v", err)
	}
	assertSaw(t, mPhases, PhaseMigration, "legacy promotion must announce migration-rebuild")
	if n := counter.count(); n != 0 {
		t.Errorf("migration re-embedded %d texts; want 0 (rename promotion moves bytes)", n)
	}

	// Phase C — unavailable: a failed attach (foreign-era corpus) surfaces
	// the terminal unavailable state through the branch-switch path.
	oid, err := CurrentCommit(context.Background(), h.rootA)
	if err != nil {
		t.Fatal(err)
	}
	corruptSnapshotEraEverywhere(t, h.storage, snapshotKey(oid))
	var uStates []IndexState
	svcU := h.newViewService(nil)
	idxU := h.newIndexer(svcU, nil, &uStates)
	if _, err := idxU.HandleBranchSwitch(context.Background(), h.rootA, "main"); err == nil {
		t.Fatal("branch switch with a foreign-era corpus succeeded; want fail-closed error")
	}
	assertSaw(t, uStates, IndexStateUnavailable, "a failed attach must surface unavailable")
}

func assertSaw[T comparable](t *testing.T, got []T, want T, msg string) {
	t.Helper()
	for _, v := range got {
		if v == want {
			return
		}
	}
	t.Errorf("%s: %v not found in %v", msg, want, got)
}

// TestEnsureCorpus_RebuildsCorpusMissingLexicalLeg is the regression for the
// "Index unavailable" dead end: a corpus.json whose lexical leg is gone is not
// a publication, because every corpus open reads that leg fail-closed. Before
// the fix the publication predicate accepted the manifest alone, so
// EnsureCorpus returned early on every attach, the open failed, the manager had
// no collection to build into, and no UI action could recover the project.
func TestEnsureCorpus_RebuildsCorpusMissingLexicalLeg(t *testing.T) {
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	storage := t.TempDir()
	const key = "snap_00112233445566aa"
	const fp = "fp-cur"

	builds := 0
	build := func() error {
		builds++
		return publishFixtureCorpus(storage, key, fp)
	}
	ctx := context.Background()

	if err := reg.EnsureCorpus(ctx, storage, key, "", fp, 8, build, nil); err != nil {
		t.Fatalf("initial EnsureCorpus: %v", err)
	}
	if builds != 1 {
		t.Fatalf("initial build count = %d, want 1", builds)
	}
	// A published, servable corpus is left alone: the second call must not
	// rebuild it.
	if err := reg.EnsureCorpus(ctx, storage, key, "", fp, 8, build, nil); err != nil {
		t.Fatalf("idempotent EnsureCorpus: %v", err)
	}
	if builds != 1 {
		t.Fatalf("build count after a healthy re-check = %d, want 1", builds)
	}

	// The binanalyzer shape: the vectors and the manifest survive, only the
	// lexical leg is gone.
	if err := os.RemoveAll(lexicalIndexPath(storage, key)); err != nil {
		t.Fatalf("remove lexical leg: %v", err)
	}
	if reg.storeFor(storage).publishedCompatible(key, fp, 8) {
		t.Fatal("a corpus without its lexical leg must not count as published")
	}
	if err := reg.EnsureCorpus(ctx, storage, key, "", fp, 8, build, nil); err != nil {
		t.Fatalf("repair EnsureCorpus: %v", err)
	}
	if builds != 2 {
		t.Fatalf("repair build count = %d, want 2 (the unservable store must be rebuilt)", builds)
	}
	if !pathExists(lexicalIndexPath(storage, key)) {
		t.Error("repair did not restore the lexical leg")
	}
	if !reg.storeFor(storage).publishedCompatible(key, fp, 8) {
		t.Error("repaired corpus is not reported as published")
	}
}

// TestPromoteLegacyStore_RefusesMissingLexicalLeg pins the PUBLISH-side half of
// the same contract: a legacy store without a lexical leg must never be
// promoted, because the publication it would write can never be opened.
func TestPromoteLegacyStore_RefusesMissingLexicalLeg(t *testing.T) {
	srcRoot := t.TempDir()
	legacyRoot := branchRootPath(srcRoot, "main")
	colDir := filepath.Join(legacyRoot, "deadbeefcol")
	if err := os.MkdirAll(colDir, 0o750); err != nil {
		t.Fatalf("mkdir collection: %v", err)
	}
	writeLegacyDocGob(t, filepath.Join(colDir, "00000001.gob"), 8)
	pruneWrite(t, fileHashesPathFor(srcRoot, "main"), `{"a.go":"h1"}`)
	pruneWrite(t, contentlessMarkerPathFor(srcRoot, "main"), "")

	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	cs := reg.storeFor(t.TempDir())
	const key = "snap_aaaa0000bbbb1111"

	migrated, reason, err := cs.promoteLegacyStore(srcRoot, "main", key, "fp-cur", 8)
	if err != nil {
		t.Fatalf("promoteLegacyStore: %v", err)
	}
	if migrated || reason != "lexical index leg absent" {
		t.Fatalf("migrated=%v reason=%q; want the missing-lexical-leg refusal", migrated, reason)
	}
	if pathExists(branchRootPath(cs.root, key)) || pathExists(filepath.Join(branchRootPath(cs.root, key), corpusManifestName)) {
		t.Error("a refused promotion must not create or publish a corpus target")
	}
	if !pathExists(legacyRoot) {
		t.Error("a refused promotion must leave the legacy source in place")
	}
}

// TestPublishedCompatible_RequiresSnapshotLexicalLeg pins the §3 half of the
// publication predicate: a snapshot table is only a publication when the
// lexical leg the §3 open reads exists in one of its two documented locations.
func TestPublishedCompatible_RequiresSnapshotLexicalLeg(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_abcdef0123456789"
	c := chunkRecordKey("package main")
	if err := writeChunkRecord(h.gen, c, []float32{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Fatalf("writeChunkRecord: %v", err)
	}
	rows := []occurrenceRow{{
		Path:     "a.go",
		BlobHash: "h",
		Chunks:   []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 2, Language: "go"}},
	}}
	if err := writeSnapshotTable(h.gen, key, 8, rows, &snapshotTable{Files: 1, Chunks: 1, UniqueChunks: 1}, nil); err != nil {
		t.Fatalf("writeSnapshotTable: %v", err)
	}

	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	cs := reg.storeFor(h.storage)

	if cs.publishedCompatible(key, "fp-era-a", 8) {
		t.Fatal("a §3 table without any lexical leg must not count as published")
	}
	// The native location: a rebuild lands the lexical index inside the
	// snapshot directory.
	if err := os.MkdirAll(filepath.Join(snapshotDir(h.gen, key), "lexical"), 0o750); err != nil {
		t.Fatalf("mkdir snapshot lexical: %v", err)
	}
	if !cs.publishedCompatible(key, "fp-era-a", 8) {
		t.Error("a §3 table with the snapshot lexical leg must count as published")
	}
	// The migrated location: the v1→v2 migration leaves bleve state at the
	// legacy per-key path until a rebuild lands it natively.
	if err := os.RemoveAll(filepath.Join(snapshotDir(h.gen, key), "lexical")); err != nil {
		t.Fatalf("remove snapshot lexical: %v", err)
	}
	if err := os.MkdirAll(lexicalIndexPath(h.storage, key), 0o750); err != nil {
		t.Fatalf("mkdir legacy lexical: %v", err)
	}
	if !cs.publishedCompatible(key, "fp-era-a", 8) {
		t.Error("a §3 table with the legacy per-key lexical leg must count as published")
	}
}

// publishFixtureCorpus writes the minimal servable corpus shape (vectors
// excluded — no test here reads them): the snapshot store, its lexical leg and
// the publication manifest.
func publishFixtureCorpus(storage, key, embeddingFP string) error {
	snapRoot := branchRootPath(storage, key)
	if err := os.MkdirAll(snapRoot, 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(lexicalIndexPath(storage, key), 0o750); err != nil {
		return err
	}
	return writeCorpusManifest(snapRoot, &CorpusManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: embeddingFP,
		Dims:        8,
		Collection:  collectionName(key),
		Files:       map[string]string{"a.go": "h1"},
	})
}

// TestRepairable_ClassifiesProvenDefectsAndTransientFaults pins the single
// classifier the repair path (rebuild, unpublish, re-embed) consults: a
// proven store defect is repairable, while a transient OS fault — permissions,
// no space, fd exhaustion, disk/AV I/O, a cancelled context — proves nothing
// about the store's content and must leave every publication intact.
func TestRepairable_ClassifiesProvenDefectsAndTransientFaults(t *testing.T) {
	proven := []error{
		fs.ErrNotExist,
		io.EOF,
		io.ErrUnexpectedEOF,
		errChunkRecordTorn,
		ErrCorpusNotPublished,
		ErrCorpusEraMismatch,
		ErrCorpusLegMissing,
		ErrCorpusUnopenable,
		ErrOverlayUnopenable,
		ErrCorpusNoIndexableFiles,
		fmt.Errorf("opening chunk record: %w", errChunkRecordTorn),
		&fs.PathError{Op: "open", Path: "a.chunk", Err: fs.ErrNotExist},
	}
	for _, err := range proven {
		if !Repairable(err) {
			t.Errorf("Repairable(%v) = false; want a proven defect to be repairable", err)
		}
	}

	transient := []error{
		fs.ErrPermission,
		syscall.EACCES,
		syscall.ENOSPC,
		syscall.EMFILE,
		syscall.ENFILE,
		syscall.EIO,
		&fs.PathError{Op: "open", Path: "a.chunk", Err: syscall.EACCES},
		&fs.PathError{Op: "write", Path: "a.chunk", Err: syscall.ENOSPC},
		context.Canceled,
		context.DeadlineExceeded,
	}
	for _, err := range transient {
		if Repairable(err) {
			t.Errorf("Repairable(%v) = true; a transient fault must never be repaired", err)
		}
	}

	if Repairable(nil) {
		t.Error("Repairable(nil) = true; an absent error proves nothing")
	}
}

// TestWriteSnapshotTable_ReembedsDamagedChunkRecords pins the same-pass repair:
// a chunk record that the orphan sweep collected mid-build (missing) or that is
// torn is re-derived from the build store and landed before the table publishes,
// so the build stops deferring forever over damage it can fix itself.
func TestWriteSnapshotTable_ReembedsDamagedChunkRecords(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_abcdef0123456789"

	intactC := chunkRecordKey("package main")
	intactVec := chunkVec(intactC)
	if err := writeChunkRecord(h.gen, intactC, intactVec); err != nil {
		t.Fatalf("writeChunkRecord(intact): %v", err)
	}
	intactBytes, err := os.ReadFile(chunkRecordPath(h.gen, intactC))
	if err != nil {
		t.Fatalf("read intact record: %v", err)
	}

	tornC := chunkRecordKey("func torn() {}")
	if err := os.MkdirAll(filepath.Dir(chunkRecordPath(h.gen, tornC)), 0o750); err != nil {
		t.Fatalf("mkdir torn shard: %v", err)
	}
	if err := os.WriteFile(chunkRecordPath(h.gen, tornC), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatalf("write torn record: %v", err)
	}

	lostC := chunkRecordKey("func lost() {}") // never written: an orphan-sweep victim

	rows := []occurrenceRow{{
		Path:     "a.go",
		BlobHash: "h",
		Chunks: []chunkOccurrence{
			{C: lostC, Index: 0, Start: 1, End: 2, Language: "go"},
			{C: tornC, Index: 1, Start: 3, End: 4, Language: "go"},
			{C: intactC, Index: 2, Start: 5, End: 6, Language: "go"},
		},
	}}
	repaired := map[int]string{}
	repair := func(path string, chunkIndex int) ([]float32, error) {
		repaired[chunkIndex] = path
		return chunkVec(chunkRecordKey(fmt.Sprintf("repair-%d", chunkIndex))), nil
	}

	if err := writeSnapshotTable(h.gen, key, 8, rows, &snapshotTable{
		ChunkerFP: "k", IgnoreFP: "i", Files: 1, Chunks: 3, UniqueChunks: 3,
	}, repair); err != nil {
		t.Fatalf("writeSnapshotTable with repairable damage: %v", err)
	}

	if len(repaired) != 2 || repaired[0] != "a.go" || repaired[1] != "a.go" {
		t.Errorf("repair calls = %v; want exactly the lost (0) and torn (1) occurrences", repaired)
	}
	for _, c := range []string{lostC, tornC} {
		vec, readErr := readChunkRecord(h.gen, c, 8)
		if readErr != nil {
			t.Fatalf("repaired record %s is unreadable: %v", c, readErr)
		}
		if len(vec) != 8 {
			t.Errorf("repaired record %s has %d dims; want 8", c, len(vec))
		}
	}
	after, err := os.ReadFile(chunkRecordPath(h.gen, intactC))
	if err != nil {
		t.Fatalf("read intact record after repair pass: %v", err)
	}
	if !bytes.Equal(after, intactBytes) {
		t.Error("the repair pass rewrote an intact chunk record")
	}
	if !snapshotTablePublished(h.gen, key) {
		t.Error("the repaired snapshot table did not publish")
	}
	if gotRows, _, ok := readSnapshotTable(h.gen, key); !ok || len(gotRows) != 1 || len(gotRows[0].Chunks) != 3 {
		t.Errorf("published table = %+v ok=%v; want the repaired 3-occurrence row", gotRows, ok)
	}
}

// TestWriteSnapshotTable_TransientFaultDefersWithoutDestroying pins the other
// half of the classifier: an unreadable record whose failure is a transient OS
// fault is never removed or re-embedded (the record may be perfectly fine), and
// the publish defers instead of landing a table over an unproven record.
func TestWriteSnapshotTable_TransientFaultDefersWithoutDestroying(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_0123456789abcdef"

	c := chunkRecordKey("package transient")
	// A regular file where the record's SHARD directory belongs makes every
	// stat below it fail with ENOTDIR — a transient I/O fault, not damage.
	shard := filepath.Dir(chunkRecordPath(h.gen, c))
	if err := os.WriteFile(shard, []byte("planted"), 0o644); err != nil {
		t.Fatalf("plant shard file: %v", err)
	}

	calls := 0
	repair := func(string, int) ([]float32, error) {
		calls++
		return chunkVec(c), nil
	}
	rows := []occurrenceRow{{
		Path:     "a.go",
		BlobHash: "h",
		Chunks:   []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 2, Language: "go"}},
	}}
	err := writeSnapshotTable(h.gen, key, 8, rows, &snapshotTable{Files: 1, Chunks: 1, UniqueChunks: 1}, repair)
	if err == nil {
		t.Fatal("writeSnapshotTable published over a transiently unreadable record")
	}
	if !strings.Contains(err.Error(), "transient") {
		t.Errorf("defer error = %v; want it to name the transient fault", err)
	}
	if calls != 0 {
		t.Errorf("repair ran %d time(s) on a transient fault; the record must be left untouched", calls)
	}
	if !pathExists(shard) {
		t.Error("the planted shard was removed; a transient fault must not destroy store state")
	}
	if snapshotTablePublished(h.gen, key) {
		t.Error("the snapshot published despite the deferred record")
	}
}

// TestPublishCorpus_RefusesCorpusWithoutLexicalLeg closes the SECOND entry of
// the "published but never opens" defect: a corpus build whose lexical leg did
// not open must not publish, because every corpus open reads that leg
// fail-closed. The refusal names the leg and the recorded root cause, and the
// snapshot stays unpublished so the next EnsureCorpus rebuilds it.
func TestPublishCorpus_RefusesCorpusWithoutLexicalLeg(t *testing.T) {
	storage := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ctx := context.Background()
	const key = "snap_abcdef0123456789"

	svc.mu.RLock()
	lexDir := svc.workingLexicalDirLocked(key)
	svc.mu.RUnlock()
	// Injection: a regular file where the bleve index directory belongs makes
	// lexical.Open fail deterministically.
	if err := os.MkdirAll(filepath.Dir(lexDir), 0o750); err != nil {
		t.Fatalf("mkdir lexical parent: %v", err)
	}
	if err := os.WriteFile(lexDir, []byte("not a bleve index"), 0o644); err != nil {
		t.Fatalf("plant lexical file: %v", err)
	}

	if err := svc.SwitchBranch(ctx, key); err != nil {
		t.Fatalf("SwitchBranch must stay fail-soft on a broken lexical leg: %v", err)
	}
	svc.mu.RLock()
	lexicalErr := svc.lastLexicalOpenErr
	liveLeg := svc.current.lexical != nil
	fp, dims := svc.embeddingCacheFingerprint, svc.embeddingDimension
	svc.mu.RUnlock()
	if liveLeg || lexicalErr == nil {
		t.Fatalf("injection did not take: live=%v recorded error=%v", liveLeg, lexicalErr)
	}

	pubErr := svc.PublishCorpus(ctx, key)
	if !errors.Is(pubErr, ErrCorpusLegMissing) {
		t.Fatalf("PublishCorpus error = %v; want ErrCorpusLegMissing", pubErr)
	}
	if !errors.Is(pubErr, lexicalErr) {
		t.Errorf("publish error does not carry the lexical.Open root cause %v: %v", lexicalErr, pubErr)
	}
	if !strings.Contains(pubErr.Error(), lexDir) {
		t.Errorf("publish error %v does not name the missing leg %s", pubErr, lexDir)
	}
	if m := readCorpusManifest(branchRootPath(storage, key)); m != nil {
		t.Fatalf("a refused publish landed a manifest: %+v", m)
	}
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	if reg.storeFor(storage).publishedCompatible(key, fp, dims) {
		t.Error("a refused publish must leave the snapshot unpublished (the rebuild gate depends on it)")
	}

	// Control: with a working leg the SAME publish passes the gate and lands.
	if err := os.Remove(lexDir); err != nil {
		t.Fatalf("remove planted lexical file: %v", err)
	}
	svc.CloseWorkingStore()
	if err := svc.SwitchBranch(ctx, key); err != nil {
		t.Fatalf("SwitchBranch with a working lexical leg: %v", err)
	}
	svc.mu.Lock()
	svc.current.fileHashes = map[string]string{
		"a.go": corpusAdoptionEntry("deadbeef", 1, 2, svc.chunkerFingerprint),
	}
	liveLeg = svc.current.lexical != nil
	svc.mu.Unlock()
	if !liveLeg {
		t.Fatalf("control leg did not open: %v", lexicalErr)
	}
	if err := svc.PublishCorpus(ctx, key); err != nil {
		t.Fatalf("PublishCorpus with a live lexical leg: %v", err)
	}
	if m := readCorpusManifest(branchRootPath(storage, key)); m == nil || len(m.Files) != 1 {
		t.Fatalf("a successful publish did not land the manifest: %+v", m)
	}
	if !reg.storeFor(storage).publishedCompatible(key, fp, dims) {
		t.Error("the published corpus is not reported as published")
	}
}

// TestPublishCorpus_RepairsSweptChunkRecord is the end-to-end wiring of the
// same-pass repair: the §6 sweep collected a chunk record the build had
// already committed, and the publish re-derives it from the build store's own
// document instead of deferring forever. specs/domains/workspace.md (§3 chunk
// records; a working store's records are re-derivable from its collection).
func TestPublishCorpus_RepairsSweptChunkRecord(t *testing.T) {
	storage := t.TempDir()
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      fakeEmbeddingFunc(),
		Corpus:             reg,
		EmbeddingDimension: 8,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ctx := context.Background()
	const key = "snap_abcdef0123456789"
	if err := svc.SwitchBranch(ctx, key); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	content := "func repaired() {}\n"
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}
	col := svc.GetCollection()
	if col == nil {
		t.Fatal("the corpus build store did not open")
	}
	if addErr := col.AddDocuments(ctx, []chromem.Document{{
		ID:        DocumentID("a.go", 0),
		Content:   content,
		Embedding: vec,
		Metadata: map[string]string{
			relPathMetaKey: "a.go", "content_hash": "deadbeef",
			"start_line": "1", "end_line": "2", "language": "go",
		},
	}}, 1); addErr != nil {
		t.Fatalf("committing the build store's document: %v", addErr)
	}

	c := chunkRecordKey(content)
	svc.mu.Lock()
	gen := svc.current.storeGen
	svc.current.chunkRows = map[string]occurrenceRow{"a.go": {
		Path:     "a.go",
		BlobHash: "deadbeef",
		Chunks:   []chunkOccurrence{{C: c, Index: 0, Start: 1, End: 2, Language: "go"}},
	}}
	svc.current.fileHashes = map[string]string{
		"a.go": corpusAdoptionEntry("deadbeef", 1, 2, svc.chunkerFingerprint),
	}
	svc.mu.Unlock()
	if gen == "" {
		t.Fatal("the §3 generation was not adopted; the repair path needs it")
	}
	if chunkRecordExists(gen, c) {
		t.Fatal("fixture must start without the record — the orphan sweep already collected it")
	}

	if err := svc.PublishCorpus(ctx, key); err != nil {
		t.Fatalf("PublishCorpus over a swept chunk record: %v", err)
	}
	got, err := readChunkRecord(gen, c, 8)
	if err != nil {
		t.Fatalf("the repaired chunk record is unreadable: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("repaired record has %d dims; want %d", len(got), len(vec))
	}
	// chromem normalizes every committed vector, so the build store's copy is
	// the L2-normalized form of what the fixture committed.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		want := float32(float64(vec[i]) / norm)
		if math.Abs(float64(got[i]-want)) > 1e-6 {
			t.Fatalf("repaired vector[%d] = %v; want the build store's %v", i, got[i], want)
		}
	}
	if !snapshotTablePublished(gen, key) {
		t.Error("the repaired snapshot table did not publish")
	}
	if m := readCorpusManifest(branchRootPath(storage, key)); m == nil {
		t.Error("the repaired corpus did not publish its manifest")
	}
}
