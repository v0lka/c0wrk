package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/logger"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	core "github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/toolmanager"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
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

// initConfigAndDeps loads the config file and reconciles the managed tools
// (rg, uv, markitdown) in parallel. toolsOK=false + empty toolsBinPath mark
// the two abort paths (config panic, fatal tool-install exit); otherwise the
// caller must prepend toolsBinPath to PATH before subsequent phases. The
// tool-install phase itself lives in backend (P-14); this adapter binds its
// transport hooks — the Wails emit funnel, the native splash reveal, the
// splash-denominator store and the fatal modal+quit actions.
func (a *App) initConfigAndDeps(ctx context.Context, log *slog.Logger) (resolved *config.ResolvedConfig, toolsBinPath string, toolsInstalled, toolsOK bool) {
	toolDeps := backend.ToolPhaseDeps{
		Progress:      a.emitToolManagerProgress,
		ShowWindow:    a.showWindow,
		SetToolsTotal: func(total int64) { a.toolSplashTotal.Store(total) },
		Emit:          a.emit,
		Fatal:         a.buildToolFatalActions(),
	}
	var wg sync.WaitGroup
	wg.Add(2)
	backend.SafeGo(log, "config", func() {
		defer wg.Done()
		resolved = config.ResolveAndLoad(log)
	})
	backend.SafeGo(log, "tools", func() {
		defer wg.Done()
		toolsBinPath, toolsInstalled = backend.InitTools(ctx, log, toolDeps)
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

// emitToolManagerProgress forwards one manager progress report to the
// frontend, stamping the splash denominator: Wails drops events emitted
// before the webview subscribes, so tool_manager:start can be lost and every
// progress event must self-describe the total
// (specs/contracts/event-catalog.md, tool_manager:progress).
func (a *App) emitToolManagerProgress(p toolmanager.ToolProgress) {
	a.emit(backend.EventToolManagerProgress, map[string]any{
		"tool":        p.Tool,
		"stage":       p.Stage,
		"bytes_done":  p.BytesDone,
		"bytes_total": p.BytesTotal,
		"detail":      p.Detail,
		"error":       p.Error,
		"tools_total": a.toolSplashTotal.Load(),
	})
}

// buildToolFatalActions returns the production fatal actions: the native
// "Tool Installation Failed" modal followed by Quit. Tests inject recorders.
func (a *App) buildToolFatalActions() backend.ToolFatalActions {
	return backend.ToolFatalActions{
		Dialog: func(ctx context.Context, message string) {
			_, _ = wailsRuntime.MessageDialog(ctx, wailsRuntime.MessageDialogOptions{
				Type:          wailsRuntime.ErrorDialog,
				Title:         "Tool Installation Failed",
				Message:       message + "\n\nCheck your internet connection and disk space, then restart c0wrk.",
				Buttons:       []string{"Exit"},
				DefaultButton: "Exit",
			})
		},
		Quit: func(ctx context.Context) {
			wailsRuntime.Quit(ctx)
		},
	}
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
			backend.SessionEventCoalesceKey(evt.SessionID, evt.Type, evt.Data), backend.IsImmediateFlushEvent(evt.Type), 0)
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
	cfg.OnExperimentalChanged = a.webAccessService.ApplyExperimental
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

// startMCPReadyNotifier waits for the MCP gateway startup goroutine to finish
// and then emits EventMCPReady so the MCP settings dialog can refresh its
// transient "Starting…" placeholder into the real per-server status without
// manual polling. It runs in a goroutine spawned after EventBackendReady,
// mirroring the vector-index background init (the backend vectorIndexLauncher's
// Launch, relocated by stage P-12):
// MCP startup (runMCPInit) is decoupled
// from initDone and may still be in flight (discovering remote servers) when
// the app is otherwise ready.
//
// If the app/builder is not wired (e.g. LLM unconfigured path), or if the
// startup finishes before this goroutine starts, MCPStartupDone short-circuits
// and the event is emitted immediately. On shutdown the ctx is cancelled,
// unblocking the wait.
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
	if !backend.WebAccessMountAllowed(cfg, cfg.Experimental.Enabled) {
		log.Debug("web access sidecar not mounted: gate closed",
			"experimental_enabled", cfg.Experimental.Enabled,
			"web_access_enabled", cfg.WebAccess.Enabled)
		return
	}
	startTime := time.Now()
	if err := a.webAccessService.MountWebAccess(log); err != nil {
		log.Error("web access sidecar failed to start; continuing without it", "error", err)
		return
	}
	log.Info("startup phase complete", "phase", "web_access",
		"elapsed_ms", time.Since(startTime).Milliseconds())
}

// stopWebAccess stops the sidecar during Shutdown (behind the service's
// transition lock, so it
// cannot interleave with a SetWebAccessEnabled transition) before the judge
// drain and store close — a connected tab sees its stream end while the app
// winds down. The service's stop budget (~3s) converts a wedged graceful Shutdown
// into Server.Stop's force close; a failure is logged, never fatal (see
// specs/domains/web-access.md).
func (a *App) stopWebAccess(ctx context.Context) {
	if err := a.webAccessService.StopForShutdown(ctx); err != nil {
		a.log().Error("failed to stop the web access sidecar during shutdown", "error", err)
	}
}
