package backend

// Session-lifecycle worktree integration tests (ADR-080 step: lifecycle
// ownership). Everything runs against the REAL git binary through the
// production primitives (core/workspace → worktrees.Owner → the RPC flows
// under test), mirroring the primitive tests in core/workspace.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"io"

	"github.com/google/uuid"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/c0wrk/internal/gittest"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/memory"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// wtHarness wires a real FrontendAPI + session manager over a real git
// repository registered as an external project. The project's registered
// root (symlink-resolved by the project manager) is used everywhere the
// lifecycle derives tree paths, matching production.
type wtHarness struct {
	api      *FrontendAPI
	store    *session.SQLiteSessionStore
	manager  *session.Manager
	project  *project.ProjectInfo
	repoRoot string
	closeDB  func()
	eventsMu sync.Mutex
	events   []session.Event
	agentDir string
	terminal *recordingTerminal
}

// wtLLM is a minimal agent.LLMCaller (same stand-in as the session package's
// finishLLM): the orchestrator is constructed but never executes tasks here.
type wtLLM struct{}

func (wtLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "unused"},
		StopReason: "end_turn",
	}, nil
}

// wtFactory builds a real orchestrator wired with a mock LLM (mirrors
// functionalOrchestratorFactory from the session package tests).
func wtFactory() session.OrchestratorFactory {
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		registry := sdktools.NewToolRegistry()
		cf := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) core.ContextManager {
			cw := memory.NewContextWindow(memory.ContextWindowConfig{
				SystemPrompt: systemPrompt,
				ModelMeta:    llm.ModelMetadata{ContextWindow: 128000, OutputLimit: 4096},
			})
			return core.NewCoreContextManager(cw)
		}
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{
			LLM:            wtLLM{},
			ToolExec:       registry,
			ToolRegistry:   registry,
			TokenCounter:   llm.NewSimpleTokenCounter(),
			ContextFactory: cf,
			Emitter:        emitter,
		}), nil
	}
}

// wtFailingFactory mirrors wtFactory but always fails, driving the creation
// compensation path.
func wtFailingFactory() session.OrchestratorFactory {
	return func(core.Emitter, *slog.Logger, string, core.BlackboardFactory, io.Writer, *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, errors.New("orchestrator construction failed")
	}
}

// recordingTerminal is a TerminalManager fake that records Stop calls; the
// onStop hook lets a test capture the world as seen at Stop time.
type recordingTerminal struct {
	mu      sync.Mutex
	active  map[string]bool
	stopped []string
	onStop  func(sessionID string)
}

func newRecordingTerminal() *recordingTerminal {
	return &recordingTerminal{active: map[string]bool{}}
}

func (r *recordingTerminal) Start(sessionID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[sessionID] = true
	return nil
}

func (r *recordingTerminal) Write(string, []byte) error    { return nil }
func (r *recordingTerminal) Resize(string, int, int) error { return nil }
func (r *recordingTerminal) StopAll()                      {}

func (r *recordingTerminal) Stop(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, sessionID)
	delete(r.active, sessionID)
	if r.onStop != nil {
		r.onStop(sessionID)
	}
	return nil
}

func (r *recordingTerminal) IsActive(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[sessionID]
}

func (r *recordingTerminal) stopCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stopped)
}

// newWTHarness builds the full stack: SQLite stores, project registered at a
// fresh git repo, session manager + FrontendAPI with the workspace ensurer
// installed (the same wiring NewFrontendAPI performs).
func newWTHarness(t *testing.T, factory session.OrchestratorFactory) *wtHarness {
	t.Helper()
	gittest.RequireGit(t)

	h := &wtHarness{terminal: newRecordingTerminal(), agentDir: t.TempDir()}

	dbConn := openProjectSwitchTestDB(t)
	h.closeDB = func() { _ = dbConn.Close() }
	t.Cleanup(h.closeDB)

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	h.store, err = session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	reviewStore, err := review.NewSQLiteReviewStore(dbConn)
	if err != nil {
		t.Fatalf("review store: %v", err)
	}

	repo := gittest.InitRepo(t, filepath.Join(t.TempDir(), "repo"), "seed\n")
	h.repoRoot = repo.Root

	projectManager := project.NewManager(projectStore, h.agentDir, nil)
	// Register the repo as an EXTERNAL project so the registered root IS the
	// repository root (the project manager resolves symlinks; every derived
	// path below uses proj.WorkspacePath so the resolved form is canonical).
	proj, err := projectManager.CreateProject("Worktree Lifecycle", repo.Root)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	h.project = proj

	h.manager = session.NewManager(factory, h.captureEvent, h.agentDir)
	h.manager.SetLogger(slog.New(slog.DiscardHandler))
	h.manager.SetSessionStore(h.store)
	h.manager.SetProjectResolver(func(string) (string, error) { return proj.WorkspacePath, nil })

	h.api = &FrontendAPI{
		app:             &Application{manager: h.manager},
		logger:          slog.New(slog.DiscardHandler),
		store:           h.store,
		reviewStore:     reviewStore,
		projectManager:  projectManager,
		agentDir:        h.agentDir,
		terminalManager: h.terminal,
	}
	h.api.installWorkspaceEnsurer()
	h.api.activeProjectMu.Lock()
	h.api.activeProjectID = proj.ID
	h.api.activeProjectPath = proj.WorkspacePath
	h.api.activeProjectMu.Unlock()
	return h
}

func (h *wtHarness) captureEvent(e session.Event) {
	h.eventsMu.Lock()
	defer h.eventsMu.Unlock()
	h.events = append(h.events, e)
}

// restart simulates an app restart: a fresh manager + API over the same
// store, project, and repository, with their own event capture.
func (h *wtHarness) restart(t *testing.T) (*FrontendAPI, *session.Manager, *wtRestartCapture) {
	t.Helper()
	capture := &wtRestartCapture{}
	mgr := session.NewManager(wtFactory(), capture.capture, h.agentDir)
	mgr.SetLogger(slog.New(slog.DiscardHandler))
	mgr.SetSessionStore(h.store)
	mgr.SetProjectResolver(func(string) (string, error) { return h.project.WorkspacePath, nil })
	api := &FrontendAPI{
		app:             &Application{manager: mgr},
		logger:          slog.New(slog.DiscardHandler),
		store:           h.store,
		projectManager:  h.api.projectManager,
		agentDir:        h.agentDir,
		terminalManager: h.terminal,
	}
	api.installWorkspaceEnsurer()
	api.activeProjectMu.Lock()
	api.activeProjectID = h.project.ID
	api.activeProjectPath = h.project.WorkspacePath
	api.activeProjectMu.Unlock()
	return api, mgr, capture
}

type wtRestartCapture struct {
	mu     sync.Mutex
	events []session.Event
}

func (c *wtRestartCapture) capture(e session.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *wtRestartCapture) serviceEvents(phase string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		if e.Type != "service" {
			continue
		}
		if data, ok := e.Data.(map[string]any); ok {
			if p, _ := data["phase"].(string); p == phase {
				content, _ := data["content"].(string)
				out = append(out, content)
			}
		}
	}
	return out
}

// wtGit runs a raw git command in dir (test setup/assertions only).
func wtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (dir %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// wtTryGit runs git and reports success instead of failing the test.
func wtTryGit(dir string, args ...string) bool {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// branchExists reports whether ref resolves in repoRoot.
func branchExists(t *testing.T, repoRoot, branch string) bool {
	t.Helper()
	return wtTryGit(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
}

// treeEntry returns the worktree entry at path, or nil.
func treeEntry(t *testing.T, repoRoot, path string) *workspace.WorktreeInfo {
	t.Helper()
	trees, err := workspace.ListWorktrees(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	return workspace.FindWorktree(trees, path)
}

// managedBindingOf loads the session's persisted binding.
func managedBindingOf(t *testing.T, store *session.SQLiteSessionStore, id string) *session.WorkspaceBinding {
	t.Helper()
	info, err := store.LoadSession(context.Background(), id)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if info == nil {
		t.Fatalf("session %s not found", id)
	}
	return info.WorkspaceBinding
}

// ---------------------------------------------------------------------------
// Creation: tree before orchestrator, persisted binding, compensation
// ---------------------------------------------------------------------------

func TestCreateManagedSession_ProvisionsTreePersistsBinding(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}

	binding := info.WorkspaceBinding
	if binding == nil || binding.Kind != session.WorkspaceManagedWorktree {
		t.Fatalf("expected managed binding, got %+v", binding)
	}
	if binding.WorktreeName != managedTreeName(info.ID) {
		t.Fatalf("tree name %q != derived %q", binding.WorktreeName, managedTreeName(info.ID))
	}
	if binding.Branch != "session-"+shortIdentity(info.ID) {
		t.Fatalf("branch %q not the derived default", binding.Branch)
	}

	// Tree exists on the pinned branch.
	entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath)
	if entry == nil || entry.Kind != workspace.WorktreeManaged || entry.Branch != binding.Branch {
		t.Fatalf("managed tree missing or wrong identity: %+v", entry)
	}
	// The runtime session executes in the tree.
	wp, ok := h.manager.GetSessionWorkspacePath(info.ID)
	if !ok || wp != binding.WorkspacePath {
		t.Fatalf("runtime workspace %q (ok=%v) != tree path %q", wp, ok, binding.WorkspacePath)
	}
	// The binding is persisted (restart-visible), not just in memory.
	persisted := managedBindingOf(t, h.store, info.ID)
	if persisted == nil || persisted.Kind != session.WorkspaceManagedWorktree || persisted.Branch != binding.Branch {
		t.Fatalf("persisted binding mismatch: %+v", persisted)
	}
}

func TestCreateManagedSession_ExistingBranchCheckout(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	wtGit(t, h.repoRoot, "branch", "feature/topic")
	info, err := h.api.CreateManagedSession("feature/topic", false, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	if info.WorkspaceBinding.Branch != "feature/topic" {
		t.Fatalf("branch = %q", info.WorkspaceBinding.Branch)
	}
	entry := treeEntry(t, h.project.WorkspacePath, info.WorkspaceBinding.WorkspacePath)
	if entry == nil || entry.Branch != "feature/topic" {
		t.Fatalf("tree not on feature/topic: %+v", entry)
	}
}

func TestCreateManagedSession_RefusesNonRepoBranchReuse(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	// main is checked out by the main worktree → branch-busy refusal before
	// any session state is created.
	_, err := h.api.CreateManagedSession("main", false, "")
	if err == nil {
		t.Fatal("expected branch-busy failure")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Fatalf("error should name the branch: %v", err)
	}
	if got := len(h.manager.ListSessions()); got != 0 {
		t.Fatalf("no session may exist after refusal, got %d", got)
	}
}

func TestCreateManagedSession_RollbackWhenOrchestratorFails(t *testing.T) {
	h := newWTHarness(t, wtFailingFactory())
	_, err := h.api.CreateManagedSession("", true, "")
	if err == nil {
		t.Fatal("expected orchestrator failure to surface")
	}
	if got := len(h.manager.ListSessions()); got != 0 {
		t.Fatalf("no session may remain in memory, got %d", got)
	}
	// Compensation: the provisioned tree is gone; the fresh branch is KEPT
	// (no operation in this path deletes a branch).
	trees, err := workspace.ListWorktrees(context.Background(), h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			t.Fatalf("managed tree %q survived rollback", tr.Path)
		}
	}
}

func TestCreateManagedSession_RollbackWhenBindingPersistFails(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	// Break the DB precondition for persisting the binding: the projects row
	// disappears, so SaveSession's binding validation fails while the runtime
	// session (built from the resolver func) succeeded.
	if err := h.api.projectManager.DeleteProject(h.project.ID); err != nil {
		t.Fatalf("delete project row: %v", err)
	}
	_, err := h.api.CreateManagedSession("", true, "")
	if err == nil {
		t.Fatal("expected persistence failure to surface")
	}
	if got := len(h.manager.ListSessions()); got != 0 {
		t.Fatalf("unpersisted session must be removed, got %d", got)
	}
	trees, err := workspace.ListWorktrees(context.Background(), h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			t.Fatalf("managed tree %q survived rollback", tr.Path)
		}
	}
}

// ---------------------------------------------------------------------------
// Restart / restore: no-op, recreate-from-branch, missing branch
// ---------------------------------------------------------------------------

func TestManagedSessionRestart_TreePresentRestoreIsNoOp(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}

	api2, mgr2, capture := h.restart(t)
	wp, ok := mgr2.GetSessionWorkspacePath(info.ID)
	if !ok || wp != info.WorkspaceBinding.WorkspacePath {
		t.Fatalf("restored workspace %q (ok=%v)", wp, ok)
	}
	if warnings := capture.serviceEvents("orchestration"); len(warnings) != 0 {
		t.Fatalf("healthy tree must not warn, got %v", warnings)
	}
	if _, err := api2.store.LoadSession(context.Background(), info.ID); err != nil {
		t.Fatalf("row must survive restart: %v", err)
	}
	_ = mgr2 // api2 retained for symmetry with other restart tests
}

func TestManagedSessionRestart_MissingTreeRecreatedFromPinnedBranch(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding

	// Uncommitted work lives only in the tree; then the tree directory is
	// lost (disk loss / manual removal).
	uncommitted := filepath.Join(binding.WorkspacePath, "uncommitted.txt")
	if err := os.WriteFile(uncommitted, []byte("only in tree"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(binding.WorkspacePath); err != nil {
		t.Fatal(err)
	}

	_, mgr2, capture := h.restart(t)
	wp, ok := mgr2.GetSessionWorkspacePath(info.ID)
	if !ok || wp != binding.WorkspacePath {
		t.Fatalf("restored workspace %q (ok=%v) != tree %q", wp, ok, binding.WorkspacePath)
	}
	// The tree is back on the pinned branch…
	entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath)
	if entry == nil || entry.Branch != binding.Branch {
		t.Fatalf("tree not recreated on %s: %+v", binding.Branch, entry)
	}
	// …without the uncommitted data (it cannot be recovered)…
	if _, err := os.Stat(uncommitted); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted file must be gone, stat err = %v", err)
	}
	// …and the user was warned explicitly.
	warnings := capture.serviceEvents("orchestration")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "could not be recovered") {
		t.Fatalf("expected exactly one data-loss warning, got %v", warnings)
	}
}

func TestManagedSessionRestart_MissingBranchFailsExplicitlyNeverFallsBackToLocal(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding

	// Remove tree AND branch: restore must refuse to invent either.
	if err := os.RemoveAll(binding.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	wtGit(t, h.repoRoot, "worktree", "prune")
	wtGit(t, h.repoRoot, "branch", "-D", binding.Branch)

	_, mgr2, capture := h.restart(t)
	if sess, ok := mgr2.GetSession(info.ID); ok {
		t.Fatalf("session must not restore when the branch is gone, got %+v", sess)
	}
	if wp, ok := mgr2.WorkspacePathFor(context.Background(), info.ID); ok && wp == h.project.WorkspacePath {
		t.Fatal("restore must never fall back to the project checkout")
	}
	if warnings := capture.serviceEvents("orchestration"); len(warnings) != 0 {
		t.Fatalf("failure must not emit a recreation warning, got %v", warnings)
	}
}

// ---------------------------------------------------------------------------
// Deletion: dirty confirmation, terminal stop ordering, branch survival
// ---------------------------------------------------------------------------

func TestDeleteManagedSession_DirtyTreeRequiresExplicitConfirmation(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding
	if err := os.WriteFile(filepath.Join(binding.WorkspacePath, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unconfirmed deletion is refused; the session stays fully retryable.
	err = h.api.DeleteSession(info.ID)
	var blocked *SessionDeleteBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected SessionDeleteBlockedError, got %v", err)
	}
	if blocked.Option != "confirm_uncommitted_loss" || !strings.Contains(blocked.Reason, "uncommitted") {
		t.Fatalf("blocked error content: %+v", blocked)
	}
	if row, lerr := h.store.LoadSession(context.Background(), info.ID); lerr != nil || row == nil {
		t.Fatalf("session row must survive a blocked deletion (err=%v row=%v)", lerr, row)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry == nil {
		t.Fatal("tree must survive a blocked deletion")
	}
	if _, statErr := os.Stat(filepath.Join(binding.WorkspacePath, "dirty.txt")); statErr != nil {
		t.Fatal("uncommitted data must survive a blocked deletion")
	}

	// Confirmed deletion removes tree + rows, never the branch.
	if err := h.api.DeleteSessionWithOptions(info.ID, SessionDeleteOptions{ConfirmUncommittedLoss: true}); err != nil {
		t.Fatalf("confirmed deletion: %v", err)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry != nil {
		t.Fatalf("tree must be removed, still listed: %+v", entry)
	}
	if row, _ := h.store.LoadSession(context.Background(), info.ID); row != nil {
		t.Fatal("session row must be deleted")
	}
	if !branchExists(t, h.repoRoot, binding.Branch) {
		t.Fatal("branch must never be deleted by session deletion")
	}
}

func TestDeleteManagedSession_StopsTerminalBeforeTreeRemoval(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding

	if err := h.terminal.Start(info.ID, binding.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	var treePresentAtStop bool
	h.terminal.mu.Lock()
	h.terminal.onStop = func(string) {
		_, statErr := os.Stat(binding.WorkspacePath)
		treePresentAtStop = statErr == nil
	}
	h.terminal.mu.Unlock()

	if err := h.api.DeleteSession(info.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if h.terminal.stopCount() != 1 {
		t.Fatalf("terminal Stop must run exactly once, got %d", h.terminal.stopCount())
	}
	if !treePresentAtStop {
		t.Fatal("terminal must be stopped BEFORE the tree is removed")
	}
}

func TestDeleteManagedSession_StoreOnlySessionReleasesTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding

	// Fresh app instance: the session was never restored this run.
	api2, _, _ := h.restart(t)
	if err := api2.DeleteSession(info.ID); err != nil {
		t.Fatalf("store-only deletion: %v", err)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry != nil {
		t.Fatalf("tree must be released, still listed: %+v", entry)
	}
	if row, _ := h.store.LoadSession(context.Background(), info.ID); row != nil {
		t.Fatal("row must be deleted")
	}
	if !branchExists(t, h.repoRoot, binding.Branch) {
		t.Fatal("branch must never be deleted")
	}
}

func TestDeleteLocalSession_NeverTouchesCheckout(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := h.api.DeleteSession(info.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	// The project checkout is untouched.
	if _, err := os.Stat(filepath.Join(h.repoRoot, "file.txt")); err != nil {
		t.Fatalf("checkout file must survive: %v", err)
	}
	wtGit(t, h.repoRoot, "status", "--porcelain") // repo must still be a working repo
}

// ---------------------------------------------------------------------------
// Fork: own tree, committed HEAD only, guard preserved, rollback
// ---------------------------------------------------------------------------

func TestForkManagedSession_NewTreeAtCommittedHead(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	src := info.WorkspaceBinding

	// Committed work after creation: a second commit on the session branch.
	commitFile := filepath.Join(src.WorkspacePath, "committed.txt")
	if err := os.WriteFile(commitFile, []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, src.WorkspacePath, "add", "committed.txt")
	wtGit(t, src.WorkspacePath, "commit", "-m", "session work")

	// Uncommitted work that must NOT be claimed by the fork.
	if err := os.WriteFile(filepath.Join(src.WorkspacePath, "uncommitted.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHead := wtGit(t, src.WorkspacePath, "rev-parse", "HEAD")

	fork, err := h.api.ForkSession(info.ID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	fb := fork.WorkspaceBinding
	if fb == nil || fb.Kind != session.WorkspaceManagedWorktree {
		t.Fatalf("fork binding: %+v", fb)
	}
	wantBranch := src.Branch + "-fork-" + shortIdentity(fork.ID)
	if fb.Branch != wantBranch {
		t.Fatalf("fork branch %q != %q", fb.Branch, wantBranch)
	}
	if fb.WorktreeName == src.WorktreeName || fb.WorkspacePath == src.WorkspacePath {
		t.Fatal("fork must own a NEW tree")
	}

	// The fork tree exists, on the derived branch, at the source's committed
	// HEAD — and does not contain the source's uncommitted file.
	entry := treeEntry(t, h.project.WorkspacePath, fb.WorkspacePath)
	if entry == nil || entry.Branch != wantBranch {
		t.Fatalf("fork tree missing or wrong branch: %+v", entry)
	}
	if got := wtGit(t, fb.WorkspacePath, "rev-parse", "HEAD"); got != sourceHead {
		t.Fatalf("fork HEAD %s != source committed HEAD %s", got, sourceHead)
	}
	if _, err := os.Stat(filepath.Join(fb.WorkspacePath, "uncommitted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fork must not claim uncommitted source changes")
	}
	if _, err := os.Stat(filepath.Join(fb.WorkspacePath, "committed.txt")); err != nil {
		t.Fatal("committed source work must be present in the fork")
	}
	// The source tree is left exactly as it was (still dirty).
	if _, err := os.Stat(filepath.Join(src.WorkspacePath, "uncommitted.txt")); err != nil {
		t.Fatal("source tree must be untouched")
	}
	// Persisted fork row carries the new binding.
	persisted := managedBindingOf(t, h.store, fork.ID)
	if persisted == nil || persisted.Branch != wantBranch {
		t.Fatalf("persisted fork binding: %+v", persisted)
	}
}

func TestForkManagedSession_UnfinishedTaskStillBlocked(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	if err := h.store.SaveTask(context.Background(), session.TaskRecord{
		ID: "task-run", SessionID: info.ID, OriginalRequest: "running",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.ForkSession(info.ID); err == nil || !strings.Contains(err.Error(), "unfinished task") {
		t.Fatalf("expected unfinished-task guard, got %v", err)
	}
	// No tree was provisioned for the refused fork.
	trees, err := workspace.ListWorktrees(context.Background(), h.project.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	managed := 0
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			managed++
		}
	}
	if managed != 1 {
		t.Fatalf("only the source tree may exist, got %d", managed)
	}
}

func TestForkManagedSession_RollbackWhenStoreForkFails(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	h.api.managedForkCommit = func(context.Context, string, string, *session.WorkspaceBinding, session.ForkReviewCloner) (*session.SessionInfo, error) {
		return nil, errors.New("store fork failed")
	}
	if _, err := h.api.ForkSession(info.ID); err == nil {
		t.Fatal("expected fork failure")
	}
	// The provisioned fork tree is compensated away; the source tree remains.
	trees, err := workspace.ListWorktrees(context.Background(), h.project.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	managed := 0
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			managed++
		}
	}
	if managed != 1 {
		t.Fatalf("only the source tree may remain after rollback, got %d", managed)
	}
}

// ---------------------------------------------------------------------------
// Archive: retains tree and branch
// ---------------------------------------------------------------------------

func TestArchiveManagedSession_RetainsTreeAndBranch(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	binding := info.WorkspaceBinding

	if err := h.api.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	row, err := h.store.LoadSession(context.Background(), info.ID)
	if err != nil || row == nil || !row.Archived {
		t.Fatalf("row must be archived (err=%v row=%v)", err, row)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry == nil {
		t.Fatal("archiving must retain the tree")
	}
	if !branchExists(t, h.repoRoot, binding.Branch) {
		t.Fatal("archiving must retain the branch")
	}

	// Unarchive round-trips and the tree still restores.
	if err := h.api.ArchiveSession(info.ID); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	row, _ = h.store.LoadSession(context.Background(), info.ID)
	if row == nil || row.Archived {
		t.Fatalf("row must be unarchived: %+v", row)
	}
}

// Guard against future renames: forkBranchName format is contractual
// (<branch>-fork-<short id>).
func TestForkBranchNameFormat(t *testing.T) {
	id := uuid.NewString()
	got := forkBranchName("feature/topic", id)
	want := "feature/topic-fork-" + shortIdentity(id)
	if got != want {
		t.Fatalf("forkBranchName = %q, want %q", got, want)
	}
}
