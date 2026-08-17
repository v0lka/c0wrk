package session

import (
	"context"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// JudgeContext enriches a context with the session's security context for an
// on-demand judge evaluation ("Ask Agent" on a pending confirmation). It
// mirrors the context pieces of the task-context construction in SendMessage:
// session workspace path (+ case-folding flag), session temp directory,
// EnvInfo, and the auxiliary work directories as allowed roots (including the
// implicit host temp roots — see injectWorkDirectories). Without this
// enrichment the judge LLM cannot know the session's directory scope and
// treats operations inside legitimate additional work directories
// (user-configured or implicitly provided) as out-of-workspace.
//
// The passed ctx is returned unchanged when the session cannot be resolved —
// the judge is advisory, so a missing session degrades to the previous
// (scope-less) behaviour rather than failing the evaluation. Callers must not
// hold the manager or session locks (work directories are loaded from the
// persistent stores).
func (m *Manager) JudgeContext(ctx context.Context, sessionID string) context.Context {
	session, ok := m.GetSession(sessionID)
	if !ok || session == nil {
		m.log().Warn("judge context: session not found", "session_id", sessionID)
		return ctx
	}

	session.mu.Lock()
	workspacePath := session.WorkspacePath
	tempDir := session.TempDir
	session.mu.Unlock()

	ctx = sdktools.WithWorkspacePathNoProbe(ctx, workspacePath)
	ctx = sdktools.WithCaseInsensitivePaths(ctx, m.detectCaseInsensitive(workspacePath))
	ctx = sdktools.WithTempDir(ctx, tempDir)

	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		ctx = sdktools.WithEnvInfo(ctx, envInfo)
	}

	// Same allowed roots as a live task: configured work directories plus the
	// implicit host temp roots, loaded fresh so mid-session changes apply.
	dirs := m.loadWorkDirectories(session)
	return m.injectWorkDirectories(ctx, dirs)
}
