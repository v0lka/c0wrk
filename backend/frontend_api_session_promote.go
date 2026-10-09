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
// project with that single session in it. The session keeps its identity —
// same id, full message history, tasks with their steps/facts/trajectory,
// terminal history, work directories, and review — while its on-disk state
// moves into the new project's layout:
//
//	projects/__no_project__/<sid>/            → projects/<new>/<sid>/
//	projects/__no_project__/<sid>/workspace/  → projects/<new>/Workspace/
//
// At the next lazy restore the session is rebuilt WITHOUT NoProjectMode
// (derived from project_id at orchestrator-build time) against the new
// workspace, so the CODE toolset and the vector index come alive with it.
// Structured path references inside persisted message metadata (image
// attachment paths) are rewritten to the new locations in the same
// transaction that re-parents the session row; historical message text keeps
// its original absolute paths.
//
// Guards: the session must belong to No Project and must be settled — no
// unfinished task in the store, no running task or in-flight compaction in
// memory (a HITL-pending session still has an in_progress task, so the store
// guard covers it). Any failure after the project row is created is
// compensated: the files move back and the fresh project is deleted, leaving
// the session exactly as it was in CHAT. When the file-level undo itself
// fails, nothing is deleted — a compensation that cannot prove the files
// came home must never reach for a recursive delete — so the fresh project
// row and its directory stay in place and the error names the paths for
// manual recovery.
//
// Best-effort tail: the saved-session pointers (the new project starts on the
// promoted session; No Project drops its stale pointer) and a fail-soft
// `git init` in the promoted workspace (skipped when a repository already
// exists; a failure logs a warning and never fails the promotion — the user
// can init manually later). The frontend switches to the returned project.
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

	// Guard: only a CHAT session can be promoted.
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

	// Guard: settled only — forking's exact rule. A paused/failed task is an
	// unfinished one here too; promoting mid-execution would move the tree
	// out from under a resumable run.
	unfinished, err := f.store.GetUnfinishedTask(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to check session tasks: %w", err)
	}
	if unfinished != nil {
		return nil, errors.New("cannot promote a session that has an unfinished task")
	}

	// The destination project: internal workspace under ~/.c0wrk/projects.
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

	// Best-effort tail — a failure here logs but never fails the promotion:
	// the session is already a citizen of the new project.
	f.savePromotionSessionPointers(ctx, sessionID, proj.ID)
	f.initPromotedWorkspaceGit(proj.WorkspacePath)
	f.emitEvent(EventProjectCreated, proj)

	f.log().Info("promoted CHAT session to project",
		"session_id", sessionID, "project_id", proj.ID, "project_name", projectName)
	return proj, nil
}

// runPromotionMoves performs the ordered mutation core of the promotion:
// terminal stop → in-memory evict → restore reservation → file moves → store
// re-parent. The session terminal is stopped first because its PTY's cwd sits
// inside the tree the moves rename (and the frontend is told via
// terminal_exited so it can lazily resurrect the shell in the new workspace);
// the evict releases open log/dump handles for the same reason. The restore
// reservation then parks lazy GetSession restores for the whole evict →
// commit window, so no restore can rebuild the session from the pre-commit
// store state or recreate the source directory the undo needs.
//
// Each failure compensates exactly what already happened: the inverse file
// move runs only when the forward move succeeded, while the fresh project is
// removed once the files are verifiably back home (a failed undo keeps
// everything in place for manual recovery — see compensateFailedPromotion).
func (f *FrontendAPI) runPromotionMoves(ctx context.Context, sessionID string, proj *project.ProjectInfo) error {
	if f.stopSessionTerminal(sessionID) {
		// Explicit stop at promotion is the one deliberate exception to the
		// terminal manager's silent explicit-stop rule: without the event
		// the frontend's xterm instance would sit frozen on a dead PTY with
		// no user-reachable restart. The event arms the existing lazy
		// resurrection — the shell re-spawns in the promoted workspace on
		// the session's next activation.
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

// compensateFailedPromotion undoes a failed promotion: when the file moves
// happened, they are reversed first (so the restored layout is valid while the
// project still exists), then the fresh project is deleted — but only once
// the tree is verifiably free of session files. The original CHAT state is
// the invariant to defend: when the undo fails, or the project directory
// still holds the moved session directory (a failed in-move rollback looks
// the same), nothing is deleted. The project row and its tree stay in place
// for manual recovery and an Error names the paths — the user may see a
// leftover project, never silently lose the session files to os.RemoveAll.
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
// frontend's project switch lands on the promoted session and CHAT's stale
// pointer cannot resurrect it in the old list. Best-effort by contract: the
// next selection or switch-away snapshot repairs any missed write.
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
// workspace (no initial commit — the content stays untracked, the first
// commit belongs to the user). A workspace that already carries a repository
// (the user ran git inside CHAT) is left untouched. The spawn goes through
// sysproc.GitCmd — SECURITY.md rule 12: the hardened environment and the
// neutralizing -c overrides (a user's global init.templateDir or
// core.hooksPath must not leak into the fresh repository) apply to every git
// invocation, and HideConsole keeps the GUI-subsystem app flash-free on
// Windows. A failure (including GitCmd refusing to spawn) logs a warning and
// continues: a CODE project works without a repository, and the git panel
// degrades gracefully until the user initializes one.
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
