package vectorindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/safeio"
)

// The reader/writer size caps (occurrenceTabMaxBytes, corpusManifestMaxBytes,
// fileHashSidecarMaxBytes) are reader-parity contracts: a payload its reader
// cannot open is refused BEFORE it lands on disk (writers), and on the read
// side folds to the absent/unpublished semantics with the cause preserved and
// no legacy fallback. The sparse-truncate shape mirrors
// TestSnapshotReadLimit_PreservesCauseAndPublication (os.Truncate — the bytes
// are never materialized) at the new, larger caps.

// bloatEntries fills a payload map with 70 × 1 MiB hash-like values — models
// a project-sized Files/Entries map (~70 MiB encoded) rather than a crafted
// header, without allocating a single gigantic string.
func bloatEntries() map[string]string {
	const entries = 70
	m := make(map[string]string, entries)
	for i := 0; i < entries; i++ {
		m[fmt.Sprintf("src/pkg/f%03d.go", i)] = strings.Repeat("a", 1<<20)
	}
	return m
}

// TestOccurrenceTableReadLimit_RefusesBeyondTabCap pins the occurrence
// table's read cap: a sparse occurrences.tab one byte over
// occurrenceTabMaxBytes refuses the open with the cause preserved (path and
// limit detail), never folded into ErrCorpusNotPublished (no legacy v1
// fallback), still classified non-repairable/non-transient at no store —
// and the attach refuses, so the unopenable table is never served.
func TestOccurrenceTableReadLimit_RefusesBeyondTabCap(t *testing.T) {
	h := newChunkStoreHarness(t)
	const key = "snap_0123456789abcdef"
	dir := snapshotDir(h.gen, key)
	pruneMkdir(t, dir)
	header, err := json.Marshal(snapshotTable{Format: storeFormatV2Generation})
	if err != nil {
		t.Fatal(err)
	}
	pruneWrite(t, filepath.Join(dir, snapshotTableName), string(header))
	occPath := filepath.Join(dir, occurrencesTabName)
	pruneWrite(t, occPath, "")
	if err := os.Truncate(occPath, occurrenceTabMaxBytes+1); err != nil {
		t.Fatal(err)
	}

	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	handle, err := reg.Open(context.Background(), h.storage, key, fakeEmbeddingFunc(), "fp-era-a", 8)
	if handle != nil || !errors.Is(err, safeio.ErrTooLarge) {
		t.Fatalf("open = %v, %v; want the occurrence-table read cap refusal", handle, err)
	}
	var tooLarge *safeio.TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Path != occPath || tooLarge.Limit != occurrenceTabMaxBytes {
		t.Fatalf("read-limit detail = %+v; want the occurrences.tab path at occurrenceTabMaxBytes", tooLarge)
	}
	if errors.Is(err, ErrCorpusNotPublished) {
		t.Fatalf("the tab-cap refusal must not fold into ErrCorpusNotPublished (no fallback): %v", err)
	}

	svc, serviceErr := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: "fp-era-a",
		EmbeddingDimension:        8,
		Corpus:                    reg,
	})
	if serviceErr != nil {
		t.Fatal(serviceErr)
	}
	t.Cleanup(func() { _ = svc.Close() })
	svc.current.projectPath = h.storage
	attachErr := svc.AttachView(context.Background(), key, "root_0123456789abcdef")
	if !errors.Is(attachErr, safeio.ErrTooLarge) || !errors.Is(attachErr, ErrCorpusUnopenable) {
		t.Fatalf("attach lost the size refusal or stage: %v", attachErr)
	}
	for _, stage := range []error{nil, ErrCorpusUnopenable, ErrOverlayUnopenable} {
		wrapped := attachErr
		if stage != nil {
			wrapped = fmt.Errorf("%w: %w", stage, err)
		}
		if Repairable(wrapped) || QuarantineScopeOf(wrapped) != QuarantineNoStore || Transient(wrapped) {
			t.Errorf("size refusal classified incorrectly under stage %v", stage)
		}
	}
}

// TestOccurrenceTableWrite_RefusesBeyondReaderCap pins writeOccurrenceTable's
// pre-write refusal: a table its own reader cannot open is never written.
// The payload is ~256 MiB of clean ('a') path bytes — escapeTabField returns
// it unchanged, so the transient peak is the encoder's own buffer; skipped
// under -short.
func TestOccurrenceTableWrite_RefusesBeyondReaderCap(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes a >256 MiB table body")
	}
	rows := []occurrenceRow{{
		Path:     strings.Repeat("a", int(occurrenceTabMaxBytes)),
		BlobHash: "hash0123456789abcdef",
		Chunks:   []chunkOccurrence{{C: "c0", Index: 0, Start: 1, End: 2, Language: "go"}},
	}}
	path := filepath.Join(t.TempDir(), "occurrences.tab")
	err := writeOccurrenceTable(path, rows)
	var tooLarge *safeio.TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Path != path || tooLarge.Limit != occurrenceTabMaxBytes {
		t.Fatalf("write = %v; want a *safeio.TooLargeError at occurrenceTabMaxBytes", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("refused table must not exist on disk: %v", statErr)
	}
}

// TestCorpusManifestWrite_RefusesBeyondReaderCap pins writeCorpusManifest's
// reader-parity refusal: a record readCorpusManifest could never open is
// refused BEFORE publication — nothing lands on disk, and the reader still
// reports the store as unpublished instead of entering the read-fail
// re-derivation loop.
func TestCorpusManifestWrite_RefusesBeyondReaderCap(t *testing.T) {
	branchRoot := filepath.Join(t.TempDir(), "branches", "col_aaaa0000bbbb1111")
	m := &CorpusManifest{
		Format:      storeFormatV2Generation,
		Coords:      coordsRelativeV1,
		EmbeddingFP: "fp-era-a",
		Dims:        8,
		Collection:  "col_aaaa0000bbbb1111",
		Files:       bloatEntries(),
	}
	manifestPath := filepath.Join(branchRoot, corpusManifestName)
	err := writeCorpusManifest(branchRoot, m)
	var tooLarge *safeio.TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Path != manifestPath || tooLarge.Limit != corpusManifestMaxBytes {
		t.Fatalf("write = %v; want a *safeio.TooLargeError at corpusManifestMaxBytes", err)
	}
	if _, statErr := os.Stat(manifestPath); !os.IsNotExist(statErr) {
		t.Fatalf("refused manifest must not exist on disk: %v", statErr)
	}
	if got := readCorpusManifest(branchRoot); got != nil {
		t.Fatal("reader must still report the store as unpublished after the refused write")
	}
}

// TestCorpusManifestReadLimit_FoldsToUnpublished pins the read side: a sparse
// corpus.json one byte over corpusManifestMaxBytes folds to nil (unpublished)
// — never a partial read, never a fallback — so the real consumer
// (publishedV1CorpusKeys) sees no published corpus for it.
func TestCorpusManifestReadLimit_FoldsToUnpublished(t *testing.T) {
	storage := t.TempDir()
	const key = "snap_0123456789abcdef"
	manifestPath := filepath.Join(branchRootPath(storage, key), corpusManifestName)
	pruneWrite(t, manifestPath, "{}")
	if err := os.Truncate(manifestPath, corpusManifestMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if got := readCorpusManifest(branchRootPath(storage, key)); got != nil {
		t.Fatalf("oversized corpus.json = %+v; want nil (unpublished)", got)
	}
	keys, err := publishedV1CorpusKeys(storage, "fp-era-a", 8)
	if err != nil {
		t.Fatalf("publishedV1CorpusKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v; want none (an oversized manifest folds to unpublished)", keys)
	}
}

// TestFileHashSidecarWrite_RefusesBeyondReaderCap pins saveFileHashes's
// reader-parity refusal: an oversized v6 sidecar is refused with the typed
// cause before any bytes land (the callers are fail-soft, so the refusal
// must be classifiable, not a bare write error).
func TestFileHashSidecarWrite_RefusesBeyondReaderCap(t *testing.T) {
	projectPath := t.TempDir()
	ps := &projectState{
		projectPath:   projectPath,
		currentBranch: "main",
		fileHashes:    bloatEntries(),
	}
	err := ps.saveFileHashes()
	sidecarPath := fileHashesPathFor(projectPath, "main")
	var tooLarge *safeio.TooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Path != sidecarPath || tooLarge.Limit != fileHashSidecarMaxBytes {
		t.Fatalf("save = %v; want a *safeio.TooLargeError at fileHashSidecarMaxBytes", err)
	}
	if _, statErr := os.Stat(sidecarPath); !os.IsNotExist(statErr) {
		t.Fatalf("refused sidecar must not exist on disk: %v", statErr)
	}
}

// TestLegacySidecarReadLimit_FoldsToAbsent pins the legacy sidecar readers:
// a sparse sidecar one byte over fileHashSidecarMaxBytes folds to the
// absent/empty semantics (never a partial map, never trusted).
func TestLegacySidecarReadLimit_FoldsToAbsent(t *testing.T) {
	storage := t.TempDir()
	sidecarPath := fileHashesPathFor(storage, "main")
	pruneWrite(t, sidecarPath, "{}")
	if err := os.Truncate(sidecarPath, fileHashSidecarMaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if m, ok := legacySidecarHashMap(storage, "main"); ok || len(m) != 0 {
		t.Fatalf("legacySidecarHashMap = (%d entries, %v); want absent", len(m), ok)
	}
	if fp := legacySidecarChunkerFP(storage, "main"); fp != "" {
		t.Fatalf("legacySidecarChunkerFP = %q; want empty", fp)
	}
}
