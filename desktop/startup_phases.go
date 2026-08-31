package desktop

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/updater"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/embedding"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// initLogger initializes the session logger with a temporary INFO level so any
// startup errors land on disk. Returns the active logger plus the underlying
// SessionLogger so the caller can re-init it later if config requests a
// different level. Errors are logged via slog.Default() but never block startup.
//
// Once the session logger is ready, the Wails log adapter is updated so that
// Wails-internal messages (including fatal RPC errors) also appear in the
// session log.
func (a *App) initLogger(logDir string) (*slog.Logger, *logger.SessionLogger) {
	sessionLogger, err := logger.Init("INFO", logDir)
	if err != nil {
		slog.Error("failed to initialize logger", "error", err)
	}
	var log *slog.Logger
	if sessionLogger != nil {
		log = sessionLogger.Logger()
		if a.wailsLogger != nil {
			a.wailsLogger.SetDelegate(log)
		}
	} else {
		log = slog.Default()
	}
	a.logger = log
	return log, sessionLogger
}

// maybeReinitLogger re-initializes the session logger if the configured level
// differs from the bootstrap "INFO". On failure, the original logger is kept.
// Returns the active logger and (possibly new) SessionLogger.
func (a *App) maybeReinitLogger(level string, sessionLogger *logger.SessionLogger, current *slog.Logger, logDir string) (*slog.Logger, *logger.SessionLogger) {
	if level == "" || level == "INFO" {
		return current, sessionLogger
	}
	newLogger, err := logger.Init(level, logDir)
	if err != nil {
		return current, sessionLogger
	}
	if sessionLogger != nil {
		if cerr := sessionLogger.Close(); cerr != nil {
			current.Error("failed to close session logger", "error", cerr)
		}
	}
	log := newLogger.Logger()
	a.logger = log
	a.sessionLogger = newLogger
	if a.wailsLogger != nil {
		a.wailsLogger.SetDelegate(log)
	}
	return log, newLogger
}

// safeGo runs fn in a goroutine that recovers from any panic, logging it
// instead of letting it propagate and crash the app. It is used for
// startup-phase goroutines (config, tools, database, terminal manager) so that
// a failure in one phase degrades gracefully rather than terminating the
// process. The caller owns WaitGroup accounting: defer wg.Done() inside fn so
// the waitgroup is released even on panic — fn's defers unwind before the
// recovered panic reaches this recover.
func safeGo(log *slog.Logger, label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic during startup", "phase", label, "panic", r)
			}
		}()
		fn()
	}()
}

// initConfigAndDeps loads the config file and reconciles managed tools
// (rg, uv, markitdown) — all in parallel. Tool work never aborts startup:
// the offline pass does local work only and anything missing is completed in
// the background (see initTools). Only a panic during config resolution
// (toolsOK=false, resolved nil) aborts startup.
//
// Returns the resolved config, the tools/bin/ directory path (always
// non-empty), whether any tools were installed during the synchronous pass,
// and toolsOK=false only when config resolution panicked.
// The caller must prepend toolsBinPath to PATH before subsequent phases.
func (a *App) initConfigAndDeps(ctx context.Context, log *slog.Logger) (resolved *config.ResolvedConfig, toolsBinPath string, toolsInstalled, toolsOK bool) {
	var wg sync.WaitGroup
	wg.Add(2)
	safeGo(log, "config", func() {
		defer wg.Done()
		resolved = config.ResolveAndLoad(log)
	})
	safeGo(log, "tools", func() {
		defer wg.Done()
		toolsBinPath, toolsInstalled = a.initTools(ctx, log)
	})
	wg.Wait()

	// A panic in config resolution leaves resolved nil; abort cleanly via the
	// existing toolsOK=false path so the caller doesn't nil-deref on
	// resolved.Config. The panic cause is already logged by the recover above.
	if resolved == nil {
		return nil, "", false, false
	}

	if toolsBinPath == "" {
		return resolved, "", false, false
	}
	return resolved, toolsBinPath, toolsInstalled, true
}

// initTools ensures managed tools (rg, uv, markitdown) are available in
// <agentDir>/tools/. Startup never depends on the network — offline operation
// is a fully supported steady state, so the synchronous pass runs with network
// access disabled: existing binaries are version-probed and cached archives
// are installed (pure local work). Tools left not Ready are retried with
// network access from a background goroutine that completes while the app is
// already usable; a runtime_error toast fires only if tools remain unavailable
// after that final attempt. Nothing here aborts startup: the function always
// returns the tools/bin/ directory path (PATH prepending happens even when
// tools are unavailable — late installs are picked up per-exec via PATH) and
// whether any tool was installed during the synchronous pass.
func (a *App) initTools(ctx context.Context, log *slog.Logger) (toolsBinPath string, toolsInstalled bool) {
	agentDir := config.AgentDir()

	toolsDir := config.ToolsDir(agentDir)
	binDir := config.ToolsBinDir(agentDir)
	pythonDir := config.ToolsPythonDir(agentDir)

	mgr := toolmanager.NewManager(toolsDir, binDir, pythonDir, log, toolmanager.ManagerConfig{
		ProgressCallback: func(toolName, stage string, bytesDone, bytesTotal int64) {
			a.emit("tool_manager:progress", map[string]any{
				"tool":        toolName,
				"stage":       stage,
				"bytes_done":  bytesDone,
				"bytes_total": bytesTotal,
			})
		},
	})

	// Show the window unconditionally at the start of tool initialization so
	// the frontend has time to mount and subscribe to events before
	// backend:ready fires in Phase 5. On first run this reveals the
	// tool-install splash; on subsequent runs it prevents the race where
	// backend:ready is emitted before the frontend subscribes, which would
	// leave the app stuck on the spinner forever.
	// WindowShow is idempotent — calling it again in emitBackendReady is a
	// no-op when the window is already visible.
	wailsRuntime.WindowShow(ctx)

	// Early detection: check which tools need installing BEFORE doing any work.
	needed, needsErr := mgr.NeedsInstall()
	if needsErr != nil {
		log.Warn("failed to check tool install status, showing window as fallback", "error", needsErr)
		// ManagedTools() or ReadVersions() failed — we can't determine which tools
		// need installing, but EnsureCriticalTools will run regardless.
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": []map[string]string{},
		})
	} else if len(needed) > 0 {
		toolNames := make([]map[string]string, len(needed))
		for i, t := range needed {
			toolNames[i] = map[string]string{"name": t.Name, "version": t.Version}
		}
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": toolNames,
		})
	}

	// Synchronous pass: strictly local work. Failures are isolated per tool
	// and never block startup.
	statuses, ensureErr := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: false})
	if ensureErr != nil {
		// Structural failure (directories, disk space, registry). The splash
		// must still resolve (done) and the user must still learn why the
		// tools are missing — but the app starts.
		log.Error("tool reconciliation failed structurally", "error", ensureErr)
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		a.emitToolRuntimeError(ctx, "Tool initialization failed: "+ensureErr.Error())
		return mgr.PrependToPATH(), false
	}

	installed := 0
	var notReady []toolmanager.ToolStatus
	for _, s := range statuses {
		if s.Installed {
			installed++
		}
		if !s.Ready {
			notReady = append(notReady, s)
		}
	}

	a.emit(backend.EventToolManagerDone, map[string]any{
		"installed_count": installed,
		"skipped_count":   len(statuses) - installed,
	})

	if len(notReady) > 0 {
		// Background pass: complete the missing installs with network access
		// while the app is already usable. It must NOT emit
		// tool_manager:start — the frontend transitions splash →
		// waiting_ready on that event, and the app is past the splash by
		// now; progress and the final done event are harmless anywhere.
		safeGo(log, "tools-background", func() {
			a.finishToolInstallInBackground(ctx, log, mgr, notReady)
		})
	}

	return mgr.PrependToPATH(), installed > 0
}

// finishToolInstallInBackground retries the tools the offline startup pass
// could not satisfy, this time with network access. It emits the closing
// tool_manager:done event and, when tools are still unavailable afterwards
// (genuinely offline, or a broken mirror), a single runtime_error toast —
// the only user-facing signal; startup itself is never re-blocked.
func (a *App) finishToolInstallInBackground(ctx context.Context, log *slog.Logger, mgr *toolmanager.Manager, pending []toolmanager.ToolStatus) {
	pendingNames := make([]string, 0, len(pending))
	for _, s := range pending {
		pendingNames = append(pendingNames, s.Tool.Name)
	}
	log.Info("retrying tool install with network access in background", "tools", pendingNames)

	statuses, err := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: true})
	if err != nil {
		log.Error("background tool reconciliation failed structurally", "error", err)
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		a.emitToolRuntimeError(ctx, "Tool installation failed: "+err.Error())
		return
	}

	installed := 0
	var failed []toolmanager.ToolStatus
	for _, s := range statuses {
		if s.Installed {
			installed++
		}
		if !s.Ready {
			failed = append(failed, s)
		}
	}

	a.emit(backend.EventToolManagerDone, map[string]any{
		"installed_count": installed,
		"skipped_count":   len(statuses) - installed,
	})

	if len(failed) == 0 {
		log.Info("background tool install complete", "installed", installed)
		return
	}

	failedNames := make([]string, 0, len(failed))
	for _, s := range failed {
		failedNames = append(failedNames, s.Tool.Name)
		log.Error("managed tool unavailable after background install", "tool", s.Tool.Name, "error", s.Err)
	}
	a.emitToolRuntimeError(ctx,
		"c0wrk could not install managed tools ("+strings.Join(failedNames, ", ")+"). "+
			"Features that rely on them are unavailable this session. "+
			"Restart c0wrk with network access to retry.")
}

// emitToolRuntimeError raises a non-fatal, user-visible toast about the
// managed tools. Tool problems must never escalate to a fatal startup path —
// this is the deliberate inverse of the pre-offline-support Exit modal.
func (a *App) emitToolRuntimeError(ctx context.Context, message string) {
	a.emit(backend.EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": "tool_install_failed",
	})
}

// initDatabase opens the shared SQLite connection. On failure logs the error
// and returns nil — callers must tolerate a nil DB (downstream stores will be
// nil too and behavior degrades gracefully rather than panicking).
func (a *App) initDatabase(dbPath string, log *slog.Logger) *sql.DB {
	db, err := backend.OpenDatabase(dbPath, log)
	if err != nil {
		log.Error("failed to open sqlite database", "error", err)
		return nil
	}
	return db
}

// initTerminalManager constructs the PTY-backed terminal manager. Output is
// base64-encoded before emission to preserve raw bytes across JSON serialization
// (string(data) would corrupt invalid UTF-8 split across read boundaries, and
// json.Marshal replaces invalid UTF-8 with U+FFFD).
// userEnv carries config `terminal.env` entries; ${VAR} references are expanded
// here (backend/config convention: refs are stored raw, resolved at use time)
// and values are never logged — they may contain user secrets.
func (a *App) initTerminalManager(log *slog.Logger, userEnv map[string]string) *terminal.Manager {
	env := make(map[string]string, len(userEnv))
	for k, v := range userEnv {
		env[k] = config.ExpandEnvVars(v)
	}
	return terminal.NewManager(a.ctx, log,
		func(sessionID string, data []byte) {
			eventName := fmt.Sprintf("session:%s:terminal_output", sessionID)
			a.emit(eventName, map[string]string{"data": base64.StdEncoding.EncodeToString(data)})
		},
		func(sessionID string) {
			// Natural shell exit (user typed `exit` / shell crashed). The UI
			// keeps the per-session terminal instance mounted and resurrects
			// the shell lazily on next activation. Empty-object payload so it
			// passes the frontend's null-payload event filter.
			eventName := fmt.Sprintf("session:%s:terminal_exited", sessionID)
			a.emit(eventName, map[string]string{})
		},
		env,
	)
}

// initStores creates the project + session + review SQLite stores. Order
// matters: the session store has an FK reference to the projects table, and
// the review store has FK references to the sessions table, so it must be
// initialized after the session store.
func (a *App) initStores(db *sql.DB, log *slog.Logger) (*project.SQLiteProjectStore, *session.SQLiteSessionStore, *review.SQLiteReviewStore) {
	if db == nil {
		return nil, nil, nil
	}
	var projStore *project.SQLiteProjectStore
	if ps, err := project.NewSQLiteProjectStore(db); err != nil {
		log.Error("failed to init project store", "error", err)
	} else {
		projStore = ps
	}
	var sessStore *session.SQLiteSessionStore
	if s, err := session.NewSQLiteSessionStore(db); err != nil {
		log.Error("failed to init session store", "error", err)
	} else {
		sessStore = s
	}
	// Review store is initialized AFTER the session store because its tables
	// carry FK references to the sessions table.
	var reviewStore *review.SQLiteReviewStore
	if rs, err := review.NewSQLiteReviewStore(db); err != nil {
		log.Error("failed to init review store", "error", err)
	} else {
		rs.SetLogger(log)
		reviewStore = rs
	}
	return projStore, sessStore, reviewStore
}

// preloadProjectsAndSessions emits projects + sessions from the most recent
// project to the frontend before the slow NewApplication() call so the
// sidebar populates immediately. Returns the cached project list so
// emitBackendReady can reuse it without re-querying the database.
//
// NOTE: We intentionally do NOT set activeProjectID here — that would cause
// SwitchProject's idempotency guard to reject the frontend's first call,
// skipping vector index initialization. The frontend calls SwitchProject
// after EventBackendReady which sets activeProjectID properly.
func (a *App) preloadProjectsAndSessions(projectMgr *project.Manager, sessStore *session.SQLiteSessionStore, log *slog.Logger) []project.ProjectInfo {
	if projectMgr == nil {
		return nil
	}
	projects, err := projectMgr.ListProjects()
	if err != nil {
		log.Warn("failed to pre-load projects for early emit", "error", err)
		return nil
	}
	if len(projects) == 0 {
		return nil
	}

	a.emit(backend.EventProjectsLoaded, projects)

	if sessStore != nil {
		sessions, sErr := sessStore.ListSessionsByProject(context.Background(), projects[0].ID)
		if sErr == nil {
			a.emit(backend.EventSessionsLoaded, sessions)
		} else {
			log.Warn("failed to pre-load sessions for early emit", "error", sErr)
		}
	}
	return projects
}

// buildUIEmitFunc returns the session-event emitter used by the orchestrator
// and tool callbacks. It logs at debug level and forwards the typed
// session.Event to the Wails frontend via a.emit so tests can substitute the
// underlying transport.
func (a *App) buildUIEmitFunc() func(session.Event) {
	return func(evt session.Event) {
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		a.emit(eventName, evt.Data)
		// session_renamed is a session-list metadata change (it mirrors the
		// global project:renamed event). Re-emit it globally so the sidebar
		// updates the title even when the renamed session is NOT the active
		// one — e.g. when background auto-titling completes after the user has
		// already switched to another session. Without this, the session-scoped
		// event has no listener and the title stays stale until a project
		// switch or app reload.
		if evt.Type == "session_renamed" {
			if rd, ok := evt.Data.(session.SessionRenamedData); ok {
				a.emit(backend.EventSessionRenamed, map[string]string{"id": rd.ID, "name": rd.NewName})
			}
		}
		a.log().Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}
}

// buildAskUserCallback returns the closure that turns ask_user tool invocations
// into Wails events and waits for the frontend response. Errors out cleanly
// when no UI context or session is available so the tool reports the
// unavailability instead of blocking forever.
func (a *App) buildAskUserCallback(uiEmit func(session.Event)) coretools.AskUserFunc {
	return func(ctx context.Context, req coretools.AskUserRequest) (coretools.AskUserResponse, error) {
		if a.ctx == nil {
			return coretools.AskUserResponse{}, errors.New("ask_user not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return coretools.AskUserResponse{}, errors.New("ask_user not available: no session context")
		}

		requestID := uuid.New().String()
		ch := make(chan coretools.AskUserResponse, 1)
		payload := session.AskUserPayload{RequestID: requestID, Questions: req.Questions}
		a.pendingAskUser.Store(requestID, &pendingAskUserEntry{
			ch:        ch,
			sessionID: sessionID,
			payload:   payload,
		})
		uiEmit(session.Event{SessionID: sessionID, Type: "ask_user", Data: payload})

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return coretools.AskUserResponse{}, ctx.Err()
		case <-a.ctx.Done():
			a.pendingAskUser.Delete(requestID)
			return coretools.AskUserResponse{}, a.ctx.Err()
		}
	}
}

// planApprovalResponse carries the user's decision back to the blocked
// declare_plan tool call.
type planApprovalResponse struct {
	Decision string
	Feedback string
}

// goalProposalResponse carries the user's decision back to the blocked
// propose_goal tool call.
type goalProposalResponse struct {
	Decision         string // "approve" or "cancel"
	Condition        string // approved condition (possibly user-edited)
	Verify           string // approved verify clause (possibly user-edited)
	VerificationMode string // approved verification mode (possibly user-edited); echoes proposal when unchanged
}

// buildGoalProposalCallback returns the closure that turns propose_goal's
// sign-off request into a Wails event and waits for the frontend response
// (event- or RPC-based). Mirrors buildPlanApprovalCallback.
func (a *App) buildGoalProposalCallback(uiEmit func(session.Event)) coretools.GoalProposer {
	return &goalProposerAdapter{ctx: a.ctx, app: a, uiEmit: uiEmit}
}

// goalProposerAdapter implements tools.GoalProposer, bridging the core
// propose_goal tool to the desktop pending-action flow.
type goalProposerAdapter struct {
	ctx    context.Context
	app    *App
	uiEmit func(session.Event)
}

func (g *goalProposerAdapter) Propose(ctx context.Context, proposal coretools.GoalProposal) (coretools.GoalProposalResponse, error) {
	if g.ctx == nil {
		return coretools.GoalProposalResponse{}, errors.New("goal proposal not available: no UI context")
	}
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return coretools.GoalProposalResponse{}, errors.New("goal proposal not available: no session context")
	}

	requestID := uuid.New().String()
	ch := make(chan goalProposalResponse, 1)
	payload := session.GoalProposalPayload{
		RequestID:        requestID,
		SessionID:        sessionID,
		Condition:        proposal.Condition,
		Verify:           proposal.Verify,
		VerificationMode: proposal.VerificationMode,
	}
	g.app.pendingGoalProposals.Store(requestID, &pendingGoalProposalEntry{
		ch:        ch,
		sessionID: sessionID,
		payload:   payload,
	})
	// Emit through the Application's combined UI + persistence path so the
	// goal_proposal event survives app restarts. Fall back to the raw UI
	// emitter when the Application is not yet initialized.
	evt := session.Event{SessionID: sessionID, Type: "goal_proposal", Data: payload}
	if g.app.app != nil {
		g.app.app.EmitSessionEvent(evt)
	} else {
		g.uiEmit(evt)
	}

	select {
	case resp := <-ch:
		return coretools.GoalProposalResponse{
			Decision:         resp.Decision,
			Condition:        resp.Condition,
			Verify:           resp.Verify,
			VerificationMode: resp.VerificationMode,
		}, nil
	case <-ctx.Done():
		g.app.pendingGoalProposals.Delete(requestID)
		return coretools.GoalProposalResponse{}, ctx.Err()
	case <-g.ctx.Done():
		g.app.pendingGoalProposals.Delete(requestID)
		return coretools.GoalProposalResponse{}, g.ctx.Err()
	}
}

// buildPlanApprovalCallback returns the closure that turns declare_plan's
// await_approval mode into a Wails event and waits for the frontend response.
func (a *App) buildPlanApprovalCallback(uiEmit func(session.Event)) coretools.ApprovalFunc {
	return func(ctx context.Context, planPath, planMarkdown string) (string, string, error) {
		if a.ctx == nil {
			return "", "", errors.New("plan approval not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return "", "", errors.New("plan approval not available: no session context")
		}

		if planMarkdown == "" && planPath != "" {
			content, err := os.ReadFile(planPath)
			if err != nil {
				return "", "", fmt.Errorf("plan approval: failed to read plan file: %w", err)
			}
			planMarkdown = string(content)
		}

		requestID := uuid.New().String()
		ch := make(chan planApprovalResponse, 1)
		payload := session.PlanApprovalPayload{
			RequestID:   requestID,
			PlanPath:    planPath,
			PlanContent: planMarkdown,
		}
		a.pendingPlanApprovals.Store(requestID, &pendingPlanApprovalEntry{
			ch:        ch,
			sessionID: sessionID,
			payload:   payload,
		})
		// Emit through the Application's combined UI + persistence path so
		// the plan_review_ready event survives app restarts. Fall back to
		// the raw UI emitter when the Application is not yet initialized.
		evt := session.Event{SessionID: sessionID, Type: "plan_review_ready", Data: payload}
		if a.app != nil {
			a.app.EmitSessionEvent(evt)
		} else {
			uiEmit(evt)
		}

		select {
		case resp := <-ch:
			return resp.Decision, resp.Feedback, nil
		case <-ctx.Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", ctx.Err()
		case <-a.ctx.Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", a.ctx.Err()
		}
	}
}

// buildConfirmCallback returns the tool-confirmation closure. C-4 contract:
// when no UI context is available we return ConfirmDenyAndStop and log a
// warning rather than auto-approving — silently allowing in this path would
// let any tool execute without user oversight.
func (a *App) buildConfirmCallback(uiEmit func(session.Event)) sdktools.ConfirmFunc {
	return func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		if a.ctx == nil {
			a.log().Warn("confirmation callback denied: app context unavailable",
				"tool", req.ToolName, "reason", "ctx_nil")
			return sdktools.ConfirmDenyAndStop, nil
		}

		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			a.log().Warn("confirmation callback denied: no session ID in context",
				"tool", req.ToolName, "reason", "session_id_missing")
			return sdktools.ConfirmDenyAndStop, nil
		}

		requestID := uuid.New().String()
		ch := make(chan sdktools.ConfirmationResponse, 1)

		// Resolve the tool_call_id of the triggering tool_call. The emitter
		// records the most recent tool_call_id per session; since confirmation
		// fires sequentially right after that ToolCall (same goroutine), the
		// recorded id is the one being confirmed. The tool-name guard rejects
		// a stale id left by a concurrent subagent call to a *different* tool.
		var toolCallID string
		if a.app != nil {
			if id, tool := a.app.LastToolCallID(sessionID); id != "" && tool == req.ToolName {
				toolCallID = id
			}
		}

		a.pendingConfirmations.Store(requestID, &pendingConfirmData{
			ch:           ch,
			taskContext:  sdktools.TaskContextFrom(ctx),
			toolName:     req.ToolName,
			input:        req.Input,
			sessionID:    sessionID,
			reasoning:    req.JudgeReasoning,
			toolCallID:   toolCallID,
			disableJudge: req.DisableJudge,
		})

		payload := session.ToolConfirmPayload{
			ConfirmID:    requestID,
			Tool:         req.ToolName,
			Args:         string(req.Input),
			Reasoning:    req.JudgeReasoning,
			ToolCallID:   toolCallID,
			DisableJudge: req.DisableJudge,
		}
		// Route through the Application so the event passes the live session
		// emitter — keeping the runtime-status snapshot honest ("Awaiting
		// confirmation..." instead of a stale "Running tool: ..."). Raw UI
		// emitter only when the Application is not yet initialized.
		if a.app != nil {
			a.app.EmitToolConfirm(sessionID, payload)
		} else {
			uiEmit(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})
		}

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return sdktools.ConfirmDenyAndStop, ctx.Err()
		case <-a.ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return sdktools.ConfirmDenyAndStop, a.ctx.Err()
		}
	}
}

// buildStepLimitCallback returns a HITLHandler that handles step-limit prompts.
// Tool confirmation is handled separately via buildConfirmCallback → ToolRegistry.ConfirmFunc.
func (a *App) buildStepLimitCallback(uiEmit func(session.Event)) agent.HITLHandler {
	return &stepLimitHITLAdapter{
		ctx:              a.ctx,
		pendingStepLimit: &a.pendingStepLimit,
		uiEmit:           uiEmit,
	}
}

// stepLimitHITLAdapter wraps the step-limit UI prompt logic as an agent.HITLHandler.
// Tool confirmation is handled separately by the ToolRegistry's ConfirmFunc (policy-driven).
type stepLimitHITLAdapter struct {
	ctx              context.Context
	pendingStepLimit *sync.Map
	uiEmit           func(session.Event)
}

// OnToolCall allows all tool calls unchanged. Tool confirmation is handled
// by the ToolRegistry's ConfirmFunc (policy-driven, only for PolicyUserConfirm tools).
func (s *stepLimitHITLAdapter) OnToolCall(_ context.Context, _ string, _ json.RawMessage) (*agent.HITLToolDecision, error) {
	return &agent.HITLToolDecision{Allow: true}, nil
}

func (s *stepLimitHITLAdapter) OnStepLimit(ctx context.Context, currentStep, maxSteps int, reason string) (agent.StepLimitResponse, error) {
	if s.ctx == nil {
		return agent.StepLimitDeny, nil
	}
	sessionID := session.SessionIDFromContext(ctx)
	if sessionID == "" {
		return agent.StepLimitDeny, nil
	}

	requestID := uuid.New().String()
	ch := make(chan agent.StepLimitResponse, 1)
	payload := session.StepLimitPayload{
		RequestID:   requestID,
		CurrentStep: currentStep,
		MaxSteps:    maxSteps,
		Reason:      reason,
	}
	s.pendingStepLimit.Store(requestID, &pendingStepLimitEntry{
		ch:        ch,
		sessionID: sessionID,
		payload:   payload,
	})
	s.uiEmit(session.Event{SessionID: sessionID, Type: "step_limit", Data: payload})

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		s.pendingStepLimit.Delete(requestID)
		return agent.StepLimitDeny, ctx.Err()
	case <-s.ctx.Done():
		s.pendingStepLimit.Delete(requestID)
		return agent.StepLimitDeny, s.ctx.Err()
	}
}

// buildVectorCallbacks returns the lazy vector-search callbacks that gate on
// background ONNX initialization. The two callbacks have disjoint jobs so a
// readiness wait is never paid twice:
//
//   - waitFunc is THE bounded readiness waiter. One deadline
//     (vector_index.search_wait_timeout_ms) covers both stages — waiting for
//     the embedder (vectorReady) and the per-project Service.WaitReady —
//     after which an actionable error built from the manager's index status
//     (progress, current file) is returned instead of blocking until
//     indexing finishes. searchWaitTimeout <= 0 is the fail-fast sentinel:
//     readiness is checked without waiting and a not-ready index errors
//     immediately.
//   - searchFunc never waits for readiness: it dispatches to
//     HybridSearchNoWait, so an incremental pass that starts between the
//     caller's readiness gate and this call cannot block it (it fails fast
//     with the actionable not-ready error instead). The knob's timeout wrap
//     here bounds only the query execution (embedding the query, scanning
//     and fusing), as defense-in-depth.
//
// Callers pair them under a single budget: the semantic_search tool calls
// waitFunc then searchFunc (readiness wait ≤ knob, query execution ≤ knob),
// and the RAG-hint path calls both under one shared deadline (see
// Orchestrator.injectVectorSearchHints).
func (a *App) buildVectorCallbacks(vectorMgrPtr *atomic.Pointer[vectorindex.Manager], vectorReady <-chan struct{}, searchWaitTimeout time.Duration) (builtins.VectorSearchFunc, builtins.VectorSearchWaitFunc) { //nolint:gocritic // unnamedResult is acceptable for tuple returns with distinct types
	errStillLoading := errors.New("vector search not ready (index backend still loading); retry semantic_search later or use ripgrep/glob")

	// notReadyErr prefers the manager's actionable status (state, progress,
	// current file); the static message covers the embedder-still-loading
	// window where no manager exists yet.
	notReadyErr := func() error {
		if mgr := vectorMgrPtr.Load(); mgr != nil {
			return mgr.NotReadyError()
		}
		return errStillLoading
	}

	// awaitVectorReady blocks until the embedder is loaded or the (already
	// bounded) ctx expires. For the fail-fast sentinel it never blocks.
	awaitVectorReady := func(ctx context.Context) error {
		if searchWaitTimeout <= 0 {
			select {
			case <-vectorReady:
				return nil
			default:
				return errStillLoading
			}
		}
		select {
		case <-vectorReady:
			return nil
		case <-ctx.Done():
			return notReadyErr()
		}
	}

	searchFunc := builtins.VectorSearchFunc(func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		// The timeout wrap bounds the query execution only (embedding the
		// query, scanning and fusing) — defense-in-depth, not a readiness
		// wait. Readiness is the caller's bounded step (waitFunc for the
		// tool, the shared-deadline pre-wait for RAG hints), and the search
		// below never waits for it.
		if searchWaitTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, searchWaitTimeout)
			defer cancel()
		}
		if err := awaitVectorReady(ctx); err != nil {
			return nil, err
		}
		mgr := vectorMgrPtr.Load()
		if mgr == nil {
			return nil, errors.New("vector search unavailable")
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
		if searchWaitTimeout <= 0 {
			// Fail-fast: non-blocking readiness checks only, zero waiting.
			if err := awaitVectorReady(ctx); err != nil {
				return err
			}
			mgr := vectorMgrPtr.Load()
			if mgr == nil {
				return errors.New("vector search unavailable")
			}
			if !mgr.Service().IsReady() {
				return mgr.NotReadyError()
			}
			return nil
		}
		// One deadline covers both stages: waiting for the embedder
		// (vectorReady) and waiting for per-project index readiness
		// (Service.WaitReady).
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, searchWaitTimeout)
		defer cancel()
		if err := awaitVectorReady(ctx); err != nil {
			return err
		}
		mgr := vectorMgrPtr.Load()
		if mgr == nil {
			return errors.New("vector search unavailable")
		}
		if err := mgr.Service().WaitReady(ctx); err != nil {
			return mgr.NotReadyError()
		}
		return nil
	})

	return searchFunc, waitFunc
}

// buildApplication constructs the backend.Application and stores it on the App.
// Returns the application (also stored as a.app) on success, or an error.
func (a *App) buildApplication(cfg backend.ApplicationConfig, log *slog.Logger, startTime time.Time) (*backend.Application, error) {
	application, err := backend.NewApplication(cfg)
	if err != nil {
		log.Error("failed to create backend application", "error", err)
		a.emit(backend.EventStartupError, map[string]string{
			"message": "failed to create backend application",
			"error":   err.Error(),
		})
		return nil, err
	}
	a.app = application
	log.Info("startup phase complete", "phase", "application", "elapsed_ms", time.Since(startTime).Milliseconds())
	return application, nil
}

// buildFrontendAPI constructs the FrontendAPI, wires the project resolver, and
// validates LLM provider config. Stores the result on a.FrontendAPI.
func (a *App) buildFrontendAPI(
	application *backend.Application,
	cfg backend.FrontendAPIConfig,
	configLoadErrors []string,
	projStore *project.SQLiteProjectStore,
	log *slog.Logger,
	startTime time.Time,
) {
	a.FrontendAPI = backend.NewFrontendAPI(cfg)
	log.Info("startup phase complete", "phase", "frontend_api", "elapsed_ms", time.Since(startTime).Milliseconds())

	a.Lifecycle().SetConfigLoadState(configLoadErrors)

	// Wire project resolver for lazy session restoration.
	if projStore != nil {
		application.Manager().SetProjectStore(projStore)
		application.Manager().SetProjectResolver(func(projectID string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			proj, err := projStore.LoadProject(ctx, projectID)
			if err != nil {
				return "", fmt.Errorf("failed to load project: %w", err)
			}
			if proj == nil {
				return "", fmt.Errorf("project %s not found", projectID)
			}
			return proj.WorkspacePath, nil
		})
	}
	application.Manager().SetLogger(log)

	if cfg.Config != nil && cfg.Config.LLM.DefaultModel == "" {
		log.Error("no default model configured - check your config.yaml")
		a.emit(backend.EventStartupError, map[string]string{
			"message":    "no default model configured - check your config.yaml",
			"error":      "config has no default_model defined under llm",
			"error_code": "missing_default_model",
		})
	}
}

// emitBackendReady fires the EventBackendReady event with cached projects
// when available, falling back to a fresh ListProjects call. The signal tells
// the frontend that all synchronous backend subsystems are wired up.
// WindowShow is called unconditionally — when the window was already shown
// for tool installation this is a no-op; when no tools were needed this is
// the first time the window becomes visible.
//
// filterNoProject strips the No Project pseudo-project from the emitted list
// regardless of whether projects come from cache or a fresh query.
// This is used when LLM is unconfigured to prevent the frontend from
// auto-loading No Project before the settings dialog is shown.
func (a *App) emitBackendReady(cachedProjects []project.ProjectInfo, projectMgr *project.Manager, filterNoProject bool, log *slog.Logger) {
	// When the window is still hidden (no tools needed installing), show it now.
	// WindowShow is idempotent: if already visible (tools were installed), it's a no-op.
	// Guard against nil ctx in tests (no Wails lifecycle).
	if a.ctx != nil {
		wailsRuntime.WindowShow(a.ctx)
	}

	// Collect projects from cache or fresh query, applying No Project filter if
	// requested.
	var projects []project.ProjectInfo
	switch {
	case len(cachedProjects) > 0:
		projects = cachedProjects
	case projectMgr != nil:
		var err error
		projects, err = projectMgr.ListProjects()
		if err != nil {
			log.Warn("failed to load projects for backend:ready", "error", err)
			a.emit(backend.EventBackendReady)
			return
		}
	}

	if filterNoProject {
		filtered := make([]project.ProjectInfo, 0, len(projects))
		for _, p := range projects {
			if p.ID != project.NoProjectID {
				filtered = append(filtered, p)
			}
		}
		projects = filtered
	}

	if len(projects) > 0 {
		a.emit(backend.EventBackendReady, projects)
	} else {
		a.emit(backend.EventBackendReady)
	}
}

// startMCPReadyNotifier waits for the MCP gateway startup goroutine to finish
// and then emits EventMCPReady so the MCP settings dialog can refresh its
// transient "Starting…" placeholder into the real per-server status without
// manual polling. It runs in a goroutine spawned after EventBackendReady,
// mirroring startVectorIndexBackground: MCP startup (runMCPInit) is decoupled
// from initDone and may still be in flight (discovering remote servers) when
// the app is otherwise ready.
//
// If the app/builder is not wired (e.g. LLM unconfigured path), or if the
// startup finishes before this goroutine starts, MCPStartupDone short-circuits
// and the event is emitted immediately. On shutdown the ctx is cancelled,
// unblocking the wait.
func (a *App) startMCPReadyNotifier(ctx context.Context, log *slog.Logger) {
	if a.app == nil || a.app.Builder() == nil {
		return
	}
	b := a.app.Builder()

	// Fast path: startup already finished before this notifier ran.
	if b.MCPStartupDone() {
		a.emit(backend.EventMCPReady)
		return
	}

	go func() {
		waitCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		if err := b.WaitMCPStartup(waitCtx); err != nil {
			log.Debug("mcp:ready notifier stopped", "reason", err)
			return
		}
		a.emit(backend.EventMCPReady)
	}()
}

// startVectorIndexBackground launches the ONNX-backed vector index in a
// goroutine after EventBackendReady so it never blocks the critical path.
// The vectorReady channel is closed exactly once on completion (success or
// known-unavailable) so callers waiting on it always unblock.
func (a *App) startVectorIndexBackground(
	agentDir string,
	cfg *config.Config,
	vectorMgrPtr *atomic.Pointer[vectorindex.Manager],
	vectorReady chan struct{},
	vectorOnce *sync.Once,
	startTime time.Time,
	log *slog.Logger,
) {
	go func() {
		defer vectorOnce.Do(func() { close(vectorReady) })
		// Recover from a CGO/ONNX panic (version mismatch, malformed model
		// file, …) so the app survives with vector search disabled rather than
		// crashing after EventBackendReady has already been emitted.
		defer func() {
			if r := recover(); r != nil {
				log.Error("vector index background init panicked", "panic", r)
				a.emit("vector_index:status", map[string]any{"available": false, "reason": fmt.Sprint(r)})
			}
		}()

		modelPath := resolveModelPath("jina-v2-small.onnx", agentDir)
		tokenizerPath := resolveModelPath("jina-v2-small-tokenizer.json", agentDir)
		libraryPath := resolveONNXLibPath()

		// Create embedder directly via github.com/v0lka/sp4rk/embedding.
		if modelPath == "" || tokenizerPath == "" || libraryPath == "" {
			log.Info("vector search disabled (model files not found)")
			a.emit("vector_index:status", map[string]any{"available": false, "reason": "model files not found"})
			return
		}
		emb, embErr := embedding.NewEmbedder(embedding.EmbedderConfig{
			ModelPath:      modelPath,
			TokenizerPath:  tokenizerPath,
			LibraryPath:    libraryPath,
			MaxSeqLength:   512,
			HiddenDim:      512,
			BatchSize:      cfg.VectorIndex.EmbeddingBatchSize,
			IntraOpThreads: cfg.VectorIndex.EmbeddingThreads,
			Logger:         log,
		})
		if embErr != nil {
			log.Warn("vector search unavailable", "error", embErr)
			a.emit("vector_index:status", map[string]any{"available": false, "reason": embErr.Error()})
			return
		}

		vectorMgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: emb.EmbeddingFunc(),
			// BatchEmbedder enables the batched document-embedding path in
			// Service.AddDocuments: chunk contents are embedded via the
			// embedder's batch ONNX session BEFORE the chromem commit, so
			// chromem skips its one-inference-per-chunk calls entirely.
			// *embedding.Embedder satisfies the interface directly.
			BatchEmbedder: emb,
			CloseFn:       emb.Close,
			HybridConfig: vectorindex.HybridConfig{
				RRFK:              cfg.VectorIndex.HybridRRFK,
				FanoutMultiplier:  cfg.VectorIndex.HybridFanoutMultiplier,
				FanoutMin:         cfg.VectorIndex.HybridFanoutMin,
				VectorScoreFloor:  derefFloat(cfg.VectorIndex.HybridVectorScoreFloor),
				VectorScoreRatio:  derefFloat(cfg.VectorIndex.HybridVectorScoreRatio),
				LexicalScoreRatio: derefFloat(cfg.VectorIndex.HybridLexicalScoreRatio),
			},
			MaxFileSize:      cfg.VectorIndex.MaxFileSize,
			MaxChunkSize:     cfg.VectorIndex.MaxChunkSize,
			MaxChunksPerFile: cfg.VectorIndex.MaxChunksPerFile,
			// Indexing/search tuning knobs (vector_index.*). The config is
			// resolved (ApplyDefaults ran), so every value carries an
			// explicit default here; EmbeddingBatchSize must match the
			// EmbedderConfig value above — the Manager stores it for the
			// batched-embedding path, the embedder uses it as its ONNX
			// batch session capacity.
			EmbeddingBatchSize: cfg.VectorIndex.EmbeddingBatchSize,
			PrepWorkers:        cfg.VectorIndex.PrepWorkers,
			Debounce:           time.Duration(cfg.VectorIndex.DebounceMs) * time.Millisecond,
			ChunkOverlap:       cfg.VectorIndex.ChunkOverlap,
			// SearchWaitTimeout: 0 = "fail fast" (explicit sentinel from
			// config, never defaulted); stored on the Manager for the
			// search-path wiring.
			SearchWaitTimeout: time.Duration(derefInt(cfg.VectorIndex.SearchWaitTimeoutMs)) * time.Millisecond,
			Logger:            log,
		})
		if err != nil {
			log.Warn("vector search unavailable", "error", err)
			a.emit("vector_index:status", map[string]any{"available": false, "reason": err.Error()})
			return
		}

		// Abort registration if Shutdown has already run Cleanup — registering a
		// freshly-created manager after cleanup would leak it (never closed).
		if a.ctx != nil && a.ctx.Err() != nil {
			log.Info("vector search init aborted: app shutting down")
			vectorMgr.Shutdown()
			return
		}
		vectorMgrPtr.Store(vectorMgr)
		// Store on FrontendAPI immediately so Cleanup can shut it down — doing
		// this here instead of in a separate goroutine eliminates the race where
		// Shutdown runs Cleanup before SetVectorManager completes (W3).
		a.Lifecycle().SetVectorManager(vectorMgr)
		log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
	}()
}

// derefFloat returns *p when p is non-nil, else 0. Used to convert the
// pointer-float64 hybrid thresholds from config into the value-based
// vectorindex.HybridConfig.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefInt returns *p when p is non-nil, else 0. Used to convert the
// pointer-int vector-index sentinel (search_wait_timeout_ms: unset → default,
// explicit 0 → fail-fast) into a plain value for vectorindex.ManagerConfig.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// startUpdateCheckerBackground reaps stale updater artifacts from a prior
// interrupted self-update, then runs a single best-effort automatic update
// check in a goroutine. The check itself (operator + user gates, interval,
// result caching, event emission) lives in FrontendAPI.RunBackgroundUpdateCheck
// — the sole automatic-check path — so a discovered update is always
// downloadable. It never blocks or breaks startup; network failures are
// swallowed inside RunBackgroundUpdateCheck.
func (a *App) startUpdateCheckerBackground(log *slog.Logger) {
	// Reap orphaned updater artifacts (e.g. Windows c0wrk-updater-*.exe copies
	// that cannot self-delete while running). Best-effort, runs unconditionally.
	updater.CleanupStaleUpdaters(log)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("automatic update check panicked", "panic", r)
			}
		}()
		a.RunBackgroundUpdateCheck()
	}()
}
