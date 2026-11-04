package vectorindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// generationManifestName is the branch generation manifest's file name inside
// the per-branch chromem root. chromem-go's persistent DB skips every file
// whose extension is not .gob, so a JSON manifest may live beside the
// collection directories without disturbing the decode.
const generationManifestName = "manifest.json"

// branchGenerationManifest is the generation record of one branch store
// (ADR-104 §2/§3, staged inside the per-branch chromem root until the
// store-level generations tree lands): it pins the coordinate scheme, the
// embedding era and the policy fingerprints every document under the root was
// produced with. Generations are SEQUENTIAL — an incompatible manifest means
// the root's embeddings and the current coordinates must never be mixed (old
// embeddings keyed by absolute paths, new documents keyed by relative ones),
// so the root is reset and re-indexed rather than partially trusted. The
// embedding-keyed re-index cost is bounded by the content-addressed embedding
// cache, which survives generations: compatible chunks are not re-embedded.
type branchGenerationManifest struct {
	// Format is the on-disk format version (storeFormatV2). An unknown
	// version reads as absent (forward compatibility refused, ADR-104 §3).
	Format int `json:"format"`
	// Coords is the document-coordinate scheme (coordsRelativeV1).
	Coords string `json:"coords"`
	// EmbeddingFP is the embedding fingerprint (E) the vectors were produced
	// under; a change is a generation boundary (re-embed, cache-spared).
	EmbeddingFP string `json:"embedding_fp"`
	// Dims is the embedding dimension the vectors were produced with.
	Dims int `json:"dims"`
	// ChunkerFP is the chunker fingerprint (K) recorded at creation; per-file
	// re-chunking on a K change stays driven by the sidecar's 4th field —
	// this field records the configuration the generation started under.
	ChunkerFP string `json:"chunker_fp"`
	// IgnoreFP is the ignore/eligibility fingerprint (I) the first walk ran
	// under; membership is re-derived per pass against the CURRENT policy.
	IgnoreFP string `json:"ignore_fp"`
	// CreatedAt is the generation creation time (RFC 3339, diagnostics).
	CreatedAt string `json:"created_at"`
}

// compatible reports whether a manifest describes a generation the current
// process may read: same format, same coordinate scheme, same embedding era
// (fingerprint E and dimension). Chunker/ignore fingerprints do NOT gate
// reads — ADR-104 §2 assigns them per-table re-derivation semantics, not
// generation boundaries.
func (m *branchGenerationManifest) compatible(embeddingFP string, dims int) bool {
	return m != nil &&
		m.Format == storeFormatV2 &&
		m.Coords == coordsRelativeV1 &&
		m.EmbeddingFP == embeddingFP &&
		m.Dims == dims
}

// readBranchManifest loads the branch root's generation manifest; nil (with
// no error) when absent, unreadable, or not valid JSON — an unreadable
// manifest is treated as a missing one and the generation gate decides.
func readBranchManifest(branchRoot string) *branchGenerationManifest {
	data, err := safeio.ReadFile(filepath.Join(branchRoot, generationManifestName))
	if err != nil {
		return nil
	}
	var m branchGenerationManifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

// writeBranchManifest atomically publishes the generation manifest. It lands
// BEFORE any document of the generation (the root is empty at this point), so
// a published manifest always describes a consistent — possibly still empty —
// generation; a crash mid-index leaves an incomplete but coordinate-consistent
// generation, never a torn one (ADR-104 §3 write ordering).
func writeBranchManifest(branchRoot string, m *branchGenerationManifest) error {
	m.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(filepath.Join(branchRoot, generationManifestName), data, 0o644)
}

// dirHasEntries reports whether dir exists and holds at least one entry.
func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// pathExists reports whether path exists (any type).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ensureBranchGeneration enforces sequential generations for one branch store
// (ADR-104 §2/§3): a branch root whose manifest is missing or incompatible
// with the current era (format, coordinate scheme, embedding fingerprint or
// dimension) is RESET — the chromem root, the branch's file-hash sidecar, its
// content-less marker and its lexical index are discarded together, so no
// artifact of the old coordinates (documents, sidecar rows, lexical entries)
// ever mixes with the new generation. A fresh (empty or absent) root gets the
// manifest written and nothing else is touched. The caller must hold the
// service write lock and must have released the branch's residents (the
// lexical index is closed before this runs).
func (s *Service) ensureBranchGeneration(branch, branchRoot string) error {
	if existing := readBranchManifest(branchRoot); existing.compatible(s.embeddingCacheFingerprint, s.embeddingDimension) {
		return nil
	}

	projectPath := s.current.projectPath
	sidecar := fileHashesPathFor(projectPath, branch)
	marker := contentlessMarkerPathFor(projectPath, branch)
	lexDir := lexicalIndexPath(projectPath, branch)

	hasData := dirHasEntries(branchRoot) || pathExists(sidecar) || pathExists(marker) || dirHasEntries(lexDir)
	if hasData {
		// Superseded generation: discard every artifact keyed by the old
		// coordinates. Fail closed — a wipe failure returns the error and
		// the manifest is NOT written: the half-wiped root has no manifest,
		// so the next open re-enters this gate and finishes the reset
		// before anything is served under the new coordinates.
		for _, victim := range []string{branchRoot, lexDir, sidecar, marker} {
			if victim == "" {
				continue
			}
			if err := os.RemoveAll(victim); err != nil {
				return fmt.Errorf("discarding superseded branch generation artifact %s: %w", victim, err)
			}
		}
		s.logger.Info("vector index: reset branch generation (incompatible identity or embedding era)",
			"branch", branch,
			"reason", "manifest missing or mismatched; sequential generations never mix coordinates")
	}

	if err := ensureRealDir(branchRoot, 0o750); err != nil {
		return err
	}
	return writeBranchManifest(branchRoot, &branchGenerationManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: s.embeddingCacheFingerprint,
		Dims:        s.embeddingDimension,
		ChunkerFP:   s.chunkerFingerprint,
		IgnoreFP:    s.ignoreFingerprint,
	})
}

// lexicalIndexPath returns the per-branch lexical index directory under the
// project's vector-index storage root.
func lexicalIndexPath(projectPath, branch string) string {
	return filepath.Join(projectPath, lexicalDirName, lexicalBranchDirName(branch))
}
