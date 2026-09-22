package vectorindex

import (
	"context"
	"encoding/gob"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// TestService_BranchScopedLazyOpen is the ADR-064 acceptance test. It seeds a
// LEGACY storage layout (one chromem collection per branch directly under the
// project's vector-index storage root — what a pre-ADR-064 NewPersistentDB at
// that root decoded in full), then proves:
//
//   - SetProject opens NO persistent DB (the open is lazy);
//   - activating a branch opens exactly ONE DB, on that branch's own root;
//   - the multi-collection storage root is NEVER handed to chromem again;
//   - the active branch's documents are actually loaded (legacy layout works);
//   - the inactive branches were migrated to their per-branch roots on disk,
//     unread — so the open cost depends on one branch, not on branch count.
func TestService_BranchScopedLazyOpen(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbeddingFunc()
	root := t.TempDir()

	// Seed the legacy layout: a collection per branch directly under the root.
	for _, br := range []string{"main", "feature"} {
		db, err := chromem.NewPersistentDB(root, false)
		if err != nil {
			t.Fatalf("seed legacy DB: %v", err)
		}
		col, err := db.GetOrCreateCollection(collectionName(br), nil, emb)
		if err != nil {
			t.Fatalf("seed legacy collection %s: %v", br, err)
		}
		doc := chromem.Document{
			ID:        "d-" + br,
			Content:   "content of " + br,
			Embedding: []float32{1, 0, 0, 0, 0, 0, 0, 0},
		}
		if err := col.AddDocument(ctx, doc); err != nil {
			t.Fatalf("seed doc %s: %v", br, err)
		}
	}

	counter := installPersistentDBOpenCounter(t)

	svc, err := NewService(ServiceConfig{EmbeddingFunc: emb})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("p", root); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if got := counter.total(); got != 0 {
		t.Fatalf("SetProject opened %d persistent DBs, want 0 (the open is lazy)", got)
	}

	// Activating a branch decodes exactly that branch, from its own root.
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}
	if got := counter.count(root); got != 0 {
		t.Errorf("the multi-collection storage root was opened %d times, want 0", got)
	}
	if got := counter.count(branchRootPath(root, "main")); got != 1 {
		t.Errorf("main branch root opened %d times, want 1", got)
	}
	if got := counter.total(); got != 1 {
		t.Errorf("total persistent DB opens = %d, want 1 (only the active branch)", got)
	}
	if col := svc.GetCollection(); col == nil || col.Count() != 1 {
		t.Errorf("main collection = %v, want 1 document loaded from the legacy layout", col)
	}

	// The inactive branch's legacy directory was migrated into its own root —
	// on disk and unread (no open was counted for it above). Its collection
	// lives in chromem's hashed subdirectory, exactly as when it was seeded.
	if entries, derr := os.ReadDir(branchRootPath(root, "feature")); derr != nil || len(entries) != 1 {
		t.Errorf("feature branch root not migrated: err=%v entries=%d", derr, len(entries))
	}

	// Switching to the other branch decodes exactly that branch too.
	if err := svc.SwitchBranch(ctx, "feature"); err != nil {
		t.Fatalf("SwitchBranch feature: %v", err)
	}
	if got := counter.count(branchRootPath(root, "feature")); got != 1 {
		t.Errorf("feature branch root opened %d times, want 1", got)
	}
	if col := svc.GetCollection(); col == nil || col.Count() != 1 {
		t.Errorf("feature collection = %v, want 1 document", col)
	}
	if got := counter.count(root); got != 0 {
		t.Errorf("the storage root was opened %d times, want 0", got)
	}
}

// writeLegacyCollectionMetadata writes the chromem per-collection metadata
// file (`00000000.gob`, uncompressed) that the legacy migration reads the
// collection name back from.
func writeLegacyCollectionMetadata(t *testing.T, dir, name string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, chromemMetadataFileName))
	if err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := gob.NewEncoder(f).Encode(chromemCollectionMetadata{Name: name}); err != nil {
		t.Fatalf("encode metadata: %v", err)
	}
}

// TestMigrateLegacyLayout_OrphansUnnameableDirectories covers the migration's
// fail-safe: a legacy collection directory whose name cannot be recovered (no
// metadata file) or is not in collectionName's shape (a hostile or corrupted
// value) is parked under branches/_orphan/ — never joined into a path, so it
// can neither escape the storage root nor hijack a real branch's root.
func TestMigrateLegacyLayout_OrphansUnnameableDirectories(t *testing.T) {
	root := t.TempDir()

	noMeta := filepath.Join(root, "01234567")
	if err := os.MkdirAll(noMeta, 0o750); err != nil {
		t.Fatalf("mkdir noMeta: %v", err)
	}
	hostile := filepath.Join(root, "89abcdef")
	if err := os.MkdirAll(hostile, 0o750); err != nil {
		t.Fatalf("mkdir hostile: %v", err)
	}
	writeLegacyCollectionMetadata(t, hostile, "../../escape")

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	svc.migrateLegacyLayoutLocked(root)

	orphans, err := os.ReadDir(filepath.Join(root, branchRootsDirName, orphanBranchRootName))
	if err != nil {
		t.Fatalf("orphan root not created: %v", err)
	}
	if len(orphans) != 2 {
		t.Errorf("orphan root holds %d entries, want 2 (both unnameable directories)", len(orphans))
	}
	for _, legacy := range []string{"01234567", "89abcdef"} {
		if _, statErr := os.Stat(filepath.Join(root, legacy)); !os.IsNotExist(statErr) {
			t.Errorf("legacy directory %s was not moved out of the storage root (stat err = %v)", legacy, statErr)
		}
	}
	// The hostile name must not have been honoured as a path.
	if _, statErr := os.Stat(filepath.Join(root, "..", "escape")); !os.IsNotExist(statErr) {
		t.Errorf("migration escaped the storage root (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(root, branchRootsDirName, "..", "..", "escape")); !os.IsNotExist(statErr) {
		t.Errorf("migration created a traversal destination (stat err = %v)", statErr)
	}
}

// TestSwitchBranch_OpenFailureDoesNotServeStaleBranch pins the fail-closed half
// of ADR-064 step (a): SwitchBranch releases the outgoing branch's residents
// BEFORE opening the incoming one, so a failed open leaves no collection behind
// instead of silently serving the previous branch while the worktree has moved
// on to the new one.
func TestSwitchBranch_OpenFailureDoesNotServeStaleBranch(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbeddingFunc()
	root := t.TempDir()

	svc, err := NewService(ServiceConfig{EmbeddingFunc: emb})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("p", root); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}
	if col := svc.GetCollection(); col == nil {
		t.Fatal("main collection missing after a successful switch")
	}

	// Block the target branch's root: MkdirAll over a regular file fails, so
	// openBranchDBLocked cannot open it.
	blocked := branchRootPath(root, "feature")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	if err := svc.SwitchBranch(ctx, "feature"); err == nil {
		t.Fatal("SwitchBranch to a blocked branch root must fail")
	}
	if col := svc.GetCollection(); col != nil {
		t.Errorf("stale branch collection still served after a failed open: %v", col)
	}
	if db := svc.GetDB(); db != nil {
		t.Error("stale branch DB still resident after a failed open")
	}
}

// TestSwitchBranch_CollectionCreateFailureDoesNotPublishDB pins the second half
// of ADR-064's fail-closed open contract. The sibling test above stages a
// failure inside openBranchDBLocked; here the branch's chromem DB opens fine
// but GetOrCreateCollection fails (chromem's CreateCollection persists the
// collection metadata file, so a full or read-only volume — the condition a
// multi-GB index actually runs into — fails here, not in NewPersistentDB).
// SwitchBranch must NOT publish the DB in that case: a half-open state
// (db resident, collection nil) is parkable — parkCurrentLocked gates on
// db != nil — while estimateStateBytes sizes a parked state from its
// COLLECTION, so the documents that DB decoded would be accounted as 0 bytes
// and the park byte budget could never evict them.
func TestSwitchBranch_CollectionCreateFailureDoesNotPublishDB(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbeddingFunc()
	root := t.TempDir()

	svc, err := NewService(ServiceConfig{EmbeddingFunc: emb})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("p", root); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}

	// Discover the directory name chromem derives for the collection so the
	// failure can be staged without replicating chromem's internal hashing.
	mainRoot := branchRootPath(root, "main")
	entries, err := os.ReadDir(mainRoot)
	if err != nil {
		t.Fatalf("read branch root: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("branch root holds %d entries, want the single collection directory", len(entries))
	}
	colDir := filepath.Join(mainRoot, entries[0].Name())

	// Stage the failure: NewPersistentDB skips non-directory entries, so the DB
	// still opens cleanly — but CreateCollection then cannot make its directory
	// where a regular file already sits.
	if err := os.RemoveAll(colDir); err != nil {
		t.Fatalf("remove collection dir: %v", err)
	}
	if err := os.WriteFile(colDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	// Leave "main" so the switch back is a real open rather than the same-branch
	// no-op early return.
	if err := svc.SwitchBranch(ctx, "feature"); err != nil {
		t.Fatalf("SwitchBranch feature: %v", err)
	}

	if err := svc.SwitchBranch(ctx, "main"); err == nil {
		t.Fatal("SwitchBranch back to a blocked collection directory must fail")
	}
	if col := svc.GetCollection(); col != nil {
		t.Errorf("collection served after a failed open: %v", col)
	}
	if db := svc.GetDB(); db != nil {
		t.Error("DB published after a failed collection create; the state must stay closed so parkCurrentLocked cannot park a resident the byte budget accounts as 0")
	}
}

// TestHandleBranchSwitch_AnnouncesPreOpenState pins the status-honesty half of
// ADR-064 on the git-monitor path. ADR-064 turned a branch switch into a real
// branch-scoped open (release the outgoing branch's residents, then gob-decode
// the target branch's own chromem DB) where it used to be an O(1)
// GetOrCreateCollection on an already-fully-decoded DB. HandleBranchSwitch must
// therefore announce the same distinct {state: "loading", phase: "open"}
// pre-open state initProject announces — otherwise the status bar keeps showing
// the previous pass's green "Index ready" for the whole decode while the
// collection is nil and readiness is dropped, and a search whose bounded
// WaitReady expires inside that window reports the self-contradictory
// "index not yet ready (ready)". The same-branch no-op must NOT announce it:
// SwitchBranch early-returns there, so an announcement would only flicker the
// pill.
func TestHandleBranchSwitch_AnnouncesPreOpenState(t *testing.T) {
	svc := setupTestService(t)
	wsDir := createTestWorkspace(t)

	var mu sync.Mutex
	var states []IndexState
	var loadingPhases []IndexPhase
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		OnProgress: func(phase IndexPhase, state IndexState, _, _ int, _ string) {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, state)
			if state == IndexStateLoading {
				loadingPhases = append(loadingPhases, phase)
			}
		},
	})

	if err := indexer.IndexFull(context.Background(), wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	reset := func() {
		mu.Lock()
		states, loadingPhases = nil, nil
		mu.Unlock()
	}
	snapshot := func() ([]IndexState, []IndexPhase) {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(states), slices.Clone(loadingPhases)
	}

	// A real branch change announces the pre-open state FIRST, with PhaseOpen.
	reset()
	if _, err := indexer.HandleBranchSwitch(context.Background(), wsDir, "feature/x"); err != nil {
		t.Fatalf("HandleBranchSwitch feature/x: %v", err)
	}
	gotStates, gotPhases := snapshot()
	if len(gotStates) == 0 || gotStates[0] != IndexStateLoading {
		t.Errorf("branch-switch emissions = %v, want %q first", gotStates, IndexStateLoading)
	}
	if len(gotPhases) == 0 {
		t.Error("the pre-open announcement carried no phase record")
	}
	for _, ph := range gotPhases {
		if ph != PhaseOpen {
			t.Errorf("%q announced with phase %q, want %q", IndexStateLoading, ph, PhaseOpen)
		}
	}

	// The same-branch no-op must not announce it.
	reset()
	if _, err := indexer.HandleBranchSwitch(context.Background(), wsDir, "feature/x"); err != nil {
		t.Fatalf("HandleBranchSwitch same branch: %v", err)
	}
	gotStates, _ = snapshot()
	for _, st := range gotStates {
		if st == IndexStateLoading {
			t.Errorf("a same-branch no-op announced %q (emissions %v); it must not flicker the pill",
				IndexStateLoading, gotStates)
		}
	}
}

// TestHandleBranchSwitch_AnnouncesRetryAfterFailedOpen pins the second clause of
// HandleBranchSwitch's announcement guard. A FAILED open returns before
// SwitchBranch updates currentBranch, so the state is left naming the OUTGOING
// branch with no collection. Retrying that same branch name is therefore not a
// same-branch no-op — SwitchBranch really re-opens — and must be announced. A
// guard comparing branch names alone would skip it and leave the pill on
// whatever the failed open last published.
func TestHandleBranchSwitch_AnnouncesRetryAfterFailedOpen(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbeddingFunc()
	root := t.TempDir()
	wsDir := createTestWorkspace(t)

	svc, err := NewService(ServiceConfig{EmbeddingFunc: emb})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("p", root); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}

	var mu sync.Mutex
	var states []IndexState
	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		OnProgress: func(_ IndexPhase, state IndexState, _, _ int, _ string) {
			mu.Lock()
			states = append(states, state)
			mu.Unlock()
		},
	})
	if err := indexer.IndexFull(ctx, wsDir); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}

	// Block the target's branch root: MkdirAll over a regular file fails, so the
	// switch fails inside openBranchDBLocked.
	if err := os.WriteFile(branchRootPath(root, "blocked"), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	if _, err := indexer.HandleBranchSwitch(ctx, wsDir, "blocked"); err == nil {
		t.Fatal("HandleBranchSwitch to a blocked branch root must fail")
	}

	// The failed open left the outgoing branch name behind with no collection.
	if got := svc.CurrentBranchName(); got != "main" {
		t.Fatalf("currentBranch after a failed open = %q, want the stale %q", got, "main")
	}
	if col := svc.GetCollection(); col != nil {
		t.Fatalf("collection survived a failed open: %v", col)
	}

	mu.Lock()
	states = nil
	mu.Unlock()

	// "main" matches the stale currentBranch, so only the collection-nil clause
	// can announce this (very real) re-open.
	if _, err := indexer.HandleBranchSwitch(ctx, wsDir, "main"); err != nil {
		t.Fatalf("HandleBranchSwitch retry to main: %v", err)
	}
	mu.Lock()
	got := slices.Clone(states)
	mu.Unlock()
	if len(got) == 0 || got[0] != IndexStateLoading {
		t.Errorf("retry-after-failed-open emissions = %v, want %q first", got, IndexStateLoading)
	}
	if col := svc.GetCollection(); col == nil {
		t.Error("the retry did not restore a collection")
	}
}

// TestMigrateLegacyLayout_NamesBranchRootsAndOrphansForeignNames pins the
// migration's naming contract (ADR-064 §Decision item 1): a recovered name is
// honoured as a branch root ONLY in the exact shape collectionName produces —
// the "branch_" prefix plus sanitizeRe's allowed set. A sanitized-but-unprefixed
// name is not a branch identity, so accepting it would let foreign or corrupt
// metadata squat an arbitrary directory name under branches/ (including the
// orphan pseudo-name itself); it is parked under branches/_orphan/ like an
// unreadable or traversal-bearing name.
func TestMigrateLegacyLayout_NamesBranchRootsAndOrphansForeignNames(t *testing.T) {
	root := t.TempDir()

	legacy := func(dir, metaName string) {
		t.Helper()
		p := filepath.Join(root, dir)
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if metaName != "" {
			writeLegacyCollectionMetadata(t, p, metaName)
		}
	}
	legacy("aaaaaaaa", collectionName("main")) // real branch identity
	legacy("bbbbbbbb", "docs")                 // sanitized, but NOT a branch
	legacy("cccccccc", orphanBranchRootName)   // the orphan pseudo-name itself
	legacy("dddddddd", "branch_../../escape")  // traversal inside the prefix

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	svc.migrateLegacyLayoutLocked(root)

	// The conforming name is honoured: the collection directory now lives in
	// its own stable branch root, where openBranchDBLocked will find it.
	if _, statErr := os.Stat(filepath.Join(branchRootPath(root, "main"), "aaaaaaaa")); statErr != nil {
		t.Errorf("conforming branch root not populated: %v", statErr)
	}
	// Everything else is parked under the orphan root, unopened and untrusted.
	orphans, err := os.ReadDir(filepath.Join(root, branchRootsDirName, orphanBranchRootName))
	if err != nil {
		t.Fatalf("orphan root not created: %v", err)
	}
	if len(orphans) != 3 {
		t.Errorf("orphan root holds %d entries, want 3 (unprefixed, orphan-name, traversal)", len(orphans))
	}
	for _, dir := range []string{"bbbbbbbb", "cccccccc", "dddddddd"} {
		if _, statErr := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(statErr) {
			t.Errorf("rejected legacy directory %s was not moved out of the storage root (stat err = %v)", dir, statErr)
		}
	}
	// No foreign name became a branch root of its own.
	roots, err := os.ReadDir(filepath.Join(root, branchRootsDirName))
	if err != nil {
		t.Fatalf("branches/ not created: %v", err)
	}
	if len(roots) != 2 {
		names := make([]string, 0, len(roots))
		for _, r := range roots {
			names = append(names, r.Name())
		}
		t.Errorf("branches/ holds %d roots (%v), want 2 (branch_main + _orphan)", len(roots), names)
	}
}
