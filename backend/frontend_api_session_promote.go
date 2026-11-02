package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/internal/sysproc"
)

// gitInitTimeout bounds the fail-soft `git init` run in a promoted workspace.
// git init is a fast, local, hook-free operation; the ceiling exists only so a
// wedged filesystem cannot hang the promotion RPC.
const gitInitTimeout = 30 * time.Second

// PromoteSessionToProject transforms a CHAT (No Project) session into a CODE
// project with that single session in it: same id, full history/tasks/
// terminal state; on-disk storage moves into the project layout, metadata
// path references are rewritten in the same transaction that re-parents the
// session row, and the next lazy restore rebuilds the session as CODE.
// Settled-only guard; any failure after the project row is created is
// compensated — and a compensation that cannot prove the files came home
// never deletes anything, leaving the leftover project for manual recovery.
// Pointer swap and `git init` are best-effort.
// See specs/domains/session-lifecycle.md (§ Session Promotion).
func (f *FrontendAPI) PromoteSessionToProject(sessionID, projectName string) (*project.ProjectInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}

	sessionID = strings.TrimSpace(sessionID)
	projectName = strings.TrimSpace(projectName)
	if sessionID == "" {
		return nil, errors.New("session_id is required")
	}
	if projectName == "" {
		return nil, errors.New("project name is required")
	}

	ctx := context.Background()

	info, err := f.store.LoadSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("session %q not found", sessionID)
	}
	if info.ProjectID != project.NoProjectID {
		return nil, fmt.Errorf("session %q is not a CHAT (No Project) session", sessionID)
	}

	// Settled only — forking's exact rule: promoting mid-execution would
	// move the tree out from under a resumable run.
	unfinished, err := f.store.GetUnfinishedTask(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to check session tasks: %w", err)
	}
	if unfinished != nil {
		return nil, errors.New("cannot promote a session that has an unfinished task")
	}

	proj, err := f.projectManager.CreateProject(projectName, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create the project: %w", err)
	}

	// --- Compensation scope begins: every failure below restores the
	// pre-promotion state (files first, then the fresh project row). ---
	if err := f.runPromotionMoves(ctx, sessionID, proj); err != nil {
		return nil, err
	}
	// --- Compensation scope ends. ---

	f.savePromotionSessionPointers(ctx, sessionID, proj.ID)
	f.initPromotedWorkspaceGit(proj.WorkspacePath)
	f.emitEvent(EventProjectCreated, proj)
	// The session re-parented from No Project to the new project: both
	// session lists changed.
	f.emitSessionsChanged()

	f.log().Info("promoted CHAT session to project",
		"session_id", sessionID, "project_id", proj.ID, "project_name", projectName)
	return proj, nil
}

// runPromotionMoves performs the ordered mutation core of the promotion:
// terminal stop → in-memory evict → restore reservation → file moves → store
// re-parent. The terminal stops first because its PTY's cwd sits inside the
// renamed tree (the evict releases open log/dump handles for the same
// reason); the restore reservation parks lazy GetSession restores for the
// whole evict → commit window, so none can rebuild from the pre-commit store
// state or recreate the source directory the undo needs. Each failure
// compensates exactly what already happened (see compensateFailedPromotion).
func (f *FrontendAPI) runPromotionMoves(ctx context.Context, sessionID string, proj *project.ProjectInfo) error {
	if f.stopSessionTerminal(sessionID) {
		// The one deliberate exception to the terminal manager's silent
		// explicit-stop rule: without terminal_exited the frontend's xterm
		// would sit frozen on a dead PTY; the event arms its lazy
		// resurrection in the promoted workspace.
		f.emitEvent(fmt.Sprintf("session:%s:terminal_exited", sessionID), map[string]string{})
	}
	if err := f.app.Manager().EvictSession(sessionID); err != nil {
		f.compensateFailedPromotion(sessionID, proj, false)
		return fmt.Errorf("failed to quiesce the session: %w", err)
	}
	releaseRestores, err := f.app.Manager().ReserveRestores(sessionID)
	if err != nil {
		f.compensateFailedPromotion(sessionID, proj, false)
		return fmt.Errorf("failed to reserve the session: %w", err)
	}
	defer releaseRestores()
	if err := f.app.Manager().MoveSessionStorage(sessionID, project.NoProjectID, proj.ID); err != nil {
		f.compensateFailedPromotion(sessionID, proj, false)
		return fmt.Errorf("failed to move the session storage: %w", err)
	}
	if err := f.store.PromoteSessionToProject(ctx, sessionID, proj.ID, promotionPathRewrites(f.agentDir, sessionID, proj.ID)); err != nil {
		f.compensateFailedPromotion(sessionID, proj, true)
		return fmt.Errorf("failed to re-parent the session in the store: %w", err)
	}
	return nil
}

// compensateFailedPromotion undoes a failed promotion: reverse the file moves
// first, then delete the fresh project — but only once the tree is verifiably
// free of session files. The original CHAT state is the invariant to defend:
// when the undo fails or the session directory is still inside the project,
// nothing is deleted — the user may see a leftover project, never silently
// lose session files to os.RemoveAll.
func (f *FrontendAPI) compensateFailedPromotion(sessionID string, proj *project.ProjectInfo, movesCompleted bool) {
	if movesCompleted {
		if err := f.app.Manager().UndoMoveSessionStorage(sessionID, proj.ID); err != nil {
			f.log().Error("failed to undo the promotion file moves; keeping the promotion project and its directory for manual recovery",
				"session_id", sessionID, "project_id", proj.ID,
				"project_dir", config.ProjectDir(f.agentDir, proj.ID),
				"restore_destination", config.SessionDir(f.agentDir, project.NoProjectID, sessionID),
				"error", err)
			return
		}
	}
	// Defensive gate before any deletion: the project tree must not hold the
	// session directory. Covers the moves-not-completed path too, where a
	// failed internal rollback can strand the files inside the fresh project.
	dstSessionDir := config.SessionDir(f.agentDir, proj.ID, sessionID)
	if _, err := os.Lstat(dstSessionDir); err == nil {
		f.log().Error("the promotion project directory still contains the session directory; keeping everything for manual recovery",
			"session_id", sessionID, "project_id", proj.ID,
			"session_dir", dstSessionDir)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		f.log().Error("cannot inspect the promoted session directory; keeping the promotion project",
			"session_id", sessionID, "project_id", proj.ID,
			"session_dir", dstSessionDir, "error", err)
		return
	}
	if err := f.projectManager.DeleteProject(proj.ID); err != nil {
		f.log().Error("failed to delete the compensation project",
			"session_id", sessionID, "project_id", proj.ID, "error", err)
	}
}

// savePromotionSessionPointers updates the saved-session pointers so the
// project switch lands on the promoted session and CHAT's stale pointer
// cannot resurrect it. Best-effort: the next selection or switch repairs a
// missed write.
func (f *FrontendAPI) savePromotionSessionPointers(ctx context.Context, sessionID, dstProjectID string) {
	if f.projStore == nil {
		return
	}
	if err := f.projStore.SaveSavedSessionID(ctx, dstProjectID, sessionID); err != nil {
		f.log().Warn("failed to save the promoted project's session pointer",
			"session_id", sessionID, "project_id", dstProjectID, "error", err)
	}
	if err := f.projStore.SaveSavedSessionID(ctx, project.NoProjectID, ""); err != nil {
		f.log().Warn("failed to clear the No Project session pointer after promotion",
			"session_id", sessionID, "error", err)
	}
}

// initPromotedWorkspaceGit runs a fail-soft `git init` in the promoted
// workspace — no initial commit, an existing repository is left untouched.
// The spawn goes through sysproc.GitCmd (SECURITY.md rule 12: hardened env +
// neutralizing -c overrides, so a global init.templateDir or core.hooksPath
// cannot leak into the fresh repository). A failure only logs: a CODE
// project works without a repository.
func (f *FrontendAPI) initPromotedWorkspaceGit(workspacePath string) {
	if workspacePath == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(workspacePath, ".git")); err == nil {
		f.log().Debug("promoted workspace already contains a git repository; skipping init",
			"path", workspacePath)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitInitTimeout)
	defer cancel()
	cmd, err := sysproc.GitCmd(ctx, "init")
	if err != nil {
		f.log().Warn("failed to prepare the git init in the promoted workspace",
			"path", workspacePath, "error", err)
		return
	}
	cmd.Dir = workspacePath
	// git init never prompts, but the pin keeps even the spawn-time
	// environment uniform with the rest of the backend's git operations (a
	// background operation must never hang on an interactive prompt).
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.log().Warn("failed to initialize a git repository in the promoted workspace",
			"path", workspacePath, "error", err, "output", strings.TrimSpace(string(out)))
	}
}

// promotionPathRewrites derives the metadata path-rewrite pairs for a
// promotion from the same centralized path helpers the file moves use: the
// per-session directory pair and the more specific workspace pair (the store
// applies the longer prefix first).
func promotionPathRewrites(agentDir, sessionID, dstProjectID string) [][2]string {
	oldSessionDir := config.SessionDir(agentDir, project.NoProjectID, sessionID)
	newSessionDir := config.SessionDir(agentDir, dstProjectID, sessionID)
	oldWorkspace := filepath.Join(oldSessionDir, config.NoProjectWorkspaceSegment)
	newWorkspace := config.ProjectWorkspacePath(agentDir, dstProjectID)
	return [][2]string{
		{oldSessionDir, newSessionDir},
		{oldWorkspace, newWorkspace},
	}
}
