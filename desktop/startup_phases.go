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
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/review"
	"github.com/v0lka/c0wrk/backend/session"
	core "github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/terminal"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/embedding"
	"github.com/v0lka/sp4rk/safeio"
	sdktools "github.com/v0lka/sp4rk/tools"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// initLogger initializes the session logger at a temporary INFO level so
// startup errors land on disk (maybeReinitLogger re-inits per config); once
// ready, the Wails log adapter delegates to it so Wails-internal messages —
// including fatal RPC errors — reach the session log. Errors are logged,
// never fatal.
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
	a.setLogger(log)
	return log, sessionLogger
}

// maybeReinitLogger re-initializes the session logger when the configured
// level differs from the bootstrap "INFO"; the original is kept on failure.
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
			// Log through the NEW logger (CS #46): `current` wraps the sink
			// just closed, so a diagnostic routed there is silently dropped.
			newLogger.Logger().Warn("failed to close previous session logger", "error", cerr)
		}
	}
	log := newLogger.Logger()
	a.setLogger(log)
	a.setSessionLogger(newLogger)
	if a.wailsLogger != nil {
		a.wailsLogger.SetDelegate(log)
	}
	return log, newLogger
}

// safeGo runs fn in a goroutine that recovers from any panic and logs it —
// a startup-phase failure degrades instead of crashing the app. The caller
// owns WaitGroup accounting (defer wg.Done() inside fn; fn's defers unwind
// before this recover sees the panic).
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

// initConfigAndDeps loads the config file and reconciles the managed tools
// (rg, uv, markitdown) in parallel. toolsOK=false + empty toolsBinPath mark
// the two abort paths (config panic, fatal tool-install exit); otherwise the
// caller must prepend toolsBinPath to PATH before subsequent phases.
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

	if resolved == nil {
		return nil, "", false, false
	}

	if toolsBinPath == "" {
		return resolved, "", false, false
	}
	return resolved, toolsBinPath, toolsInstalled, true
}

// initTools ensures the managed tools (rg, uv, markitdown) exist in
// <agentDir>/tools/ before the app becomes usable; the two-pass reconcile and
// its fatal/toast outcomes are specified in specs/domains/tool-manager.md
// (ADR-100). ("", false) on the fatal path so initConfigAndDeps aborts.
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

	// Safety-net reveal before the tool-install splash and backend:ready need
	// the window: it is already visible (main.go creates it so on purpose),
	// but a window hidden for any other reason must come back — showWindow
	// also raises/focuses (see App.showWindow's activation matrix).
	a.showWindow(ctx)

	installed, ok := a.reconcileToolsWithSplash(ctx, log, mgr, a.buildToolFatalActions())
	if !ok {
		// Fatal path already showed the modal and quit; the empty bin path
		// makes initConfigAndDeps report toolsOK=false so Startup aborts.
		return "", false
	}
	return mgr.PrependToPATH(), installed
}

// toolEnsurer is the manager seam of the startup tool orchestration:
// production binds a *toolmanager.Manager, tests bind a fake, so the
// two-pass flow is exercisable without the filesystem or the network.
type toolEnsurer interface {
	NeedsInstall() ([]toolmanager.ToolSpec, error)
	EnsureCriticalTools(ctx context.Context, opts toolmanager.EnsureOptions) ([]toolmanager.ToolStatus, error)
}

// toolFatalActions carries the fatal-exit seam of the tool-install flow.
type toolFatalActions struct {
	dialog func(ctx context.Context, message string)
	quit   func(ctx context.Context)
}

// buildToolFatalActions returns the production fatal actions: the native
// "Tool Installation Failed" modal followed by Quit. Tests inject recorders.
func (a *App) buildToolFatalActions() toolFatalActions {
	return toolFatalActions{
		dialog: func(ctx context.Context, message string) {
			_, _ = wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
				Type:          wailsRuntime.ErrorDialog,
				Title:         "Tool Installation Failed",
				Message:       message + "\n\nCheck your internet connection and disk space, then restart c0wrk.",
				Buttons:       []string{"Exit"},
				DefaultButton: "Exit",
			})
		},
		quit: func(ctx context.Context) {
			wailsRuntime.Quit(ctx)
		},
	}
}

// reconcileToolsWithSplash runs the two-pass managed-tool reconciliation and
// owns the splash lifecycle (tool_manager:start before installable work,
// tool_manager:done exactly once). The pass structure, blocking network
// retry, fatal modal+quit and the single toast degradation live in
// specs/domains/tool-manager.md (ADR-100); ok=false aborts startup.
func (a *App) reconcileToolsWithSplash(ctx context.Context, log *slog.Logger, mgr toolEnsurer, fatal toolFatalActions) (installed, ok bool) {
	// Early detection: show the splash before any work whenever tools are
	// (or may be) missing.
	needed, needsErr := mgr.NeedsInstall()
	toolsMissing := needsErr != nil // unknown counts as missing until probed
	startEmitted := false
	if needsErr != nil {
		log.Warn("failed to check tool install status, showing splash as fallback", "error", needsErr)
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": []map[string]string{},
		})
		startEmitted = true
	} else if len(needed) > 0 {
		toolsMissing = true
		toolNames := make([]map[string]string, len(needed))
		for i, t := range needed {
			toolNames[i] = map[string]string{"name": t.Name, "version": t.Version}
		}
		a.emit(backend.EventToolManagerStart, map[string]any{
			"tools": toolNames,
		})
		startEmitted = true
	}

	// Pass one: strictly local work. Per-tool failures surface as not-Ready
	// statuses; only structural failures (directories, disk space, registry)
	// come back as an error.
	statuses, ensureErr := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: false})
	if ensureErr == nil {
		offlineInstalled := 0
		var notReady []toolmanager.ToolStatus
		for _, s := range statuses {
			if s.Installed {
				offlineInstalled++
			}
			if !s.Ready {
				notReady = append(notReady, s)
			}
		}
		if len(notReady) == 0 {
			a.emit(backend.EventToolManagerDone, map[string]any{
				"installed_count": offlineInstalled,
				"skipped_count":   len(statuses) - offlineInstalled,
			})
			return offlineInstalled > 0, true
		}
		if !startEmitted {
			// NeedsInstall saw a complete install but probing disagrees (e.g.
			// a stale binary behind a matching .versions entry) — the
			// blocking network pass needs its splash after all.
			toolNames := make([]map[string]string, len(notReady))
			for i, s := range notReady {
				toolNames[i] = map[string]string{"name": s.Tool.Name, "version": s.Tool.Version}
			}
			a.emit(backend.EventToolManagerStart, map[string]any{
				"tools": toolNames,
			})
		}
	} else if !toolsMissing {
		// Structural failure over a complete install: nothing is missing, so
		// degrade to a toast instead of blocking or quitting.
		log.Error("tool reconciliation failed structurally", "error", ensureErr)
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		a.emitToolRuntimeError(ctx, "Tool initialization failed: "+ensureErr.Error())
		return false, true
	}

	// Pass two: blocking network retry. The splash is up by now and done is
	// emitted exactly once after this pass — the frontend leaves the splash
	// on it.
	pendingNames := make([]string, 0, len(needed))
	if ensureErr == nil {
		for _, s := range statuses {
			if !s.Ready {
				pendingNames = append(pendingNames, s.Tool.Name)
			}
		}
	} else {
		for _, t := range needed {
			pendingNames = append(pendingNames, t.Name)
		}
	}
	log.Info("retrying tool install with network access (blocking)", "tools", pendingNames)

	netStatuses, netErr := mgr.EnsureCriticalTools(ctx, toolmanager.EnsureOptions{AllowNetwork: true})
	if netErr != nil {
		log.Error("network tool reconciliation failed structurally", "error", netErr)
		a.emit(backend.EventToolManagerDone, map[string]any{
			"installed_count": 0,
			"skipped_count":   0,
		})
		fatal.dialog(ctx, "c0wrk was unable to download required tools:\n\n"+netErr.Error())
		fatal.quit(ctx)
		return false, false
	}

	netInstalled := 0
	var failed []toolmanager.ToolStatus
	for _, s := range netStatuses {
		if s.Installed {
			netInstalled++
		}
		if !s.Ready {
			failed = append(failed, s)
		}
	}
	a.emit(backend.EventToolManagerDone, map[string]any{
		"installed_count": netInstalled,
		"skipped_count":   len(netStatuses) - netInstalled,
	})
	if len(failed) == 0 {
		return netInstalled > 0, true
	}

	failedNames := make([]string, 0, len(failed))
	for _, s := range failed {
		failedNames = append(failedNames, s.Tool.Name)
		log.Error("managed tool unavailable after network install", "tool", s.Tool.Name, "error", s.Err)
	}
	fatal.dialog(ctx, "c0wrk was unable to install required tools:\n\n"+strings.Join(failedNames, ", "))
	fatal.quit(ctx)
	return false, false
}

// emitToolRuntimeError raises a non-fatal toast about the managed tools —
// the degradation path for a structurally failed offline pass over an
// already-complete install (fatal outcomes go through toolFatalActions).
func (a *App) emitToolRuntimeError(ctx context.Context, message string) {
	a.emit(backend.EventRuntimeError, map[string]string{
		"id":         uuid.New().String(),
		"message":    message,
		"error_code": "tool_install_failed",
	})
}

// initDatabase opens the shared SQLite connection; on failure it logs and
// returns nil — callers must tolerate a nil DB (downstream stores are nil
// too and behavior degrades rather than panicking).
func (a *App) initDatabase(dbPath string, log *slog.Logger) *sql.DB {
	db, err := backend.OpenDatabase(dbPath, log)
	if err != nil {
		log.Error("failed to open sqlite database", "error", err)
		return nil
	}
	return db
}

// initTerminalManager constructs the PTY-backed terminal manager. Output is
// base64-encoded before emission — string(data) corrupts invalid UTF-8 split
// across read boundaries and json.Marshal replaces it with U+FFFD. userEnv
// carries config `terminal.env` entries; ${VAR} refs are expanded here and
// values are never logged — they may contain user secrets.
func (a *App) initTerminalManager(log *slog.Logger, userEnv map[string]string) *terminal.Manager {
	env := make(map[string]string, len(userEnv))
	for k, v := range userEnv {
		env[k] = config.ExpandEnvVars(v)
	}
	return terminal.NewManager(a.wailsCtx(), log,
		func(sessionID string, data []byte) {
			eventName := fmt.Sprintf("session:%s:terminal_output", sessionID)
			encoded := base64.StdEncoding.EncodeToString(data)
			a.emitBatchedEvent(eventName, []any{map[string]string{"data": encoded}}, "", false, len(encoded))
		},
		func(sessionID string) {
			// Natural shell exit: the UI keeps the instance mounted and
			// resurrects the shell lazily on next activation
			// (specs/contracts/event-catalog.md, terminal_exited).
			// Empty-object payload passes the frontend's null-payload filter.
			eventName := fmt.Sprintf("session:%s:terminal_exited", sessionID)
			a.emitBatchedEvent(eventName, []any{map[string]string{}}, "", false, 0)
		},
		env,
	)
}

// initStores creates the project + session + review SQLite stores; the
// session store must precede the review store (FK references).
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
// project before the slow NewApplication() call so the sidebar populates
// immediately; the returned list is reused by emitBackendReady.
//
// It deliberately does NOT set activeProjectID: that would make SwitchProject's
// idempotency guard reject the frontend's first call (which follows
// EventBackendReady) and skip vector index initialization — the frontend's
// SwitchProject sets it properly.
// See specs/domains/workspace.md (startup preload / first-SwitchProject
// idempotency contract).
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
		// Bounded read: this runs synchronously inside Startup, which arms no
		// hard watchdog — a never-deadlined context would park the OnStartup
		// goroutine (splash forever) behind a contended SQLite pool; expiry
		// just skips the early sessions emit.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sessions, sErr := sessStore.ListSessionsByProject(ctx, projects[0].ID)
		cancel()
		if sErr == nil {
			a.emit(backend.EventSessionsLoaded, sessions)
		} else {
			log.Warn("failed to pre-load sessions for early emit", "error", sErr)
		}
	}
	return projects
}

// isTaskLifecycleEvent reports whether a session event is session-list
// state: activation, a terminal state, or pause/resume. Each is mirrored
// into the global sessions:changed push — see
// specs/contracts/event-catalog.md (sessions:changed, task_started).
func isTaskLifecycleEvent(eventType string) bool {
	switch eventType {
	case "task_started", "task_complete", "task_cancelled", "error",
		"task_failed_resumable", "session_paused", "session_resumed":
		return true
	default:
		return false
	}
}

// buildUIEmitFunc returns the session-event emitter used by the orchestrator
// and tool callbacks: debug-logged, forwarded to the Wails frontend via
// a.emit so tests can substitute the transport.
func (a *App) buildUIEmitFunc() func(session.Event) {
	return func(evt session.Event) {
		eventName := fmt.Sprintf("session:%s:%s", evt.SessionID, evt.Type)
		// Route through the event batcher: transient streaming events
		// coalesce (latest-wins), content events queue in order
		// (specs/contracts/event-catalog.md, event_batcher.go).
		a.emitBatchedEvent(eventName, []any{evt.Data},
			sessionEventCoalesceKey(evt.SessionID, evt.Type, evt.Data), isImmediateFlushEvent(evt.Type), 0)
		// Task lifecycle transitions are session-list state: mirror each into
		// the global sessions:changed push, in the batcher's immediate-flush
		// (barrier) class so event and list change land together. No payload —
		// consumers re-read the authoritative list behind their debounces.
		if isTaskLifecycleEvent(evt.Type) {
			a.emitBatchedEvent(backend.EventSessionsChanged, nil, "", true, 0)
		}
		// session_renamed mirrors the global project:renamed event so the
		// sidebar updates a NON-active session's title (e.g. background
		// auto-titling after a switch); without it the title stays stale
		// until a project switch or reload. Same batch, so ordering vs the
		// session-scoped event is preserved.
		if evt.Type == "session_renamed" {
			if rd, ok := evt.Data.(session.SessionRenamedData); ok {
				a.emitBatchedEvent(backend.EventSessionRenamed,
					[]any{map[string]string{"id": rd.ID, "name": rd.NewName}}, "", false, 0)
			}
		}
		a.log().Debug("desktop: Wails EventsEmit called", "eventName", eventName)
	}
}

// buildAskUserCallback returns the closure that turns ask_user tool
// invocations into Wails events and waits for the frontend response, erroring
// cleanly when no UI or session context is available.
func (a *App) buildAskUserCallback(uiEmit func(session.Event)) coretools.AskUserFunc {
	return func(ctx context.Context, req coretools.AskUserRequest) (coretools.AskUserResponse, error) {
		if a.wailsCtx() == nil {
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
		case <-a.wailsCtx().Done():
			a.pendingAskUser.Delete(requestID)
			return coretools.AskUserResponse{}, a.wailsCtx().Err()
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
// (event- or RPC-based), mirroring buildPlanApprovalCallback.
func (a *App) buildGoalProposalCallback(uiEmit func(session.Event)) coretools.GoalProposer {
	return &goalProposerAdapter{ctx: a.wailsCtx(), app: a, uiEmit: uiEmit}
}

// goalSuggestionResponse is the user's answer to a pending goal-mode
// suggestion card. decision is "accept" or "decline".
type goalSuggestionResponse struct {
	Decision string
	// Shutdown marks the teardown drain's resolution: the app is quitting and
	// no user answer exists. The adapter surfaces it as
	// errGoalSuggestionShutdown so the core gate fails OPEN — an app quit is
	// not a user decline (specs/domains/goal-mode.md).
	Shutdown bool
}

// errGoalSuggestionShutdown is returned by the adapter when a pending
// goal-suggestion wait was resolved by the shutdown drain; the core gate
// fails open on any error, and the sentinel exists so the drain produces that
// outcome explicitly instead of masquerading as a user decline.
var errGoalSuggestionShutdown = errors.New("goal suggestion wait aborted by app shutdown")

// buildGoalSuggestionCallback returns the closure that turns the orchestrator
// gate's suggestion offer into a Wails event and waits for the frontend
// response (event- or RPC-based), mirroring buildGoalProposalCallback.
func (a *App) buildGoalSuggestionCallback(uiEmit func(session.Event)) core.GoalSuggestionProposer {
	return &goalSuggestionAdapter{ctx: a.wailsCtx(), app: a, uiEmit: uiEmit}
}

// goalSuggestionAdapter implements core.GoalSuggestionProposer, bridging the
// orchestrator's post-routing suggestion gate to the desktop pending-action
// flow — a BLOCKING card, because an accepted suggestion must turn goal mode
// on before the task starts. See specs/decisions/086-goal-mode-suggestion.md.
type goalSuggestionAdapter struct {
	ctx    context.Context
	app    *App
	uiEmit func(session.Event)
}

func (g *goalSuggestionAdapter) ProposeGoalSuggestion(ctx context.Context, sessionID string, suggestion core.GoalSuggestion) (bool, error) {
	if g.ctx == nil {
		return false, errors.New("goal suggestion not available: no UI context")
	}
	if sessionID == "" {
		return false, errors.New("goal suggestion not available: no session context")
	}

	requestID := uuid.New().String()
	ch := make(chan goalSuggestionResponse, 1)
	payload := session.GoalSuggestionPayload{
		RequestID: requestID,
		SessionID: sessionID,
		Reason:    suggestion.Reason,
	}
	g.app.pendingGoalSuggestions.Store(requestID, &pendingGoalSuggestionEntry{
		ch:        ch,
		sessionID: sessionID,
		payload:   payload,
	})
	// Persisted emit path: Application.EmitSessionEvent so the goal_suggestion
	// event survives app restarts; raw uiEmit only before the Application
	// exists.
	evt := session.Event{SessionID: sessionID, Type: "goal_suggestion", Data: payload}
	if g.app.app != nil {
		g.app.app.EmitSessionEvent(evt)
	} else {
		g.uiEmit(evt)
	}

	select {
	case resp := <-ch:
		if resp.Shutdown {
			// Teardown drain, not a user decline — see errGoalSuggestionShutdown.
			return false, errGoalSuggestionShutdown
		}
		return resp.Decision == "accept", nil
	case <-ctx.Done():
		g.app.pendingGoalSuggestions.Delete(requestID)
		return false, ctx.Err()
	case <-g.ctx.Done():
		g.app.pendingGoalSuggestions.Delete(requestID)
		return false, g.ctx.Err()
	}
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
	// Persisted emit path: Application.EmitSessionEvent so the goal_proposal
	// event survives app restarts; raw uiEmit only before the Application
	// exists.
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
		if a.wailsCtx() == nil {
			return "", "", errors.New("plan approval not available: no UI context")
		}
		sessionID := session.SessionIDFromContext(ctx)
		if sessionID == "" {
			return "", "", errors.New("plan approval not available: no session context")
		}

		if planMarkdown == "" && planPath != "" {
			content, err := safeio.ReadFile(planPath)
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
		// Persisted emit path: Application.EmitSessionEvent so the
		// plan_review_ready event survives app restarts; raw uiEmit only
		// before the Application exists.
		evt := session.Event{SessionID: sessionID, Type: "plan_review_ready", Data: payload}
		if app := a.application(); app != nil {
			app.EmitSessionEvent(evt)
		} else {
			uiEmit(evt)
		}

		select {
		case resp := <-ch:
			return resp.Decision, resp.Feedback, nil
		case <-ctx.Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", ctx.Err()
		case <-a.wailsCtx().Done():
			a.pendingPlanApprovals.Delete(requestID)
			return "", "", a.wailsCtx().Err()
		}
	}
}

// buildConfirmCallback returns the tool-confirmation closure. C-4 contract:
// with no UI or session context it returns ConfirmDenyAndStop and logs —
// silently allowing here would execute tools without user oversight.
func (a *App) buildConfirmCallback(uiEmit func(session.Event)) sdktools.ConfirmFunc {
	return func(ctx context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		if a.wailsCtx() == nil {
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

		// Resolve the triggering tool_call's id: the emitter records the most
		// recent tool_call_id per session, and confirmation fires sequentially
		// after that ToolCall (same goroutine), so the recorded id is the one
		// being confirmed; the tool-name guard rejects a stale id from a
		// concurrent subagent call to a different tool.
		var toolCallID string
		if app := a.application(); app != nil {
			if id, tool := app.LastToolCallID(sessionID); id != "" && tool == req.ToolName {
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
		// Through the Application's live session emitter so the runtime-status
		// snapshot stays honest ("Awaiting confirmation..."); raw uiEmit only
		// before the Application exists.
		if app := a.application(); app != nil {
			app.EmitToolConfirm(sessionID, payload)
		} else {
			uiEmit(session.Event{SessionID: sessionID, Type: "tool_confirm", Data: payload})
		}

		select {
		case resp := <-ch:
			return resp, nil
		case <-ctx.Done():
			a.pendingConfirmations.Delete(requestID)
			return sdktools.ConfirmDenyAndStop, ctx.Err()
		case <-a.wailsCtx().Done():
			a.pendingConfirmations.Delete(requestID)
			return sdktools.ConfirmDenyAndStop, a.wailsCtx().Err()
		}
	}
}

// buildStepLimitCallback returns a HITLHandler that handles step-limit prompts.
// Tool confirmation is handled separately via buildConfirmCallback → ToolRegistry.ConfirmFunc.
func (a *App) buildStepLimitCallback(uiEmit func(session.Event)) agent.HITLHandler {
	return &stepLimitHITLAdapter{
		ctx:              a.wailsCtx(),
		pendingStepLimit: &a.pendingStepLimit,
		uiEmit:           uiEmit,
		resolver:         appStepLimitResolver{app: a},
	}
}

// stepLimitResolver resolves a step-limit boundary autonomously under silent
// mode (security.silent_mode AND step_limit.mode = "auto"): the trajectory
// comes from the session emitter's recent-execution window, the judge is the
// session-pinned ToolJudge; handled=false shows the interactive card.
type stepLimitResolver interface {
	ResolveSilentStepLimit(ctx context.Context, sessionID string, currentStep, maxSteps int, abortReason string) (agent.StepLimitResponse, string, bool)
}

// appStepLimitResolver adapts *App to stepLimitResolver, lazily reaching the
// backend Application (created after this adapter). UNEXPORTED on purpose:
// Wails auto-binds exported methods on *App, and a renderer-callable
// step-limit RPC would let compromised renderer JS drive the loop judge.
type appStepLimitResolver struct{ app *App }

func (r appStepLimitResolver) ResolveSilentStepLimit(ctx context.Context, sessionID string, currentStep, maxSteps int, abortReason string) (agent.StepLimitResponse, string, bool) {
	if r.app == nil || r.app.app == nil {
		return agent.StepLimitDeny, "", false
	}
	return r.app.app.ResolveSilentStepLimit(ctx, sessionID, currentStep, maxSteps, abortReason)
}

// stepLimitHITLAdapter wraps the step-limit UI prompt logic as an agent.HITLHandler.
// Tool confirmation is handled separately by the ToolRegistry's ConfirmFunc (policy-driven).
type stepLimitHITLAdapter struct {
	ctx              context.Context
	pendingStepLimit *sync.Map
	uiEmit           func(session.Event)
	// resolver, when non-nil, offers an autonomous silent-mode decision before
	// the interactive prompt. See stepLimitResolver.
	resolver stepLimitResolver
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

	// Silent mode + step_limit=auto: resolve autonomously from the trajectory
	// + loop judge (the resolver fails closed internally); handled=false falls
	// through to the interactive card below.
	if s.resolver != nil {
		if resp, _, handled := s.resolver.ResolveSilentStepLimit(ctx, sessionID, currentStep, maxSteps, reason); handled {
			return resp, nil
		}
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
	a.setApplication(application)
	log.Info("startup phase complete", "phase", "application", "elapsed_ms", time.Since(startTime).Milliseconds())
	return application, nil
}

// buildFrontendAPI constructs the FrontendAPI and validates LLM config. The
// embedded seed instance is initialized IN PLACE (a.frontendAPI()): the
// pointer is published once in NewApp and never swapped, so the Wails binding
// dispatch can never race a reassignment (review finding #52).
func (a *App) buildFrontendAPI(
	application *backend.Application,
	cfg backend.FrontendAPIConfig,
	configLoadErrors []string,
	projStore *project.SQLiteProjectStore,
	log *slog.Logger,
	startTime time.Time,
) {
	// Experimental-gate live-follow hook (ADR-101), installed through the
	// FrontendAPIConfig seam before Init so it can never race a toggle RPC —
	// see specs/domains/web-access.md.
	cfg.OnExperimentalChanged = a.applyExperimentalWebAccess
	a.frontendAPI().Lifecycle().Init(cfg)
	// The desktop's own handle on the live config + path: the web-access
	// control RPCs (ADR-101) flip and persist the desktop-owned section
	// through it; the same pointer Init stores.
	a.setConfigRef(cfg.Config, cfg.ConfigPath)
	log.Info("startup phase complete", "phase", "frontend_api", "elapsed_ms", time.Since(startTime).Milliseconds())

	a.frontendAPI().Lifecycle().SetConfigLoadState(configLoadErrors)

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

// emitBackendReady fires EventBackendReady with cached projects (or a fresh
// ListProjects), after the last safety-net window reveal — one of several
// idempotent reveals, not the moment the window appears
// (specs/architecture/data-flow.md). filterNoProject strips the No Project
// pseudo-project when LLM is unconfigured, so the frontend cannot auto-load
// it before the settings dialog is shown.
func (a *App) emitBackendReady(cachedProjects []project.ProjectInfo, projectMgr *project.Manager, filterNoProject bool, log *slog.Logger) {
	// Last safety-net reveal before the frontend is told the backend is up.
	// Guard against nil ctx in tests (no Wails lifecycle).
	if a.wailsCtx() != nil {
		a.showWindow(a.wailsCtx())
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

// startMCPReadyNotifier waits for the MCP gateway startup goroutine (bounded
// 35s, unblocked by ctx cancellation) and emits EventMCPReady so the settings
// dialog can replace its transient "Starting…" placeholder. Spawned after
// EventBackendReady, mirroring startVectorIndexBackground; an unwired builder
// or finished startup short-circuits via MCPStartupDone.
func (a *App) startMCPReadyNotifier(ctx context.Context, log *slog.Logger) {
	app := a.application()
	if app == nil || app.Builder() == nil {
		return
	}
	b := app.Builder()

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
// goroutine after EventBackendReady; vectorReady is closed exactly once on
// completion (success or known-unavailable) so waiters always unblock.
func (a *App) startVectorIndexBackground(
	agentDir string,
	cfg *config.Config,
	vectorReady chan struct{},
	vectorOnce *sync.Once,
	startTime time.Time,
	log *slog.Logger,
) {
	go func() {
		defer vectorOnce.Do(func() { close(vectorReady) })
		// Recover from a CGO/ONNX panic so the app survives with vector search
		// disabled instead of crashing after backend:ready.
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
		const (
			maxSeqLength = embedding.DefaultMaxSeqLength
			hiddenDim    = embedding.DefaultHiddenDim
		)
		cacheFingerprint, fingerprintErr := vectorindex.EmbeddingFingerprint(modelPath, tokenizerPath, vectorindex.EmbeddingFingerprintParams{
			MaxSeqLength:           maxSeqLength,
			Dimension:              hiddenDim,
			NormalizationAlgorithm: vectorindex.EmbeddingNormalizationAlgorithm,
		})
		if fingerprintErr != nil {
			// Cache is an optimization: fingerprint I/O failure must not disable
			// vector search or indexing.
			log.Warn("embedding cache disabled: artifact fingerprint failed", "error", fingerprintErr)
			cacheFingerprint = ""
		}

		// ONNX execution provider, driven by config (vector_index.
		// execution_provider / device_id). Resolved exactly ONCE — the
		// embedder and its ONNX session are created once per process, so
		// changing either knob requires an app restart. "auto" walks the
		// hardware ladder (CUDA, then OpenVINO) falling back to CPU with a
		// WARN inside sp4rk; "cpu"/"cuda"/"openvino" force their provider.
		// See specs/decisions/045-gpu-embedding-provider.md.
		requestedProvider := cfg.VectorIndex.ExecutionProvider
		if requestedProvider == "" {
			requestedProvider = config.VectorIndexProviderAuto
		}
		onnxDevice := cfg.VectorIndex.DeviceID
		embCfg := embedding.EmbedderConfig{
			ModelPath:         modelPath,
			TokenizerPath:     tokenizerPath,
			LibraryPath:       libraryPath,
			MaxSeqLength:      maxSeqLength,
			HiddenDim:         hiddenDim,
			BatchSize:         cfg.VectorIndex.EmbeddingBatchSize,
			IntraOpThreads:    cfg.VectorIndex.EmbeddingThreads,
			ExecutionProvider: requestedProvider,
			DeviceID:          onnxDevice,
			Logger:            log,
		}
		emb, embErr := embedding.NewEmbedder(embCfg)
		// Requested/effective provider facts for every vector-index status
		// payload — c0wrk's observability contract per ADR-045 (the settings
		// UI renders the pair; the explicit-cuda/openvino→cpu fallback is the
		// only true fallback — an "auto" request always resolves a winner).
		embedderInfo := backend.VectorEmbedderInfo{RequestedProvider: requestedProvider, DeviceID: onnxDevice}
		if embErr != nil && (requestedProvider == config.VectorIndexProviderCUDA || requestedProvider == config.VectorIndexProviderOpenVINO) {
			// An explicit "cuda"/"openvino" that cannot come up must not
			// silently kill vector search: continue on CPU, but make the
			// fallback unmissable — WARN + runtime_error toast + the
			// requested/effective mismatch in every status (ADR-045).
			providerLabel, errorCode, fixHint := "CUDA", "vector_cuda_fallback",
				"Fix the GPU setup (make fetch-onnx-gpu + CUDA driver) or set vector_index.execution_provider: auto|cpu."
			if requestedProvider == config.VectorIndexProviderOpenVINO {
				providerLabel, errorCode, fixHint = "OpenVINO", "vector_openvino_fallback",
					"Fix the Intel setup (make fetch-onnx-openvino installs the OpenVINO execution-provider plugin next to the ONNX Runtime library, Linux x64 only) or set vector_index.execution_provider: auto|cpu."
			}
			log.Warn(providerLabel+" execution provider unavailable, falling back to CPU",
				"error", embErr,
				"deviceID", onnxDevice,
				"library", libraryPath)
			a.emit(backend.EventRuntimeError, map[string]string{
				"id":         uuid.New().String(),
				"message":    "Vector index: " + providerLabel + " execution provider is unavailable — running on the CPU, embeddings will be slower. Cause: " + embErr.Error() + " " + fixHint,
				"error_code": errorCode,
			})
			embedderInfo.FallbackReason = embErr.Error()
			embCfg.ExecutionProvider = embedding.ExecutionProviderCPU
			emb, embErr = embedding.NewEmbedder(embCfg)
		}
		if embErr != nil {
			log.Warn("vector search unavailable",
				"error", embErr,
				"executionProvider", requestedProvider,
				"deviceID", onnxDevice,
				"library", libraryPath)
			embedderInfo.FallbackReason = embErr.Error()
			a.frontendAPI().Lifecycle().SetVectorEmbedderInfo(embedderInfo)
			a.emit("vector_index:status", map[string]any{
				"available":                    false,
				"reason":                       embErr.Error(),
				"requested_execution_provider": requestedProvider,
			})
			return
		}
		embedderInfo.EffectiveProvider = emb.ExecutionProvider()
		if embedderInfo.EffectiveProvider == requestedProvider && embedderInfo.FallbackReason != "" {
			// Unreachable combination today (reason set ⇒ divergence); guard
			// keeps the invariant explicit for future editors.
			embedderInfo.FallbackReason = ""
		}
		if embedderInfo.EffectiveProvider == embedding.ExecutionProviderCPU &&
			requestedProvider == config.VectorIndexProviderAuto {
			// "auto" degraded to CPU inside sp4rk (its WARN carries the
			// cause); keep one app-level line tying the divergence to vector
			// search. An "auto" request that WON with CUDA is not a fallback.
			log.Warn("vector index embedder running on CPU provider",
				"requestedExecutionProvider", requestedProvider,
				"effectiveExecutionProvider", embedderInfo.EffectiveProvider,
				"hint", "no usable hardware execution provider; see the WARN above for the per-provider causes")
		}
		if embedderInfo.EffectiveProvider == embedding.ExecutionProviderCUDA {
			// External verification that the process really runs on the GPU —
			// diagnostics only, never blocks or fails startup.
			verified := a.verifyEmbedderGPU(emb, log)
			embedderInfo.CUDAVerified = &verified
		}
		if embedderInfo.EffectiveProvider == embedding.ExecutionProviderOpenVINO {
			// External verification of the device class the OpenVINO provider
			// actually drives ("gpu"|"cpu"|"" unverified): sp4rk picks a
			// concrete device inside the rung, so only an external probe can
			// tell GPU from CPU. Diagnostics only, never fatal.
			embedderInfo.OVDevice = vectorindex.VerifyEmbedderOvDevice(context.Background(), emb, log)
		}
		a.frontendAPI().Lifecycle().SetVectorEmbedderInfo(embedderInfo)

		// Deterministic early rejection of generated / minified / pathological
		// files before chunking; the resolved policy (vector_index.
		// content_filter) participates in the chunker fingerprint, so policy
		// changes re-validate sidecars.
		contentFilter := cfg.VectorIndex.ContentFilter.ResolveContentFilter()
		// Per-root registry (ADR-081), built on demand through this factory.
		// CloseFn stays nil on every per-root manager: the ONNX runtime is
		// process-global and shared, so an LRU eviction must never close it —
		// the registry's ShutdownAll closes the embedder exactly once (see
		// specs/decisions/081-vector-per-root-registry.md).
		factory := func() (*vectorindex.Manager, error) {
			return vectorindex.NewManager(vectorindex.ManagerConfig{
				EmbeddingFunc: emb.EmbeddingFunc(),
				// BatchEmbedder enables the batched document-embedding path in
				// Service.AddDocuments: chunks are embedded via the embedder's
				// batch ONNX session BEFORE the chromem commit, so chromem
				// skips its one-inference-per-chunk calls entirely.
				BatchEmbedder: emb,
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
				ContentFilter:    &contentFilter,
				// Indexing/search tuning knobs (vector_index.*): the config is
				// resolved, so every value carries an explicit default here.
				// EmbeddingBatchSize must match the EmbedderConfig value —
				// the Manager stores it for the batched-embedding path.
				EmbeddingBatchSize:        cfg.VectorIndex.EmbeddingBatchSize,
				EmbeddingCacheFingerprint: cacheFingerprint,
				EmbeddingDimension:        hiddenDim,
				EmbeddingCacheMaxBytes:    cfg.VectorIndex.EmbeddingCacheMaxBytes,
				PrepWorkers:               cfg.VectorIndex.PrepWorkers,
				Debounce:                  time.Duration(cfg.VectorIndex.DebounceMs) * time.Millisecond,
				ChunkOverlap:              cfg.VectorIndex.ChunkOverlap,
				// SearchWaitTimeout: 0 = explicit "fail fast" sentinel from
				// config, never defaulted.
				SearchWaitTimeout: time.Duration(derefInt(cfg.VectorIndex.SearchWaitTimeoutMs)) * time.Millisecond,
				// ParkCapacity: recently-closed projects keeping their vector
				// index resident (vector_index.park_capacity; default 3,
				// explicit 0 disables parking).
				ParkCapacity: derefInt(cfg.VectorIndex.ParkCapacity),
				// ParkBudgetBytes: cumulative resident-memory budget for the
				// park LRU (see derefParkBudgetBytes).
				ParkBudgetBytes: derefParkBudgetBytes(cfg.VectorIndex.ParkBudgetMb),
				Logger:          log,
			})
		}

		// Quitting during init must not leak the ONNX runtime: once Shutdown
		// has run Cleanup, the registry is closed and nobody would release
		// the embedder — close it here instead.
		if a.wailsCtx() != nil && a.wailsCtx().Err() != nil {
			log.Info("vector search init aborted: app shutting down")
			_ = emb.Close()
			return
		}
		// Hand the factory + readiness to the backend registry, then apply the
		// first project's deferred setup (it ran before the factory was wired
		// and skipped vector setup) — see
		// specs/decisions/081-vector-per-root-registry.md.
		a.frontendAPI().Lifecycle().SetVectorRootsFactory(factory, vectorReady, emb.Close)
		a.frontendAPI().Lifecycle().InitVectorIndexForActiveProject()
		log.Info("background init complete", "phase", "vector_index", "elapsed_ms", time.Since(startTime).Milliseconds())
	}()
}

// verifyEmbedderGPU performs the one-shot external GPU verification after a
// successful CUDA embedder init (lazy CUDA context → warmup, then an
// nvidia-smi probe of THIS process) — the cuda_verified verdict of the ADR-045
// status contract. Diagnostics only: errors are treated as "unverified",
// never as failure.
func (a *App) verifyEmbedderGPU(emb *embedding.Embedder, log *slog.Logger) bool {
	// Warmup inference materializes the CUDA context. A failure here is a
	// strong signal the GPU path is broken end-to-end (later real searches
	// would fail too): WARN loudly and let the probe answer.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := emb.EmbedQuery(ctx, "warmup"); err != nil {
		log.Warn("CUDA embedder warmup inference failed",
			"error", err,
			"hint", "embedder stays enabled; real searches may fail if this persists")
	}
	inUse, err := embedding.GPUInUse(ctx)
	if err != nil {
		// Probe failure ≠ "not on GPU": treated as unverified, but always
		// visible — a missing verdict is suspicious.
		log.Warn("CUDA GPU verification probe failed",
			"error", err,
			"hint", "treated as unverified, not as CPU fallback")
	}
	if inUse {
		log.Info("CUDA verified via nvidia-smi",
			"pid", os.Getpid())
		return true
	}
	log.Warn("process absent from GPU compute apps — possible silent CPU fallback",
		"pid", os.Getpid(),
		"hint", "nvidia-smi did not list this PID; the CUDA-capable build may be sliding to the CPU provider")
	return false
}

// derefFloat returns *p when non-nil, else 0 — the pointer-float64 hybrid
// thresholds from config into vectorindex.HybridConfig.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefInt returns *p when non-nil, else 0 — the pointer-int vector-index
// sentinel (search_wait_timeout_ms) as a plain value.
func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// derefParkBudgetBytes converts vector_index.park_budget_mb into bytes. A nil
// pointer resolves to vectorindex.DefaultParkBudgetBytes (1024 MiB), not 0 —
// the service reads a zero budget as "no byte budget", so the old
// derefInt64(nil)<<20 fail-open silently dropped the ceiling. -1 keeps its
// "byte budget disabled" sentinel meaning.
func derefParkBudgetBytes(p *int64) int64 {
	if p == nil {
		return vectorindex.DefaultParkBudgetBytes
	}
	return *p << 20
}

// startUpdateCheckerBackground runs one best-effort automatic update check in
// a goroutine; FrontendAPI.RunBackgroundUpdateCheck is the sole auto-check
// path (gates, interval, caching).
//
// Deliberately NO updater.CleanupStaleUpdaters here: Startup runs only in the
// owning (first) instance — exactly while the app-level lock is held — so a
// reap now would delete the live staging of a self-update that is mid-apply.
// The only reap site is main.go's post-lock, first-instance-gated call.
func (a *App) startUpdateCheckerBackground(log *slog.Logger) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("automatic update check panicked", "panic", r)
			}
		}()
		a.frontendAPI().RunBackgroundUpdateCheck()
	}()
}

// startAutoFetchBackground starts the periodic git auto-fetch ticker exactly
// once after the backend is ready (mirroring startUpdateCheckerBackground);
// stopped by FrontendAPILifecycle.Cleanup. See
// specs/domains/git-auto-fetch.md.
func (a *App) startAutoFetchBackground() {
	a.frontendAPI().Lifecycle().StartAutoFetch()
}

// initEmbeddedLLM restores the embedded local-model state: a manifest.json
// read plus one embedded_llm:state event — no download, no network, no probe,
// no model load (startup stays offline-first and never blocks on weights; see
// specs/domains/embedded-llm.md). One stat + one small JSON read, far below
// the phase budget — hence inline in Phase 5, complete before the first
// GetEmbeddedLLMStatus.
func (a *App) initEmbeddedLLM(log *slog.Logger) {
	if a.frontendAPI() == nil {
		return
	}
	startTime := time.Now()
	a.frontendAPI().Lifecycle().InitEmbeddedLLM()
	log.Info("startup phase complete", "phase", "embedded_llm",
		"elapsed_ms", time.Since(startTime).Milliseconds())
}

// initChatGPTAuth constructs the ChatGPT subscription-auth token manager and
// restores keychain credentials (see specs/domains/llm-providers.md, ChatGPT
// Subscription Auth). Mirrors initEmbeddedLLM in doing no network I/O and no
// browser flow — a failed construction records the actionable error and
// leaves the app usable on api_key auth.
//
// UNLIKE initEmbeddedLLM, the restore runs on a BACKGROUND goroutine: the OS
// keychain read is not provably bounded (a locked Linux login collection
// unlocks with an interactive prompt, no timeout), so a synchronous restore
// could hold backend readiness hostage for an api_key-only user. The auth
// surface tolerates the gap by design (status RPC answers the signed-out
// zero state; mutating RPCs refuse transiently; the late restore publishes
// itself like an interactive sign-in). Nothing can cancel a native keyring
// call, so on shutdown the context checks inside InitChatGPTAuth DISCARD the
// result — a late restore never races the teardown.
func (a *App) initChatGPTAuth(log *slog.Logger) {
	if a.frontendAPI() == nil {
		return
	}
	// Off the startup path through safeGo — the keyring backend is native
	// code, and an unrecovered panic would take the process down for a
	// feature designed to degrade gracefully. Cancellation is checked before
	// touching FrontendAPI state, so a shutdown is never raced.
	safeGo(log, "chatgpt_auth", func() {
		startTime := time.Now()
		a.frontendAPI().Lifecycle().InitChatGPTAuth()
		log.Info("startup phase complete", "phase", "chatgpt_auth",
			"elapsed_ms", time.Since(startTime).Milliseconds())
	})
}

// stopEmbeddedLLM stops the supervised llama-server during Shutdown,
// releasing the weights' RAM/VRAM early in the teardown. Idempotent; a
// failure is logged, never fatal.
//
// Why "never fatal" is safe — the teardown contract (specs/domains/embedded-llm.md):
// the supervised child is deliberately DETACHED (context.WithoutCancel, no
// Pdeathsig/Setpgid/job-object tie), so the OS does not reclaim it when this
// process exits. This call is the ONLY thing that terminates it, and the
// guarantee comes from the bounded context: backend.stopEmbeddedLLM arms
// embeddedStopTimeout (30s) around core's Stop, whose force path kills the
// child when the single-instance gate is held by an in-flight cold load
// (up to 15 min) — an UNBOUNDED context would hang the quit for the length
// of the load.
//
// What the bound does NOT promise is that the child is always dead: a stop
// that did not take (the wedged child surviving both signal and kill) makes
// core's terminate return an error, and both stop paths then RE-ATTACH the
// live run handle and record StateError — tracked, never lost. Shutdown logs
// that error non-fatally: the ONE quit outcome that can still leave
// llama-server running. A crash or SIGKILL runs neither this nor Shutdown:
// the port self-heals next launch, the memory does not.
func (a *App) stopEmbeddedLLM(ctx context.Context) {
	if a.frontendAPI() == nil {
		return
	}
	stop := a.embeddedLLMStopFn
	if stop == nil {
		stop = a.frontendAPI().Lifecycle().StopEmbeddedLLM
	}
	if err := stop(ctx); err != nil {
		a.log().Error("failed to stop the embedded LLM server during shutdown", "error", err)
	}
}

// initWebAccess mounts the browser web-access sidecar (ADR-101) when BOTH
// gates are open: web_access.enabled AND the master experimental gate — the
// startup mount point, by the initChatGPTAuth pattern, after buildFrontendAPI
// and emitBackendReady so it neither delays nor reorders the readiness
// contract; the concrete assembly (dispatch target, SPA tree, CSP middleware,
// config section) is handed to internal/webaccess. Mid-session gate flips
// ride the desktop hook, not a restart. See specs/domains/web-access.md.
//
// A closed gate is a QUIET skip (Debug): an intentionally-off feature is not
// a failure. Failure is fail-soft and LOUD (Error log): the sidecar
// degrades, the desktop app must not — the section stays enabled, so the
// next launch retries.
func (a *App) initWebAccess(cfg *config.Config, log *slog.Logger) {
	if a.frontendAPI() == nil || cfg == nil {
		return
	}
	if !webAccessMountAllowed(cfg, cfg.Experimental.Enabled) {
		log.Debug("web access sidecar not mounted: gate closed",
			"experimental_enabled", cfg.Experimental.Enabled,
			"web_access_enabled", cfg.WebAccess.Enabled)
		return
	}
	startTime := time.Now()
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()
	srv, err := a.startWebAccessLocked(log)
	if err != nil {
		log.Error("web access sidecar failed to start; continuing without it", "error", err)
		return
	}
	logWebAccessReady(log, srv)
	log.Info("startup phase complete", "phase", "web_access",
		"elapsed_ms", time.Since(startTime).Milliseconds())
}

// stopWebAccess stops the sidecar during Shutdown (behind webAccessMu, so it
// cannot interleave with a SetWebAccessEnabled transition) before the judge
// drain and store close — a connected tab sees its stream end while the app
// winds down. webAccessStopBudget (~3s) converts a wedged graceful Shutdown
// into Server.Stop's force close; a failure is logged, never fatal (see
// specs/domains/web-access.md).
func (a *App) stopWebAccess(ctx context.Context) {
	a.webAccessMu.Lock()
	defer a.webAccessMu.Unlock()
	sctx, cancel := context.WithTimeout(ctx, webAccessStopBudget)
	defer cancel()
	if err := a.stopWebAccessLocked(sctx); err != nil {
		a.log().Error("failed to stop the web access sidecar during shutdown", "error", err)
	}
}
