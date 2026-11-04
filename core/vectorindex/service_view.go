package vectorindex

// Service view-model methods (ADR-104 stage 2, owner/view contract): the
// corpus participation of one Service. The WORKING store of a view-mode
// service is its root's private OVERLAY (SwitchBranch keyed by the overlay
// root key); the shared corpus is attached read-only through the corpus
// registry and consulted by the read paths (search, browse, file list) with
// per-root VISIBILITY filtering applied BEFORE the final top-K/RRF trim — a
// corpus entry is visible to this root exactly while the root's own sidecar
// records the same content hash for the path (the root still holds the
// corpus's bytes; a diverged, renamed-away, deleted or policy-excluded file
// is served from the overlay or not at all, never from the corpus).
//
// Writes never touch the corpus: a corpus is built once (the build lease in
// corpus.go) while the working store is temporarily bound to the snapshot
// key, published by an atomic manifest, and immutable afterwards — which is
// what makes root isolation structural (root A's changes land in A's
// overlay; no other root's results can move).

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	chromem "github.com/philippgille/chromem-go"
)

// corpusAdoptionEntry composes a sidecar entry for a corpus-current file:
// the 4-field form (no chunk set — the corpus, not the overlay, serves the
// document; lexical rebuilds re-derive on demand).
func corpusAdoptionEntry(hash string, size, mtimeUnixNano int64, chunkerFP string) string {
	return hash + fileHashEntrySep +
		strconv.FormatInt(size, 10) + fileHashEntrySep +
		strconv.FormatInt(mtimeUnixNano, 10) + fileHashEntrySep +
		chunkerFP
}

// AttachView binds the service's read side to a published corpus and its
// working store to the root's private overlay:
//
//   - snapKey identifies the shared corpus (commit-keyed; every root of the
//     project at that commit attaches the SAME handle — one decode, one
//     read-only lexical instance — never a per-root copy on disk or in RAM);
//   - rootKey identifies the overlay store (root-fingerprint keyed).
//
// Fail-closed (ADR-104): when the corpus cannot be opened — unpublished,
// foreign embedding era, unreadable — the error names the snapshot and the
// service NEVER falls back to another snapshot's or root's collection; the
// caller surfaces an honest unavailable status.
func (s *Service) AttachView(ctx context.Context, snapKey, rootKey string) error {
	if s.corpusRegistry == nil {
		return errors.New("vector corpus registry not wired")
	}
	s.mu.Lock()
	storage := s.current.projectPath
	if storage == "" {
		s.mu.Unlock()
		return errors.New("no project initialized; call SetProject first")
	}
	// Idempotent re-attach: the same corpus and overlay are already bound
	// (a re-run of initProject after a park/restore round-trip lands here
	// with the handle released only when the state was parked).
	if s.current.corpus != nil && s.current.corpus.key == snapKey &&
		s.current.rootKey == rootKey && s.current.collection != nil {
		s.mu.Unlock()
		return nil
	}
	s.releaseCorpusLocked(s.current)
	s.mu.Unlock()

	// Registry open performs the (shared, refcounted) decode — outside s.mu,
	// mirroring the SwitchBranch discipline of keeping long IO off the lock
	// where possible; the corpus is published and immutable, so there is no
	// write race to close.
	h, err := s.corpusRegistry.Open(ctx, storage, snapKey, s.embeddingFunc, s.embeddingCacheFingerprint, s.embeddingDimension)
	if err != nil {
		return fmt.Errorf("attaching vector corpus %s: %w", snapKey, err)
	}

	// The overlay open reuses the branch-scoped machinery wholesale: the
	// overlay key flows through collectionName/lexicalBranchDirName/
	// fileHashesPathFor exactly like a branch name.
	if err := s.SwitchBranch(ctx, rootKey); err != nil {
		h.Release()
		return fmt.Errorf("opening vector overlay %s: %w", rootKey, err)
	}

	s.mu.Lock()
	if s.current.projectPath != storage {
		// A concurrent switch displaced the project mid-attach: drop the
		// freshly taken reference rather than bind it onto a foreign state.
		s.mu.Unlock()
		h.Release()
		return errors.New("vector project switched during corpus attach")
	}
	s.current.corpus = h
	s.current.rootKey = rootKey
	s.mu.Unlock()
	return nil
}

// PinCorpusView durably records the root's ownership of its view (ADR-104
// stage 5): refs/<rootKey>.ref under the storage root names the workspace
// tree (the liveness anchor) and snapKey (the corpus the root views). The
// attach path calls it BEFORE EnsureCorpus — the marker is an intent pin,
// so the reachability sweep can never reclaim a corpus a live root is in
// the middle of building or adopting, and a rebuilt/evicted manager that
// re-attaches rewrites the same record. The marker survives manager
// eviction, restarts and shutdown; only the tree's removal (DeleteRootOverlay)
// or a dead root's sweep takes it away.
func (s *Service) PinCorpusView(workspaceRoot, snapKey string) error {
	if workspaceRoot == "" || snapKey == "" {
		return errors.New("pinning a corpus view needs a workspace root and a snapshot key")
	}
	s.mu.RLock()
	storage := s.current.projectPath
	s.mu.RUnlock()
	if storage == "" {
		return errors.New("no project initialized; call SetProject first")
	}
	return writeRootRef(storage, overlayRootKey(workspaceRoot), workspaceRoot, snapKey)
}

// PinBranchView durably records a BRANCH reference to a snapshot corpus:
// refs/bref_<sanitized>.ref under the storage root. The attaching root
// writes it when its HEAD is the branch's tip (the corpus then captures the
// branch's commit), and the branch's deletion removes it
// (DeleteBranchData) — the branch is an owner through this pointer, never
// through a storage key (ADR-104 §1 L3, stage 5).
func (s *Service) PinBranchView(branch, snapKey string) error {
	if branch == "" || snapKey == "" {
		return errors.New("pinning a branch view needs a branch and a snapshot key")
	}
	s.mu.RLock()
	storage := s.current.projectPath
	s.mu.RUnlock()
	if storage == "" {
		return errors.New("no project initialized; call SetProject first")
	}
	return writeBranchRef(storage, branch, snapKey)
}

// BuildCorpusStore rebinds the service's WORKING store to the snapshot key
// for a corpus build: the builder's IndexFull commits into the corpus store
// (documents, lexical, sidecar) through the ordinary pipelines, and
// PublishCorpus turns the result into an immutable shared corpus. Called
// only by the build-lease holder (CorpusRegistry.EnsureCorpus).
func (s *Service) BuildCorpusStore(ctx context.Context, snapKey string) error {
	return s.SwitchBranch(ctx, snapKey)
}

// PublishCorpus atomically writes the corpus publication manifest from the
// working state's sidecar hash map. The working store must currently be the
// corpus (BuildCorpusStore); the manifest landing is the publication gate —
// everything before it may crash into a rebuilt store, never a half-served
// one. Under an adopted §3 generation the snapshot's occurrence table lands
// FIRST (chunks were written during the pass — chunks-before-tables, ADR
// §3): the table is itself a publication the §3 open path serves directly,
// and a crash between the two replays through the v1 gate.
func (s *Service) PublishCorpus(snapKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.currentBranch != snapKey || s.current.collection == nil {
		return fmt.Errorf("publishing corpus %s: the working store is not the corpus build store", snapKey)
	}
	if err := s.publishCorpusTableLocked(snapKey); err != nil {
		return err
	}
	files, ok := hashColumn(s.current.fileHashes)
	if !ok {
		return fmt.Errorf("publishing corpus %s: the build produced no sidecar hashes", snapKey)
	}
	return writeCorpusManifest(branchRootPath(s.current.projectPath, snapKey), &CorpusManifest{
		Format:      storeFormatV2,
		Coords:      coordsRelativeV1,
		EmbeddingFP: s.embeddingCacheFingerprint,
		Dims:        s.embeddingDimension,
		ChunkerFP:   s.chunkerFingerprint,
		IgnoreFP:    s.ignoreFingerprint,
		Collection:  collectionName(snapKey),
		Files:       files,
	})
}

// CloseWorkingStore closes the current working store's residents (lexical
// index, chromem DB, collection) and forgets the store key, leaving the
// project state ready for the next SwitchBranch/AttachView. The corpus
// build path uses it to release the corpus's read-write lexical handle
// BEFORE the build lease settles — a read-only opener of the just-published
// corpus must never contend the lexical store's file lock (bleve's bolt
// flock waits unboundedly).
func (s *Service) CloseWorkingStore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseBranchResidentsLocked()
	s.current.currentBranch = ""
	s.current.fileHashes = nil
	s.current.validationsSinceFullHash.Store(0)
}

// releaseCorpusLocked drops the state's corpus reference (refcounted shared
// handle). The caller must hold s.mu; the registry never calls back into
// the service while holding its own lock, so s.mu → registry.mu is a safe
// order.
func (s *Service) releaseCorpusLocked(ps *projectState) {
	if ps == nil || ps.corpus == nil {
		return
	}
	ps.corpus.Release()
	ps.corpus = nil
}

// CorpusAttached reports whether a shared corpus is currently attached (the
// view model is live for this service).
func (s *Service) CorpusAttached() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.corpus != nil
}

// recordCorpusAdoption notes a corpus-current classification made under the
// read lock (ValidateCollection): the entry is drained by the indexing pass
// (TakeCorpusAdoptions) and persisted into the overlay's sidecar, making the
// corpus entry VISIBLE to this root without writing any document.
func (s *Service) recordCorpusAdoption(rel, hash string, size, mtimeUnixNano int64) {
	s.corpusAdoptionsMu.Lock()
	defer s.corpusAdoptionsMu.Unlock()
	if s.corpusAdoptions == nil {
		s.corpusAdoptions = make(map[string]string)
	}
	s.corpusAdoptions[rel] = corpusAdoptionEntry(hash, size, mtimeUnixNano, s.chunkerFingerprint)
}

// TakeCorpusAdoptions drains the adoptions recorded by the last
// ValidateCollection pass (nil when none). Ownership transfers to the
// caller, which persists them via AdoptCorpusFiles.
func (s *Service) TakeCorpusAdoptions() map[string]string {
	s.corpusAdoptionsMu.Lock()
	defer s.corpusAdoptionsMu.Unlock()
	a := s.corpusAdoptions
	s.corpusAdoptions = nil
	return a
}

// CorpusEnabled reports whether the shared-corpus view model is wired for
// this service (a corpus registry was provided at construction).
func (s *Service) CorpusEnabled() bool { return s.corpusRegistry != nil }

// EmbeddingEra returns the service's era identity (embedding cache
// fingerprint + dimension) — the compatibility boundary every corpus
// promotion, open and migration gate reads (ADR-104 §2).
func (s *Service) EmbeddingEra() (fingerprint string, dims int) {
	return s.embeddingCacheFingerprint, s.embeddingDimension
}

// StorageRoot returns the active project's storage root ("" when unset).
func (s *Service) StorageRoot() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current.projectPath
}

// EnsureCorpus drives the snapshot's corpus to a published state through the
// wired registry (see CorpusRegistry.EnsureCorpus); the era identity is the
// service's own (embedding fingerprint + dimension).
func (s *Service) EnsureCorpus(ctx context.Context, storageRoot, snapKey, legacyBranch string, build func() error, onMigrate func()) error {
	if s.corpusRegistry == nil {
		return errors.New("vector corpus registry not wired")
	}
	return s.corpusRegistry.EnsureCorpus(ctx, storageRoot, snapKey, legacyBranch, s.embeddingCacheFingerprint, s.embeddingDimension, build, onMigrate)
}

// RunStoreMigration drives the physical store's unified legacy-layout
// migration sweep through the wired registry (see
// CorpusRegistry.RunStoreMigration), gated on this service's own era
// identity — the same boundary every corpus promotion reads.
func (s *Service) RunStoreMigration(ctx context.Context, storageRoot string, facts StoreMigrationFacts) *StoreMigrationState {
	if s.corpusRegistry == nil {
		return nil
	}
	fp, dims := s.EmbeddingEra()
	return s.corpusRegistry.RunStoreMigration(ctx, storageRoot, fp, dims, facts)
}

// adoptCorpusFilesLocked merges adopted sidecar rows into the overlay's
// sidecar and persists it; the caller holds s.mu (write).
func (s *Service) adoptCorpusFilesLocked(entries map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	if s.current.fileHashes == nil {
		s.current.fileHashes = make(map[string]string)
	}
	for k, v := range entries {
		s.current.fileHashes[k] = v
	}
	return s.current.saveFileHashes()
}

// AdoptCorpusFiles merges adopted sidecar rows into the overlay's sidecar
// and persists it. Called by the indexing pass under the write discipline;
// adopted rows carry no documents (the corpus serves those paths).
func (s *Service) AdoptCorpusFiles(entries map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adoptCorpusFilesLocked(entries)
}

// corpusVisible is the per-root visibility arbiter for a stored corpus
// document (ADR-104 stage 2): the entry is visible exactly while the root's
// own sidecar records the SAME content hash for the occurrence path — i.e.
// the root's tree still holds the corpus's bytes. A diverged (edited),
// renamed-away, deleted, or policy-excluded file fails the check and is
// never served from the corpus; the overlay serves the root's current
// version if it has one. Caller must hold s.mu (read).
func (s *Service) corpusVisible(md map[string]string) bool {
	cv := s.current.corpus
	if cv == nil {
		return false // no corpus attached — nothing to arbitrate
	}
	rel := occurrencePath(md)
	if rel == "" {
		return false
	}
	want, inCorpus := cv.manifest.Files[rel]
	if !inCorpus {
		return false
	}
	entry, ok := s.current.fileHashes[rel]
	return ok && fileHashEntryHash(entry) == want
}

// CorpusFileCount returns the number of corpus entries visible to this root
// (the per-root view size, used by status surfaces and tests). 0 when no
// corpus is attached.
func (s *Service) CorpusFileCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cv := s.current.corpus
	if cv == nil {
		return 0
	}
	n := 0
	for rel, want := range cv.manifest.Files {
		if entry, ok := s.current.fileHashes[rel]; ok && fileHashEntryHash(entry) == want {
			n++
		}
	}
	return n
}

// corpusVectorCandidates queries the corpus collection for the corpus leg of
// a search: visibility-filtered (per-root) BEFORE any trimming, so the
// overlay and corpus rank spaces see the same per-root world. Caller must
// hold s.mu (read); the corpus collection is shared and immutable, safe for
// concurrent readers.
func (s *Service) corpusVectorCandidates(ctx context.Context, query string, fanout int, filePattern string, mustMatch []string, resolver *contentResolver) ([]chromem.Result, error) {
	cv := s.current.corpus
	if cv == nil || fanout <= 0 {
		return nil, nil
	}
	if cv.v2 != nil {
		return s.v2VectorCandidates(ctx, cv.v2, query, fanout, filePattern, mustMatch, resolver)
	}
	if cv.collection == nil {
		return nil, nil
	}
	if n := cv.collection.Count(); n == 0 {
		return nil, nil
	} else if fanout > n {
		fanout = n
	}
	results, err := cv.collection.Query(ctx, query, fanout, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("corpus vector search: %w", err)
	}
	out := make([]chromem.Result, 0, len(results))
	for _, r := range results {
		if !s.corpusVisible(r.Metadata) {
			continue
		}
		if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// corpusLexicalHit pairs a corpus lexical hit with its corpus document.
type corpusLexicalHit struct {
	doc   chromem.Document
	score float32
}

// corpusLexicalCandidates queries the corpus's read-only lexical index and
// enriches surviving hits from the corpus collection (visibility-filtered
// pre-trim, like corpusVectorCandidates).
func (s *Service) corpusLexicalCandidates(ctx context.Context, query string, fanout int, filePattern string, mustMatch []string, resolver *contentResolver) ([]corpusLexicalHit, error) {
	cv := s.current.corpus
	if cv == nil || cv.lexical == nil || fanout <= 0 {
		return nil, nil
	}
	if cv.v2 != nil {
		hits, err := cv.lexical.Query(ctx, query, fanout)
		if err != nil {
			return nil, fmt.Errorf("corpus lexical search: %w", err)
		}
		out := make([]corpusLexicalHit, 0, len(hits))
		for _, h := range hits {
			doc, ok := cv.v2.docByID(h.ID)
			if !ok {
				continue // drift between the corpus lexical and table sides
			}
			r := chromem.Result{ID: doc.ID, Metadata: doc.Metadata}
			if !s.corpusVisible(doc.Metadata) {
				continue
			}
			if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
				continue
			}
			out = append(out, corpusLexicalHit{doc: doc, score: h.Score})
		}
		return out, nil
	}
	if cv.collection == nil {
		return nil, nil
	}
	hits, err := cv.lexical.Query(ctx, query, fanout)
	if err != nil {
		return nil, fmt.Errorf("corpus lexical search: %w", err)
	}
	out := make([]corpusLexicalHit, 0, len(hits))
	for _, h := range hits {
		doc, getErr := cv.collection.GetByID(ctx, h.ID)
		if getErr != nil {
			continue // drift between the corpus lexical and vector sides
		}
		r := chromem.Result{ID: doc.ID, Metadata: doc.Metadata, Content: doc.Content}
		if !s.corpusVisible(r.Metadata) {
			continue
		}
		if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
			continue
		}
		out = append(out, corpusLexicalHit{doc: doc, score: h.Score})
	}
	return out, nil
}
