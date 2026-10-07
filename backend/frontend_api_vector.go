package backend

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
//
// This is the INITIAL Git-panel-focus resolution at project switch: the
// resolved root is handed to the per-root registry (ApplyFocus), which owns
// the manager for it. It no longer re-points a shared singleton — sessions
// route through their own task context, the user's search follows the focus.
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
// The embedding cache is per-tree too (WorktreeEmbeddingCachePath): with
// per-root managers a checkout manager and a tree manager run concurrently,
// and the cache accounting walks its root recursively, so a shared root is
// no longer safe.
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

// deleteWorktreeVectorIndex releases a managed tree's per-root manager and
// removes the tree's persisted vector-index state — its whole worktree-scoped
// storage root plus its per-tree embedding cache. Best-effort by design: the
// tree is already gone, so leftover index data is only disk garbage.
// The registry Release must happen FIRST: it shuts the manager down (dropping
// its open chromem handles so the directory removal works on Windows too)
// before DeleteProjectData wipes the storage.
func (f *FrontendAPI) deleteWorktreeVectorIndex(repoRoot, projectID, name string) {
	vr := f.vectorRootsRegistry()
	root, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree root for release",
			"project", projectID, "worktree", name, "error", err)
		root = filepath.Join(config.ManagedWorktreesDir(repoRoot), name)
	}
	mgr := vr.Release(root)

	storage, err := config.WorktreeVectorIndexPath(f.agentDir, projectID, name)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree storage path for cleanup",
			"project", projectID, "worktree", name, "error", err)
		return
	}
	clean, disposable := mgr, false
	if clean == nil {
		// No live manager ever routed to this tree in this app run, but
		// persisted data from a previous run may exist; any manager can
		// remove it (DeleteProjectData is a plain fs + park-slot operation).
		clean, disposable = vr.ManagerForCleanup()
	}
	if clean != nil {
		if err := clean.DeleteProjectData(storage); err != nil {
			f.log().Warn("vector index: failed to remove released worktree index data",
				"project", projectID, "worktree", name, "error", err)
		}
		if disposable {
			clean.Shutdown()
		}
	}

	// The per-tree embedding cache is a sibling of the tree's storage (its
	// content-addressed entries are meaningless without the tree) — drop it.
	if cache, cerr := config.WorktreeEmbeddingCachePath(f.agentDir, projectID, name); cerr == nil {
		if err := os.RemoveAll(cache); err != nil {
			f.log().Warn("vector index: failed to remove released worktree embedding cache",
				"project", projectID, "worktree", name, "error", err)
		}
	}
}

// seedWorktreeEmbeddingCache populates a freshly provisioned worktree's
// content-addressed embedding cache from the project checkout's cache, so the
// tree's first index pass reuses the embeddings the checkout already computed
// instead of re-running ONNX inference over near-identical content. A session
// worktree is 99-100% byte-identical to the branch point, and the cache — not
// the branch collections — is the layer where that identity is reusable:
// entries are keyed by (format version, embedding fingerprint, chunk text
// hash) with paths deliberately excluded from the key, and every embed path
// (full or incremental) consults the cache before the model.
//
// Correctness: entries carry a trailing checksum, so a partially copied or
// torn file decodes as a cold miss, never as a wrong vector; the fingerprint
// covers the model/tokenizer bytes and every vector-producing parameter, so a
// model swap turns the seed into dead weight the per-root FIFO prune evicts.
// The per-tree cache keeps its single-owner-directory model (ADR-081): the
// seed runs once, synchronously, in the provisioning RPC — after the session
// row is persisted and before the RPC returns, so the frontend's immediate
// focus move cannot build the tree's vector manager (and install its cache
// instance) until the copied state is already in place. The checkout's
// manager may concurrently write or prune the source; the tolerant copier
// treats a vanished or unreadable entry as the cold miss it is.
//
// Contract: best-effort and idempotent — never fails the caller. A skipped or
// partial seed only means the tree's first index pass runs cold. Call from
// the provisioning flows after their success point (create and fork); the
// recreate path needs no call (its cache root survives the lost tree).
func (f *FrontendAPI) seedWorktreeEmbeddingCache(projectID, worktreeName string) {
	src := config.ProjectEmbeddingCachePath(f.agentDir, projectID)
	dst, err := config.WorktreeEmbeddingCachePath(f.agentDir, projectID, worktreeName)
	if err != nil {
		f.log().Debug("vector index: cannot derive worktree cache path for seeding",
			"project", projectID, "worktree", worktreeName, "error", err)
		return
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		if !os.IsNotExist(err) {
			f.log().Debug("vector index: checkout embedding cache unreadable; seed skipped",
				"project", projectID, "worktree", worktreeName, "error", err)
		}
		return
	}
	if !srcInfo.IsDir() {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		// Already seeded (or the tree's manager created the root): never
		// touch an existing cache directory.
		return
	} else if !os.IsNotExist(err) {
		f.log().Warn("vector index: worktree embedding cache state unreadable; seed skipped",
			"project", projectID, "worktree", worktreeName, "error", err)
		return
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		f.log().Debug("vector index: cannot create worktree embedding cache; seed skipped",
			"project", projectID, "worktree", worktreeName, "error", err)
		return
	}

	started := time.Now()
	files, bytes, err := copyEmbeddingCacheTree(src, dst)
	if err != nil {
		f.log().Warn("vector index: worktree embedding cache seed incomplete; the tree's first index pass stays cold",
			"project", projectID, "worktree", worktreeName,
			"files", files, "bytes", bytes, "error", err)
		return
	}
	f.log().Info("vector index: seeded worktree embedding cache from the project checkout",
		"project", projectID, "worktree", worktreeName,
		"files", files, "bytes", bytes, "duration", time.Since(started))
}

// copyEmbeddingCacheTree mirrors src's fanout tree (two-level hex dirs of
// .vec entries; only regular files are meaningful) into dst, which must not
// exist. Tolerant by design: the source is a live cache the checkout's
// manager may be writing or pruning concurrently, so per-file failures are
// skipped — a missing entry is a cold miss downstream, and a torn copy fails
// the entry's checksum at read time. Directory and file modes match the
// cache's own writer (0o750 dirs, 0o600 files). Returns the copied entry
// count and byte size; the error is the first fatal walk error (an unreadable
// source directory), not a per-file skip.
func copyEmbeddingCacheTree(src, dst string) (filesCopied int, bytesCopied int64, err error) {
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == src {
				return walkErr // unreadable source root: nothing sensible to copy
			}
			return nil //nolint:nilerr // a vanished subtree is a skip, not a failure
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return nil //nolint:nilerr // cannot express the entry relative to src: skip it
		}
		if d.IsDir() {
			if path == src {
				return nil // dst root already exists
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o750)
		}
		if !d.Type().IsRegular() {
			return nil // caches hold only regular entries; skip anything else
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil //nolint:nilerr // the entry vanished between ReadDir and Info: cold miss
		}
		if copyErr := copyFileExact(filepath.Join(dst, rel), path, info.Mode().Perm()); copyErr != nil {
			return nil //nolint:nilerr // unreadable or just-pruned source entry: cold miss
		}
		filesCopied++
		bytesCopied += info.Size()
		return nil
	})
	return filesCopied, bytesCopied, err
}

// copyFileExact copies one regular file, creating dstPath exclusively with
// the source's permission bits. A failed copy removes the partial so the
// destination never holds a half-written entry under a name a later copy
// would consider done.
func copyFileExact(dstPath, srcPath string, perm os.FileMode) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dstPath)
		return err
	}
	return out.Close()
}

// SearchVectorStore searches the vector store for the given request, routed
// to the Git-panel FOCUS tree's manager (ADR-080): the user's search follows
// the tree the panel displays, not the last session that was driven. Session
// tools route separately, through their task context (vector_roots.go).
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

	vr := f.vectorRootsRegistry()
	vm, err := vr.FocusManager()
	if err != nil {
		return nil, errVectorNoTarget
	}

	topK := req.TopK
	if topK <= 0 {
		topK = defaultVectorBrowseTopK
	}

	vectorSvc := vm.Service()

	var results []vectorindex.SearchResult

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
// vector index for the FOCUS tree (the Git-panel focus root; ADR-080).
func (f *FrontendAPI) GetVectorIndexStatus() VectorIndexStatus {
	// No Project (CHAT mode): the vector index is disabled. Report an
	// unavailable state so the frontend UI reflects the dormant subsystem
	// (neither "building" nor "ready").
	if f.isNoProject() {
		st := VectorIndexStatus{State: "unavailable", Indices: []string{}}
		f.applyEmbedderInfo(&st)
		return st
	}

	vm, err := f.vectorRootsRegistry().FocusManager()
	if err != nil {
		result := VectorIndexStatus{State: "unavailable", Indices: []string{}}
		f.applyEmbedderInfo(&result)
		return result
	}

	result := f.vectorIndexStatusFromManager(vm)
	f.applyEmbedderInfo(&result)
	return result
}

// vectorIndexStatusFromManager builds the wire status from a manager's live
// index state (progress fraction, active indices). Embedder-info enrichment
// stays with the callers via applyEmbedderInfo.
func (f *FrontendAPI) vectorIndexStatusFromManager(vm *vectorindex.Manager) VectorIndexStatus {
	st := vm.GetIndexStatus()
	result := VectorIndexStatus{
		State:        string(st.State),
		Phase:        string(st.Phase),
		FilesIndexed: st.FilesIndexed,
		TotalFiles:   st.TotalFiles,
		CurrentFile:  st.CurrentFile,
		Branch:       st.Branch,
	}

	// Compute progress as a fraction.
	if st.TotalFiles > 0 {
		result.Progress = float64(st.FilesIndexed) / float64(st.TotalFiles)
	}

	// Determine which indices are active.
	svc := vm.Service()
	indices := make([]string, 0, 2)
	if svc != nil {
		if svc.GetCollection() != nil {
			indices = append(indices, "vector")
		}
		if svc.GetLexical() != nil {
			indices = append(indices, "lexical")
		}
	}
	result.Indices = indices
	return result
}

// ReindexVectorIndex forces a full reindex of the FOCUS tree's vector index.
// It reconciles the existing index against the workspace, re-indexing
// changed/new/deleted files; when no index exists yet (empty collection) it
// falls back to a full build from scratch. The pass runs in the background and
// streams progress through vector_index:status events.
//
// It returns an error when the vector index is unavailable: No Project (CHAT
// mode, indexing is disabled), no focus root resolved (no project switch has
// pointed the Git panel anywhere yet), or the embedding backend is still
// loading / failed to load.
func (f *FrontendAPI) ReindexVectorIndex() error {
	// No Project (CHAT mode): the vector index is disabled — nothing to reindex.
	if f.isNoProject() {
		return errors.New("vector index is unavailable in No Project mode")
	}

	vm, err := f.vectorRootsRegistry().FocusManager()
	if err != nil {
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
