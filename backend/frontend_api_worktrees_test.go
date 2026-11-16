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
	return func(emitter core.Emitter, _ *slog.Logger, _ string, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
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
		projStore:       projectStore,
		agentDir:        h.agentDir,
		terminalManager: h.terminal,
	}
	h.api.seedPublished.Store(true)
	h.api.installWorkspaceEnsurer()
	h.api.activeProjectMu.Lock()
	h.api.activeProjectID = proj.ID
	h.api.activeProjectPath = proj.WorkspacePath
	h.api.activeProjectMu.Unlock()
	// Shut the manager down on cleanup: sessions hold open LLM dump file
	// handles (DEBUG log level), and an unclosed handle makes t.TempDir()'s
	// RemoveAll fail on Windows. Cleanups are LIFO, so this runs before the
	// DB is closed.
	t.Cleanup(h.manager.Shutdown)
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
	t.Cleanup(mgr.Shutdown)
	api := &FrontendAPI{
		app:             &Application{manager: mgr},
		logger:          slog.New(slog.DiscardHandler),
		store:           h.store,
		projectManager:  h.api.projectManager,
		agentDir:        h.agentDir,
		terminalManager: h.terminal,
	}
	api.seedPublished.Store(true)
	api.seedPublished.Store(true)
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
func commitRemoteTip(t *testing.T, repoRoot, branch string) string {
	t.Helper()
	startBranch := strings.TrimSpace(wtGit(t, repoRoot, "rev-parse", "--abbrev-ref", "HEAD"))
	wtGit(t, repoRoot, "checkout", "-q", "-b", "tmp-remote-src")
	if err := os.WriteFile(filepath.Join(repoRoot, "file.txt"), []byte("remote tip\n"), 0o644); err != nil {
		t.Fatalf("write remote-tip file: %v", err)
	}
	wtGit(t, repoRoot, "add", ".")
	wtGit(t, repoRoot, "commit", "-q", "-m", "remote tip")
	sha := wtGit(t, repoRoot, "rev-parse", "HEAD")
	wtGit(t, repoRoot, "update-ref", "refs/remotes/origin/"+branch, sha)
	wtGit(t, repoRoot, "checkout", "-q", startBranch)
	wtGit(t, repoRoot, "branch", "-D", "tmp-remote-src")
	return sha
}
func TestManagedSessionRestart_TreePresentRestoreIsNoOp(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	info := h.api.mustManagedSession(t, "", true, "")

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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
	binding := info.WorkspaceBinding
	if err := os.WriteFile(filepath.Join(binding.WorkspacePath, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Unconfirmed deletion is refused; the session stays fully retryable.
	err := h.api.DeleteSession(info.ID)
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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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

func TestDeleteManagedSession_SharedTreeSurvivesUntilLastOwner(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	wtGit(t, h.repoRoot, "branch", "feature/shared")
	first := h.api.mustManagedSession(t, "feature/shared", false, "")
	second := h.api.mustManagedSession(t, "feature/shared", false, "")
	sharedPath := second.WorkspaceBinding.WorkspacePath
	if first.WorkspaceBinding.WorkspacePath != sharedPath {
		t.Fatalf("fixture broken: sessions do not share the tree")
	}
	uncommitted := filepath.Join(sharedPath, "dirty.txt")
	if err := os.WriteFile(uncommitted, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Deleting ONE co-owner never touches the shared tree — no dirty
	// confirmation is even asked, because nothing is removed.
	if err := h.api.DeleteSession(first.ID); err != nil {
		t.Fatalf("deleting one co-owner must succeed without confirmations: %v", err)
	}
	if row, _ := h.store.LoadSession(context.Background(), first.ID); row != nil {
		t.Fatal("deleted co-owner's row must be gone")
	}
	if entry := treeEntry(t, h.project.WorkspacePath, sharedPath); entry == nil {
		t.Fatal("shared tree must survive a co-owner deletion")
	}
	if _, statErr := os.Stat(uncommitted); statErr != nil {
		t.Fatalf("uncommitted work must survive a co-owner deletion: %v", statErr)
	}
	if wp, ok := h.manager.GetSessionWorkspacePath(second.ID); !ok || wp != sharedPath {
		t.Fatalf("surviving co-owner workspace %q (ok=%v) != shared tree %q", wp, ok, sharedPath)
	}

	// The LAST owner deletion removes the tree — with the dirty confirmation,
	// because the uncommitted work is now truly at stake.
	err := h.api.DeleteSession(second.ID)
	var blocked *SessionDeleteBlockedError
	if !errors.As(err, &blocked) || blocked.Option != "confirm_uncommitted_loss" {
		t.Fatalf("last-owner dirty deletion must be blocked, got %v", err)
	}
	if err := h.api.DeleteSessionWithOptions(second.ID, SessionDeleteOptions{ConfirmUncommittedLoss: true}); err != nil {
		t.Fatalf("confirmed last-owner deletion: %v", err)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, sharedPath); entry != nil {
		t.Fatalf("tree must be removed with its last session, still listed: %+v", entry)
	}
	if !branchExists(t, h.repoRoot, "feature/shared") {
		t.Fatal("branch must never be deleted by session deletion")
	}
}

func TestDeleteManagedSession_ArchivedCoOwnerKeepsTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	wtGit(t, h.repoRoot, "branch", "feature/archived")
	first := h.api.mustManagedSession(t, "feature/archived", false, "")
	second := h.api.mustManagedSession(t, "feature/archived", false, "")
	sharedPath := second.WorkspaceBinding.WorkspacePath
	// Archive one co-owner: an archived session can be unarchived and runs in
	// its tree again, so its reference keeps the tree alive.
	if err := h.api.ArchiveSession(first.ID); err != nil {
		t.Fatalf("archive co-owner: %v", err)
	}
	if err := h.api.DeleteSession(second.ID); err != nil {
		t.Fatalf("deleting the live co-owner must succeed: %v", err)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, sharedPath); entry == nil {
		t.Fatal("archived co-owner must keep the tree alive")
	}
	// Deleting the archived last owner finally releases the (clean) tree.
	if err := h.api.DeleteSession(first.ID); err != nil {
		t.Fatalf("deleting the archived last owner: %v", err)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, sharedPath); entry != nil {
		t.Fatalf("tree must be removed with its last owner, still listed: %+v", entry)
	}
}

// TestDeleteManagedSession_ConcurrentLastCoOwnersStillReleaseTree pins the
// deletion↔deletion half of the adoption↔deletion critical section
// (managedTreeMu): when the last two co-owners of a tree are deleted
// simultaneously, each pre-flight counts the other's row — under the mutex
// the second protocol observes the first's own-row removal and releases, so
// the tree cannot survive as an orphan with zero bound rows.
func TestDeleteManagedSession_ConcurrentLastCoOwnersStillReleaseTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	ctx := context.Background()
	wtGit(t, h.repoRoot, "branch", "feature/co-race")
	first := h.api.mustManagedSession(t, "feature/co-race", false, "")
	second := h.api.mustManagedSession(t, "feature/co-race", false, "")
	sharedPath := second.WorkspaceBinding.WorkspacePath
	if first.WorkspaceBinding.WorkspacePath != sharedPath {
		t.Fatalf("fixture broken: sessions do not share the tree")
	}
	bindingOf := func(id string) *session.WorkspaceBinding {
		info, err := h.store.LoadSession(ctx, id)
		if err != nil || info == nil {
			t.Fatalf("load session %s: %v", id, err)
		}
		return info.WorkspaceBinding
	}

	// Replay the interleaving the mutex excludes: both deletions' protocols
	// run back-to-back under managedTreeMu, before either session's state is
	// removed by its own deletion flow.
	h.api.managedTreeMu.Lock()
	firstReleased, firstRowGoneProject, _, err := h.api.removeOwnerAndReleaseTreeLocked(ctx, h.project.ID, bindingOf(first.ID), first.ID, SessionDeleteOptions{})
	if err != nil {
		h.api.managedTreeMu.Unlock()
		t.Fatalf("first co-owner protocol: %v", err)
	}
	if firstReleased {
		h.api.managedTreeMu.Unlock()
		t.Fatal("first co-owner must not release the tree its peer still binds")
	}
	if firstRowGoneProject == "" {
		h.api.managedTreeMu.Unlock()
		t.Fatal("first co-owner's row must be removed by its own protocol")
	}
	secondReleased, _, _, err := h.api.removeOwnerAndReleaseTreeLocked(ctx, h.project.ID, bindingOf(second.ID), second.ID, SessionDeleteOptions{})
	h.api.managedTreeMu.Unlock()
	if err != nil {
		t.Fatalf("second co-owner protocol: %v", err)
	}
	if !secondReleased {
		t.Fatal("the last remaining owner's protocol must release the tree")
	}
	if entry := treeEntry(t, h.repoRoot, sharedPath); entry != nil {
		t.Fatalf("shared tree must not survive both co-owner deletions, still listed: %+v", entry)
	}
	if !branchExists(t, h.repoRoot, "feature/co-race") {
		t.Fatal("branch must survive (branches are never deleted)")
	}
	if row, _ := h.store.LoadSession(ctx, first.ID); row != nil {
		t.Fatal("first co-owner's row must be gone (removed by its protocol)")
	}
	// The second co-owner's row removal belongs to its own deletion flow —
	// finish it through the real RPC and assert full cleanup. The pre-flight
	// finds no tree anymore (already released) and still removes the row via
	// the flow's tail.
	if err := h.api.DeleteSession(second.ID); err != nil {
		t.Fatalf("finish second co-owner deletion: %v", err)
	}
	if row, _ := h.store.LoadSession(ctx, second.ID); row != nil {
		t.Fatal("second co-owner's row must be gone after its deletion flow")
	}
	// The tree is adoptable again, not bricked: a fresh session on the same
	// branch provisions (no holder) and works.
	_ = h.api.mustManagedSession(t, "feature/co-race", false, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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
	info := h.api.mustManagedSession(t, "", true, "")
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
