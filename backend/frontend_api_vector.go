package backend

import (
	"context"
	"errors"
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
// project p. A managed-worktree session of p (the saved session of the
// project at switch time) targets that session's OWN tree root with a
// worktree-scoped storage dir — index and search route per session workspace
// root, the branch is resolved from that root by the manager's async init,
// and concurrent sessions on different trees keep disjoint persisted index
// state. Every other case — local sessions, no resolvable session, or a
// session workspace that is not a managed tree of this project — keeps the
// project checkout with the project storage dir: the default local-project
// flow, unchanged. Membership is decided by containment (the tree must live
// inside THIS project's .worktrees container), never by comparing paths to
// the checkout.
func (f *FrontendAPI) resolveVectorIndexTarget(p *project.ProjectInfo) vectorIndexTarget {
	base := vectorIndexTarget{
		workspacePath: p.WorkspacePath,
		storagePath:   config.ProjectVectorIndexPath(f.agentDir, p.ID),
	}
	if p.IsNoProject || f.app == nil || f.app.Manager() == nil {
		return base
	}
	savedID := ""
	if f.projStore != nil {
		if st, err := f.projStore.LoadUIState(context.Background(), p.ID); err == nil && st != nil {
			savedID = strings.TrimSpace(st.SavedSessionID)
		}
	}
	if savedID == "" {
		return base
	}
	ws, ok := f.app.Manager().WorkspacePathFor(context.Background(), savedID)
	if !ok || ws == "" || ws == p.WorkspacePath {
		return base
	}
	target, ok := f.managedWorktreeVectorTarget(p, ws)
	if !ok {
		return base
	}
	return target
}

// managedWorktreeVectorTarget derives the worktree-scoped vector target for
// workspace ws when ws is a managed worktree of project p's checkout, and
// reports whether it is. The workspace is the tree root itself; the storage
// root is <project vector index>/worktrees/<name> (config owns the layout).
// The project-level embedding cache stays shared: it is content-addressed and
// accessed by one manager at a time, so trees of one project dedup embeddings
// instead of duplicating them.
func (f *FrontendAPI) managedWorktreeVectorTarget(p *project.ProjectInfo, ws string) (vectorIndexTarget, bool) {
	name, err := config.ManagedWorktreeNameFromPath(p.WorkspacePath, ws)
	if err != nil {
		return vectorIndexTarget{}, false
	}
	storage, err := config.WorktreeVectorIndexPath(f.agentDir, p.ID, name)
	if err != nil {
		f.log().Warn("vector index: deriving worktree storage path failed; indexing the project checkout instead",
			"project", p.ID, "workspace", ws, "error", err)
		return vectorIndexTarget{}, false
	}
	return vectorIndexTarget{workspacePath: ws, storagePath: storage}, true
}

// maybeReScopeVectorIndexToSession re-points the vector index at the given
// session's execution workspace when it differs from the current target. The
// SendMessage path is the authoritative "this session is being driven"
// signal: re-scoping there makes the send target's RAG hint injection and its
// semantic_search calls resolve against its own tree (branch included). The
// lookup restores the session first (GetSession runs the WorkspaceEnsurer,
// so a managed tree that is missing gets recreated before the index targets
// it), then re-scopes only when the workspace is a managed worktree of the
// active project and the target actually moved. No-op for CHAT, foreign
// projects, local workspaces, unchanged targets, or an unwired vector
// manager (the project-switch handshake still applies the project setup).
func (f *FrontendAPI) maybeReScopeVectorIndexToSession(sessionID string) {
	if sessionID == "" || f.app == nil || f.app.Manager() == nil {
		return
	}
	f.activeProjectMu.RLock()
	activeID := f.activeProjectID
	activePath := f.activeProjectPath
	f.activeProjectMu.RUnlock()
	if activeID == "" || activeID == project.NoProjectID || activePath == "" {
		return
	}

	// Cheap pre-check that avoids the restoring lookup in the overwhelmingly
	// common case: the session's workspace already matches the checkout.
	if ws, ok := f.app.Manager().WorkspacePathFor(context.Background(), sessionID); ok && (ws == "" || ws == activePath) {
		return
	}

	sess, ok := f.app.Manager().GetSession(sessionID)
	if !ok || sess == nil || sess.ProjectID != activeID {
		return
	}
	ws := sess.WorkspacePath
	if ws == "" || ws == activePath {
		return
	}

	f.vectorSetupMu.Lock()
	unchanged := f.vectorTargetWorkspace == ws
	f.vectorTargetWorkspace = ws
	vm := f.getVectorManager()
	f.vectorSetupMu.Unlock()
	if unchanged || vm == nil {
		return
	}

	p := &project.ProjectInfo{ID: activeID, WorkspacePath: activePath}
	target, ok := f.managedWorktreeVectorTarget(p, ws)
	if !ok {
		f.log().Debug("vector index re-scope: session workspace is not a managed tree of the active project",
			"session", sessionID, "workspace", ws)
		return
	}
	f.log().Info("vector index re-scoped to session worktree", "session", sessionID, "workspace", ws)
	if err := f.switchVectorIndexToTarget(p, target, vm); err != nil {
		f.log().Warn("vector index re-scope failed", "session", sessionID, "error", err)
	}
}

// switchVectorIndexToTarget points the vector manager at the resolved target
// for CODE project p. Vector init (chromem DB open, branch detect — from the
// TARGET workspace — branch-collection switch, background indexing, git
// monitor) runs asynchronously inside the manager's initProject goroutine.
func (f *FrontendAPI) switchVectorIndexToTarget(p *project.ProjectInfo, target vectorIndexTarget, vm *vectorindex.Manager) error {
	return vm.SwitchProject(p.ID, target.workspacePath, target.storagePath, vectorindex.ProjectCallbacks{
		OnProgress: func(phase vectorindex.IndexPhase, state vectorindex.IndexState, indexed, total int, file string) {
			st := VectorIndexStatus{
				State:        string(state),
				Phase:        string(phase),
				Indices:      []string{"vector", "lexical"},
				Progress:     progressFraction(indexed, total),
				FilesIndexed: indexed,
				TotalFiles:   total,
				CurrentFile:  file,
				Branch:       vm.GetIndexStatus().Branch,
			}
			f.applyEmbedderInfo(&st)
			f.emitEvent(EventVectorIndexStatus, st)
		},
		OnFailure: func(err error) {
			f.log().Warn("vector index init failed for project; search unavailable",
				"project", p.ID, "workspace", target.workspacePath, "error", err)
			st := VectorIndexStatus{
				State:   string(vectorindex.IndexStateUnavailable),
				Indices: []string{},
			}
			f.applyEmbedderInfo(&st)
			f.emitEvent(EventVectorIndexStatus, st)
		},
	}, config.ProjectEmbeddingCachePath(f.agentDir, p.ID))
}

// deleteWorktreeVectorIndex removes a released managed tree's persisted
// vector-index state — its whole worktree-scoped storage root, parked service
// slot included. Best-effort by design: the tree is already gone, so leftover
// index data is only disk garbage.
func (f *FrontendAPI) deleteWorktreeVectorIndex(projectID, name string) {
	vm := f.getVectorManager()
	if vm == nil {
		return
	}
	storage, err := config.WorktreeVectorIndexPath(f.agentDir, projectID, name)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree storage path for cleanup",
			"project", projectID, "worktree", name, "error", err)
		return
	}
	if err := vm.DeleteProjectData(storage); err != nil {
		f.log().Warn("vector index: failed to remove released worktree index data",
			"project", projectID, "worktree", name, "error", err)
	}
}

// SearchVectorStore searches the vector store for the given request.
//
// When req.Query is empty it browses arbitrary chunks (no semantic
// ordering) through BrowseWithFilter. Otherwise it dispatches to
// Service.HybridSearch with the requested mode (hybrid | vector |
// lexical; empty defaults to hybrid with auto-fallback to vector when
// the lexical index is empty).
//
// req.TopK defaults to 50 when <= 0.
func (f *FrontendAPI) SearchVectorStore(req SearchRequest) ([]VectorStoreEntry, error) {
	// No Project (CHAT mode): the vector index is disabled. Return empty
	// results (not an error) so the frontend renders an empty state rather
	// than attempting a search against a dormant subsystem.
	if f.isNoProject() {
		return []VectorStoreEntry{}, nil
	}

	vm := f.getVectorManager()
	if vm == nil {
		return nil, errors.New("vector search not available")
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := vm.Service()

	var results []vectorindex.SearchResult
	var err error

	// Defense-in-depth: bound the readiness wait inside HybridSearch /
	// BrowseWithFilter with the same knob that bounds the semantic_search
	// tool (vector_index.search_wait_timeout_ms), so the RPC can never block
	// unboundedly while a full index is stuck. The fail-fast sentinel (0)
	// skips waiting entirely: it dispatches to the NoWait variants, so a
	// not-ready index errors immediately AND an incremental pass starting
	// between the readiness state and the call cannot block the RPC until
	// the pass finishes.
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
		// Fail-fast dispatch observed a not-ready index (including the
		// pass-started-after-gate race): surface the actionable index
		// status (progress, current file) instead of a bare error.
		if errors.Is(err, vectorindex.ErrNotReady) {
			return nil, vm.NotReadyError()
		}
		// The bound expired while waiting for readiness: surface the
		// actionable index status (progress, current file) instead of a
		// bare deadline error.
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

// GetVectorIndexStatus returns the current state and progress of the
// vector index for the active project.
func (f *FrontendAPI) GetVectorIndexStatus() VectorIndexStatus {
	// No Project (CHAT mode): the vector index is disabled. Report an
	// unavailable state so the frontend UI reflects the dormant subsystem
	// (neither "building" nor "ready").
	if f.isNoProject() {
		st := VectorIndexStatus{State: "unavailable", Indices: []string{}}
		f.applyEmbedderInfo(&st)
		return st
	}

	result := VectorIndexStatus{}

	vm := f.getVectorManager()
	if vm == nil {
		result.State = "unavailable"
		f.applyEmbedderInfo(&result)
		return result
	}

	st := vm.GetIndexStatus()
	result.State = string(st.State)
	result.Phase = string(st.Phase)
	result.FilesIndexed = st.FilesIndexed
	result.TotalFiles = st.TotalFiles
	result.CurrentFile = st.CurrentFile
	result.Branch = st.Branch

	// Compute progress as a fraction.
	if st.TotalFiles > 0 {
		result.Progress = float64(st.FilesIndexed) / float64(st.TotalFiles)
	}

	// Determine which indices are active.
	svc := vm.Service()
	indices := make([]string, 0, 2)
	if svc == nil {
		result.Indices = indices
		f.applyEmbedderInfo(&result)
		return result
	}
	if svc.GetCollection() != nil {
		indices = append(indices, "vector")
	}
	if svc.GetLexical() != nil {
		indices = append(indices, "lexical")
	}
	result.Indices = indices

	f.applyEmbedderInfo(&result)
	return result
}

// ReindexVectorIndex forces a full reindex of the active project's vector
// index. It reconciles the existing index against the workspace, re-indexing
// changed/new/deleted files; when no index exists yet (empty collection) it
// falls back to a full build from scratch. The pass runs in the background and
// streams progress through vector_index:status events.
//
// It returns an error when the vector index is unavailable: No Project (CHAT
// mode, indexing is disabled) or no wired vector manager (before startup's
// background vector init completes).
func (f *FrontendAPI) ReindexVectorIndex() error {
	// No Project (CHAT mode): the vector index is disabled — nothing to reindex.
	if f.isNoProject() {
		return errors.New("vector index is unavailable in No Project mode")
	}

	vm := f.getVectorManager()
	if vm == nil {
		return errors.New("vector index not available")
	}

	return vm.Reindex(f.ctx())
}

// ListVectorIndexGPUs enumerates the NVIDIA GPUs visible on the machine for
// the vector-index settings UI (populating the execution-provider device
// picker). It is the lazy, on-demand counterpart to the startup GPU probe:
// the UI calls it when the picker is opened, instead of the backend probing
// unconditionally at startup.
//
// The call delegates to embedding.ListGPUDevices, which shells out to
// nvidia-smi under the same bounded probe budget (gpuProbeTimeout, 2s) as
// GPUInUse — so the RPC can never block the UI thread indefinitely on a
// wedged driver.
//
// Semantics:
//   - nvidia-smi not found in PATH → an empty, non-nil slice with a nil
//     error. Absence of the NVIDIA userspace means "no GPUs to offer", not
//     a failure — the picker renders an empty/CPU-only state.
//   - nvidia-smi found but the invocation fails (driver down, non-zero
//     exit, probe timeout) → nil slice with the wrapped error, so the UI
//     can distinguish "no hardware" from "hardware present, probe failed".
//
// The result is independent of the running embedder: it reflects what the
// driver reports right now, not what the embedder was created with (the
// embedder's facts travel in VectorIndexStatus via applyEmbedderInfo).
func (f *FrontendAPI) ListVectorIndexGPUs() ([]GPUDeviceResponse, error) {
	devices, err := embedding.ListGPUDevices(f.ctx())
	if err != nil {
		return nil, err
	}

	out := make([]GPUDeviceResponse, len(devices))
	for i, d := range devices {
		out[i] = GPUDeviceResponse{
			Index: d.Index,
			Name:  d.Name,
		}
	}
	return out, nil
}
