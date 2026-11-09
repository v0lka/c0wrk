package vectorindex

// Self-healing attach (ADR-104, specs/domains/workspace.md "shared corpus"):
// an attach that fails with a PROVEN store defect quarantines the affected
// store and retries EXACTLY ONCE, while a transient OS fault leaves the
// publication intact and reports unavailable with its cause. These tests pin
// the five acceptance criteria of that contract:
//
//  1. a published corpus whose lexical leg is gone attaches again in one pass
//     with no user involvement;
//  2. a corrupt overlay is repaired WITHOUT rebuilding the corpus (the
//     snapshot's ledger bytes are unchanged);
//  3. an injected EACCES keeps the publication, quarantines nothing and
//     surfaces the cause;
//  4. the retry is exactly one pass — a persistent defect ends in a reported
//     failure, never in a cycle — and the fail-closed rule survives it (a
//     failed repair serves no snapshot at all, foreign ones included);
//  5. quarantine scopes are disjoint and never touch the shared chunk records
//     or the embedding cache.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	chromem "github.com/philippgille/chromem-go"
)

// repairFixture is one project storage root shared by a workspace tree, a
// corpus registry and a set of services — the smallest fixture that can build
// a shared corpus and its per-root overlay.
type repairFixture struct {
	t        *testing.T
	root     string
	storage  string
	registry *CorpusRegistry
}

// newRepairFixture writes a small non-git workspace (the synthetic
// root-owned snapshot, ADR-104 §1) over a fresh storage root.
func newRepairFixture(t *testing.T) *repairFixture {
	t.Helper()
	base := t.TempDir()
	f := &repairFixture{
		t:        t,
		root:     filepath.Join(base, "ws"),
		storage:  filepath.Join(base, "vector_index"),
		registry: NewCorpusRegistry(nil),
	}
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	for name, content := range map[string]string{
		"a.go":  "package a\n\nfunc A() int { return 1 }\n",
		"b.md":  "# notes\n\nsome text about the corpus\n",
		"c.txt": "third file\n",
	} {
		if err := os.WriteFile(filepath.Join(f.root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Cleanup(f.registry.Close)
	return f
}

// newService builds a service bound to the shared registry; counter (optional)
// records every embedding request, which is how a test proves a corpus was NOT
// rebuilt.
func (f *repairFixture) newService(counter *embedCounter, logs *logCapture) *Service {
	f.t.Helper()
	ef := func(ctx context.Context, text string) ([]float32, error) {
		if counter != nil {
			counter.mu.Lock()
			counter.texts = append(counter.texts, text)
			counter.mu.Unlock()
		}
		return fakeEmbeddingFunc()(ctx, text)
	}
	cfg := ServiceConfig{
		EmbeddingFunc:             ef,
		EmbeddingCacheFingerprint: "fp-repair",
		EmbeddingDimension:        8,
		Corpus:                    f.registry,
	}
	if logs != nil {
		cfg.Logger = slog.New(logs)
	}
	svc, err := NewService(cfg)
	if err != nil {
		f.t.Fatalf("NewService: %v", err)
	}
	f.t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// newIndexer builds an indexer over svc; passes counts the preparation
// announces, i.e. how many attach passes actually ran.
func (f *repairFixture) newIndexer(svc *Service, passes *int32Counter, logs *logCapture) *Indexer {
	f.t.Helper()
	cfg := IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	}
	if passes != nil {
		cfg.OnProgress = func(phase IndexPhase, _ IndexState, _, _ int, _ string) {
			if phase == PhasePreparation {
				passes.inc()
			}
		}
	}
	if logs != nil {
		cfg.Logger = slog.New(logs)
	}
	return NewIndexer(cfg)
}

// attachView drives one full view attach: project + root binding, the
// snapshot attach (corpus ensure included) and the reconciliation pass that
// populates the root's overlay sidecar by adopting corpus-current files.
func (f *repairFixture) attachView(svc *Service, idx *Indexer) error {
	if err := svc.SetProject("repair", f.storage); err != nil {
		return err
	}
	svc.SetWorkspaceRoot(f.root)
	if err := idx.AttachSnapshot(context.Background(), f.root, ""); err != nil {
		return err
	}
	return idx.IndexIncremental(context.Background(), f.root)
}

// attachedKey is the snapshot key the service currently serves.
func (f *repairFixture) attachedKey(svc *Service) string {
	f.t.Helper()
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	if svc.current.corpus == nil {
		f.t.Fatal("no corpus attached")
	}
	return svc.current.corpus.key
}

// int32Counter is a tiny concurrent counter (the progress callback may run
// from worker goroutines).
type int32Counter struct {
	mu sync.Mutex
	n  int
}

func (c *int32Counter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *int32Counter) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// logCapture is a minimal slog.Handler that keeps the rendered level+message
// of every record, so a test can count rewrite decisions.
type logCapture struct {
	mu      sync.Mutex
	records []string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Level.String())
	sb.WriteString(" ")
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		sb.WriteString(" ")
		sb.WriteString(a.Key)
		sb.WriteString("=")
		sb.WriteString(a.Value.String())
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, sb.String())
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// count returns how many records mention every one of subs.
func (c *logCapture) count(subs ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.records {
		matches := true
		for _, sub := range subs {
			if !strings.Contains(r, sub) {
				matches = false
				break
			}
		}
		if matches {
			n++
		}
	}
	return n
}

// repairDBSeam is a fault-injecting wrapper over the package's
// newPersistentDB seam: it counts every open per path and can fail selected
// paths with a caller-supplied error, which is how these tests stage a PROVEN
// store defect (a decode failure) or a transient OS fault (EACCES) at exactly
// the store they name.
type repairDBSeam struct {
	mu    sync.Mutex
	opens map[string]int
	fail  func(path string) error
}

func installRepairDBSeam(t *testing.T) *repairDBSeam {
	t.Helper()
	s := &repairDBSeam{opens: make(map[string]int)}
	orig := newPersistentDB
	newPersistentDB = func(path string, compress bool) (*chromem.DB, error) {
		clean := filepath.Clean(path)
		s.mu.Lock()
		s.opens[clean]++
		fail := s.fail
		s.mu.Unlock()
		if fail != nil {
			if err := fail(clean); err != nil {
				return nil, err
			}
		}
		return orig(path, compress)
	}
	t.Cleanup(func() { newPersistentDB = orig })
	return s
}

func (s *repairDBSeam) setFail(fn func(path string) error) {
	s.mu.Lock()
	s.fail = fn
	s.mu.Unlock()
}

func (s *repairDBSeam) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens[filepath.Clean(path)]
}

// corruptGobFiles overwrites every gob payload under root, which is exactly
// the on-disk shape of a store root that cannot be decoded.
func corruptGobFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".gob") {
			return nil //nolint:nilerr // best-effort corruption of a test fixture
		}
		n++
		return os.WriteFile(p, []byte("corrupted store payload, not a gob stream"), 0o644)
	})
	if err != nil {
		t.Fatalf("corrupting the store root %s: %v", root, err)
	}
	if n == 0 {
		t.Fatalf("no gob payload found under %s — nothing to corrupt", root)
	}
	return n
}

// TestAttachSnapshot_RepairsCorpusWithMissingLexicalLeg is acceptance
// criterion 1: the lexical leg of a PUBLISHED corpus is gone (nothing can
// serve it), and one ordinary attach brings it back — no user action, no
// second pass. The publication gate itself reports the snapshot unpublished
// (`publishedCompatible`), so the same pass rebuilds it.
func TestAttachSnapshot_RepairsCorpusWithMissingLexicalLeg(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	genDir, _, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("the fixture never adopted a §3 generation")
	}
	snapLex := filepath.Join(snapshotDir(genDir, key), "lexical")
	if !pathExists(snapLex) {
		t.Fatalf("the build landed no §3 snapshot lexical leg at %s", snapLex)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}

	// Damage: both possible lexical legs of the published corpus are gone.
	for _, leg := range []string{snapLex, lexicalIndexPath(f.storage, key)} {
		if err := os.RemoveAll(leg); err != nil {
			t.Fatalf("removing the corpus lexical leg %s: %v", leg, err)
		}
	}
	if pathExists(snapLex) || pathExists(lexicalIndexPath(f.storage, key)) {
		t.Fatal("the lexical-leg injection did not take")
	}
	if !snapshotTablePublished(genDir, key) {
		t.Fatal("the fixture must keep the §3 table: this criterion is about the missing LEG, not a lost table")
	}
	if f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Fatal("a corpus whose every lexical leg is gone must not report as published")
	}

	// ONE pass, no user involvement.
	second := f.newService(nil, nil)
	passes := &int32Counter{}
	if err := f.attachView(second, f.newIndexer(second, passes, nil)); err != nil {
		t.Fatalf("attach over the missing lexical leg: %v", err)
	}
	if got := passes.value(); got != 1 {
		t.Errorf("attach ran %d passes; the leg repair rides the one ordinary pass", got)
	}
	if !second.CorpusAttached() {
		t.Fatal("the repaired corpus is not attached")
	}
	if n := second.CorpusFileCount(); n == 0 {
		t.Error("the repaired corpus serves no file to this root")
	}
	if !pathExists(snapLex) && !pathExists(lexicalIndexPath(f.storage, key)) {
		t.Error("the rebuild did not restore the corpus's lexical leg on disk")
	}
	if !f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Error("the rebuilt corpus is not published")
	}
}

// TestAttachSnapshot_RepairsCorruptOverlayWithoutRebuildingCorpus is
// acceptance criterion 2: a corrupt overlay (an undecodable chromem root)
// heals, and the shared corpus is NOT rebuilt — the snapshot's ledger bytes
// and the chunk records are byte-identical, and not one text is re-embedded.
func TestAttachSnapshot_RepairsCorruptOverlayWithoutRebuildingCorpus(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}

	before := computeStoreLedger(f.storage)
	if before == nil || before.ChunkRecords == 0 {
		t.Fatalf("the fixture published no §3 chunk records: %+v", before)
	}
	corpusDir := branchRootPath(f.storage, key)
	corpusBytes := dirBytes(corpusDir)

	// Damage the OVERLAY only: every gob payload under the root's own store.
	overlayKey := overlayRootKey(f.root)
	if n := corruptGobFiles(t, branchRootPath(f.storage, overlayKey)); n == 0 {
		t.Fatal("no overlay store to corrupt")
	}

	// The retry decision is logged exactly once — the assertion for
	// acceptance criterion 4's "exactly one retry" on the success path.
	logs := &logCapture{}
	counter := &embedCounter{}
	passes := &int32Counter{}
	second := f.newService(counter, logs)
	if err := f.attachView(second, f.newIndexer(second, passes, logs)); err != nil {
		t.Fatalf("attach over a corrupt overlay: %v", err)
	}
	if got := passes.value(); got != 2 {
		t.Errorf("attach ran %d passes; a quarantined overlay costs exactly one retry (2 passes)", got)
	}
	if n := logs.count("quarantining the affected store and retrying"); n != 1 {
		t.Errorf("quarantine-and-retry was logged %d times; want exactly 1", n)
	}
	if n := logs.count("quarantined a store with a proven defect"); n != 1 {
		t.Errorf("the quarantine itself was logged %d times; want exactly 1", n)
	}
	if !second.CorpusAttached() {
		t.Fatal("the repaired attach serves no corpus")
	}
	if got := f.attachedKey(second); got != key {
		t.Fatalf("the repaired attach serves snapshot %q; want %q", got, key)
	}
	if n := counter.count(); n != 0 {
		t.Errorf("the repair re-embedded %d texts; a corrupt overlay must not rebuild the corpus", n)
	}

	after := computeStoreLedger(f.storage)
	if after == nil {
		t.Fatal("the §3 ledger vanished across the repair")
	}
	if after.ChunkRecords != before.ChunkRecords || after.ChunkBytes != before.ChunkBytes {
		t.Errorf("chunk records changed across the overlay repair: %d/%d → %d/%d",
			before.ChunkRecords, before.ChunkBytes, after.ChunkRecords, after.ChunkBytes)
	}
	if after.SnapshotBytes[key] != before.SnapshotBytes[key] {
		t.Errorf("snapshot %s ledger bytes changed across the overlay repair: %d → %d",
			key, before.SnapshotBytes[key], after.SnapshotBytes[key])
	}
	if got := dirBytes(corpusDir); got != corpusBytes {
		t.Errorf("corpus store bytes changed across the overlay repair: %d → %d", corpusBytes, got)
	}
	if second.GetCollection() == nil {
		t.Error("the repaired overlay has no working collection")
	}
}

// TestAttachSnapshot_TransientFaultLeavesPublicationIntact is acceptance
// criterion 3: an injected EACCES proves nothing about the store, so the
// publication stays byte-identical, NOTHING is quarantined, no retry runs and
// the failure surfaces its cause.
func TestAttachSnapshot_TransientFaultLeavesPublicationIntact(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	before := computeStoreLedger(f.storage)
	corpusDir := branchRootPath(f.storage, key)
	corpusBytes := dirBytes(corpusDir)

	// Every overlay open is denied — the transient fault of the criterion.
	seam := installRepairDBSeam(t)
	overlayRoot := branchRootPath(f.storage, overlayRootKey(f.root))
	seam.setFail(func(path string) error {
		if path != filepath.Clean(overlayRoot) {
			return nil
		}
		return fmt.Errorf("opening the overlay store at %s: %w", path,
			&fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission})
	})

	logs := &logCapture{}
	passes := &int32Counter{}
	second := f.newService(nil, logs)
	err := f.attachView(second, f.newIndexer(second, passes, logs))
	if err == nil {
		t.Fatal("attach over a denied overlay must fail")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the failure does not carry EACCES: %v", err)
	}
	if !errors.Is(err, ErrOverlayUnopenable) {
		t.Errorf("the failure does not name the overlay stage: %v", err)
	}
	if Repairable(err) {
		t.Error("a permission fault must not classify as a repairable store defect")
	}
	if !Transient(err) {
		t.Error("a permission fault is transient")
	}
	if kind := ClassifyInitError(err); kind != IndexErrorKindTransient {
		t.Errorf("the status surface classifies the cause as %q; want %q", kind, IndexErrorKindTransient)
	}
	if got := passes.value(); got != 1 {
		t.Errorf("attach ran %d passes; a transient fault must not be retried", got)
	}
	if n := logs.count("quarantining the affected store and retrying"); n != 0 {
		t.Errorf("a transient fault was quarantined (%d log records); want 0", n)
	}
	if n := logs.count("quarantined a store with a proven defect"); n != 0 {
		t.Errorf("a transient fault quarantined a store (%d log records); want 0", n)
	}
	if n := seam.count(overlayRoot); n != 1 {
		t.Errorf("the overlay store was opened %d times; want exactly 1 (no retry)", n)
	}

	// Nothing was destroyed: the publication is byte-identical.
	after := computeStoreLedger(f.storage)
	if after == nil || after.ChunkRecords != before.ChunkRecords || after.SnapshotBytes[key] != before.SnapshotBytes[key] {
		t.Errorf("the transient fault destroyed publication bytes: %+v → %+v", before, after)
	}
	if got := dirBytes(corpusDir); got != corpusBytes {
		t.Errorf("the corpus store changed under a transient fault: %d → %d", corpusBytes, got)
	}
	if !second.CorpusAttached() == false {
		t.Error("a failed attach must bind no corpus")
	}

	// With the fault gone the SAME store attaches — still without a rebuild.
	seam.setFail(nil)
	counter := &embedCounter{}
	third := f.newService(counter, nil)
	if attachErr := f.attachView(third, f.newIndexer(third, nil, nil)); attachErr != nil {
		t.Fatalf("attach after the transient fault cleared: %v", attachErr)
	}
	if n := counter.count(); n != 0 {
		t.Errorf("the recovered attach re-embedded %d texts; the publication was never damaged", n)
	}
	if got := dirBytes(corpusDir); got != corpusBytes {
		t.Errorf("the corpus store changed on the recovered attach: %d → %d", corpusBytes, got)
	}
}

// TestAttachSnapshot_RetriesExactlyOnceAndStaysFailClosed is acceptance
// criterion 4 (and the fail-closed half of the contract): a defect the repair
// cannot fix ends after exactly ONE retry, serves nothing at all — no
// foreign snapshot either — and leaves the healthy stores byte-identical, so
// the next attach recovers.
func TestAttachSnapshot_RetriesExactlyOnceAndStaysFailClosed(t *testing.T) {
	f := newRepairFixture(t)

	// A second root publishes its OWN corpus in the same storage: the
	// "foreign snapshot" a failed repair must never fall back to.
	other := &repairFixture{t: t, root: filepath.Join(t.TempDir(), "ws2"), storage: f.storage, registry: f.registry}
	if err := os.MkdirAll(other.root, 0o755); err != nil {
		t.Fatalf("mkdir second workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(other.root, "other.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatalf("write second workspace file: %v", err)
	}

	foreignSvc := other.newService(nil, nil)
	if err := other.attachView(foreignSvc, other.newIndexer(foreignSvc, nil, nil)); err != nil {
		t.Fatalf("attach of the second root: %v", err)
	}
	foreignKey := other.attachedKey(foreignSvc)
	foreignLedger := computeStoreLedger(f.storage)
	foreignBytes := dirBytes(branchRootPath(f.storage, foreignKey))
	if err := foreignSvc.Close(); err != nil {
		t.Fatalf("closing the second-root service: %v", err)
	}

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if key == foreignKey {
		t.Fatalf("the fixture's two roots resolved the same snapshot key %q", key)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	before := computeStoreLedger(f.storage)
	keyBytes := dirBytes(branchRootPath(f.storage, key))
	overlayRoot := branchRootPath(f.storage, overlayRootKey(f.root))

	// A defect the repair cannot clear: every overlay open fails, before and
	// after the quarantine.
	seam := installRepairDBSeam(t)
	seam.setFail(func(path string) error {
		if path != filepath.Clean(overlayRoot) {
			return nil
		}
		return fmt.Errorf("opening the overlay store at %s: %w", path, errChunkRecordTorn)
	})
	logs := &logCapture{}
	passes := &int32Counter{}
	second := f.newService(nil, nil)
	err := f.attachView(second, f.newIndexer(second, passes, logs))
	if err == nil {
		t.Fatal("attach over a persistent overlay defect must fail")
	}
	if got := passes.value(); got != 2 {
		t.Errorf("attach ran %d passes; a quarantining attach runs exactly one retry (2 passes)", got)
	}
	if n := seam.count(overlayRoot); n != 2 {
		t.Errorf("the overlay store was opened %d times; want exactly 2 (attempt + one retry, no cycle)", n)
	}
	if n := logs.count("retrying once"); n > 1 {
		t.Errorf("the retry decision was logged %d times; want at most 1", n)
	}
	// Fail-closed: nothing is served, neither this snapshot nor the foreign one.
	if second.CorpusAttached() {
		t.Error("a failed attach must bind no corpus at all")
	}
	if second.CorpusFileCount() != 0 {
		t.Errorf("a failed attach serves %d corpus files", second.CorpusFileCount())
	}
	if second.GetCollection() != nil {
		t.Error("a failed attach must leave no working collection behind")
	}
	// The repair never destroyed a healthy publication, and never reached
	// past its own scope.
	after := computeStoreLedger(f.storage)
	if after == nil || after.ChunkRecords != before.ChunkRecords || after.ChunkBytes != before.ChunkBytes {
		t.Errorf("chunk records changed across the failed repair: %+v → %+v", before, after)
	}
	if after.SnapshotBytes[key] != before.SnapshotBytes[key] {
		t.Errorf("snapshot %s bytes changed across the failed repair: %d → %d",
			key, before.SnapshotBytes[key], after.SnapshotBytes[key])
	}
	if after.SnapshotBytes[foreignKey] != foreignLedger.SnapshotBytes[foreignKey] {
		t.Errorf("the repair touched the FOREIGN snapshot %s: %d → %d", foreignKey,
			foreignLedger.SnapshotBytes[foreignKey], after.SnapshotBytes[foreignKey])
	}
	if got := dirBytes(branchRootPath(f.storage, foreignKey)); got != foreignBytes {
		t.Errorf("the foreign corpus store changed: %d → %d", foreignBytes, got)
	}
	if got := dirBytes(branchRootPath(f.storage, key)); got != keyBytes {
		t.Errorf("the failed repair destroyed the healthy corpus store: %d → %d", keyBytes, got)
	}

	// No repair opens a door to another embedding era: a foreign-era reader
	// is still refused the corpus, and the refusal names the era.
	if h, eraErr := f.registry.Open(context.Background(), f.storage, key, fakeEmbeddingFunc(), "fp-foreign", 8); !errors.Is(eraErr, ErrCorpusEraMismatch) {
		if h != nil {
			h.Release()
		}
		t.Fatalf("a foreign-era open returned %v; want ErrCorpusEraMismatch", eraErr)
	}

	// The defect cleared: the very next attach recovers WITHOUT a rebuild and
	// serves exactly its own snapshot.
	seam.setFail(nil)
	counter := &embedCounter{}
	third := f.newService(counter, nil)
	if attachErr := f.attachView(third, f.newIndexer(third, nil, nil)); attachErr != nil {
		t.Fatalf("attach after the defect cleared: %v", attachErr)
	}
	if got := f.attachedKey(third); got != key {
		t.Errorf("recovered attach serves snapshot %q; want its own %q", got, key)
	}
	if n := counter.count(); n != 0 {
		t.Errorf("the recovered attach re-embedded %d texts; no corpus was damaged", n)
	}
}

// TestQuarantineStoreView_ScopesAreDisjointAndSpareSharedData is acceptance
// criterion 5: each scope removes exactly its own store's artifacts. Neither
// scope touches the content-addressed chunk records or the embedding cache,
// and neither scope crosses into the other store.
func TestQuarantineStoreView_ScopesAreDisjointAndSpareSharedData(t *testing.T) {
	ctx := context.Background()
	f := newRepairFixture(t)

	svc := f.newService(nil, nil)
	if err := f.attachView(svc, f.newIndexer(svc, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(svc)
	overlayKey := overlayRootKey(f.root)
	genDir, _, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("no §3 generation adopted")
	}
	overlayStateDir := rootStateDir(genDir, overlayKey)
	if !pathExists(overlayStateDir) {
		t.Fatalf("the fixture landed no §3 overlay state at %s", overlayStateDir)
	}
	ledger := computeStoreLedger(f.storage)
	chunksDir := filepath.Join(genDir, storeChunksDirName)
	chunkBytes := dirBytes(chunksDir)
	cacheDir := filepath.Join(f.storage, "embedding_cache")
	cacheBytes := dirBytes(cacheDir)
	if chunkBytes == 0 {
		t.Fatal("the fixture published no chunk records")
	}

	// OVERLAY scope: the root's private store goes, every shared artifact stays.
	if err := svc.QuarantineStoreView(ctx, key, overlayKey, QuarantineOverlayStore); err != nil {
		t.Fatalf("quarantining the overlay: %v", err)
	}
	for _, gone := range []string{
		branchRootPath(f.storage, overlayKey),
		lexicalIndexPath(f.storage, overlayKey),
		fileHashesPathFor(f.storage, overlayKey),
		overlayStateDir,
	} {
		if pathExists(gone) {
			t.Errorf("the overlay quarantine left %s behind", gone)
		}
	}
	for _, kept := range []string{
		branchRootPath(f.storage, key),
		filepath.Join(branchRootPath(f.storage, key), corpusManifestName),
		filepath.Join(snapshotDir(genDir, key), snapshotTableName),
		filepath.Join(snapshotDir(genDir, key), "lexical"),
		chunksDir,
	} {
		if !pathExists(kept) {
			t.Errorf("the overlay quarantine destroyed the corpus artifact %s", kept)
		}
	}
	if got := dirBytes(chunksDir); got != chunkBytes {
		t.Errorf("the overlay quarantine changed the chunk records: %d → %d", chunkBytes, got)
	}
	if got := dirBytes(cacheDir); got != cacheBytes {
		t.Errorf("the overlay quarantine changed the embedding cache: %d → %d", cacheBytes, got)
	}
	if !f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Error("the corpus is no longer published after an OVERLAY quarantine")
	}

	// CORPUS scope: the snapshot's publication and artifacts go, the shared
	// chunk records and the cache stay (they are content-addressed and
	// re-derivable, never one store's private damage).
	if err := svc.QuarantineStoreView(ctx, key, overlayKey, QuarantineCorpusStore); err != nil {
		t.Fatalf("quarantining the corpus: %v", err)
	}
	for _, gone := range []string{
		filepath.Join(snapshotDir(genDir, key), snapshotTableName),
		snapshotDir(genDir, key),
		branchRootPath(f.storage, key),
		lexicalIndexPath(f.storage, key),
		fileHashesPathFor(f.storage, key),
	} {
		if pathExists(gone) {
			t.Errorf("the corpus quarantine left %s behind", gone)
		}
	}
	if !pathExists(chunksDir) {
		t.Error("the corpus quarantine destroyed the content-addressed chunk records")
	}
	if got := dirBytes(chunksDir); got != chunkBytes {
		t.Errorf("the corpus quarantine changed the chunk records: %d → %d", chunkBytes, got)
	}
	if got := dirBytes(cacheDir); got != cacheBytes {
		t.Errorf("the corpus quarantine changed the embedding cache: %d → %d", cacheBytes, got)
	}
	if got := computeStoreLedger(f.storage); got == nil || got.ChunkRecords != ledger.ChunkRecords {
		t.Errorf("the corpus quarantine dropped chunk records: %+v → %+v", ledger, got)
	}
	if f.registry.storeFor(f.storage).publishedCompatible(key, "fp-repair", 8) {
		t.Error("a quarantined corpus still reports as published")
	}

	// The ordinary attach rebuilds the quarantined corpus in one pass.
	counter := &embedCounter{}
	rebuilt := f.newService(counter, nil)
	if err := f.attachView(rebuilt, f.newIndexer(rebuilt, nil, nil)); err != nil {
		t.Fatalf("attach after the corpus quarantine: %v", err)
	}
	if counter.count() == 0 {
		t.Error("the rebuild reused nothing: a quarantined corpus is rebuilt from the workspace")
	}
	if !rebuilt.CorpusAttached() || rebuilt.CorpusFileCount() == 0 {
		t.Error("the rebuilt corpus is not served")
	}
}

// TestAttachSnapshot_RepairsCorpusStageWithCorpusScope pins the stage→scope
// rule: a defect in the CORPUS open selects the corpus scope (never the
// overlay's), so the one retry rebuilds the snapshot — a chunk record that no
// longer decodes is re-derived from source — instead of replaying the same
// failing open. The v1 fallback is removed first, so the corpus stage really
// is the failing one (the §3 open's own unpublish and this quarantine both
// run before the retry).
func TestAttachSnapshot_RepairsCorpusStageWithCorpusScope(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("no §3 generation adopted")
	}

	// One chunk record no longer decodes, and the v1 fallback is gone.
	victim := ""
	chunksRoot := filepath.Join(genDir, storeChunksDirName)
	if walkErr := filepath.WalkDir(chunksRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || victim != "" {
			return nil //nolint:nilerr // best-effort fixture damage
		}
		victim = p
		return nil
	}); walkErr != nil {
		t.Fatalf("walking the chunk records: %v", walkErr)
	}
	if victim == "" {
		t.Fatal("the fixture published no chunk records")
	}
	if err := os.WriteFile(victim, []byte("torn"), 0o644); err != nil {
		t.Fatalf("tearing the chunk record: %v", err)
	}
	if err := os.Remove(filepath.Join(branchRootPath(f.storage, key), corpusManifestName)); err != nil {
		t.Fatalf("removing the v1 corpus manifest: %v", err)
	}

	logs := &logCapture{}
	counter := &embedCounter{}
	passes := &int32Counter{}
	second := f.newService(counter, logs)
	if err := f.attachView(second, f.newIndexer(second, passes, logs)); err != nil {
		t.Fatalf("attach over a torn corpus chunk record: %v", err)
	}
	if got := passes.value(); got != 2 {
		t.Errorf("attach ran %d passes; the corpus repair costs exactly one retry (2 passes)", got)
	}
	if n := logs.count("quarantining the affected store and retrying", "scope=corpus"); n != 1 {
		t.Errorf("the repair chose its scope %d times from the corpus stage; want exactly 1 corpus scope", n)
	}
	if n := logs.count("scope=overlay"); n != 0 {
		t.Errorf("a corpus-stage defect was scoped to the overlay (%d records)", n)
	}
	if !second.CorpusAttached() {
		t.Fatal("the rebuilt corpus is not attached")
	}
	if n := second.CorpusFileCount(); n == 0 {
		t.Error("the rebuilt corpus serves no file to this root")
	}
	if n := counter.count(); n == 0 {
		t.Error("the corpus was not rebuilt from source")
	}
	if !pathExists(filepath.Join(branchRootPath(f.storage, key), corpusManifestName)) {
		t.Error("the rebuilt corpus published no manifest")
	}
	if info, statErr := os.Stat(victim); statErr != nil || info.Size() == int64(len("torn")) {
		t.Errorf("the torn chunk record was not re-derived: %v (size %d)", statErr, info.Size())
	}
}
