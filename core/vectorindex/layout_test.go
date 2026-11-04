package vectorindex

import (
	"context"
	"encoding/gob"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// seedBranchGenerationManifest declares a seeded branch store's generation
// compatible with svc's era, so tests that hand-build chromem data keep
// exercising what they meant to test instead of the ADR-104 generation gate
// (which would — correctly — discard a manifest-less branch store as another
// coordinate era).
func seedBranchGenerationManifest(t *testing.T, svc *Service, branchRoot string) {
	t.Helper()
	if err := ensureRealDir(branchRoot, 0o750); err != nil {
		t.Fatalf("creating branch root %s: %v", branchRoot, err)
	}
	if err := writeBranchManifest(branchRoot, &branchGenerationManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: svc.embeddingCacheFingerprint,
		Dims:        svc.embeddingDimension,
		ChunkerFP:   svc.chunkerFingerprint,
		IgnoreFP:    svc.ignoreFingerprint,
	}); err != nil {
		t.Fatalf("seed generation manifest at %s: %v", branchRoot, err)
	}
}

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
	// The seeded branch stores predate the ADR-104 generation manifest; the
	// sequential-generation gate would (correctly) discard them as another
	// coordinate era. Declare the seeded era compatible so this ADR-064 test
	// still exercises the branch-scoped lazy open, not the reset.
	for _, br := range []string{"main", "feature"} {
		seedBranchGenerationManifest(t, svc, branchRootPath(root, br))
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
	// lives in chromem's hashed subdirectory, exactly as when it was seeded;
	// the branch root also carries its generation manifest (ADR-104).
	if entries, derr := os.ReadDir(branchRootPath(root, "feature")); derr != nil || len(entries) != 2 {
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
	// The branch root also holds its generation manifest (ADR-104), so the
	// collection directory is the entry that IS a directory.
	mainRoot := branchRootPath(root, "main")
	entries, err := os.ReadDir(mainRoot)
	if err != nil {
		t.Fatalf("read branch root: %v", err)
	}
	colDir := ""
	for _, e := range entries {
		if e.IsDir() {
			colDir = filepath.Join(mainRoot, e.Name())
			break
		}
	}
	if colDir == "" {
		t.Fatalf("branch root holds no collection directory (entries=%d)", len(entries))
	}

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
// whatever the failed open last published. The failed open itself must also
// TERMINATE the announcement with `unavailable`: without it the loading state
// published before the open stays in the manager snapshot and the UI stream
// until the next branch change or project switch, parking the status bar on
// "Preparing index…" for the rest of the session.
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

	// Only the failed switch's own emissions matter below; drop IndexFull's.
	mu.Lock()
	states = nil
	mu.Unlock()

	if _, err := indexer.HandleBranchSwitch(ctx, wsDir, "blocked"); err == nil {
		t.Fatal("HandleBranchSwitch to a blocked branch root must fail")
	}

	// The failed open must terminate the pre-open announcement: loading first,
	// then the terminal unavailable — never a dangling loading state.
	mu.Lock()
	failed := slices.Clone(states)
	mu.Unlock()
	if len(failed) != 2 || failed[0] != IndexStateLoading || failed[1] != IndexStateUnavailable {
		t.Errorf("failed-open emissions = %v, want [%q %q]",
			failed, IndexStateLoading, IndexStateUnavailable)
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

// TestFailedPassWithoutCollectionAnnouncesUnavailable pins the honesty
// conditional in IndexFull/IndexIncremental's deferred terminal emission. A
// pass that fails while NO collection exists (a watcher pass or an
// init/reindex attempt after a failed branch open) must terminate with
// `unavailable`: announcing `ready` would claim queryable data that does not
// exist, flipping the honest post-failure state back to a green pill within a
// second of the next file edit. With a LIVE collection the previous data is
// still queryable, so the same failure class must keep terminating with
// `ready`.
func TestFailedPassWithoutCollectionAnnouncesUnavailable(t *testing.T) {
	ctx := context.Background()
	// The persistence root is created before the close cleanup is registered:
	// t.Cleanup runs LIFO, so Close releases root.bolt before the TempDir
	// RemoveAll — an open handle fails that removal on Windows (EBUSY).
	persistDir := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("p", persistDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	// No SwitchBranch: no collection exists — the exact shape a failed open
	// leaves behind.
	if svc.GetCollection() != nil {
		t.Fatal("precondition: a fresh SetProject must leave no collection")
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
	snapshot := func() []IndexState {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(states)
	}
	wsDir := createTestWorkspace(t)

	// Incremental over the missing collection fails at ValidateCollection and
	// must terminate with exactly one `unavailable` emission.
	if err := indexer.IndexIncremental(ctx, wsDir); err == nil {
		t.Fatal("IndexIncremental without a collection must fail")
	}
	if got := snapshot(); len(got) != 1 || got[0] != IndexStateUnavailable {
		t.Errorf("incremental emissions without a collection = %v, want [%q]",
			got, IndexStateUnavailable)
	}

	// Full over the missing collection walks fine but has nothing to add to.
	mu.Lock()
	states = nil
	mu.Unlock()
	if err := indexer.IndexFull(ctx, wsDir); err == nil {
		t.Fatal("IndexFull without a collection must fail")
	}
	if got := snapshot(); len(got) < 2 || got[0] != IndexStateIndexing ||
		got[len(got)-1] != IndexStateUnavailable || slices.Contains(got, IndexStateReady) {
		t.Errorf("full emissions without a collection = %v, want %q progress terminating with %q and no %q",
			got, IndexStateIndexing, IndexStateUnavailable, IndexStateReady)
	}

	// Positive control: the same failure class with a LIVE collection still
	// terminates with `ready` — the previous data remains queryable. A
	// workspace path that does not exist fails the validation walk.
	if err := svc.SwitchBranch(ctx, "main"); err != nil {
		t.Fatalf("SwitchBranch main: %v", err)
	}
	mu.Lock()
	states = nil
	mu.Unlock()
	if err := indexer.IndexIncremental(ctx, filepath.Join(wsDir, "does-not-exist")); err == nil {
		t.Fatal("IndexIncremental over a missing workspace must fail")
	}
	if got := snapshot(); len(got) == 0 || got[len(got)-1] != IndexStateReady {
		t.Errorf("incremental emissions with a live collection = %v, want terminal %q",
			got, IndexStateReady)
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

// ---------------------------------------------------------------------------
// Unified legacy-layout migration (ADR-104 §3, stage 3)
// ---------------------------------------------------------------------------

// migrationHarness is one store-migration fixture: a real git repository, a
// shared vector-index storage root, and a corpus registry. The legacy stores
// are seeded through a legacy-mode Service (no Corpus registry — the exact
// producer of the on-disk layouts the sweep must upgrade).
type migrationHarness struct {
	repo    *gittest.Repo
	root    string // the checkout (branch main)
	storage string
	reg     *CorpusRegistry
	t       *testing.T
}

const migrationTestFP = "fp-mig"

func newMigrationHarness(t *testing.T, files map[string]string) *migrationHarness {
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
	return &migrationHarness{
		repo:    repo,
		root:    root,
		storage: filepath.Join(t.TempDir(), "vector_index"),
		reg:     NewCorpusRegistry(migrationTestLogger()),
		t:       t,
	}
}

// migrationTestLogger returns a stderr logger when MIG_DEBUG is set (test
// debugging only), nil otherwise.
func migrationTestLogger() *slog.Logger {
	if os.Getenv("MIG_DEBUG") == "" {
		return nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// facts wires the sweep's git resolution over the repository itself.
func (h *migrationHarness) facts() StoreMigrationFacts {
	return StoreMigrationFacts{
		ListBranches: func(ctx context.Context) ([]string, error) {
			return ListLocalBranches(ctx, h.root)
		},
		BranchCommit: func(ctx context.Context, branch string) (string, error) {
			return CommitOfBranch(ctx, h.root, branch)
		},
	}
}

// oidOf resolves a branch's commit OID through the sweep's own resolution.
func (h *migrationHarness) oidOf(t *testing.T, branch string) string {
	t.Helper()
	oid, err := CommitOfBranch(context.Background(), h.root, branch)
	if err != nil {
		t.Fatalf("resolving oid of %s: %v", branch, err)
	}
	return oid
}

// seedLegacyStore indexes the repository's CURRENT checkout into a legacy
// branch store (branch name only — the pre-ADR-104 identity) under the
// given storage root, through a legacy-mode service with its own counting
// embedder. Returns the service so callers can Close it (its read-write
// lexical handle owns the bolt lock that follows the store through a
// rename).
func (h *migrationHarness) seedLegacyStore(t *testing.T, storage, workspaceRoot, branch, fp string) (*Service, *embedCounter) {
	t.Helper()
	counter := &embedCounter{}
	ef := func(ctx context.Context, text string) ([]float32, error) {
		counter.mu.Lock()
		counter.texts = append(counter.texts, text)
		counter.mu.Unlock()
		return fakeEmbeddingFunc()(ctx, text)
	}
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             ef,
		EmbeddingCacheFingerprint: fp,
		EmbeddingDimension:        8,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.SetProject("legacy", storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), branch); err != nil {
		t.Fatalf("SwitchBranch %s: %v", branch, err)
	}
	idx := NewIndexer(IndexerConfig{Service: svc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := idx.IndexFull(context.Background(), workspaceRoot); err != nil {
		t.Fatalf("legacy full pass on %s: %v", branch, err)
	}
	return svc, counter
}

// runSweep runs the unified migration and returns the final state.
func (h *migrationHarness) runSweep(ctx context.Context) *StoreMigrationState {
	return h.reg.RunStoreMigration(ctx, h.storage, migrationTestFP, 8, h.facts())
}

func (h *migrationHarness) stateFile(t *testing.T) *StoreMigrationState {
	t.Helper()
	st := readStoreMigrationState(h.storage)
	if st == nil {
		t.Fatal("store migration state file missing")
	}
	return st
}

func (h *migrationHarness) skipReason(t *testing.T, unit string) string {
	t.Helper()
	for _, rec := range h.stateFile(t).Skipped {
		if rec.Unit == unit {
			return rec.Reason
		}
	}
	return ""
}

func (h *migrationHarness) completed(t *testing.T, unit string) bool {
	t.Helper()
	return slices.Contains(h.stateFile(t).Completed, unit)
}

// TestStoreMigration_PromotesLegacyBranchStore_ZeroEmbeddings pins the
// reuse criterion: a legacy per-branch store (ADR-064 layout) of a branch
// nobody has attached is promoted by the background sweep into the shared
// snapshot corpus of its commit — pure renames, no embeddings — and a later
// view attach of a worktree at that commit reuses the published corpus with
// ZERO embeddings (the provable mapping: the counter covers the sweep AND
// the attach).
func TestStoreMigration_PromotesLegacyBranchStore_ZeroEmbeddings(t *testing.T) {
	files := map[string]string{
		"a.go":     "package main\n\n// alpha marker\n",
		"sub/b.go": "package sub\n\n// beta marker\n",
	}
	h := newMigrationHarness(t, files)

	// Branch feature diverges from main (its own commit ⇒ its own OID).
	h.repo.Git(t, "checkout", "-b", "feature")
	h.repo.Write(t, "feature.go", "package main\n\n// feature marker\n")
	h.repo.Git(t, "add", "feature.go")
	h.repo.Git(t, "commit", "-m", "feature commit")

	legacySvc, _ := h.seedLegacyStore(t, h.storage, h.root, "feature", migrationTestFP)
	if err := legacySvc.Close(); err != nil {
		t.Fatalf("closing legacy service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")

	key := snapshotKey(h.oidOf(t, "feature"))
	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done", st)
	}
	if !h.completed(t, "branch:"+collectionName("feature")) {
		t.Errorf("feature unit not recorded completed: %+v", h.stateFile(t))
	}
	// The corpus is published and the legacy store is GONE (renamed, not
	// copied): every leg moved.
	snapRoot := branchRootPath(h.storage, key)
	if readCorpusManifest(snapRoot) == nil {
		t.Fatalf("no published corpus at %s", snapRoot)
	}
	if pathExists(branchRootPath(h.storage, "feature")) {
		t.Error("legacy feature store still on disk after promotion")
	}
	if pathExists(contentlessMarkerPathFor(h.storage, "feature")) {
		t.Error("legacy content-less marker still at the storage root after promotion")
	}
	// Zero-embedding proof across sweep + attach: attach a worktree of the
	// feature branch (same commit) through the view model.
	wt := filepath.Join(t.TempDir(), "wt-feature")
	h.repo.Git(t, "worktree", "add", wt, "feature")

	counter := &embedCounter{}
	ef := func(ctx context.Context, text string) ([]float32, error) {
		counter.mu.Lock()
		counter.texts = append(counter.texts, text)
		counter.mu.Unlock()
		return fakeEmbeddingFunc()(ctx, text)
	}
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             ef,
		EmbeddingCacheFingerprint: migrationTestFP,
		EmbeddingDimension:        8,
		Corpus:                    h.reg,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("view", h.storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	idx := NewIndexer(IndexerConfig{Service: svc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	if err := idx.AttachSnapshot(context.Background(), wt, "feature"); err != nil {
		t.Fatalf("attach after sweep: %v", err)
	}
	if err := idx.IndexIncremental(context.Background(), wt); err != nil {
		t.Fatalf("reconciliation after sweep: %v", err)
	}
	if n := counter.count(); n != 0 {
		t.Errorf("reuse re-embedded %d texts; want 0 (renamed corpus adopted as-is)", n)
	}
	if n := svc.CorpusFileCount(); n == 0 {
		t.Error("no corpus files visible to the attached root after adoption")
	}
}

// TestStoreMigration_FallbackReasons_HonestSkips pins the diagnosable
// rebuild-fallback: a legacy store that cannot be proven reusable is left in
// place with an honest reason recorded in the state file, and the
// recognized volume still settles (done) — the skips are the terminal
// diagnosis, never a deletion license and never a false marker.
func TestStoreMigration_FallbackReasons_HonestSkips(t *testing.T) {
	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})

	// Foreign era: the store was produced under another embedding era.
	h.repo.Git(t, "checkout", "-b", "foreign-era")
	eraSvc, _ := h.seedLegacyStore(t, h.storage, h.root, "foreign-era", "fp-of-another-era")
	if err := eraSvc.Close(); err != nil {
		t.Fatalf("close era service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")

	// Missing content-less marker: pre-ADR-049 fat documents.
	h.repo.Git(t, "checkout", "-b", "fat-store")
	fatSvc, _ := h.seedLegacyStore(t, h.storage, h.root, "fat-store", migrationTestFP)
	if err := fatSvc.Close(); err != nil {
		t.Fatalf("close fat service: %v", err)
	}
	if err := os.Remove(contentlessMarkerPathFor(h.storage, "fat-store")); err != nil {
		t.Fatalf("removing the content-less marker: %v", err)
	}
	h.repo.Git(t, "checkout", "main")

	// Branch absent: the branch is gone from the clone.
	h.repo.Git(t, "checkout", "-b", "ghost")
	ghostSvc, _ := h.seedLegacyStore(t, h.storage, h.root, "ghost", migrationTestFP)
	if err := ghostSvc.Close(); err != nil {
		t.Fatalf("close ghost service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")
	h.repo.Git(t, "branch", "-D", "ghost")

	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done despite skips", st)
	}
	for unit, want := range map[string]string{
		"branch:" + collectionName("foreign-era"): "generation manifest names a foreign embedding era",
		"branch:" + collectionName("fat-store"):   "content-less marker absent",
		"branch:" + collectionName("ghost"):       "branch absent from the local clone (store left in place)",
	} {
		if got := h.skipReason(t, unit); got != want {
			t.Errorf("%s skip reason = %q, want %q", unit, got, want)
		}
	}
	// Every fallback source stays exactly where it was.
	for _, branch := range []string{"foreign-era", "fat-store", "ghost"} {
		if !pathExists(branchRootPath(h.storage, branch)) {
			t.Errorf("fallback source of %s was removed; skips never license deletion", branch)
		}
	}
}

// TestStoreMigration_CrashResume_Converges pins the replay contract: a run
// cancelled between units persists the settled prefix (status=running, no
// done marker), the next run resumes at the unsettled unit, and a completed
// tree never re-runs (the done marker is the replay gate).
func TestStoreMigration_CrashResume_Converges(t *testing.T) {
	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})
	for _, branch := range []string{"r1", "r2"} {
		h.repo.Git(t, "checkout", "-b", branch)
		h.repo.Write(t, branch+".go", "package main\n\n// "+branch+" marker\n")
		h.repo.Git(t, "add", branch+".go")
		h.repo.Git(t, "commit", "-m", branch)
		svc, _ := h.seedLegacyStore(t, h.storage, h.root, branch, migrationTestFP)
		if err := svc.Close(); err != nil {
			t.Fatalf("close %s service: %v", branch, err)
		}
		h.repo.Git(t, "checkout", "main")
	}

	// Cancel deterministically after the first unit settled: the facts'
	// BranchCommit hook fires per unit, so the second call cancels the run.
	ctx, cancel := context.WithCancel(context.Background())
	facts := h.facts()
	calls := 0
	inner := facts.BranchCommit
	facts.BranchCommit = func(ctx context.Context, branch string) (string, error) {
		calls++
		if calls == 2 {
			cancel()
		}
		return inner(ctx, branch)
	}
	st := h.reg.RunStoreMigration(ctx, h.storage, migrationTestFP, 8, facts)
	cancel()
	if st == nil || st.Status != storeMigrationStatusRunning {
		t.Fatalf("interrupted sweep state = %+v, want running (no false done marker)", st)
	}
	if done := readCorpusManifest(branchRootPath(h.storage, snapshotKey(h.oidOf(t, "r1")))); done == nil {
		t.Error("the settled unit's corpus was not published before the interruption")
	}
	if !pathExists(branchRootPath(h.storage, "r2")) {
		t.Fatal("the pending unit's store vanished before its promotion")
	}

	// Replay: the second run settles the remainder and writes the marker.
	st = h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("resumed sweep state = %+v, want done", st)
	}
	for _, branch := range []string{"r1", "r2"} {
		if !h.completed(t, "branch:"+collectionName(branch)) {
			t.Errorf("%s not recorded completed after resume", branch)
		}
		if readCorpusManifest(branchRootPath(h.storage, snapshotKey(h.oidOf(t, branch)))) == nil {
			t.Errorf("%s corpus not published after resume", branch)
		}
	}

	// The done marker is the replay gate: a third run is a no-op.
	corpusCount := 0
	if entries, err := os.ReadDir(filepath.Join(h.storage, branchRootsDirName)); err == nil {
		for _, e := range entries {
			if e.IsDir() && isViewStoreCollectionName(e.Name()) {
				corpusCount++
			}
		}
	}
	st3 := h.runSweep(context.Background())
	if st3 == nil || st3.Status != storeMigrationStatusDone {
		t.Fatalf("third sweep state = %+v, want done (replay gate)", st3)
	}
	if entries, err := os.ReadDir(filepath.Join(h.storage, branchRootsDirName)); err == nil {
		n := 0
		for _, e := range entries {
			if e.IsDir() && isViewStoreCollectionName(e.Name()) {
				n++
			}
		}
		if n != corpusCount {
			t.Errorf("third run changed the corpus count (%d → %d); a done tree must be a no-op", corpusCount, n)
		}
	}
}

// TestStoreMigration_UnknownEntries_LeftInPlaceAndReported pins the scope
// discipline: unrecognized directories (unknown shapes, the orphan root, a
// legacy worktree container without branch stores) are excluded from the
// recognized volume, reported as skip records, left in place, and never
// turned into a path — a hostile legacy metadata name cannot escape the
// storage root.
func TestStoreMigration_UnknownEntries_LeftInPlaceAndReported(t *testing.T) {
	h := newMigrationHarness(t, nil)

	// Unknown shapes under branches/ and the worktrees container.
	for _, dir := range []string{
		filepath.Join(h.storage, branchRootsDirName, "not-a-branch-name"),
		filepath.Join(h.storage, branchRootsDirName, orphanBranchRootName, "held"),
		filepath.Join(h.storage, legacyWorktreesDirName, "junk"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// A hostile pre-ADR-064 root-level collection directory.
	hostile := filepath.Join(h.storage, "89abcdef")
	if err := os.MkdirAll(hostile, 0o750); err != nil {
		t.Fatalf("mkdir hostile: %v", err)
	}
	writeLegacyCollectionMetadata(t, hostile, "../../escape")

	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done with unknown volume excluded", st)
	}
	if !pathExists(filepath.Join(h.storage, branchRootsDirName, "not-a-branch-name")) {
		t.Error("unrecognized branches/ entry was removed; unknown volume stays in place")
	}
	if !pathExists(filepath.Join(h.storage, branchRootsDirName, orphanBranchRootName, "held")) {
		t.Error("orphan root content was removed; the orphan root is keep-forever")
	}
	if !pathExists(filepath.Join(h.storage, legacyWorktreesDirName, "junk")) {
		t.Error("legacy worktree container without branch stores was removed")
	}
	for _, unit := range []string{
		"branch:not-a-branch-name",
		"branch:" + orphanBranchRootName,
		"worktree:junk",
	} {
		if h.skipReason(t, unit) == "" {
			t.Errorf("unknown entry %q has no skip record; the state file must report the excluded volume", unit)
		}
	}
	// The hostile name was parked under the orphan root, never honoured.
	if _, statErr := os.Stat(filepath.Join(h.storage, "..", "escape")); !os.IsNotExist(statErr) {
		t.Errorf("migration escaped the storage root (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(h.storage, branchRootsDirName, "..", "..", "escape")); !os.IsNotExist(statErr) {
		t.Errorf("migration created a traversal destination (stat err = %v)", statErr)
	}
	if pathExists(hostile) {
		t.Error("hostile root-level directory was not re-homed away from the storage root")
	}
}

// TestStoreMigration_NeverOverwritesTarget pins the validation-before-impact
// rule: an existing target corpus is never overwritten by a promotion (the
// source stays, a skip record explains), and a legacy store whose snapshot
// corpus is ALREADY PUBLISHED is superseded — the validated new state wins
// and the duplicate legacy copy is removed.
func TestStoreMigration_NeverOverwritesTarget(t *testing.T) {
	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})

	// collision: its corpus target exists but is NOT published (debris).
	h.repo.Git(t, "checkout", "-b", "collision")
	h.repo.Write(t, "collision.go", "package main\n\n// collision marker\n")
	h.repo.Git(t, "add", "collision.go")
	h.repo.Git(t, "commit", "-m", "collision")
	colSvc, _ := h.seedLegacyStore(t, h.storage, h.root, "collision", migrationTestFP)
	if err := colSvc.Close(); err != nil {
		t.Fatalf("close collision service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")
	colKey := snapshotKey(h.oidOf(t, "collision"))
	colDir := branchRootPath(h.storage, colKey)
	if err := os.MkdirAll(colDir, 0o750); err != nil {
		t.Fatalf("mkdir collision target: %v", err)
	}
	sentinel := []byte("unpublished debris — must survive untouched")
	if err := os.WriteFile(filepath.Join(colDir, "debris.txt"), sentinel, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// published: its snapshot corpus is already published (the validated
	// new state supersedes the legacy duplicate).
	h.repo.Git(t, "checkout", "-b", "published")
	pubSvc, _ := h.seedLegacyStore(t, h.storage, h.root, "published", migrationTestFP)
	if err := pubSvc.Close(); err != nil {
		t.Fatalf("close published service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")
	pubKey := snapshotKey(h.oidOf(t, "published"))
	files, ok := legacySidecarHashMap(h.storage, "published")
	if !ok {
		t.Fatal("published fixture has no sidecar hash map")
	}
	if err := os.MkdirAll(branchRootPath(h.storage, pubKey), 0o750); err != nil {
		t.Fatalf("mkdir published corpus: %v", err)
	}
	if err := writeCorpusManifest(branchRootPath(h.storage, pubKey), &CorpusManifest{
		Format: storeFormatV2, Coords: coordsRelativeV1,
		EmbeddingFP: migrationTestFP, Dims: 8,
		Collection: collectionName("published"), Files: files,
	}); err != nil {
		t.Fatalf("seed published corpus: %v", err)
	}

	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done", st)
	}
	if got := h.skipReason(t, "branch:"+collectionName("collision")); got != "promotion target already exists" {
		t.Errorf("collision skip reason = %q, want the target-exists diagnosis", got)
	}
	if !pathExists(branchRootPath(h.storage, "collision")) {
		t.Error("collision source was removed; an unpublished target must never consume the only copy")
	}
	got, rerr := os.ReadFile(filepath.Join(colDir, "debris.txt"))
	if rerr != nil || !slices.Equal(got, sentinel) {
		t.Errorf("unpublished target was overwritten (err=%v equal=%v)", rerr, slices.Equal(got, sentinel))
	}
	if !h.completed(t, "branch:"+collectionName("published")) {
		t.Errorf("published unit not recorded completed: %+v", h.stateFile(t))
	}
	if pathExists(branchRootPath(h.storage, "published")) {
		t.Error("superseded legacy duplicate of a published corpus survived the sweep")
	}
}

// TestStoreMigration_PromotesWorktreeStore pins the ADR-081 layout leg: a
// legacy per-worktree storage root (worktrees/<name>/ with its own branches/,
// sidecars and markers) is drained into the shared store — the worktree's
// branch store promoted by rename under its commit, zero embeddings, the
// leftover container (its embedding cache) left for the tree-release
// cleanup.
func TestStoreMigration_PromotesWorktreeStore(t *testing.T) {
	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})
	h.repo.Git(t, "checkout", "-b", "wtt")
	h.repo.Write(t, "wtt.go", "package main\n\n// worktree marker\n")
	h.repo.Git(t, "add", "wtt.go")
	h.repo.Git(t, "commit", "-m", "wtt")
	h.repo.Git(t, "checkout", "main")
	wt := filepath.Join(t.TempDir(), "wt-tree")
	h.repo.Git(t, "worktree", "add", wt, "wtt")

	wtStore := filepath.Join(h.storage, legacyWorktreesDirName, "wt-tree")
	svc, _ := h.seedLegacyStore(t, wtStore, wt, "wtt", migrationTestFP)
	if err := svc.Close(); err != nil {
		t.Fatalf("close worktree service: %v", err)
	}

	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("sweep state = %+v, want done", st)
	}
	key := snapshotKey(h.oidOf(t, "wtt"))
	if readCorpusManifest(branchRootPath(h.storage, key)) == nil {
		t.Fatal("the worktree store was not promoted into the shared store")
	}
	if pathExists(branchRootPath(wtStore, "wtt")) {
		t.Error("the worktree's legacy branch store survived promotion")
	}
	if !h.completed(t, "worktree:wt-tree:"+collectionName("wtt")) {
		t.Errorf("worktree unit not recorded completed: %+v", h.stateFile(t))
	}
	// The container's remaining content (its embedding cache) stays — the
	// tree-release cleanup owns it, not the sweep.
	if pathExists(wtStore) {
		t.Log("worktree container kept for release-time cleanup (documented behavior)")
	}
}

// TestStoreMigration_TransientFailuresKeepRunning pins the no-false-marker
// rule for TRANSIENT conditions: when the git facts fail (branches cannot
// be listed), the recognized volume is not certifiably settled, so the
// sweep leaves status=running — done is written only after provable full
// success.
func TestStoreMigration_TransientFailuresKeepRunning(t *testing.T) {
	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})
	h.repo.Git(t, "checkout", "-b", "stuck")
	svc, _ := h.seedLegacyStore(t, h.storage, h.root, "stuck", migrationTestFP)
	if err := svc.Close(); err != nil {
		t.Fatalf("close stuck service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")

	facts := StoreMigrationFacts{
		ListBranches: func(context.Context) ([]string, error) {
			return nil, errors.New("git unavailable")
		},
		BranchCommit: func(context.Context, string) (string, error) {
			return "", errors.New("unreachable")
		},
	}
	st := h.reg.RunStoreMigration(context.Background(), h.storage, migrationTestFP, 8, facts)
	if st == nil || st.Status != storeMigrationStatusRunning {
		t.Fatalf("sweep state = %+v, want running on transient failure", st)
	}
	if !pathExists(branchRootPath(h.storage, "stuck")) {
		t.Error("the store vanished under a transient failure")
	}
	// The next healthy run settles it.
	if st := h.runSweep(context.Background()); st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("healthy re-run state = %+v, want done", st)
	}
}

// TestStoreMigration_LeaseDefersUnit pins the serialization with corpus
// builds: while another actor holds the snapshot key's build lease (a live
// corpus build), the sweep waits only a SHORT bound, then defers the unit
// and leaves the tree running — it never parks for the attach path's full
// drain window and never writes done for an unsettled unit.
func TestStoreMigration_LeaseDefersUnit(t *testing.T) {
	old := sweepLeaseWait
	sweepLeaseWait = 50 * time.Millisecond
	t.Cleanup(func() { sweepLeaseWait = old })

	h := newMigrationHarness(t, map[string]string{"a.go": "package main\n\n// alpha\n"})
	h.repo.Git(t, "checkout", "-b", "leased")
	h.repo.Write(t, "leased.go", "package main\n\n// leased marker\n")
	h.repo.Git(t, "add", "leased.go")
	h.repo.Git(t, "commit", "-m", "leased")
	svc, _ := h.seedLegacyStore(t, h.storage, h.root, "leased", migrationTestFP)
	if err := svc.Close(); err != nil {
		t.Fatalf("close leased service: %v", err)
	}
	h.repo.Git(t, "checkout", "main")

	key := snapshotKey(h.oidOf(t, "leased"))
	cs := h.reg.storeFor(h.storage)
	started, waitCh := cs.beginBuild(key)
	if !started {
		t.Fatal("could not acquire the build lease for the fixture")
	}
	st := h.runSweep(context.Background())
	if st == nil || st.Status != storeMigrationStatusRunning {
		t.Fatalf("sweep state = %+v, want running while the lease is held", st)
	}
	if readCorpusManifest(branchRootPath(h.storage, key)) != nil {
		t.Error("the sweep promoted under a foreign build lease")
	}
	cs.endBuild(key, nil)
	<-waitCh

	if st := h.runSweep(context.Background()); st == nil || st.Status != storeMigrationStatusDone {
		t.Fatalf("post-lease sweep state = %+v, want done", st)
	}
}
