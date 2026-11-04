package vectorindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This file pins the ADR-104 identity-model acceptance criteria
// (specs/decisions/104-content-addressed-vector-store.md):
//
//  1. compatible shared chunks are not re-embedded (content-addressed
//     inference: duplicates never reach the embedder);
//  2. identical bytes at two paths never lose an occurrence (occurrence
//     identity is (path, chunk), never content alone);
//  3. root-local size/mtime facts are never shared between trees (sidecar
//     v6 root scope);
//  4. ignore/eligibility is applied per-root and never bypassed by the
//     stored corpus (the walk's verdict evicts stored entries);
//  5. only consistent generations are published (an incompatible era is
//     reset atomically-with-open, never mixed).

// newIdentityService returns a persisted service (its own project storage
// temp dir) with the main branch opened, configured with the given
// embedding-era fingerprint.
func newIdentityService(t *testing.T, fingerprint string) (svc *Service, projectPath string) {
	t.Helper()
	proj := t.TempDir()
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("identity", proj); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	return svc, proj
}

// TestIdenticalBytes_TwoPaths_TwoOccurrencesSharedEmbedding pins criteria
// (1) and (2): two files with identical bytes at different paths index as
// TWO occurrences — both stored, both searchable — while the duplicate
// chunk never reaches the embedder: the batch path resolves embeddings by
// content (and the persistent cache extends the same content key across
// batches and generations).
func TestIdenticalBytes_TwoPaths_TwoOccurrencesSharedEmbedding(t *testing.T) {
	ws := t.TempDir()
	content := []byte("package dup\n\n// shared chunk payload line\n")
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "dup.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "sub", "dup.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	embedder := &fakeBatchEmbedder{}
	svc, _ := newIdentityService(t, "fp-identity")
	svc.batchEmbedder = embedder

	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})
	if err := idx.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Criterion 2: both occurrences exist — distinct IDs, both sidecar rows,
	// both lexical entries.
	idRoot := DocumentID("dup.go", 0)
	idSub := DocumentID("sub/dup.go", 0)
	col := svc.GetCollection()
	if col.Count() != 2 {
		t.Fatalf("collection holds %d docs; want 2 occurrences", col.Count())
	}
	for _, id := range []string{idRoot, idSub} {
		if _, err := col.GetByID(context.Background(), id); err != nil {
			t.Errorf("occurrence %s lost: %v", id, err)
		}
	}
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	if files["dup.go"] == "" || files["sub/dup.go"] == "" {
		t.Errorf("sidecar lost an occurrence row: %v", files)
	}
	lex := svc.GetLexical()
	if lex == nil {
		t.Fatalf("lexical index not open")
	}
	lexCount, err := lex.Count()
	if err != nil {
		t.Fatalf("lexical count: %v", err)
	}
	if lexCount != 2 {
		t.Errorf("lexical index holds %d entries; want both occurrences", lexCount)
	}

	// Criterion 1: the shared chunk reached the embedder exactly once — the
	// duplicate occurrence was served by content addressing, never
	// re-embedded.
	embedder.mu.Lock()
	embeddedTexts := 0
	for _, call := range embedder.calls {
		embeddedTexts += len(call)
	}
	embedder.mu.Unlock()
	if embeddedTexts != 1 {
		t.Errorf("identical chunk was embedded %d times; want 1 (content addressing serves the duplicate)", embeddedTexts)
	}

	// Both occurrences resolve through per-request hydration to the bound
	// tree and both surface in search.
	results, err := svc.HybridSearch(context.Background(), SearchOptions{Query: "shared chunk payload", TopK: 5, Mode: ModeLexical})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.FilePath] = true
	}
	if !seen[filepath.Join(ws, "dup.go")] || !seen[filepath.Join(ws, "sub", "dup.go")] {
		t.Errorf("search did not surface both occurrences: %v", seen)
	}
}

// TestSidecarV6_RootScopedStatFactsNeverCrossTrees pins criterion (3): the
// sidecar's size/mtime fast-path facts are scoped to one workspace root by
// fingerprint. A second tree — same relative layout, byte-identical files,
// DIFFERENT mtimes — must never validate the first tree's sidecar: the
// foreign file is discarded and re-derived from disk, never trusted by stat.
func TestSidecarV6_RootScopedStatFactsNeverCrossTrees(t *testing.T) {
	treeA := t.TempDir()
	treeB := t.TempDir()
	content := []byte("package scoped\n")
	for _, dir := range []string{treeA, treeB} {
		if err := os.WriteFile(filepath.Join(dir, "a.go"), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Index tree A; its sidecar lands on disk with A's root fingerprint.
	svcA, proj := newIdentityService(t, "fp-scoped")
	idxA := NewIndexer(IndexerConfig{Service: svcA, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := idxA.IndexFull(context.Background(), treeA); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	if err := svcA.SetProject("away", t.TempDir()); err != nil { // park+flush the sidecar
		t.Fatalf("SetProject away: %v", err)
	}
	sidecarPath := fileHashesPathFor(proj, "main")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("sidecar not persisted: %v", err)
	}
	var env sidecarFileV6
	if err := json.Unmarshal(data, &env); err != nil || env.Version != sidecarVersionV6 {
		t.Fatalf("sidecar is not the v6 envelope: %v", err)
	}
	if env.Root == "" || env.Root != rootFingerprint(treeA) {
		t.Errorf("sidecar root scope = %q; want tree A's fingerprint", env.Root)
	}

	// The decode helper itself must reject a mismatched scope, and a service
	// bound to tree B must surface none of A's entries.
	if entries, ok := decodeSidecarEntries(data, rootFingerprint(treeB)); ok {
		t.Errorf("decodeSidecarEntries accepted another root's sidecar: %v", entries)
	}
	svcB, _ := newIdentityService(t, "fp-scoped")
	if err := svcB.SetProject("identity", proj); err != nil {
		t.Fatalf("SetProject B: %v", err)
	}
	svcB.SetWorkspaceRoot(treeB)
	if err := svcB.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch B: %v", err)
	}
	hashes, err := svcB.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	for key := range hashes {
		t.Errorf("foreign-root sidecar leaked entry %q into tree B's state", key)
	}
}

// TestGenerationGate_IncompatibleEraIsNeverMixed pins criterion (5): a
// branch store whose generation manifest belongs to another embedding era
// is RESET before the open — its documents are never served beside the
// current coordinates — while a compatible generation round-trips intact.
func TestGenerationGate_IncompatibleEraIsNeverMixed(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package era\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Era 1 indexes under fingerprint "fp-old"; the service is then CLOSED —
	// an embedding-era change is an app-update event, observed across
	// process restarts, never as two live owners of one storage root.
	svcOld, proj := newIdentityService(t, "fp-old")
	idxOld := NewIndexer(IndexerConfig{Service: svcOld, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := idxOld.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull (old era): %v", err)
	}
	if got := svcOld.GetCollection().Count(); got != 1 {
		t.Fatalf("old era indexed %d docs; want 1", got)
	}
	if err := svcOld.Close(); err != nil {
		t.Fatalf("Close (old era): %v", err)
	}

	// A new era (embedding model update) opens the SAME storage: the gate
	// must discard the old generation — nothing of it may be served — and
	// publish a fresh manifest before any document lands.
	svcNew, _ := newIdentityService(t, "fp-new")
	if err := svcNew.SetProject("identity", proj); err != nil {
		t.Fatalf("SetProject (new era): %v", err)
	}
	if err := svcNew.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch (new era): %v", err)
	}
	if got := svcNew.GetCollection().Count(); got != 0 {
		t.Fatalf("new era served %d documents of the old generation; want 0", got)
	}
	manifest := readBranchManifest(branchRootPath(proj, "main"))
	if manifest == nil || manifest.EmbeddingFP != "fp-new" || manifest.Coords != coordsRelativeV1 {
		t.Fatalf("fresh generation manifest = %+v; want the new era's", manifest)
	}

	// The new era re-indexes, and a same-era reopen round-trips the
	// generation without a reset.
	idxNew := NewIndexer(IndexerConfig{Service: svcNew, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := idxNew.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull (new era): %v", err)
	}
	if got := svcNew.GetCollection().Count(); got != 1 {
		t.Fatalf("new era re-index produced %d docs; want 1", got)
	}
	if err := svcNew.Close(); err != nil {
		t.Fatalf("Close (new era): %v", err)
	}
	svcRe, _ := newIdentityService(t, "fp-new")
	if err := svcRe.SetProject("identity", proj); err != nil {
		t.Fatalf("SetProject (reopen): %v", err)
	}
	if err := svcRe.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch (reopen): %v", err)
	}
	if got := svcRe.GetCollection().Count(); got != 1 {
		t.Fatalf("compatible generation was reset on reopen: %d docs; want 1", got)
	}
}

// TestPerRootIgnorePolicy_EvictsStoredCorpusEntries pins criterion (4): the
// stored corpus never bypasses the CURRENT root's ignore/eligibility policy
// — files a stricter policy excludes are reported deleted by the validation
// walk and evicted from documents and sidecar by the incremental pass.
func TestPerRootIgnorePolicy_EvictsStoredCorpusEntries(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "notes.log"), []byte("log line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, _ := newIdentityService(t, "fp-ignore")
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})
	if err := idx.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	if files["notes.log"] == "" || files["main.go"] == "" {
		t.Fatalf("setup: both files must be indexed first: %v", files)
	}

	// The SAME tree tightens its policy ("*.log" excluded): the walk's
	// verdict must win over the stored corpus — notes.log is reported
	// deleted and evicted; main.go untouched.
	strict := NewIndexer(IndexerConfig{
		Service:         svc,
		ChunkFn:         fakeChunkFunc,
		HashFn:          fakeHashFunc,
		ExcludePatterns: []string{"*.log"},
	})
	stale, newF, deleted, valErr := svc.ValidateCollection(context.Background(), ws, strict.resolverFor(ws), false)
	if valErr != nil {
		t.Fatalf("ValidateCollection: %v", valErr)
	}
	if len(stale) != 0 || len(newF) != 0 || len(deleted) != 1 || filepath.Base(deleted[0]) != "notes.log" {
		t.Fatalf("per-root policy not applied: stale=%v new=%v deleted=%v", stale, newF, deleted)
	}
	if err := strict.IndexIncremental(context.Background(), ws); err != nil {
		t.Fatalf("IndexIncremental: %v", err)
	}
	files2, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	if files2["notes.log"] != "" {
		t.Errorf("excluded file survived in the stored corpus sidecar: %v", files2)
	}
	if files2["main.go"] == "" {
		t.Errorf("still-eligible file evicted: %v", files2)
	}
	if got := svc.GetCollection().Count(); got != 1 {
		t.Errorf("collection holds %d docs after eviction; want 1", got)
	}
}

// TestDocumentID_RootRelativeIdentity pins the L2 coordinate itself: the
// same relative layout under two different absolute roots yields identical
// IDs (shareable identity), and different relative paths never collide.
func TestDocumentID_RootRelativeIdentity(t *testing.T) {
	if DocumentID("pkg/a.go", 0) != DocumentID(filepath.ToSlash("pkg"+string(filepath.Separator)+"a.go"), 0) {
		t.Error("DocumentID not stable across separator spellings of one coordinate")
	}
	if DocumentID("pkg/a.go", 0) == DocumentID("pkg/a.go", 1) {
		t.Error("chunk index collision")
	}
	if DocumentID("pkg/a.go", 0) == DocumentID("other/pkg/a.go", 0) {
		t.Error("distinct occurrences collapsed — identical bytes must keep two IDs")
	}
	if relPathFromAbs("/ws", "/ws/pkg/a.go") != "pkg/a.go" {
		t.Error("relPathFromAbs produced a non-relative coordinate")
	}
	if relPathFromAbs("/ws", "/elsewhere/a.go") != "" {
		t.Error("path outside the root must not produce a coordinate")
	}
	if rootFingerprint("/ws") == rootFingerprint("/other") {
		t.Error("distinct roots share a fingerprint")
	}
}
