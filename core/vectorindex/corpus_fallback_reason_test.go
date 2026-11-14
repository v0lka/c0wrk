package vectorindex

// The §3 reason is never erased (ADR-120 D7): when a §3 open falls back to
// the v1 store, the fallback event and the preceding publication revocation
// are WARN records naming the snapshot and the reason, and a fallback that
// ITSELF fails returns an error carrying BOTH causes — errors.Is matches the
// §3 open's sentinel and the v1 leg's alike — so the reported status keeps
// the §3 fragment on its way to the UI.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureCorpusStoreLogs points the shared corpus store's logger at logs.
// The fallback and unpublish records are emitted through the REGISTRY's
// logger (newRepairFixture builds the registry with a discard handler), and
// the swap happens between two quiescent attaches, so it races nothing.
func captureCorpusStoreLogs(f *repairFixture, logs *logCapture) {
	f.t.Helper()
	f.registry.storeFor(f.storage).logger = slog.New(logs)
}

// stageTornCorpusWithoutV1Manifest builds a native §3 corpus, tears one
// chunk record (proven store damage) and removes the v1 corpus.json — the
// shape where the §3 open fails AND the v1 fallback has nothing to serve:
// the both-legs-dead open of ADR-120 D7's failure path.
func stageTornCorpusWithoutV1Manifest(t *testing.T, f *repairFixture) string {
	t.Helper()
	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, _ := assertNativeCorpusShape(t, f, key)
	tearOneChunkRecord(t, genDir)
	if err := os.Remove(filepath.Join(branchRootPath(f.storage, key), corpusManifestName)); err != nil {
		t.Fatalf("removing the v1 corpus manifest: %v", err)
	}
	return key
}

// TestOpenCorpus_V1FallbackFailureCarriesBothCauses pins the never-erased
// §3 reason on the failed-fallback path: a torn §3 record fails the §3
// open (which unpublishes the table), the v1 fallback finds no publication,
// and the ONE returned error matches both sentinels — the torn chunk record
// AND the v1 leg's not-published — while naming both legs in its text.
func TestOpenCorpus_V1FallbackFailureCarriesBothCauses(t *testing.T) {
	f := newRepairFixture(t)
	key := stageTornCorpusWithoutV1Manifest(t, f)

	store := f.registry.storeFor(f.storage)
	_, err := store.open(context.Background(), key, fakeEmbeddingFunc(), "fp-repair", 8)
	if err == nil {
		t.Fatal("an open whose §3 leg is torn and whose v1 fallback has no manifest must fail")
	}
	if !errors.Is(err, errChunkRecordTorn) {
		t.Errorf("the failure does not carry the §3 torn cause: %v", err)
	}
	if !errors.Is(err, ErrCorpusNotPublished) {
		t.Errorf("the failure does not carry the v1-fallback cause: %v", err)
	}
	if !strings.Contains(err.Error(), "§3 open failed") {
		t.Errorf("the failure must name the §3 open, got: %v", err)
	}
	if !strings.Contains(err.Error(), "v1 fallback") {
		t.Errorf("the failure must name the v1 fallback, got: %v", err)
	}
}

// TestAttachSnapshot_SuccessfulV1FallbackWarnsOnceWithReason pins the WARN
// contract of the SUCCESSFUL fallback: exactly one WARN carries the §3 open
// failure (the fallback decision — the level the Debug line held before),
// and the revocation that preceded it is its own single WARN naming the
// snapshot and the proven damage.
func TestAttachSnapshot_SuccessfulV1FallbackWarnsOnceWithReason(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, _ := assertNativeCorpusShape(t, f, key)
	tearOneChunkRecord(t, genDir)

	logs := &logCapture{}
	captureCorpusStoreLogs(f, logs)
	second := f.newService(nil, logs)
	if err := f.attachView(second, f.newIndexer(second, nil, logs)); err != nil {
		t.Fatalf("attach over a torn §3 record must serve the v1 store: %v", err)
	}
	if !second.CorpusAttached() {
		t.Fatal("the v1 fallback did not attach the corpus")
	}
	if n := logs.count("WARN", "falling back to the v1 store"); n != 1 {
		t.Errorf("the v1-fallback event was logged %d times; want exactly one WARN", n)
	}
	if n := logs.count("WARN", "falling back to the v1 store", "torn"); n != 1 {
		t.Error("the fallback WARN must carry the §3 reason (the torn chunk record)")
	}
	if n := logs.count("WARN", "unpublished §3 snapshot table", key); n != 1 {
		t.Errorf("the unpublish event was logged %d times with the snapshot name; want exactly one WARN", n)
	}
	if n := logs.count("DEBUG", "falling back to the v1 store"); n != 0 {
		t.Errorf("the fallback event was logged at DEBUG %d times; its level is WARN", n)
	}
}

// TestOpenV2Corpus_UnpublishWarnsSnapshotAndReason pins the call-site WARN:
// revoking a snapshot's §3 publication logs once with the snapshot and the
// proven damage — unpublishSnapshotTable itself stays best-effort and
// silent — and a lexical-only failure (no proven chunk damage) unpublishes
// nothing and warns nothing.
func TestOpenV2Corpus_UnpublishWarnsSnapshotAndReason(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, _ := assertNativeCorpusShape(t, f, key)
	tearOneChunkRecord(t, genDir)

	logs := &logCapture{}
	gen, genManifest, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("no §3 generation adopted")
	}
	v, err := openV2Corpus(gen, key, genManifest, slog.New(logs))
	if v != nil || err == nil {
		t.Fatalf("a torn chunk record must fail the §3 open (v=%v, err=%v)", v, err)
	}
	if n := logs.count("WARN", "unpublished §3 snapshot table", key); n != 1 {
		t.Errorf("the unpublish was logged %d times with the snapshot name; want exactly one WARN", n)
	}
	if n := logs.count("WARN", "unpublished §3 snapshot table", "torn"); n != 1 {
		t.Error("the unpublish WARN must carry the reason (the torn chunk record)")
	}
	if snapshotTablePublished(genDir, key) {
		t.Error("the torn open did not revoke the §3 publication")
	}
}

// TestManagerStatus_AttachFailureLastErrorCarriesV2Fragment follows the
// manager_status pattern end to end: the real failed attach (torn §3 open,
// v1 fallback with no publication) flows through notifyInitFailure exactly
// as initProject records it, and the reported last_error keeps BOTH the §3
// fragment and the v1 cause — with the repairable kind a proven defect owes.
func TestManagerStatus_AttachFailureLastErrorCarriesV2Fragment(t *testing.T) {
	f := newRepairFixture(t)
	key := stageTornCorpusWithoutV1Manifest(t, f)

	second := f.newService(nil, nil)
	if err := second.SetProject("repair", f.storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	attachErr := second.AttachView(context.Background(), key, overlayRootKey(f.root))
	if attachErr == nil {
		t.Fatal("attach over a torn §3 record with no v1 manifest must fail")
	}

	m := &Manager{}
	m.notifyInitFailure(ProjectCallbacks{}, attachErr)
	st := m.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Fatalf("state = %q, want %q", st.State, IndexStateUnavailable)
	}
	if st.ErrorKind != IndexErrorKindRepairable {
		t.Errorf("kind = %q, want %q (the dual cause is a proven store defect)", st.ErrorKind, IndexErrorKindRepairable)
	}
	if !strings.Contains(st.LastError, "§3 open failed") {
		t.Errorf("last error = %q, want the §3 fragment", st.LastError)
	}
	if !strings.Contains(st.LastError, ErrCorpusNotPublished.Error()) {
		t.Errorf("last error = %q, want the v1 fallback cause", st.LastError)
	}
}
