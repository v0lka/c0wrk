package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/embedding"
)

const defaultVectorBrowseTopK = 50

// vectorIndexTarget is the resolved (workspace root, storage root) pair the
// vector index must target: the workspace the indexer walks and the branch is
// detected from, plus the per-target storage root for its collections.
type vectorIndexTarget struct {
	workspacePath string
	storagePath   string
}

// resolveVectorIndexTarget resolves the effective vector-index target for
// project p: a managed-worktree session of p targets its OWN tree root with
// worktree-scoped storage; every other case keeps the checkout. Membership is
// decided by containment inside THIS project's .worktrees container. This is
// the INITIAL focus resolution at project switch (the root is handed to
// ApplyFocus). See specs/domains/workspace.md (session-workspace routing).
func (f *FrontendAPI) resolveVectorIndexTarget(switchCtx context.Context, p *project.ProjectInfo) vectorIndexTarget {
	base := vectorIndexTarget{
		workspacePath: p.WorkspacePath,
		storagePath:   config.ProjectVectorIndexPath(f.agentDir, p.ID),
	}
	if p.IsNoProject || f.appCell() == nil || f.app.Manager() == nil {
		return base
	}
	// Both reads below are bounded by terminalPathLookupTimeout:
	// Manager.WorkspacePathFor's contract makes the CALLER bound the
	// session-row read, and the bound derives from the switch-section
	// deadline so a saturated pool cannot stack budgets inside the critical
	// section. A timeout degrades to base — the fail-soft default of every
	// negative branch here.
	ctx, cancel := context.WithTimeout(switchCtx, terminalPathLookupTimeout)
	defer cancel()
	savedID := ""
	if f.projStore != nil {
		if st, err := f.projStore.LoadUIState(ctx, p.ID); err == nil && st != nil {
			savedID = strings.TrimSpace(st.SavedSessionID)
		}
	}
	if savedID == "" {
		return base
	}
	ws, ok := f.app.Manager().WorkspacePathFor(ctx, savedID)
	if !ok || ws == "" || ws == p.WorkspacePath {
		return base
	}
	target, ok := f.managedWorktreeVectorTarget(p, ws)
	if !ok {
		return base
	}
	return target
}

// managedWorktreeVectorTarget derives the worktree's vector target: the
// workspace is the tree root itself; storage is the PROJECT's shared store
// (ADR-104 stage 2 owner/view — the tree reads the snapshot-keyed corpus
// read-only and writes only its per-root overlay; the embedding cache is the
// project's single content-addressed cache). Membership is decided by
// containment inside THIS project's .worktrees container.
func (f *FrontendAPI) managedWorktreeVectorTarget(p *project.ProjectInfo, ws string) (vectorIndexTarget, bool) {
	f.seedAcquire()
	if _, err := config.ManagedWorktreeNameFromPath(p.WorkspacePath, ws); err != nil {
		return vectorIndexTarget{}, false
	}
	return vectorIndexTarget{
		workspacePath: ws,
		storagePath:   config.ProjectVectorIndexPath(f.agentDir, p.ID),
	}, true
}

// storedProjectRoot returns the project row's stored (unresolved) workspace
// path — the registration form — for legacy-form comparisons. Fail-soft:
// ok=false when the manager is unwired or the row is unreadable.
func (f *FrontendAPI) storedProjectRoot(projectID string) (string, bool) {
	if f.projectManager == nil {
		return "", false
	}
	p, err := f.projectManager.GetProject(projectID)
	if err != nil || p == nil {
		f.log().Debug("vector index: cannot read the project row for the stored root",
			"project", projectID, "error", err)
		return "", false
	}
	return p.WorkspacePath, true
}

// deleteWorktreeVectorIndex releases a managed tree's per-root manager and
// removes its persisted vector-index state (worktree-scoped storage root plus
// per-tree embedding cache). Best-effort by design — the tree is already gone.
// The registry Release happens FIRST: it shuts the manager down (dropping open
// chromem handles) before DeleteProjectData wipes the storage, so the removal
// works on Windows too.
func (f *FrontendAPI) deleteWorktreeVectorIndex(repoRoot, projectID, name string, releasedPath ...string) {
	f.seedAcquire()
	vr := f.vectorRootsRegistry()
	root, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree root for release",
			"project", projectID, "worktree", name, "error", err)
		root = filepath.Join(config.ManagedWorktreesDir(repoRoot), name)
	}
	if len(releasedPath) > 0 && releasedPath[0] != "" {
		root = canonicalWorktreeRoot(releasedPath[0])
	}
	overlayPaths := []string{root}
	// RESURRECTION GUARD (ADR-104 stage 5), inner half: a tree path that
	// exists again is a NEW tree (a racing adoption's Recreate), and its
	// overlay, ownership marker and legacy storage are the live tree's
	// data. The os.Stat probe must run BEFORE vr.Release — Release removes
	// the root's manager, so a LiveManager probe after it is nil by
	// construction and could never fire. The manager releases themselves
	// still happen for every spelling (callers rely on the synchronous
	// drain for Windows handle-close, and a manager is an in-process cache
	// over durable data — a resurrected root transparently rebuilds its
	// manager on next use); only the DATA cleanup is skipped for a
	// resurrected spelling, deferring it to that tree's own next release.
	resurrected := func(treePath string) bool {
		if info, err := os.Stat(treePath); err == nil && info.IsDir() {
			f.log().Debug("vector index: released worktree resurrected before cleanup; keeping its vector data",
				"project", projectID, "worktree", name, "root", treePath)
			return true
		}
		return false
	}
	rootResurrected := resurrected(root)
	mgr := vr.Release(root)
	// Legacy rows keep a symlinked ancestor in their stored root: AGENT
	// routing keys a tree's live manager under the raw stored spelling, while
	// the repo root here is canonical. Release the stored spelling too, so the
	// delete shuts the legacy-keyed manager down as well. Fail-soft: an
	// unreadable project row only skips the second key.
	if stored, ok := f.storedProjectRoot(projectID); ok && stored != repoRoot {
		alt, altErr := config.ManagedWorktreePath(stored, name)
		if altErr != nil {
			alt = filepath.Join(config.ManagedWorktreesDir(stored), name)
		}
		if alt != root {
			altResurrected := resurrected(alt)
			if m := vr.Release(alt); m != nil && mgr == nil {
				mgr = m
			}
			if !altResurrected {
				overlayPaths = append(overlayPaths, alt)
			}
		}
	}
	if rootResurrected {
		// The resurrected tree's overlay, marker AND legacy per-tree storage
		// are its live data — skip the whole vector cleanup, exactly like
		// the outer guard, deferring it to that tree's own next release.
		return
	}

	// ADR-104 stage 2 (owner/view): the tree's per-root overlay inside the
	// PROJECT's shared store goes away with the tree; the snapshot corpora
	// stay (the checkout and other trees may still view them). Overlay keys
	// are fingerprinted off the raw tree path, so every spelling the tree
	// was ever keyed under is cleaned — except the resurrected spellings the
	// guard above already removed from overlayPaths.
	sharedRoot := config.ProjectVectorIndexPath(f.agentDir, projectID)
	for _, treePath := range overlayPaths {
		if err := vectorindex.DeleteRootOverlay(sharedRoot, treePath); err != nil {
			f.log().Warn("vector index: failed to remove released worktree overlay from the shared store",
				"project", projectID, "worktree", name, "error", err)
		}
	}

	// Legacy per-tree storage roots (pre-shared-store builds) still get
	// their best-effort cleanup.
	storage, err := config.WorktreeVectorIndexPath(f.agentDir, projectID, name)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree storage path for cleanup",
			"project", projectID, "worktree", name, "error", err)
		return
	}
	clean, disposable := mgr, false
	if clean == nil {
		// No live manager ever routed here this run, but persisted data from
		// a previous run may exist; any manager can remove it (a plain fs
		// + park-slot operation).
		clean, disposable = vr.ManagerForCleanup()
	}
	switch {
	case clean != nil:
		if err := clean.DeleteProjectData(storage); err != nil {
			f.log().Warn("vector index: failed to remove released worktree index data",
				"project", projectID, "worktree", name, "error", err)
		}
		if disposable {
			clean.Shutdown()
		}
	case vr.Disabled():
		// The registry is parked by vector_index.enabled: false — a user
		// preference, not a transient state, so the skip below must not
		// apply (it would strand the tree's index storage until the whole
		// project is deleted). Disable shut every manager down before
		// dropping the factory, so no live or parked state holds handles
		// and DeleteProjectData's park-slot fixup is vacuous: the removal
		// is the plain fs operation, best-effort like the manager path.
		if err := os.RemoveAll(storage); err != nil {
			f.log().Warn("vector index: failed to remove released worktree index data",
				"project", projectID, "worktree", name, "error", err)
		}
	default:
		// Still loading or embedder unavailable: transient — the next
		// tree/branch switch (or deletion) retries the cleanup.
		f.log().Debug("vector index: worktree index cleanup skipped: no vector manager available",
			"project", projectID, "worktree", name)
	}

	// The per-tree cache lives INSIDE the tree's storage root, so the
	// DeleteProjectData removal above already dropped it; the explicit removal
	// stays as belt-and-braces. The LEGACY sibling location (pre-containment-
	// fix builds, issue #137 step 1) is removed too: those trees carry seed
	// copies nothing ever read.
	if cache, cerr := config.WorktreeEmbeddingCachePath(f.agentDir, projectID, name); cerr == nil {
		if err := os.RemoveAll(cache); err != nil {
			f.log().Warn("vector index: failed to remove released worktree embedding cache",
				"project", projectID, "worktree", name, "error", err)
		}
	}
	if legacy, lerr := config.LegacyWorktreeEmbeddingCachePath(f.agentDir, projectID, name); lerr == nil {
		if err := os.RemoveAll(legacy); err != nil {
			f.log().Warn("vector index: failed to remove legacy worktree embedding cache",
				"project", projectID, "worktree", name, "error", err)
		}
	}
}

// SearchVectorStore searches the vector store, routed to the Git-panel FOCUS
// tree's manager — the user's search follows the tree the panel displays (see
// specs/domains/workspace.md, session-workspace routing). An empty req.Query
// browses arbitrary chunks through BrowseWithFilter; otherwise it dispatches
// to Service.HybridSearch with the requested mode (hybrid | vector | lexical;
// empty defaults to hybrid with auto-fallback to vector when the lexical
// index is empty). req.TopK defaults to 50 when <= 0.
func (f *FrontendAPI) SearchVectorStore(req SearchRequest) ([]VectorStoreEntry, error) {
	// No Project (CHAT mode): the vector index is disabled. Empty results,
	// not an error, so the frontend renders an empty state.
	if f.isNoProject() {
		return []VectorStoreEntry{}, nil
	}

	vm, err := f.vectorRootsRegistry().FocusManager(f.ctx())
	if err != nil {
		return nil, errVectorNoTarget
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := vm.Service()

	var results []vectorindex.SearchResult

	// Defense-in-depth: bound the readiness wait with the same knob that
	// bounds the semantic_search tool (vector_index.search_wait_timeout_ms),
	// so the RPC can never block unboundedly while a full index is stuck. The
	// fail-fast sentinel (0) dispatches to the NoWait variants: a not-ready
	// index errors immediately AND a pass starting between the readiness
	// state and the call cannot block the RPC.
	ctx := f.ctx()
	failFast := false
	if wait := vm.SearchWaitTimeout(); wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	} else {
		failFast = true
	}

	if req.Query == "" {
		if failFast {
			results, err = vectorSvc.BrowseWithFilterNoWait(ctx, topK, req.FilePattern)
		} else {
			results, err = vectorSvc.BrowseWithFilter(ctx, topK, req.FilePattern)
		}
	} else {
		opts := vectorindex.SearchOptions{
			Query:       req.Query,
			TopK:        topK,
			Mode:        vectorindex.ParseMode(req.Mode),
			FilePattern: req.FilePattern,
			MustMatch:   req.MustMatch,
		}
		if failFast {
			results, err = vectorSvc.HybridSearchNoWait(ctx, opts)
		} else {
			results, err = vectorSvc.HybridSearch(ctx, opts)
		}
	}
	if err != nil {
		// Fail-fast observed a not-ready index (including the
		// pass-started-after-gate race): surface the actionable status.
		if errors.Is(err, vectorindex.ErrNotReady) {
			return nil, vm.NotReadyError()
		}
		// The wait bound expired before readiness: same actionable status.
		if !vectorSvc.IsReady() {
			return nil, vm.NotReadyError()
		}
		return nil, err
	}

	out := make([]VectorStoreEntry, len(results))
	for i, r := range results {
		out[i] = VectorStoreEntry{
			FilePath:     r.FilePath,
			FileName:     r.FileName,
			Content:      r.Content,
			Score:        r.Score,
			StartLine:    r.StartLine,
			EndLine:      r.EndLine,
			Language:     r.Language,
			VectorScore:  r.VectorScore,
			LexicalScore: r.LexicalScore,
			VectorRank:   r.VectorRank,
			LexicalRank:  r.LexicalRank,
		}
	}

	return out, nil
}

// Status literals for GetVectorIndexStatus's non-manager branches. The
// manager reports its own states (ready/indexing/reindexing/loading/...);
// these name the routing outcomes the registry contributes.
const (
	// vectorStateUnavailable is the terminal "nothing to search" state — the
	// index cannot answer for this target. Every such snapshot carries a
	// non-empty LastError, so the UI never renders a bare broken state.
	vectorStateUnavailable = "unavailable"
	// vectorStateLoading is the non-failure window: the embedder init has not
	// settled yet, or the root's manager is being replaced (LRU eviction).
	// Neither is a broken index — both resolve on their own.
	vectorStateLoading = "loading"
)

// Vector status error kinds the registry contributes (VectorIndexStatus.
// ErrorKind). The manager's own values come from vectorindex.IndexErrorKind
// (repairable / transient / fatal / unknown, plus the incremental circuit
// breaker's "suspended"); these name the routing and window outcomes that
// never reach a manager.
const (
	// vectorErrorKindLoading — the background embedder init has not produced
	// a manager factory yet.
	vectorErrorKindLoading = "loading"
	// vectorErrorKindRestarting — the root's manager is still draining from
	// an LRU eviction over the same storage; the next request finds it live.
	vectorErrorKindRestarting = "restarting"
	// vectorErrorKindNoTarget — no focus root resolved (or a foreign root):
	// there is nothing to search against.
	vectorErrorKindNoTarget = "no_target"
	// vectorErrorKindShuttingDown — the registry is shutting down (app exit).
	vectorErrorKindShuttingDown = "shutting_down"
)

// vectorIndexNoProjectReason is the cause attached to the dormant subsystem's
// status in No Project (CHAT) mode — the same string on every path that
// reports it, so the UI can render one coherent explanation.
const vectorIndexNoProjectReason = "vector index is unavailable in No Project mode (there is no workspace to index)"

// GetVectorIndexStatus returns the current state and progress of the
// vector index for the FOCUS tree (the Git-panel focus root; ADR-080).
func (f *FrontendAPI) GetVectorIndexStatus() VectorIndexStatus {
	// No Project (CHAT mode): the vector index is disabled. Report an
	// unavailable state so the frontend UI reflects the dormant subsystem
	// (neither "building" nor "ready") — WITH its cause, because a bare
	// "unavailable" is indistinguishable from a broken index.
	if f.isNoProject() {
		st := VectorIndexStatus{
			State:     vectorStateUnavailable,
			Indices:   []string{},
			LastError: vectorIndexNoProjectReason,
			ErrorKind: vectorErrorKindNoTarget,
		}
		f.applyEmbedderInfo(&st)
		return st
	}

	vm, err := f.vectorRootsRegistry().FocusManager(f.ctx())
	if err != nil {
		result := f.vectorIndexStatusFromRegistryError(err)
		f.applyEmbedderInfo(&result)
		return result
	}

	result := f.vectorIndexStatusFromManager(vm)
	f.applyEmbedderInfo(&result)
	return result
}

// vectorIndexStatusFromRegistryError maps a FocusManager/routing failure onto
// the wire status. The contract is asymmetric on purpose:
//
//   - a disabled registry is a USER state, not a failure — "disabled" with no
//     cause (the switch IS the explanation);
//   - the still-loading window and an LRU-eviction drain are NOT failures —
//     "loading" (kind loading / restarting), never "unavailable": the index
//     was never broken and both resolve without user action;
//   - every genuinely unavailable outcome carries its cause and kind, so the
//     "unavailable always explains itself" invariant holds for foreign/no
//     targets, a shutting-down registry, a dead embedder and any wrapped
//     build/init error alike.
func (f *FrontendAPI) vectorIndexStatusFromRegistryError(err error) VectorIndexStatus {
	result := VectorIndexStatus{Indices: []string{}}
	switch {
	case errors.Is(err, errVectorDisabled):
		result.State = vectorStateDisabled
	case errors.Is(err, errVectorStillLoading):
		result.State = vectorStateLoading
		result.ErrorKind = vectorErrorKindLoading
		result.LastError = err.Error()
	case errors.Is(err, errVectorRestarting):
		result.State = vectorStateLoading
		result.ErrorKind = vectorErrorKindRestarting
		result.LastError = err.Error()
	case errors.Is(err, errVectorNoTarget):
		result.State = vectorStateUnavailable
		result.ErrorKind = vectorErrorKindNoTarget
		result.LastError = err.Error()
	case errors.Is(err, errVectorShuttingDown):
		result.State = vectorStateUnavailable
		result.ErrorKind = vectorErrorKindShuttingDown
		result.LastError = err.Error()
	default:
		// Building/initializing a manager failed: the embedder never came up
		// or the store rejected the root. The kind rides the SAME taxonomy
		// the manager's own failures use (ClassifyInitError), so one cause
		// keeps one verdict whichever layer surfaced it — an EACCES/ENOSPC
		// while creating the storage directories stays `transient` (a retry
		// after the fault clears can succeed), and only genuinely settled
		// failures read `fatal`. A nil error cannot reach this branch; the
		// fallback keeps the wire kind non-empty regardless.
		result.State = vectorStateUnavailable
		kind := vectorindex.ClassifyInitError(err)
		if kind == "" {
			kind = vectorindex.IndexErrorKindUnknown
		}
		result.ErrorKind = string(kind)
		result.LastError = vectorindex.InitFailureReason(err)
	}
	return result
}

// vectorIndexStatusFromManager builds the wire status from a manager's live
// index state; embedder-info enrichment stays with the callers via
// applyEmbedderInfo.
func (f *FrontendAPI) vectorIndexStatusFromManager(vm *vectorindex.Manager) VectorIndexStatus {
	st := vm.GetIndexStatus()
	result := VectorIndexStatus{
		State:        string(st.State),
		Phase:        string(st.Phase),
		FilesIndexed: st.FilesIndexed,
		TotalFiles:   st.TotalFiles,
		CurrentFile:  st.CurrentFile,
		Branch:       st.Branch,
		LastError:    st.LastError,
		ErrorKind:    string(st.ErrorKind),
	}

	// Compute progress as a fraction.
	if st.TotalFiles > 0 {
		result.Progress = float64(st.FilesIndexed) / float64(st.TotalFiles)
	}

	// Determine which indices are active.
	svc := vm.Service()
	indices := make([]string, 0, 2)
	if svc != nil {
		vector, lexical := svc.IndexAvailability()
		if vector {
			indices = append(indices, "vector")
		}
		if lexical {
			indices = append(indices, "lexical")
		}
	}
	result.Indices = indices

	// The §5 disk ledger (ADR-104): the registry's cached snapshot for the
	// manager's physical store — populated by the hygiene pass; omitted
	// until then. The root comes from the MANAGER (lock-free for status:
	// the service's own StorageRoot takes the service read lock an
	// in-flight indexing pass holds — ADR-104 §4's never-block contract).
	if reg := f.vectorCorpusRegistry(); reg != nil {
		if root := vm.StorageRoot(); root != "" {
			result.Store = reg.StoreLedgerSnapshot(root)
		}
	}
	return result
}

// ReindexVectorIndex forces a full reindex of the FOCUS tree's vector index,
// reconciling it against the workspace (full build when no index exists). The
// pass runs in the background and streams progress through
// vector_index:status events; an error is returned when the index is
// unavailable (No Project, no focus root, embedder still loading or failed).
//
// Since the store can be left unopenable by an incomplete publication (a
// corpus whose lexical leg is gone, a foreign era, torn §3 tables), the same
// action is the user-facing REPAIR for a stuck "unavailable" index: with no
// live collection the manager re-attaches the snapshot view, which makes the
// publication predicate reject the unopenable corpus and rebuild it from
// source (cache-spared) instead of silently doing nothing. The frontend keeps
// the action enabled in `unavailable` for exactly this reason.
func (f *FrontendAPI) ReindexVectorIndex() error {
	// No Project (CHAT mode): the vector index is disabled — nothing to reindex.
	if f.isNoProject() {
		return errors.New("vector index is unavailable in No Project mode")
	}

	vm, err := f.vectorRootsRegistry().FocusManager(f.ctx())
	if err != nil {
		// Carry the routing cause instead of a bare refusal: the frontend
		// surfaces the rejection (a runtime_error toast), and the honest
		// cause names the actual remedy (a dead embedder, a missing focus
		// root, the shutdown window) instead of a generic "not available".
		return fmt.Errorf("vector index not available: %w", err)
	}

	return vm.Reindex(f.ctx())
}

// ListVectorIndexGPUs enumerates the GPUs visible on the machine for the
// vector-index settings device picker — the lazy, on-demand counterpart to the
// startup probe. Delegates to embedding.ListGPUDevices: NVIDIA via nvidia-smi
// (provider "cuda"), Intel via vulkaninfo (provider "openvino", always
// device_id 0 — the only value the plugin surface accepts; see
// specs/domains/workspace.md).
//
// No probe tool in PATH → an empty, non-nil slice with a nil error ("no GPUs
// to offer", not a failure). Probes found but failing with no source
// answering → nil slice with the wrapped error, so the UI distinguishes "no
// hardware" from "hardware present, probe failed"; a healthy source wins over
// a failing one. Independent of the running embedder — its facts travel in
// VectorIndexStatus via applyEmbedderInfo.
func (f *FrontendAPI) ListVectorIndexGPUs() ([]GPUDeviceResponse, error) {
	devices, err := embedding.ListGPUDevices(f.ctx())
	if err != nil {
		return nil, err
	}

	out := make([]GPUDeviceResponse, len(devices))
	for i, d := range devices {
		out[i] = GPUDeviceResponse{
			Index:    d.Index,
			Name:     d.Name,
			Provider: d.Provider,
		}
	}
	return out, nil
}
