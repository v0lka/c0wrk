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
		return fmt.Errorf("attaching vector corpus %s: %w: %w", snapKey, ErrCorpusUnopenable, err)
	}

	// The overlay open reuses the branch-scoped machinery wholesale: the
	// overlay key flows through collectionName/lexicalBranchDirName/
	// fileHashesPathFor exactly like a branch name.
	if err := s.SwitchBranch(ctx, rootKey); err != nil {
		h.Release()
		return fmt.Errorf("opening vector overlay %s: %w: %w", rootKey, ErrOverlayUnopenable, err)
	}

	s.mu.Lock()
	if s.current.projectPath != storage {
		// A concurrent switch displaced the project mid-attach: drop the
		// freshly taken reference rather than bind it onto a foreign state.
		s.mu.Unlock()
		h.Release()
		return errors.New("vector project switched during corpus attach")
	}
	// Release-before-bind: the registry open above runs with s.mu released,
	// so a second AttachView (the git monitor dispatches one goroutine per
	// event) may have bound its own handle in the window — overwriting that
	// binding without releasing it would pin the handle's refcount (and its
	// residency/GC protection) forever. The release/bind pair is atomic
	// under this lock hold; the top-of-function release stays as the early
	// switch-path cleanup.
	s.releaseCorpusLocked(s.current)
	s.current.corpus = h
	s.current.rootKey = rootKey
	s.mu.Unlock()
	return nil
}

// QuarantineStoreView destroys the artifacts of the ONE store a proven
// attach defect belongs to (see QuarantineScope), so the caller's single
// retry rebuilds that store instead of replaying the same defect. The
// caller must have classified the failure with QuarantineScopeOf: a
// transient fault is never repaired this way — it proves nothing about the
// store, so the publication stays and the attach reports unavailable with
// its cause. This service's own bindings on the doomed store are dropped
// first (a decode held open across the removal would keep the directory
// alive on Windows, and a stale binding would be served afterwards).
func (s *Service) QuarantineStoreView(ctx context.Context, snapKey, rootKey string, scope QuarantineScope) error {
	if s.corpusRegistry == nil {
		return errors.New("vector corpus registry not wired")
	}
	if scope == QuarantineNoStore {
		return errors.New("quarantining a store needs a scope")
	}
	s.mu.Lock()
	storage := s.current.projectPath
	if storage == "" {
		s.mu.Unlock()
		return errors.New("no project initialized; call SetProject first")
	}
	switch scope {
	case QuarantineCorpusStore:
		if cv := s.current.corpus; cv != nil && cv.key == snapKey {
			s.releaseCorpusLocked(s.current)
		}
		if s.current.currentBranch == snapKey {
			s.closeWorkingStoreLocked()
		}
	case QuarantineOverlayStore:
		if s.current.currentBranch == rootKey {
			s.closeWorkingStoreLocked()
		}
	}
	fp, dims := s.embeddingCacheFingerprint, s.embeddingDimension
	s.mu.Unlock()

	if err := s.corpusRegistry.QuarantineStoreView(ctx, storage, snapKey, rootKey, scope, fp, dims); err != nil {
		return err
	}
	s.logger.Warn("vector index: quarantined a store with a proven defect; the next pass rebuilds it",
		"scope", scope.String(), "snapshot", snapKey, "root_key", rootKey)
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
	if err := writeRootRef(storage, overlayRootKey(workspaceRoot), workspaceRoot, snapKey); err != nil {
		return err
	}
	// view.json (ADR-104 §3): the root's view record beside the marker —
	// best-effort observability; a store that has not adopted §3 (no
	// CURRENT generation) simply has no place to write it.
	if genDir, _, ok := readCurrentGeneration(storage); ok {
		if err := writeRootViewState(genDir, overlayRootKey(workspaceRoot), snapKey); err != nil {
			s.logger.Warn("vector index: failed to record root view state", "error", err)
		}
	}
	return nil
}

// PinBranchView durably records a BRANCH reference to a snapshot corpus:
// refs/bref_<sha256-of-name>.ref under the storage root (hash-keyed —
// rootrefs.go; markers written by earlier builds under the sanitized key
// are still honored on read and removed on the next write). The attaching
// root writes it when its HEAD is the branch's tip (the corpus then
// captures the branch's commit), and the branch's deletion removes it
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
// corpus (BuildCorpusStore) AND carry a live lexical leg: every corpus open
// reads that leg fail-closed, so publishing without it produces the
// "published but never opens" wedge (specs/domains/workspace.md, shared
// corpus). The manifest landing is the publication gate — everything before
// it may crash into a rebuilt store, never a half-served one. Under an
// adopted §3 generation the snapshot's occurrence table lands FIRST (chunks
// were written during the pass — chunks-before-tables, ADR-104 §3): the
// table is itself a publication the §3 open path serves directly, and a
// crash between the two replays through the v1 gate.
//
// An EMPTY index never publishes — neither the §3 snapshot table nor the v1
// manifest lands (both are publications their open paths serve). A cleanly
// walked tree with no indexable file is refused with ErrCorpusNoIndexableFiles
// and the actionable "no indexable files — check exclude patterns and ignore
// files" reason; an empty index produced by an INCOMPLETE walk (entries the
// pass could not read) is refused with ErrCorpusWalkIncomplete and the
// traversal gap named, because emptiness is unproven there.
func (s *Service) PublishCorpus(ctx context.Context, snapKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.currentBranch != snapKey || s.current.collection == nil {
		return fmt.Errorf("publishing corpus %s: the working store is not the corpus build store", snapKey)
	}
	if err := s.requireLiveLexicalLegLocked(snapKey); err != nil {
		return err
	}
	// The emptiness gate runs BEFORE any publication lands: both the §3
	// snapshot table and the v1 manifest are publications their open paths
	// serve, so an empty build must write NEITHER — not even the table
	// (publishing an empty §3 table would make an "empty but valid" corpus
	// servable while the v1 manifest never lands).
	files, ok := hashColumn(s.current.fileHashes)
	if !ok {
		// "Nothing to index" is an honest outcome only for a tree the walk
		// actually reached. When the pass could not traverse part of the
		// workspace, emptiness is UNPROVEN: the refusal names the traversal
		// gap instead of claiming the tree is empty, so a workspace that keeps
		// dropping a subdirectory never fails its rebuild with a misleading
		// "no indexable files" reason (specs/domains/workspace.md, shared
		// corpus). Neither case publishes — the manifest is the gate.
		if s.current.walkGapBranch == snapKey && len(s.current.walkGaps) > 0 {
			gap := s.current.walkGaps[0]
			noun := "entries"
			if len(s.current.walkGaps) == 1 {
				noun = "entry"
			}
			// The per-entry cause is rendered as TEXT on purpose: wrapping it
			// would make errors.Is(ENOENT) true and reclassify this
			// workspace outcome as store damage (see ErrCorpusWalkIncomplete).
			return fmt.Errorf("publishing corpus %s: %w: %d unreadable %s (first %s: %s)",
				snapKey, ErrCorpusWalkIncomplete, len(s.current.walkGaps), noun, gap.Path, gap.Err.Error())
		}
		return fmt.Errorf("publishing corpus %s: %w: no indexable files — check exclude patterns and ignore files", snapKey, ErrCorpusNoIndexableFiles)
	}
	if err := s.publishCorpusTableLocked(ctx, snapKey); err != nil {
		return err
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

// errLexicalLegNeverOpened is the defensive cause for a missing leg no open
// attempt recorded — a state bound without lexical persistence ever opening.
var errLexicalLegNeverOpened = errors.New("no lexical index was opened for the working store")

// requireLiveLexicalLegLocked refuses a publication whose corpus lexical leg
// is not open, naming the leg and the recorded root cause: a corpus without
// it is unopenable by every reader, so publishing is the wedge, not a
// success. A store with no lexical persistence configured (in-memory No
// Project) has no leg to lose. Caller must hold s.mu (read).
func (s *Service) requireLiveLexicalLegLocked(snapKey string) error {
	if s.current.lexical != nil {
		return nil
	}
	if s.current.projectPath == "" || s.current.projectID == "" {
		return nil // no lexical persistence for this store
	}
	cause := s.lastLexicalOpenErr
	if cause == nil {
		cause = errLexicalLegNeverOpened
	}
	return fmt.Errorf("publishing corpus %s: %w (%s): %w",
		snapKey, ErrCorpusLegMissing, s.workingLexicalDirLocked(snapKey), cause)
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
	s.closeWorkingStoreLocked()
}

// CloseWorkingStoreIfCurrent is CloseWorkingStore gated on ownership: the
// close runs only while the working store is still snapKey's. The corpus
// build pipeline is single-flight per snapshot KEY, not globally (see
// CorpusRegistry.EnsureCorpus), so a concurrent attach of a DIFFERENT
// snapshot may displace this builder's working store between its full pass
// and the publish — at that point the slot is the displacer's, and closing
// it would tear down the displacer's freshly opened store mid-pass,
// stranding its accumulator commits on a nil collection (the silent
// app-exit crash of 2026-11-05: a chromem AddDocuments goroutine
// dereferencing a nil receiver cannot be recovered and kills the process).
// The displacer owns the slot and closes it through its own callback.
// Reports whether the close ran.
func (s *Service) CloseWorkingStoreIfCurrent(snapKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.currentBranch != snapKey {
		return false
	}
	s.closeWorkingStoreLocked()
	return true
}

// closeWorkingStoreLocked releases the working store's residents and forgets
// the store key. The caller must hold s.mu (write).
func (s *Service) closeWorkingStoreLocked() {
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
	n := cv.collection.Count()
	if n == 0 {
		return nil, nil
	}
	limit := min(fanout, n)
	// The corpus query is CORPUS-GLOBAL: the top-`limit` hits of the whole
	// snapshot precede the per-root visibility filter, so invisible
	// documents (diverged, deleted-in-tree, policy-excluded) would consume
	// the fanout slots this root's visible documents need. Over-fetch and
	// iterate deeper (chromem has no offset API; a growing limit re-ranks
	// the same in-memory scan) while the filtered survivors underflow the
	// fanout and the corpus is not exhausted — the exact filter-then-trim
	// discipline the §3 vector leg applies natively.
	seen := make(map[string]struct{})
	var out []chromem.Result
	for {
		results, err := cv.collection.Query(ctx, query, limit, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("corpus vector search: %w", err)
		}
		for _, r := range results {
			if _, dup := seen[r.ID]; dup {
				continue // deeper queries re-rank earlier hits too
			}
			seen[r.ID] = struct{}{}
			if !s.corpusVisible(r.Metadata) {
				continue
			}
			if !passesFilters(r, filePattern, mustMatch, s.current.workspaceRoot, resolver, s.logger) {
				continue
			}
			out = append(out, r)
		}
		if len(out) >= fanout || limit >= n || len(results) < limit {
			return out, nil
		}
		limit = min(limit*2, n)
	}
}

// browseCorpusCandidates enumerates the attached corpus's visibility-
// eligible documents for the browse mode (no semantic ordering). The §3
// form walks the occurrence table and filters BEFORE the limit (exact);
// the v1 form queries the corpus-global top-`limit` and then drops this
// root's invisible slice, so it over-fetches and iterates deeper while the
// filter underflows topK and the corpus is not exhausted (the browse twin
// of corpusVectorCandidates). Caller must hold s.mu (read).
func (s *Service) browseCorpusCandidates(ctx context.Context, cv *CorpusHandle, topK int) ([]chromem.Result, error) {
	if cv == nil || topK <= 0 {
		return nil, nil
	}
	if cv.v2 != nil {
		out := make([]chromem.Result, 0, min(topK, cv.v2.count()))
		for ri, row := range cv.v2.rows {
			if !s.corpusRowVisible(row) {
				continue
			}
			for ci := range row.Chunks {
				if len(out) >= topK {
					return out, nil
				}
				doc, ok := cv.v2.documentAt(v2DocRef{row: ri, chunk: ci})
				if !ok {
					continue
				}
				out = append(out, chromem.Result{ID: doc.ID, Metadata: doc.Metadata})
			}
		}
		return out, nil
	}
	if cv.collection == nil || cv.collection.Count() == 0 {
		return nil, nil
	}
	n := cv.collection.Count()
	limit := min(topK, n)
	unitVec := s.unitQueryVector()
	seen := make(map[string]struct{})
	var out []chromem.Result
	for {
		var results []chromem.Result
		var err error
		if unitVec != nil {
			results, err = cv.collection.QueryEmbedding(ctx, unitVec, limit, nil, nil)
		} else {
			results, err = cv.collection.Query(ctx, " ", limit, nil, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("browsing corpus: %w", err)
		}
		for _, r := range results {
			if _, dup := seen[r.ID]; dup {
				continue // deeper queries re-rank earlier hits too
			}
			seen[r.ID] = struct{}{}
			if !s.corpusVisible(r.Metadata) {
				continue
			}
			out = append(out, r)
		}
		if len(out) >= topK || limit >= n || len(results) < limit {
			return out, nil
		}
		limit = min(limit*2, n)
	}
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
	v2 := cv.v2
	if v2 == nil && cv.collection == nil {
		return nil, nil
	}
	// bleve (both forms) ranks corpus-globally and cannot apply the
	// per-root predicate server-side, so the top-`limit` cut precedes the
	// visibility filter — invisible documents would consume the fanout
	// slots of this root's visible ones. Over-fetch and iterate deeper
	// while the filtered survivors underflow the fanout and the index is
	// not exhausted (a query returning fewer hits than the limit is the
	// exhaustion signal; the §3 vector leg needs no such pass — it scans
	// and filters before trimming).
	limit := fanout
	seen := make(map[string]struct{})
	var out []corpusLexicalHit
	for {
		hits, err := cv.lexical.Query(ctx, query, limit)
		if err != nil {
			return nil, fmt.Errorf("corpus lexical search: %w", err)
		}
		for _, h := range hits {
			if _, dup := seen[h.ID]; dup {
				continue // deeper queries re-rank earlier hits too
			}
			seen[h.ID] = struct{}{}
			if v2 != nil {
				doc, ok := v2.docByID(h.ID)
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
				continue
			}
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
		if len(out) >= fanout || len(hits) < limit {
			return out, nil
		}
		limit *= 2
	}
}
