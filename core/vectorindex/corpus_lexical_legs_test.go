package vectorindex

// The two-legged lexical resolve shared by BOTH corpus open paths
// (openCorpusLexicalReadOnly, wired into openV2Corpus and corpusStore.open's
// v1 fallback): the §3 snapshot directory first, the legacy per-key path
// second — with each path's failure semantics pinned. The v1 fallback must
// resolve the snapshot directory's index instead of structurally ENOENTing
// on the legacy path (the native build's only lexical leg), and the §3 open
// keeps its fail-closed verdict for a snapshot with no lexical leg at all.

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/vectorindex/lexical"
)

// tearOneChunkRecord damages exactly one chunk record under the generation's
// chunk shards — the on-disk shape of proven store damage (a record that no
// longer decodes) — and returns the victim's path.
func tearOneChunkRecord(t *testing.T, genDir string) string {
	t.Helper()
	var victim string
	if walkErr := filepath.WalkDir(filepath.Join(genDir, storeChunksDirName), func(p string, d fs.DirEntry, err error) error {
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
	return victim
}

// assertNativeCorpusShape fails the test unless the fixture's native §3
// build landed exactly the shape the leg tests damage from: the tables and
// the v1 manifest published, the lexical index ONLY inside the snapshot
// directory. Returns the current generation directory and both lexical leg
// paths.
func assertNativeCorpusShape(t *testing.T, f *repairFixture, key string) (genDir, snapLex, legacyLex string) {
	t.Helper()
	dir, _, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("no §3 generation adopted")
	}
	snapLex = filepath.Join(snapshotDir(dir, key), "lexical")
	legacyLex = lexicalIndexPath(f.storage, key)
	if !pathExists(snapLex) {
		t.Fatalf("the native build landed no §3 snapshot lexical leg at %s", snapLex)
	}
	if pathExists(legacyLex) {
		t.Fatalf("the native build must leave no legacy lexical leg at %s", legacyLex)
	}
	if !pathExists(filepath.Join(branchRootPath(f.storage, key), corpusManifestName)) {
		t.Fatal("the native build published no v1 corpus manifest")
	}
	return dir, snapLex, legacyLex
}

// TestAttachSnapshot_V1FallbackResolvesSnapshotLexicalLeg pins the
// structural-ENOENT fix on the v1 fallback leg: a NATIVE §3 corpus (tables
// + corpus.json published, the lexical leg only inside the snapshot
// directory) damaged by a torn chunk record fails its §3 open — which
// unpublishes the table — and the attach must serve the v1 store by
// resolving the lexical leg from the snapshot directory. Resolving the
// legacy per-key path alone ENOENTs exactly the snapshots this fallback
// exists to serve and wedges the attach into a quarantine + rebuild.
func TestAttachSnapshot_V1FallbackResolvesSnapshotLexicalLeg(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, legacyLex := assertNativeCorpusShape(t, f, key)

	// Proven store damage: one chunk record no longer decodes. The §3
	// open fails (and unpublishes the table); the v1 store — its manifest
	// kept — becomes the serving leg.
	tearOneChunkRecord(t, genDir)

	logs := &logCapture{}
	passes := &int32Counter{}
	second := f.newService(nil, logs)
	if err := f.attachView(second, f.newIndexer(second, passes, logs)); err != nil {
		t.Fatalf("attach over a torn §3 record must serve the v1 store: %v", err)
	}
	if got := passes.value(); got != 1 {
		t.Errorf("attach ran %d passes; the v1 fallback needs no repair pass", got)
	}
	if n := logs.count("quarantining the affected store and retrying"); n != 0 {
		t.Errorf("the v1 fallback attach ran the quarantine ladder (%d records)", n)
	}
	if !second.CorpusAttached() {
		t.Fatal("the v1 fallback did not attach the corpus")
	}
	if n := second.CorpusFileCount(); n == 0 {
		t.Error("the v1-served corpus serves no file to this root")
	}
	if pathExists(legacyLex) {
		t.Error("the fallback conjured a legacy lexical leg; it must resolve the snapshot directory")
	}
	// The serving handle is the V1 one: the §3 view failed, so the handle
	// carries the decoded chromem collection, never the v2 corpus.
	second.mu.RLock()
	h := second.current.corpus
	servedV1 := h != nil && h.v2 == nil && h.collection != nil && h.lexical != nil
	second.mu.RUnlock()
	if !servedV1 {
		t.Error("the attached handle is not the v1-fallback handle (a §3 view or nil legs)")
	}
}

// TestAttachSnapshot_V1FallbackPrefersSnapshotLexicalLeg pins the resolve's
// priority: when BOTH legs exist, the snapshot directory's index is the one
// opened. The legacy leg is staged as a valid but EMPTY index — only their
// content tells the legs apart, so the attached handle's lexical count
// discriminates: a legacy-first resolve would serve the empty index.
func TestAttachSnapshot_V1FallbackPrefersSnapshotLexicalLeg(t *testing.T) {
	f := newRepairFixture(t)

	first := f.newService(nil, nil)
	if err := f.attachView(first, f.newIndexer(first, nil, nil)); err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	key := f.attachedKey(first)
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	genDir, _, legacyLex := assertNativeCorpusShape(t, f, key)

	// Both legs now exist and open; only their content tells them apart.
	empty, err := lexical.Open(legacyLex)
	if err != nil {
		t.Fatalf("staging an empty legacy lexical index: %v", err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("closing the staged legacy index: %v", err)
	}

	// Force the v1 fallback: the §3 open fails on the torn chunk record.
	tearOneChunkRecord(t, genDir)

	second := f.newService(nil, nil)
	if err := f.attachView(second, f.newIndexer(second, nil, nil)); err != nil {
		t.Fatalf("attach over a torn §3 record must serve the v1 store: %v", err)
	}
	second.mu.RLock()
	h := second.current.corpus
	servedV1 := h != nil && h.v2 == nil && h.collection != nil
	second.mu.RUnlock()
	if !servedV1 {
		t.Fatal("the attach did not run the v1 fallback (no v1 handle)")
	}
	n, err := h.lexical.Count()
	if err != nil {
		t.Fatalf("counting the served lexical index: %v", err)
	}
	if n == 0 {
		t.Error("the v1 open served the EMPTY legacy leg; the snapshot directory's index must win")
	}
}

// TestOpenCorpus_LexicalResolveFailsClosedWhenBothLegsMissing pins the
// fail-closed rule the §3 open keeps (its previous behavior): a published
// snapshot with NO lexical leg on either path errors the §3 open with the
// "opening §3 corpus lexical index" verdict, carrying the missing leg's
// ENOENT — never a silently legless corpus view.
func TestOpenCorpus_LexicalResolveFailsClosedWhenBothLegsMissing(t *testing.T) {
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

	for _, leg := range []string{
		filepath.Join(snapshotDir(genDir, key), "lexical"),
		lexicalIndexPath(f.storage, key),
	} {
		if err := os.RemoveAll(leg); err != nil {
			t.Fatalf("removing the lexical leg %s: %v", leg, err)
		}
	}

	gen, genManifest, ok := readCurrentGeneration(f.storage)
	if !ok {
		t.Fatal("no §3 generation adopted")
	}
	logs := &logCapture{}
	v, err := openV2Corpus(gen, key, genManifest, slog.New(logs))
	if v != nil {
		t.Error("a snapshot with no lexical leg must not open a corpus view")
	}
	if err == nil {
		t.Fatal("a snapshot with no lexical leg must fail the §3 open")
	}
	if n := logs.count("unpublished §3 snapshot table"); n != 0 {
		t.Errorf("a lexical-only failure unpublished nothing but logged %d unpublish records", n)
	}
	if !strings.Contains(err.Error(), "opening §3 corpus lexical index") {
		t.Errorf("the fail-closed verdict must name the §3 lexical open, got: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the verdict must carry the missing leg's ENOENT, got: %v", err)
	}
}
