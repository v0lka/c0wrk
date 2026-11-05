package session

import (
	"context"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// JudgeContext enriches a context with the session's security context for an
// on-demand judge evaluation ("Ask Agent" on a pending confirmation) — the
// same scope a live task gets (specs/architecture/security-model.md). An
// unresolvable session returns ctx unchanged: the judge is advisory, so a
// missing session degrades rather than failing. Callers must not hold the
// manager or session locks (work directories are loaded from the stores).
func (m *Manager) JudgeContext(ctx context.Context, sessionID string) context.Context {
	// Memory-only lookup (sessionByID, not the restoring GetSession): a
	// pending-confirmation session is live by construction, and lazily
	// restoring a just-deleted one would rebuild an orchestrator and reopen
	// log/dump handles nobody closes.
	session, ok := m.sessionByID(sessionID)
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

	dirs := m.loadWorkDirectories(session)
	return m.injectWorkDirectories(ctx, dirs)
}

// EmitJudgePhase reports a strict-judge evaluation phase: routed through the
// live emitter so the activity tracker and the runtime-status snapshot
// reflect the judge run, raw-pipeline fallback otherwise; both events are
// transient (specs/contracts/event-catalog.md, tool_judge_*). Best-effort.
func (m *Manager) EmitJudgePhase(sessionID string, started bool, toolName string) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		sess.mu.Lock()
		emitter := sess.emitter
		sess.mu.Unlock()
		if emitter != nil {
			if started {
				emitter.JudgeStarted(toolName)
			} else {
				emitter.JudgeFinished(toolName)
			}
			return
		}
	}

	eventType := "tool_judge_finished"
	if started {
		eventType = "tool_judge_started"
	}
	m.EmitSessionEvent(sessionID, eventType, map[string]any{"tool": toolName})
}
