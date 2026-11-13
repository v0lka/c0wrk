package vectorindex

// Tests for the "a rebuild fails identically every time" class: a workspace
// that drops a subdirectory under the walk (or a degenerate tree with nothing
// to index) must never turn into an unexplained, endlessly repeated corpus
// build failure. The walk records per-entry gaps instead of aborting, and the
// publication gate refuses an empty index with a reason that distinguishes an
// EMPTY tree from an INCOMPLETE traversal
// (specs/domains/workspace.md, shared corpus).

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// walkLogRecord is one captured slog record with its attributes rendered.
type walkLogRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// walkLogRecorder is a test slog.Handler that keeps every record (severity,
// message, attributes) so the walk's diagnostics are asserted at their source
// — severity, path and skip count — rather than silently discarded.
type walkLogRecorder struct {
	mu      sync.Mutex
	records []walkLogRecord
}

func (h *walkLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *walkLogRecorder) Handle(_ context.Context, r slog.Record) error {
	rec := walkLogRecord{level: r.Level, msg: r.Message, attrs: make(map[string]string, r.NumAttrs())}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *walkLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *walkLogRecorder) WithGroup(string) slog.Handler      { return h }

// count returns how many records at level contain substr in their message.
func (h *walkLogRecorder) count(level slog.Level, substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, rec := range h.records {
		if rec.level == level && strings.Contains(rec.msg, substr) {
			n++
		}
	}
	return n
}

// lastAt returns the last record at level containing substr, if any.
func (h *walkLogRecorder) lastAt(level slog.Level, substr string) (walkLogRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.records) - 1; i >= 0; i-- {
		rec := h.records[i]
		if rec.level == level && strings.Contains(rec.msg, substr) {
			return rec, true
		}
	}
	return walkLogRecord{}, false
}

// vanishingChecker is an ignore.IgnoreChecker that deletes one directory the
// first time the walk asks about it — the deterministic stand-in for a
// subdirectory removed between its parent's enumeration and its visit (a
// `git checkout`/editor dropping a tree mid-pass), with no sleeps and no
// scheduler luck.
type vanishingChecker struct {
	victim  string
	removed bool
}

func (c *vanishingChecker) Ignored(path string, isDir bool) bool {
	if isDir && path == c.victim && !c.removed {
		c.removed = true
		_ = os.RemoveAll(path)
	}
	return false
}

// TestWalkProjectFiles_DeletedSubtreeMidWalkIsGapNotFailure pins the walk's
// per-entry policy: an entry that vanished under the walk is recorded as a gap
// and the traversal continues, so the files it CAN reach still index.
func TestWalkProjectFiles_DeletedSubtreeMidWalkIsGapNotFailure(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "keep", "a.go"), "package keep\n")
	writeFileT(t, filepath.Join(root, "doomed", "b.go"), "package doomed\n")
	writeFileT(t, filepath.Join(root, "z", "c.go"), "package z\n")

	victim := filepath.Join(root, "doomed")
	checker := &vanishingChecker{victim: victim}

	outcome, err := walkProjectFilesDetailed(root, checker, 1<<20, false, nil)
	if err != nil {
		t.Fatalf("a subtree deleted during the walk must not fail it: %v", err)
	}

	got := make(map[string]bool, len(outcome.Files))
	for _, f := range outcome.Files {
		rel, relErr := filepath.Rel(root, f)
		if relErr != nil {
			t.Fatalf("Rel(%q): %v", f, relErr)
		}
		got[filepath.ToSlash(rel)] = true
	}
	for _, want := range []string{"keep/a.go", "z/c.go"} {
		if !got[want] {
			t.Errorf("walk lost the reachable file %q: %v", want, got)
		}
	}
	if got["doomed/b.go"] {
		t.Errorf("walk reported a file from the deleted subtree: %v", got)
	}

	if len(outcome.Gaps) != 1 {
		t.Fatalf("walk gaps = %+v; want exactly the deleted subtree", outcome.Gaps)
	}
	if outcome.Gaps[0].Path != victim {
		t.Errorf("gap path = %q; want %q", outcome.Gaps[0].Path, victim)
	}
	if !errors.Is(outcome.Gaps[0].Err, fs.ErrNotExist) {
		t.Errorf("gap cause = %v; want fs.ErrNotExist", outcome.Gaps[0].Err)
	}

	// The compatibility wrapper keeps its two-value contract for the callers
	// that do not need the report.
	files, err := walkProjectFiles(root, noopIgnoreChecker{}, 1<<20, false)
	if err != nil {
		t.Fatalf("walkProjectFiles over a healthy tree: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("walkProjectFiles returned %d files; want the 2 reachable ones", len(files))
	}
}

// TestWalkProjectFiles_UnreadableRootIsExplicitError pins the abort boundary:
// the ROOT is the walk's subject, so an unreadable root is an explicit pass
// error — never a silently empty walk (which would publish nothing and report
// "no indexable files" for a tree nobody could read).
func TestWalkProjectFiles_UnreadableRootIsExplicitError(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "gone")

	outcome, err := walkProjectFilesDetailed(missing, noopIgnoreChecker{}, 1<<20, false, nil)
	if err == nil {
		t.Fatalf("walking a missing root succeeded with %d files and %d gaps", len(outcome.Files), len(outcome.Gaps))
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("walk error = %v; want it to carry fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("walk error %v does not name the root %s", err, missing)
	}
	if _, err := walkProjectFiles(missing, noopIgnoreChecker{}, 1<<20, false); err == nil {
		t.Error("walkProjectFiles over a missing root returned nil error")
	}
}

// TestIndexFull_SurvivesSubtreeDeletionAndStillPublishes is the end-to-end
// acceptance case: a subdirectory removed during the full pass neither fails
// the pass nor the corpus publication, and the log carries ONE aggregated skip
// line naming the path and the count.
func TestIndexFull_SurvivesSubtreeDeletionAndStillPublishes(t *testing.T) {
	svc := setupTestService(t)
	ws := t.TempDir()
	writeFileT(t, filepath.Join(ws, "keep", "a.go"), "package keep\n\nfunc A() {}\n")
	writeFileT(t, filepath.Join(ws, "doomed", "b.go"), "package doomed\n\nfunc B() {}\n")
	writeFileT(t, filepath.Join(ws, "z", "c.go"), "package z\n\nfunc C() {}\n")

	victim := filepath.Join(ws, "doomed")
	rec := &walkLogRecorder{}
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		Logger:  slog.New(rec),
	})
	idx.ignoreRoot = ws
	idx.ignoreChecker = &vanishingChecker{victim: victim}

	ctx := context.Background()
	if err := idx.IndexFull(ctx, ws); err != nil {
		t.Fatalf("IndexFull failed on a subtree deleted mid-walk: %v", err)
	}

	col := svc.GetCollection()
	if col == nil {
		t.Fatal("the pass left no collection")
	}
	for _, rel := range []string{"keep/a.go", "z/c.go"} {
		if _, err := col.GetByID(ctx, DocumentID(rel, 0)); err != nil {
			t.Errorf("reachable file %q was not indexed: %v", rel, err)
		}
	}
	if _, err := col.GetByID(ctx, DocumentID("doomed/b.go", 0)); err == nil {
		t.Error("a document from the deleted subtree was indexed")
	}

	// ONE aggregated WARN per pass: the path and the skip counter.
	if got := rec.count(slog.LevelWarn, "workspace walk skipped unreadable entries"); got != 1 {
		t.Fatalf("walk-skip WARN lines = %d; want exactly 1", got)
	}
	warn, ok := rec.lastAt(slog.LevelWarn, "workspace walk skipped unreadable entries")
	if !ok {
		t.Fatal("no walk-skip WARN recorded")
	}
	if warn.attrs["skipped"] != "1" {
		t.Errorf("skip counter attr = %q; want 1", warn.attrs["skipped"])
	}
	if warn.attrs["path"] != victim {
		t.Errorf("skip path attr = %q; want %q", warn.attrs["path"], victim)
	}

	// The publication is not blocked: the corpus covers what the walk reached.
	if err := svc.PublishCorpus(ctx, "main"); err != nil {
		t.Fatalf("PublishCorpus after a mid-walk subtree deletion: %v", err)
	}
	svc.mu.RLock()
	storage := svc.current.projectPath
	svc.mu.RUnlock()
	m := readCorpusManifest(branchRootPath(storage, "main"))
	if m == nil {
		t.Fatal("the pass published no manifest")
	}
	if len(m.Files) != 2 {
		t.Fatalf("manifest files = %v; want the 2 reachable files", m.Files)
	}
	for _, rel := range []string{"keep/a.go", "z/c.go"} {
		if _, ok := m.Files[rel]; !ok {
			t.Errorf("manifest is missing %q: %v", rel, m.Files)
		}
	}
}

// corpusServiceForGate builds a corpus-enabled service with a §3 generation
// adopted, so the publication gate's ordering (empty index publishes NEITHER
// the §3 snapshot table NOR the v1 manifest) is observable.
func corpusServiceForGate(t *testing.T, storage, key string) *Service {
	t.Helper()
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
	if err := svc.SwitchBranch(context.Background(), key); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	return svc
}

// assertNothingPublished fails when a refused publish left either publication
// form behind: the v1 manifest or the §3 snapshot table (which its own open
// path serves, so an empty table would be an "empty but valid" corpus).
func assertNothingPublished(t *testing.T, svc *Service, storage, key string) {
	t.Helper()
	if m := readCorpusManifest(branchRootPath(storage, key)); m != nil {
		t.Fatalf("a refused publish landed a v1 manifest: %+v", m)
	}
	svc.mu.RLock()
	gen := svc.current.storeGen
	svc.mu.RUnlock()
	if gen == "" {
		t.Fatal("the §3 generation was not adopted; the publication ordering is not observable")
	}
	if snapshotTablePublished(gen, key) {
		t.Error("a refused publish landed the §3 snapshot table — an empty but valid corpus")
	}
}

// TestIndexFull_UnreadableRootFailsThePassExplicitly pins the pass-level
// boundary: a missing root is an explicit traversal error, never downgraded to
// a per-entry gap and never a silently empty pass.
func TestIndexFull_UnreadableRootFailsThePassExplicitly(t *testing.T) {
	svc := setupTestService(t)
	missing := filepath.Join(t.TempDir(), "gone")
	rec := &walkLogRecorder{}
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		Logger:  slog.New(rec),
	})

	err := idx.IndexFull(context.Background(), missing)
	if err == nil {
		t.Fatal("IndexFull over a missing root succeeded")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("pass error = %v; want it to carry fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "walking project files") || !strings.Contains(err.Error(), missing) {
		t.Errorf("pass error %v does not name the failed walk of %s", err, missing)
	}
	if got := rec.count(slog.LevelWarn, "workspace walk skipped unreadable entries"); got != 0 {
		t.Errorf("a root failure was reported as a per-entry skip (%d WARN lines); it must abort the pass", got)
	}
}

// TestPublishCorpus_RefusesEmptyIndexWithActionableReason pins the degenerate
// build: a cleanly walked tree with no indexable file is unavailable, with the
// actionable reason, and publishes nothing.
func TestPublishCorpus_RefusesEmptyIndexWithActionableReason(t *testing.T) {
	storage := t.TempDir()
	const key = "snap_00112233445566aa"
	svc := corpusServiceForGate(t, storage, key)

	ws := t.TempDir() // a real, empty tree: the walk reaches everything
	idx := NewIndexer(IndexerConfig{Service: svc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})
	ctx := context.Background()
	if err := idx.IndexFull(ctx, ws); err != nil {
		t.Fatalf("IndexFull over an empty workspace: %v", err)
	}

	err := svc.PublishCorpus(ctx, key)
	if !errors.Is(err, ErrCorpusNoIndexableFiles) {
		t.Fatalf("PublishCorpus error = %v; want ErrCorpusNoIndexableFiles", err)
	}
	if !strings.Contains(err.Error(), "no indexable files — check exclude patterns and ignore files") {
		t.Errorf("publish error is not actionable: %v", err)
	}
	if strings.Contains(err.Error(), "incomplete workspace walk") {
		t.Errorf("a cleanly walked empty tree was reported as an incomplete walk: %v", err)
	}
	assertNothingPublished(t, svc, storage, key)
}

// TestPublishCorpus_EmptyIndexFromIncompleteWalkNamesGap closes the
// "partial skip publishes an empty-but-valid corpus" hole: when the walk
// skipped entries, an empty index does not prove the tree is empty, so the
// refusal names the traversal gap (and still publishes nothing).
func TestPublishCorpus_EmptyIndexFromIncompleteWalkNamesGap(t *testing.T) {
	storage := t.TempDir()
	const key = "snap_ffeeddccbbaa9988"
	svc := corpusServiceForGate(t, storage, key)
	ctx := context.Background()

	gapPath := filepath.Join(t.TempDir(), "vanished")
	svc.noteWalkGaps(key, []walkGap{{
		Path: gapPath,
		Err:  &fs.PathError{Op: "readdir", Path: gapPath, Err: fs.ErrNotExist},
	}})

	err := svc.PublishCorpus(ctx, key)
	if err == nil {
		t.Fatal("PublishCorpus published an empty index produced by an incomplete walk")
	}
	if !errors.Is(err, ErrCorpusWalkIncomplete) {
		t.Errorf("publish error = %v; want ErrCorpusWalkIncomplete", err)
	}
	if errors.Is(err, ErrCorpusNoIndexableFiles) {
		t.Errorf("an incomplete traversal was reported as a genuinely empty tree: %v", err)
	}
	// The per-entry cause is diagnostic TEXT: an ENOENT under the walk must
	// not reclassify the refusal as store damage, or the repair path would
	// quarantine and rebuild the store to no effect.
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a per-entry walk cause reclassified the refusal as store damage: %v", err)
	}
	for _, want := range []string{"walk was incomplete", "1 unreadable entry", gapPath, fs.ErrNotExist.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("publish error %v does not name %q", err, want)
		}
	}
	assertNothingPublished(t, svc, storage, key)

	// Control: the SAME store, now with a clean walk report, is refused as a
	// genuinely empty tree — the gap report replaced the previous one.
	svc.noteWalkGaps(key, nil)
	cleanErr := svc.PublishCorpus(ctx, key)
	if !errors.Is(cleanErr, ErrCorpusNoIndexableFiles) {
		t.Fatalf("PublishCorpus after a clean walk = %v; want ErrCorpusNoIndexableFiles", cleanErr)
	}
	assertNothingPublished(t, svc, storage, key)
}

// TestAttachSnapshot_EmptyWorkspaceFailsOncePerAttach pins the rebuild
// contract: a project with no indexable files produces ONE build attempt and
// ONE refusal per attach — the action never loops internally, so the Rebuild
// button costs one attempt and one log entry each time it is pressed.
func TestAttachSnapshot_EmptyWorkspaceFailsOncePerAttach(t *testing.T) {
	storage := t.TempDir()
	rec := &walkLogRecorder{}
	// The per-attempt build line is emitted by the corpus registry, so the
	// attempt counter reads the registry's logger.
	reg := NewCorpusRegistry(slog.New(rec))
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
	ws := t.TempDir()
	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		Logger:  slog.New(rec),
	})
	ctx := context.Background()

	// Each attach is exactly one build attempt (the "legacy store not
	// promotable; building a fresh corpus" line is emitted per attempt).
	for attempt := 1; attempt <= 2; attempt++ {
		attachErr := idx.AttachSnapshot(ctx, ws, "")
		if !errors.Is(attachErr, ErrCorpusNoIndexableFiles) {
			t.Fatalf("attach #%d error = %v; want ErrCorpusNoIndexableFiles", attempt, attachErr)
		}
		if !strings.Contains(attachErr.Error(), "no indexable files") {
			t.Errorf("attach #%d error is not actionable: %v", attempt, attachErr)
		}
		if got := rec.count(slog.LevelDebug, "building a fresh corpus"); got != attempt {
			t.Fatalf("after attach #%d the build attempts logged = %d; want %d (one per attach, no internal retry)",
				attempt, got, attempt)
		}
	}
}

// TestAttachSnapshot_IncompleteWalkNeverPublishesEmptyCorpus is the end-to-end
// proof for the "partial skip publishes an empty-but-valid corpus" hole: a
// tree whose ONLY content disappears under the walk yields zero reachable
// files, so the pass must refuse the publication, land NEITHER publication
// form, and — because a degenerate workspace is not store damage — NOT
// quarantine the store for a second, equally empty attempt.
func TestAttachSnapshot_IncompleteWalkNeverPublishesEmptyCorpus(t *testing.T) {
	storage := t.TempDir()
	rec := &walkLogRecorder{}
	reg := NewCorpusRegistry(slog.New(rec))
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

	ws := t.TempDir()
	writeFileT(t, filepath.Join(ws, "doomed", "only.go"), "package doomed\n")
	victim := filepath.Join(ws, "doomed")

	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
		Logger:  slog.New(rec),
	})
	idx.ignoreRoot = ws
	idx.ignoreChecker = &vanishingChecker{victim: victim}

	ctx := context.Background()
	attachErr := idx.AttachSnapshot(ctx, ws, "")
	if !errors.Is(attachErr, ErrCorpusWalkIncomplete) {
		t.Fatalf("attach error = %v; want ErrCorpusWalkIncomplete", attachErr)
	}
	if !strings.Contains(attachErr.Error(), victim) {
		t.Errorf("attach error %v does not name the skipped entry %s", attachErr, victim)
	}
	if got := rec.count(slog.LevelDebug, "building a fresh corpus"); got != 1 {
		t.Errorf("build attempts = %d; want 1 (a degenerate workspace must not be quarantined and retried)", got)
	}
	if got := rec.count(slog.LevelWarn, "quarantining the affected store"); got != 0 {
		t.Errorf("attach quarantined a store for a workspace gap: %d WARN lines", got)
	}

	key := snapshotKey("ngit-" + rootFingerprint(ws))
	assertNothingPublished(t, svc, storage, key)
}

// TestWalkProjectFiles_EntryCallbackSeesEveryVisit pins the walk-heartbeat
// wiring: onEntry observes the entries the walk VISITS — the root, plain
// files, subdirectories and hidden/ignored entries alike — because a slow
// filesystem paces the arrival of entries, not of accepted files, and the
// stall-watchdog feed (see walkHeartbeater) rides on exactly that signal.
func TestWalkProjectFiles_EntryCallbackSeesEveryVisit(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "a.go"), "package main\n")
	writeFileT(t, filepath.Join(root, "sub", "b.go"), "package sub\n")
	writeFileT(t, filepath.Join(root, ".hidden", "c.go"), "package hidden\n")

	seen := make(map[string]bool)
	onEntry := func(p string) { seen[p] = true }
	if _, err := walkProjectFilesDetailed(root, noopIgnoreChecker{}, 1<<20, false, onEntry); err != nil {
		t.Fatalf("walkProjectFilesDetailed: %v", err)
	}
	for _, want := range []string{
		root,
		filepath.Join(root, "a.go"),
		filepath.Join(root, "sub"),
		filepath.Join(root, "sub", "b.go"),
		// Hidden entries are visited before being pruned — the readdir that
		// surfaced them is exactly the slow-storage work the feed covers.
		filepath.Join(root, ".hidden"),
	} {
		if !seen[want] {
			t.Errorf("entry callback missed %q; visited = %v", want, seen)
		}
	}
}
