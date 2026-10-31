package desktop

import (
	"encoding/json"
	"log/slog"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/internal/webaccess"
)

// webWSInboundDispatcher returns the WSInbound handler mounted into the
// web-access sidecar — the browser-side twin of wireWailsEventListeners
// (specs/domains/web-access.md).
func (a *App) webWSInboundDispatcher(log *slog.Logger) func(*webaccess.WSConn, []byte) {
	// One emitter per dispatcher lifetime — the same buildUIEmitFunc product
	// the Wails listeners run with, rebuilt when the sidecar (re)mounts.
	uiEmit := a.buildUIEmitFunc()
	return func(_ *webaccess.WSConn, data []byte) {
		a.dispatchWebInboundFrame(data, uiEmit, log)
	}
}

// dispatchWebInboundFrame decodes one inbound WS frame into the shared HITL
// handlers; malformed, unnamed, or off-whitelist frames drop at Debug and
// the connection stays alive. The shape gate is inline — extractPayload's
// Warn logging would let network input spam the log (Wails keeps its Warns).
func (a *App) dispatchWebInboundFrame(data []byte, uiEmit func(session.Event), log *slog.Logger) {
	var env struct {
		Name string `json:"name"`
		Args []any  `json:"args"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		log.Debug("web inbound: malformed frame dropped", "error", err)
		return
	}

	// The whitelist mirrors wireWailsEventListeners one-to-one. JSON numbers
	// arrive as float64, which parseConfirmDecision already accepts.
	var dispatch func(map[string]any)
	switch env.Name {
	case backend.EventToolConfirmResponse:
		dispatch = func(payload map[string]any) { a.handleToolConfirmResponse(payload, log) }
	case backend.EventToolJudgeRequest:
		dispatch = func(payload map[string]any) { a.handleToolJudgeRequest(payload, uiEmit, log) }
	case backend.EventAskUserResponse:
		dispatch = func(payload map[string]any) { a.handleAskUserResponse(payload, log) }
	case backend.EventStepLimitResponse:
		dispatch = func(payload map[string]any) { a.handleStepLimitResponse(payload, log) }
	case backend.EventPlanApprovalResponse:
		dispatch = func(payload map[string]any) { a.handlePlanApprovalResponse(payload, log) }
	case backend.EventGoalProposalResponse:
		dispatch = func(payload map[string]any) { a.handleGoalProposalResponse(payload, log) }
	case backend.EventGoalSuggestionResponse:
		dispatch = func(payload map[string]any) { a.handleGoalSuggestionResponse(payload, log) }
	default:
		log.Debug("web inbound: event not allowed from the web dropped", "event", env.Name)
		return
	}

	// Shape contract of the shared handlers: exactly one argument, a JSON
	// object. The Wails path tolerates extra args at Warn; this path is
	// network input — strict and silent.
	if len(env.Args) != 1 {
		log.Debug("web inbound: payload dropped", "event", env.Name, "reason", "missing payload")
		return
	}
	payload, ok := env.Args[0].(map[string]any)
	if !ok {
		log.Debug("web inbound: payload dropped", "event", env.Name, "reason", "unexpected payload type")
		return
	}
	dispatch(payload)
}
