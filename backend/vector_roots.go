// frontend_api_vector_roots.go — the per-root vector-index registry (ADR-080).
//
// The vector index used to be a process-wide SINGLETON whose single active
// target was re-pointed on every SendMessage and project switch. A background
// session's semantic_search therefore resolved against whatever tree the
// singleton happened to target, and the user-facing search followed the last
// driven session instead of the tree the Git panel focused. This file replaces
// the singleton with one Manager per workspace root:
//
//   - Every root ever routed to gets its own vectorindex.Manager with its own
//     persisted storage dir and its own embedding-cache root (exactly one
//     manager per root, ever — two managers must never open the same chromem
//     persistent storage). Concurrent sessions on different trees get
//     disjoint, simultaneously searchable index state.
//   - AGENT routing: the semantic_search tool and the RAG-hint injection run
//     under a task context carrying the session's workspace root
//     (sdktools.WithWorkspacePathNoProbe, set by the session manager before
//     Orchestrator.HandleMessage). The router closure resolves THAT root, so a
//     session always searches its own tree regardless of which session is
//     active, visible, or focused.
//   - USER routing: SearchVectorStore / GetVectorIndexStatus /
//     ReindexVectorIndex have no session context — they resolve the Git-panel
//     focus root (SetGitPanelFocus), so the user's search follows the tree the
//     panel displays.
//
// The registry keeps a small LRU of live managers (pinned focus + a bounded
// set of recent roots); evicted roots reopen transparently from their
// persisted gob state on the next search.
package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/vectorindex"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// vectorRootsCapacity bounds how many per-root managers stay live at once.
// The focus root is pinned; least-recently-used others are shut down
// (async — Manager.Shutdown is internally bounded) and reopen transparently
// from their persisted state on the next search. Four covers a checkout plus
// the managed trees of the sessions a user realistically keeps warm.
const vectorRootsCapacity = 4

// errVectorStillLoading is the pre-init surface: the background ONNX init has
// not produced the manager factory yet. Text is part of the semantic_search
// tool's error contract (tests and prompts reference the retry hint).
var errVectorStillLoading = errors.New("vector search not ready (index backend still loading); retry semantic_search later or use ripgrep/glob")

// errVectorUnavailable is the post-init surface when the embedder never came
// up (model files missing, ONNX load failure) — no manager can ever be built.
var errVectorUnavailable = errors.New("vector search unavailable (embedding backend failed to initialize; see vector_index:status events or the session log)")

// errVectorNoTarget is the legacy "nothing to search with" surface: no
// workspace in context and no focus root resolved.
var errVectorNoTarget = errors.New("vector search not available")

// vectorRootEntry is one live per-root manager with its LRU bookkeeping.
type vectorRootEntry struct {
	mgr       *vectorindex.Manager
	projectID string
	lastUsed  time.Time
}

// VectorRoots owns one vectorindex.Manager per workspace root and the
// Git-panel focus the user-facing RPCs follow. All methods are safe for
// concurrent use; manager creation is single-flight per root under mu (two
// concurrent searches on one cold root must not build two managers over the
// same chromem storage dir).
type VectorRoots struct {
	f *FrontendAPI

	mu    sync.Mutex
	roots map[string]*vectorRootEntry // key: canonical (filepath.Clean) root
	focus string                      // canonical focus root; "" = none yet

	// factory builds a fresh manager wired to the process-global embedder.
	// Nil until the desktop background ONNX init succeeds; once the ready
	// channel is closed with factory still nil, the embedder is known dead
	// and ManagerForRoot fails with errVectorUnavailable instead of waiting.
	factory func() (*vectorindex.Manager, error)
	ready   <-chan struct{}
	// closeEmbedder releases the process-global embedder exactly once, after
	// every manager is shut down (ShutdownAll). Per-manager CloseFn stays nil:
	// an LRU eviction must never close the shared ONNX runtime.
	closeEmbedder func() error

	shutdown bool
}

func newVectorRoots(f *FrontendAPI) *VectorRoots {
	return &VectorRoots{
		f:     f,
		roots: make(map[string]*vectorRootEntry),
	}
}

// SetFactory wires the manager factory produced by the desktop background
// ONNX init, together with the readiness channel (closed exactly once when
// the init settles — success or known-unavailable) and the once-guarded
// embedder close for final shutdown. Called once from the desktop layer.
// If ShutdownAll already ran (quit during the background init), the embedder
// is closed right here: the registry will never build a manager, so nobody
// else would release it.
func (vr *VectorRoots) SetFactory(factory func() (*vectorindex.Manager, error), ready <-chan struct{}, closeEmbedder func() error) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	if vr.shutdown {
		vr.f.log().Warn("vector roots: factory wired after shutdown; closing the embedder")
		if closeEmbedder != nil {
			if err := closeEmbedder(); err != nil {
				vr.f.log().Warn("vector roots: failed to close the embedder", "error", err)
			}
		}
		return
	}
	vr.factory = factory
	vr.ready = ready
	vr.closeEmbedder = closeEmbedder
}

// FactoryReady reports whether the registry can build managers: the factory
// is set, or the background init has settled (in which case creation fails
// fast with errVectorUnavailable). The project-switch handshake uses this to
// decide between applying and deferring a switch's vector setup.
func (vr *VectorRoots) FactoryReady() bool {
	if vr == nil {
		return false
	}
	vr.mu.Lock()
	defer vr.mu.Unlock()
	return vr.factory != nil || vr.readySettledLocked()
}

// readySettledLocked reports whether the background init has finished
// (successfully or not). Caller holds vr.mu.
func (vr *VectorRoots) readySettledLocked() bool {
	if vr.ready == nil {
		return false
	}
	select {
	case <-vr.ready:
		return true
	default:
		return false
	}
}

// canonicalRoot normalizes a workspace root into the registry key. Roots
// arrive absolute from every producer (session workspace bindings, Git-panel
// focus, project registration); Clean keeps trailing separators and dot
// segments from forking the map.
func canonicalRoot(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// awaitReady gates on the background ONNX init: when block is set it waits
// until the init settles or ctx expires (still-loading hint on expiry);
// otherwise it checks readiness without ever blocking (the fail-fast
// sentinel path).
func (vr *VectorRoots) awaitReady(ctx context.Context, block bool) error {
	vr.mu.Lock()
	ready := vr.ready
	vr.mu.Unlock()
	if ready == nil {
		return errVectorStillLoading
	}
	if !block {
		select {
		case <-ready:
			return nil
		default:
			return errVectorStillLoading
		}
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return errVectorStillLoading
	}
}

// resolveRootPlan derives (projectID, storage, cache) for a workspace root.
// The root must be either a registered project's checkout or a managed
// worktree of one — vector indexing is a CODE-project feature; CHAT session
// workspaces and auxiliary work dirs have no index.
func (f *FrontendAPI) resolveRootPlan(root string) (projectID, storagePath, cachePath string, err error) {
	if f.projectManager == nil {
		return "", "", "", errors.New("no project manager wired")
	}
	projects, err := f.projectManager.ListProjects()
	if err != nil {
		return "", "", "", fmt.Errorf("listing projects for vector routing: %w", err)
	}
	// Pass 1: the checkout itself. Pass 2: managed worktrees — a tree can
	// only belong to the project whose .worktrees container holds it.
	for _, p := range projects {
		if p.IsNoProject {
			continue
		}
		if canonicalRoot(p.WorkspacePath) == root {
			return p.ID, config.ProjectVectorIndexPath(f.agentDir, p.ID), config.ProjectEmbeddingCachePath(f.agentDir, p.ID), nil
		}
	}
	for _, p := range projects {
		if p.IsNoProject {
			continue
		}
		name, nameErr := config.ManagedWorktreeNameFromPath(p.WorkspacePath, root)
		if nameErr != nil {
			continue
		}
		storage, storageErr := config.WorktreeVectorIndexPath(f.agentDir, p.ID, name)
		if storageErr != nil {
			return "", "", "", fmt.Errorf("deriving worktree vector storage for %s: %w", root, storageErr)
		}
		cache, cacheErr := config.WorktreeEmbeddingCachePath(f.agentDir, p.ID, name)
		if cacheErr != nil {
			return "", "", "", fmt.Errorf("deriving worktree embedding cache for %s: %w", root, cacheErr)
		}
		return p.ID, storage, cache, nil
	}
	return "", "", "", fmt.Errorf("%w: %s is not a registered project checkout or managed worktree", errVectorNoTarget, root)
}

// ManagerForRoot returns the live manager for root, creating and initializing
// it on first touch. Creation is single-flight per root under vr.mu — two
// concurrent cold searches on one root must never build two managers over the
// same chromem persistent storage. The heavy init (branch detect, collection
// open, background indexing) runs asynchronously inside the manager exactly
// as it did for the singleton; callers gate on Service.WaitReady.
func (vr *VectorRoots) ManagerForRoot(root string) (*vectorindex.Manager, error) {
	root = canonicalRoot(root)
	if root == "" {
		return nil, errVectorNoTarget
	}

	vr.mu.Lock()
	defer vr.mu.Unlock()
	if vr.shutdown {
		return nil, errors.New("vector search is shutting down")
	}
	if e, ok := vr.roots[root]; ok {
		e.lastUsed = time.Now()
		return e.mgr, nil
	}
	if vr.factory == nil {
		if vr.readySettledLocked() {
			return nil, errVectorUnavailable
		}
		return nil, errVectorStillLoading
	}

	projectID, storagePath, cachePath, err := vr.f.resolveRootPlan(root)
	if err != nil {
		return nil, err
	}

	mgr, err := vr.factory()
	if err != nil {
		return nil, fmt.Errorf("building vector index manager for %s: %w", root, err)
	}
	err = mgr.SwitchProject(projectID, root, storagePath, vr.rootCallbacks(root), cachePath)
	if err != nil {
		mgr.Shutdown()
		return nil, fmt.Errorf("initializing vector index for %s: %w", root, err)
	}
	vr.roots[root] = &vectorRootEntry{mgr: mgr, projectID: projectID, lastUsed: time.Now()}
	vr.evictLocked()
	return mgr, nil
}

// evictLocked enforces vectorRootsCapacity: shut down the least-recently-used
// non-focus managers beyond the cap. The focus manager is pinned. Shutdown is
// dispatched to a goroutine (it drains the manager's init/indexing goroutines
// under an internal bounded grace) so a search path never pays the drain;
// the goroutine's exit is that bounded Shutdown returning.
func (vr *VectorRoots) evictLocked() {
	overflow := len(vr.roots) - vectorRootsCapacity
	if overflow <= 0 {
		return
	}
	type victim struct {
		root string
		mgr  *vectorindex.Manager
		used time.Time
	}
	candidates := make([]victim, 0, len(vr.roots))
	for root, e := range vr.roots {
		if root == vr.focus {
			continue
		}
		candidates = append(candidates, victim{root: root, mgr: e.mgr, used: e.lastUsed})
	}
	for ; overflow > 0 && len(candidates) > 0; overflow-- {
		oldest := 0
		for i := range candidates {
			if candidates[i].used.Before(candidates[oldest].used) {
				oldest = i
			}
		}
		v := candidates[oldest]
		candidates = append(candidates[:oldest], candidates[oldest+1:]...)
		delete(vr.roots, v.root)
		vr.f.log().Debug("vector roots: evicting least-recently-used root manager", "root", v.root)
		go v.mgr.Shutdown()
	}
}

// rootCallbacks builds the SwitchProject status callbacks for root. Progress
// and failure reach the vector_index:status UI stream ONLY while root is the
// current focus (the stream is a single UI channel; a background tree's
// indexing must not masquerade as the visible one). Non-focus managers log
// the same facts instead.
func (vr *VectorRoots) rootCallbacks(root string) vectorindex.ProjectCallbacks {
	emitIfFocused := func(st VectorIndexStatus) {
		vr.mu.Lock()
		focused := vr.focus == root
		vr.mu.Unlock()
		if !focused {
			return
		}
		vr.f.applyEmbedderInfo(&st)
		vr.f.emitEvent(EventVectorIndexStatus, st)
	}
	return vectorindex.ProjectCallbacks{
		OnProgress: func(phase vectorindex.IndexPhase, state vectorindex.IndexState, indexed, total int, file string) {
			st := VectorIndexStatus{
				State:        string(state),
				Phase:        string(phase),
				Indices:      []string{"vector", "lexical"},
				Progress:     progressFraction(indexed, total),
				FilesIndexed: indexed,
				TotalFiles:   total,
				CurrentFile:  file,
			}
			emitIfFocused(st)
		},
		OnFailure: func(err error) {
			vr.f.log().Warn("vector index init failed; search unavailable for this root",
				"root", root, "error", err)
			emitIfFocused(VectorIndexStatus{State: string(vectorindex.IndexStateUnavailable), Indices: []string{}})
		},
	}
}

// ApplyFocus moves the Git-panel focus root and makes sure a manager exists
// for it, then emits the manager's current status snapshot once so the UI
// stream re-syncs to the newly visible tree. The outgoing focus manager is
// left running: a background session on that tree keeps searching its own
// manager, and killing its in-flight index build because the user looked
// away would degrade exactly the routing this registry exists to provide.
func (vr *VectorRoots) ApplyFocus(root string) error {
	root = canonicalRoot(root)
	if root == "" {
		return errVectorNoTarget
	}
	vr.mu.Lock()
	vr.focus = root
	vr.mu.Unlock()

	mgr, err := vr.ManagerForRoot(root)
	if err != nil {
		vr.f.log().Warn("vector roots: focus manager unavailable", "root", root, "error", err)
		return err
	}
	st := vr.f.vectorIndexStatusFromManager(mgr)
	vr.f.applyEmbedderInfo(&st)
	vr.f.emitEvent(EventVectorIndexStatus, st)
	return nil
}

// LeaveFocus clears the focus (No Project / CHAT mode: nothing is visible to
// search against). Live managers are untouched — their sessions keep running
// and searching their own trees.
func (vr *VectorRoots) LeaveFocus() {
	vr.mu.Lock()
	vr.focus = ""
	vr.mu.Unlock()
}

// FocusRoot returns the current focus root ("" when unset).
func (vr *VectorRoots) FocusRoot() string {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	return vr.focus
}

// FocusManager returns (creating if needed) the manager for the current
// focus root — the manager the user-facing RPCs operate on.
func (vr *VectorRoots) FocusManager() (*vectorindex.Manager, error) {
	root := vr.FocusRoot()
	if root == "" {
		return nil, errVectorNoTarget
	}
	return vr.ManagerForRoot(root)
}

// LiveManager returns the already-live manager for root WITHOUT creating
// one — the lookup-only path for notification and status probes. Nil when
// the root has no live manager (it will be built on the next routed search).
// A hit refreshes the entry's LRU recency.
func (vr *VectorRoots) LiveManager(root string) *vectorindex.Manager {
	root = canonicalRoot(root)
	if root == "" {
		return nil
	}
	vr.mu.Lock()
	defer vr.mu.Unlock()
	e, ok := vr.roots[root]
	if !ok {
		return nil
	}
	e.lastUsed = time.Now()
	return e.mgr
}

// liveRootsForTest reports how many managers are currently live. Test-only
// introspection over the registry's internal state.
func (vr *VectorRoots) liveRootsForTest() int {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	return len(vr.roots)
}

// ManagerForCleanup returns a manager usable for persisted-storage cleanup
// (DeleteProjectData is a plain fs + park-slot operation, so ANY manager can
// run it): the focus manager when one is live, any other live manager
// otherwise, or — when the factory is ready — a THROWAWAY manager the caller
// must Shutdown after use (disposable=true). Nil when nothing is available
// (still loading / embedder unavailable): cleanup then stays
// best-effort-skipped, matching the pre-registry behavior.
func (vr *VectorRoots) ManagerForCleanup() (mgr *vectorindex.Manager, disposable bool) {
	vr.mu.Lock()
	var live *vectorindex.Manager
	for _, e := range vr.roots {
		live = e.mgr
		break
	}
	factory := vr.factory
	settled := vr.readySettledLocked()
	vr.mu.Unlock()
	if live != nil {
		return live, false
	}
	if factory == nil || !settled {
		return nil, false
	}
	m, err := factory()
	if err != nil {
		vr.f.log().Debug("vector roots: cleanup manager build failed", "error", err)
		return nil, false
	}
	return m, true
}

// ManagerForContext is the agent-side routing primitive: it resolves the
// manager for the workspace root carried by the executor context (the
// session's own tree), falling back to the focus root when the context
// carries none. Every semantic_search call and RAG-hint injection flows
// through here.
func (vr *VectorRoots) ManagerForContext(ctx context.Context) (*vectorindex.Manager, error) {
	root := canonicalRoot(sdktools.WorkspacePathFrom(ctx))
	if root == "" {
		root = vr.FocusRoot()
	}
	if root == "" {
		return nil, errVectorNoTarget
	}
	return vr.ManagerForRoot(root)
}

// NotifyFileChangeForRoot triggers debounced incremental re-indexing on the
// manager of root ("" = focus root). Lookup-only: a root with no live manager
// is skipped — it will index from scratch when next routed to.
func (vr *VectorRoots) NotifyFileChangeForRoot(root string) {
	if root = canonicalRoot(root); root == "" {
		root = vr.FocusRoot()
	}
	if root == "" {
		return
	}
	vr.mu.Lock()
	e, ok := vr.roots[root]
	vr.mu.Unlock()
	if ok && e.mgr != nil {
		e.mgr.NotifyFileChange()
	}
}

// Release shuts down and forgets the manager for root, returning it so the
// caller can clean the root's persisted data (DeleteProjectData stays
// callable after Shutdown). Nil when no manager was live.
func (vr *VectorRoots) Release(root string) *vectorindex.Manager {
	root = canonicalRoot(root)
	vr.mu.Lock()
	e, ok := vr.roots[root]
	delete(vr.roots, root)
	if ok && vr.focus == root {
		vr.focus = ""
	}
	vr.mu.Unlock()
	if !ok {
		return nil
	}
	e.mgr.Shutdown()
	return e.mgr
}

// ReleaseProject shuts down and forgets every manager belonging to projectID,
// returning them for the caller's data cleanup.
func (vr *VectorRoots) ReleaseProject(projectID string) []*vectorindex.Manager {
	vr.mu.Lock()
	var released []*vectorindex.Manager
	for root, e := range vr.roots {
		if e.projectID != projectID {
			continue
		}
		released = append(released, e.mgr)
		delete(vr.roots, root)
		if vr.focus == root {
			vr.focus = ""
		}
	}
	vr.mu.Unlock()
	for _, mgr := range released {
		mgr.Shutdown()
	}
	return released
}

// ShutdownAll shuts down every live manager (bounded individually by
// Manager.Shutdown's internal grace) and then closes the process-global
// embedder once. Idempotent; called from FrontendAPILifecycle.Cleanup.
func (vr *VectorRoots) ShutdownAll() {
	if vr == nil {
		return
	}
	vr.mu.Lock()
	if vr.shutdown {
		vr.mu.Unlock()
		return
	}
	vr.shutdown = true
	managers := make([]*vectorindex.Manager, 0, len(vr.roots))
	for root, e := range vr.roots {
		managers = append(managers, e.mgr)
		delete(vr.roots, root)
	}
	closeEmbedder := vr.closeEmbedder
	vr.focus = ""
	vr.mu.Unlock()

	for _, mgr := range managers {
		mgr.Shutdown()
	}
	if closeEmbedder != nil {
		if err := closeEmbedder(); err != nil {
			vr.f.log().Warn("failed to close vector embedder", "error", err)
		}
	}
}

// worktreeStorageNames lists the per-tree storage roots under a project
// vector index's worktrees container. A read error (missing container — the
// project never indexed a managed tree) yields nil: nothing to enumerate.
func worktreeStorageNames(viRoot string) []string {
	entries, err := os.ReadDir(filepath.Join(viRoot, "worktrees"))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, filepath.Join(viRoot, "worktrees", e.Name()))
	}
	return out
}

// ---------------------------------------------------------------------------
// Router: the shared search closures (semantic_search tool + RAG hints)
// ---------------------------------------------------------------------------

// SetVectorRoots late-binds the per-root registry onto the Application. The
// registry lives on FrontendAPI (it emits UI events and resolves the Git-panel
// focus), which is constructed after the Application; the search closures
// registered on the orchestrator builder resolve it at call time and report
// the still-loading error until this wiring lands (microseconds later in
// startup, and before any session can run a tool).
func (app *Application) SetVectorRoots(vr *VectorRoots) {
	app.vectorRootsPtr.Store(vr)
}

// vectorRootsRegistry returns the late-bound registry (nil before wiring).
func (app *Application) vectorRootsRegistry() *VectorRoots {
	return app.vectorRootsPtr.Load()
}

// buildVectorRouter produces the VectorSearchFunc / VectorSearchWaitFunc pair
// routed per workspace root. It preserves the singleton-era dispatch contract
// exactly (the deleted desktop buildVectorCallbacks): waitFunc is THE bounded
// readiness waiter under one searchWaitTimeout deadline covering the embedder
// wait and the per-root Service.WaitReady; searchFunc never waits for index
// readiness (HybridSearchNoWait — an incremental pass starting between the
// caller's gate and the call fails fast with the actionable status instead of
// blocking); waitTimeout <= 0 is the explicit fail-fast sentinel.
func (app *Application) buildVectorRouter(waitTimeout time.Duration) (builtins.VectorSearchFunc, builtins.VectorSearchWaitFunc) {
	resolve := func(ctx context.Context) (*VectorRoots, *vectorindex.Manager, error) {
		vr := app.vectorRootsRegistry()
		if vr == nil {
			return nil, nil, errVectorStillLoading
		}
		mgr, err := vr.ManagerForContext(ctx)
		return vr, mgr, err
	}

	searchFunc := builtins.VectorSearchFunc(func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		// The timeout wrap bounds the query execution only (embedding the
		// query, scanning and fusing) — defense-in-depth, not a readiness
		// wait. Readiness is the caller's bounded step (waitFunc for the
		// tool, the shared-deadline pre-wait for RAG hints), and the search
		// below never waits for it.
		if waitTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, waitTimeout)
			defer cancel()
		}
		vr, mgr, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		if err := vr.awaitReady(ctx, waitTimeout > 0); err != nil {
			return nil, err
		}
		// Never enter a blocking readiness wait here: an incremental pass
		// starting between the caller's readiness gate and this call must
		// fail fast with the actionable status (progress, current file),
		// not block until the pass finishes.
		results, err := mgr.Service().HybridSearchNoWait(ctx, vectorindex.SearchOptions{
			Query:       opts.Query,
			TopK:        opts.TopK,
			Mode:        vectorindex.ParseMode(opts.Mode),
			FilePattern: opts.FilePattern,
			MustMatch:   opts.MustMatch,
		})
		if err != nil {
			if errors.Is(err, vectorindex.ErrNotReady) {
				return nil, mgr.NotReadyError()
			}
			return nil, err
		}
		out := make([]builtins.VectorSearchResult, len(results))
		for i, r := range results {
			out[i] = builtins.VectorSearchResult{
				FilePath:    r.FilePath,
				FileName:    r.FileName,
				Content:     r.Content,
				Score:       r.Score,
				StartLine:   r.StartLine,
				EndLine:     r.EndLine,
				Language:    r.Language,
				VectorRank:  r.VectorRank,
				LexicalRank: r.LexicalRank,
			}
		}
		return out, nil
	})

	waitFunc := builtins.VectorSearchWaitFunc(func(ctx context.Context) error {
		if waitTimeout <= 0 {
			// Fail-fast: non-blocking readiness checks only, zero waiting.
			vr, mgr, err := resolve(ctx)
			if err != nil {
				return err
			}
			if err := vr.awaitReady(ctx, false); err != nil {
				return err
			}
			if !mgr.Service().IsReady() {
				return mgr.NotReadyError()
			}
			return nil
		}
		// One deadline covers both stages: waiting for the embedder
		// (the background init) and waiting for per-root index readiness
		// (Service.WaitReady).
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		vr, mgr, err := resolve(ctx)
		if err != nil {
			return err
		}
		if err := vr.awaitReady(ctx, true); err != nil {
			return err
		}
		if err := mgr.Service().WaitReady(ctx); err != nil {
			return mgr.NotReadyError()
		}
		return nil
	})

	return searchFunc, waitFunc
}
