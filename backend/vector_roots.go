// frontend_api_vector_roots.go — the per-root vector-index registry (ADR-081):
// one Manager per workspace root instead of the singleton-era re-pointing.
// Agent-side routing is context-borne, user-side routing follows the Git-panel
// focus. See specs/domains/workspace.md (session-workspace routing) and
// specs/decisions/081-vector-per-root-registry.md.
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

// vectorRootsCapacity bounds the live per-root managers; the focus root is
// pinned, least-recently-used others are shut down and reopen transparently
// from their persisted state on the next search.
const vectorRootsCapacity = 4

// vectorRootsEvictWait bounds the wait for an in-flight eviction's Shutdown
// drain; it sits above the manager's own two 10 s grace periods.
const vectorRootsEvictWait = 25 * time.Second

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
// Git-panel focus the user-facing RPCs follow. Manager creation is
// single-flight per root under mu — two managers must never open the same
// chromem persistent storage.
type VectorRoots struct {
	f *FrontendAPI

	mu    sync.Mutex
	roots map[string]*vectorRootEntry // key: canonical (filepath.Clean) root
	// closing tracks in-flight LRU evictions: root → channel closed when the
	// evicted manager's Shutdown has RETURNED. ManagerForRoot waits (bounded)
	// on it before building a fresh manager, so single-flight-per-root holds
	// across the eviction window — a reopen during the drain would otherwise
	// build a second manager over the same chromem storage.
	closing map[string]chan struct{}
	focus   string // canonical focus root; "" = none yet

	// factory builds a fresh manager wired to the process-global embedder; nil
	// until the desktop background ONNX init succeeds. Once ready is closed
	// with factory still nil, the embedder is known dead and ManagerForRoot
	// fails with errVectorUnavailable instead of waiting.
	factory func() (*vectorindex.Manager, error)
	ready   <-chan struct{}
	// closeEmbedder releases the process-global embedder exactly once, after
	// every manager is shut down (ShutdownAll); per-manager CloseFn stays nil
	// — an LRU eviction must never close the shared ONNX runtime.
	closeEmbedder func() error

	shutdown bool
}

func newVectorRoots(f *FrontendAPI) *VectorRoots {
	return &VectorRoots{
		f:       f,
		roots:   make(map[string]*vectorRootEntry),
		closing: make(map[string]chan struct{}),
	}
}

// SetFactory wires the manager factory from the desktop background ONNX init,
// with the readiness channel and the once-guarded embedder close. Called once
// from the desktop layer; if ShutdownAll already ran, the embedder is closed
// right here — nobody else would release it.
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

// FactoryReady reports whether the registry can build managers: the factory is
// set, or the background init has settled (creation then fails fast with
// errVectorUnavailable). The project-switch handshake decides between applying
// and deferring a switch's vector setup with this.
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

// canonicalRoot normalizes a workspace root into the registry key; Clean keeps
// trailing separators and dot segments from forking the map.
func canonicalRoot(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// awaitReady gates on the background ONNX init: block=true waits until the
// init settles or ctx expires (still-loading hint on expiry); block=false
// checks readiness without ever blocking (the fail-fast sentinel path).
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

// resolveRootPlan derives (projectID, storage, cache) for a workspace root:
// a registered project's checkout or a managed worktree of one — vector
// indexing is a CODE-project feature. ctx bounds the one store round-trip
// (the projects listing); everything after it stays time-bounded, not
// ctx-bounded.
func (f *FrontendAPI) resolveRootPlan(ctx context.Context, root string) (projectID, storagePath, cachePath string, err error) {
	f.seedAcquire()
	if f.projectManager == nil {
		return "", "", "", errors.New("no project manager wired")
	}
	readCtx, cancel := context.WithTimeout(ctx, sessionStoreOpTimeout)
	defer cancel()
	projects, err := f.projectManager.ListProjectsCtx(readCtx)
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
// it on first touch. Creation is single-flight per root under vr.mu (see
// VectorRoots.closing for the eviction window); the heavy init runs
// asynchronously inside the manager.
func (vr *VectorRoots) ManagerForRoot(ctx context.Context, root string) (*vectorindex.Manager, error) {
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
	if ch, evicting := vr.closing[root]; evicting {
		// An eviction of this root is still draining over the very storage a
		// fresh manager would open: wait (bounded) for the drain, then re-read
		// the map — this keeps creation single-flight across the window.
		vr.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(vectorRootsEvictWait):
			vr.mu.Lock()
			return nil, fmt.Errorf("%w: %s is still shutting down; retry shortly", errVectorUnavailable, root)
		}
		vr.mu.Lock()
		// Re-validate after reacquiring the lock: the wait may have raced a
		// concurrent creator, a fresh eviction, or ShutdownAll.
		if vr.shutdown {
			return nil, errors.New("vector search is shutting down")
		}
		if e, ok := vr.roots[root]; ok {
			e.lastUsed = time.Now()
			return e.mgr, nil
		}
		if _, evicting = vr.closing[root]; evicting {
			return nil, fmt.Errorf("%w: %s is still shutting down; retry shortly", errVectorUnavailable, root)
		}
	}
	if vr.factory == nil {
		if vr.readySettledLocked() {
			return nil, errVectorUnavailable
		}
		return nil, errVectorStillLoading
	}

	projectID, storagePath, cachePath, err := vr.f.resolveRootPlan(ctx, root)
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
// non-focus managers beyond the cap. Shutdown is dispatched to a goroutine so
// a search path never pays the drain; the goroutine's exit is that bounded
// Shutdown returning.
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
		// Park the root in the closing set until Shutdown has returned — see
		// VectorRoots.closing for why "gone from the map" and "safe to
		// rebuild" are one atomic transition for any waiter.
		closed := make(chan struct{})
		if vr.closing == nil {
			vr.closing = make(map[string]chan struct{})
		}
		vr.closing[v.root] = closed
		vr.f.log().Debug("vector roots: evicting least-recently-used root manager", "root", v.root)
		go func() {
			v.mgr.Shutdown()
			vr.mu.Lock()
			if ch, ok := vr.closing[v.root]; ok && ch == closed {
				delete(vr.closing, v.root)
			}
			close(closed)
			vr.mu.Unlock()
		}()
	}
}

// rootCallbacks builds the SwitchProject status callbacks for root: the
// vector_index:status stream is a single UI channel, so only the focus root
// emits — non-focus managers log the same facts instead.
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

// ApplyFocus moves the Git-panel focus root, ensures a manager exists for it,
// and emits its current status snapshot so the UI stream re-syncs. The
// outgoing focus manager stays running: a background session keeps searching
// its own tree.
func (vr *VectorRoots) ApplyFocus(ctx context.Context, root string) error {
	root = canonicalRoot(root)
	if root == "" {
		return errVectorNoTarget
	}
	vr.mu.Lock()
	vr.focus = root
	vr.mu.Unlock()

	mgr, err := vr.ManagerForRoot(ctx, root)
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

// FocusManager returns (creating if needed) the manager for the current focus
// root — the manager the user-facing RPCs operate on.
func (vr *VectorRoots) FocusManager(ctx context.Context) (*vectorindex.Manager, error) {
	root := vr.FocusRoot()
	if root == "" {
		return nil, errVectorNoTarget
	}
	return vr.ManagerForRoot(ctx, root)
}

// LiveManager returns the already-live manager for root WITHOUT creating one —
// the lookup-only path for notification and status probes. A hit refreshes the
// entry's LRU recency.
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
// (DeleteProjectData is a plain fs operation, so ANY manager can run it): the
// focus manager when live, any live manager otherwise, or a THROWAWAY one the
// caller must Shutdown (disposable=true). Nil when nothing is available.
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
// manager for the executor context's workspace root (the session's own tree),
// falling back to the focus root when the context carries none.
func (vr *VectorRoots) ManagerForContext(ctx context.Context) (*vectorindex.Manager, error) {
	root := canonicalRoot(sdktools.WorkspacePathFrom(ctx))
	if root == "" {
		root = vr.FocusRoot()
	}
	if root == "" {
		return nil, errVectorNoTarget
	}
	return vr.ManagerForRoot(ctx, root)
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
// caller can clean the root's persisted data. Nil when none was live.
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
	type closingEntry struct {
		root   string
		closed chan struct{}
	}
	var closing []closingEntry
	for root, e := range vr.roots {
		if e.projectID != projectID {
			continue
		}
		released = append(released, e.mgr)
		delete(vr.roots, root)
		if vr.focus == root {
			vr.focus = ""
		}
		// Park the root in the closing set exactly like the LRU eviction
		// above (see VectorRoots.closing): a search issued while this
		// Shutdown is still draining must not rebuild over the same chromem
		// storage.
		closed := make(chan struct{})
		if vr.closing == nil {
			vr.closing = make(map[string]chan struct{})
		}
		vr.closing[root] = closed
		closing = append(closing, closingEntry{root: root, closed: closed})
	}
	vr.mu.Unlock()
	for i, mgr := range released {
		mgr.Shutdown()
		e := closing[i]
		vr.mu.Lock()
		if ch, ok := vr.closing[e.root]; ok && ch == e.closed {
			delete(vr.closing, e.root)
		}
		close(e.closed)
		vr.mu.Unlock()
	}
	return released
}

// ShutdownAll shuts down every live manager and then closes the
// process-global embedder once. Idempotent; called from
// FrontendAPILifecycle.Cleanup.
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

// worktreeStorageNames lists the per-tree storage roots under a project vector
// index's worktrees container; a read error (missing container) yields nil.
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

// SetVectorRoots late-binds the per-root registry onto the Application; the
// search closures resolve it at call time and report the still-loading error
// until this wiring lands (microseconds later in startup, and before any
// session can run a tool).
func (app *Application) SetVectorRoots(vr *VectorRoots) {
	app.vectorRootsPtr.Store(vr)
}

// vectorRootsRegistry returns the late-bound registry (nil before wiring).
func (app *Application) vectorRootsRegistry() *VectorRoots {
	return app.vectorRootsPtr.Load()
}

// buildVectorRouter produces the per-root VectorSearchFunc /
// VectorSearchWaitFunc pair. waitFunc is THE bounded readiness waiter under
// one searchWaitTimeout deadline; searchFunc never waits for index readiness;
// waitTimeout <= 0 is the explicit fail-fast sentinel.
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
		// The timeout wrap bounds query execution only (embedding, scan,
		// fuse) — defense-in-depth, not a readiness wait; readiness is the
		// caller's bounded step (waitFunc for the tool, the shared-deadline
		// pre-wait for RAG hints).
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
		// Never a blocking readiness wait here: a pass starting between the
		// caller's gate and this call must fail fast, not block.
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
		// One deadline covers both stages: the embedder wait (background
		// init) and the per-root index readiness (Service.WaitReady).
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
