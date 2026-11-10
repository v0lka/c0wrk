package vectorindex

// The USER rung of the repair ladder ([ADR-120], specs/domains/workspace.md
// "Repair ladder"): ReindexVectorIndex reaches Manager.Reindex, which must
// re-attach the snapshot view when a failed open/attach left the service
// without a collection. Historically that branch was a logged no-op, so the
// one action the UI offered could not move an index out of "unavailable".

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReindex_NoLiveCollectionReattachesSnapshotView reproduces the incident
// state (no live collection + a published corpus whose lexical leg is gone)
// and asserts the reindex action repairs it: the collection is live again and
// the corpus is published, without a branch switch, a project reopen or any
// manual deletion under the storage root.
func TestReindex_NoLiveCollectionReattachesSnapshotView(t *testing.T) {
	f := newRepairFixture(t)
	logs := &logCapture{}
	svc := f.newService(nil, logs)
	idx := f.newIndexer(svc, nil, logs)
	if err := f.attachView(svc, idx); err != nil {
		t.Fatalf("attachView: %v", err)
	}
	key := f.attachedKey(svc)

	mgr := &Manager{
		service:       svc,
		logger:        slog.New(logs),
		chunkFn:       fakeChunkFunc,
		hashFn:        fakeHashFunc,
		workspacePath: f.root,
		indexer:       idx,
	}
	t.Cleanup(mgr.Shutdown)

	// The state the incident ends in: nothing live to reconcile into, and the
	// published corpus carries no lexical leg (the defect the read path
	// rejects). Both legs are removed — the §3 snapshot directory and the
	// legacy per-key path — so the defect is proven, not incidental.
	f.detachCorpusView(svc)
	if col := svc.GetCollection(); col != nil {
		t.Fatalf("expected no live collection after the detach, got %d documents", col.Count())
	}
	for _, leg := range corpusLexicalLegs(t, f.storage, key) {
		if err := os.RemoveAll(leg); err != nil {
			t.Fatalf("removing lexical leg %s: %v", leg, err)
		}
		if pathExists(leg) {
			t.Fatalf("lexical leg %s survived its removal", leg)
		}
	}
	if f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Fatal("a corpus without its lexical leg must read as unpublished")
	}

	// The action the search panel offers.
	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	// A live collection is the whole point: the overlay itself is empty by
	// design in view mode (the corpus carries the content), so the count is
	// not the signal — the bound collection is.
	if col := svc.GetCollection(); col == nil {
		logs.mu.Lock()
		defer logs.mu.Unlock()
		t.Fatalf("reindex did not repair the index: no live collection\n%s", strings.Join(logs.records, "\n"))
	}
	if !f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Fatal("reindex left the corpus unpublished")
	}
	if st := mgr.GetIndexStatus(); st.State == IndexStateUnavailable {
		t.Fatalf("index still reports unavailable after a successful repair: %q", st.LastError)
	}
}

// corpusLexicalLegs lists the on-disk locations the corpus read path can
// resolve its lexical index from: the §3 snapshot directory first, then the
// legacy per-key directory. Both are returned when present.
func corpusLexicalLegs(t *testing.T, storageRoot, key string) []string {
	t.Helper()
	var legs []string
	if gen, _, ok := readCurrentGeneration(storageRoot); ok {
		legs = append(legs, filepath.Join(snapshotDir(gen, key), "lexical"))
	}
	legs = append(legs, lexicalIndexPath(storageRoot, key))
	return legs
}
