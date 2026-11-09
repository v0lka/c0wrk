package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/sp4rk/agent"
)

// JudgeEvaluationBudget bounds one on-demand judge evaluation: the manual
// "Judge" action is a full LLM call against the session's own provider/model
// and must never outlive the user's attention span unbounded.
const JudgeEvaluationBudget = 120 * time.Second

// JudgeRequest is the transport-neutral input of one on-demand judge
// evaluation: the confirmation it settles and the tool-call context the judge
// re-reasons over.
type JudgeRequest struct {
	ConfirmID   string
	SessionID   string
	ToolName    string
	Input       json.RawMessage
	TaskContext string
}

// JudgeService owns the on-demand judge evaluation lifecycle: the in-flight
// claims that drop repeat frames and the evaluation call itself. It is
// transport-neutral — no App, no Wails context: the caller delivers the
// returned payload over its own transport (Wails event, web-access WS frame)
// and owns the pending-confirmation maps and the shutdown barrier.
// See specs/decisions/028-session-pinned-judge.md.
type JudgeService struct {
	// inFlight holds the confirm_ids with a live evaluation so repeat frames
	// (double-click, scripted client) are dropped instead of fanning out a
	// second full-budget LLM call that could only lose the response race.
	inFlight sync.Map
}

// Begin claims the confirm_id's in-flight slot (true = caller owns it and
// must release via Finish; false = already running, repeat dropped).
func (s *JudgeService) Begin(confirmID string) bool {
	_, loaded := s.inFlight.LoadOrStore(confirmID, struct{}{})
	return !loaded
}

// runJudgeEvaluation is the audit's P-10 anchor name (moved from
// desktop/event_handlers.go): the on-demand judge orchestration entry. It is
// transport-neutral — no App, no Wails context — and forwards to
// JudgeService.Run, which owns the 120s budget, the panic recovery and the
// session-pinned evaluation (ADR-028).
func runJudgeEvaluation(parentCtx context.Context, s *JudgeService, app *Application, req JudgeRequest, log *slog.Logger) session.JudgeResponsePayload {
	return s.Run(parentCtx, app, req, log)
}

// RunJudgeEvaluation forwards to runJudgeEvaluation for the desktop event
// adapter.
func RunJudgeEvaluation(parentCtx context.Context, s *JudgeService, app *Application, req JudgeRequest, log *slog.Logger) session.JudgeResponsePayload {
	return runJudgeEvaluation(parentCtx, s, app, req, log)
}

// Finish releases the claim taken by Begin.
func (s *JudgeService) Finish(confirmID string) {
	s.inFlight.Delete(confirmID)
}

// Run performs the on-demand judge call and returns the response payload for
// the caller to deliver over its own transport. Always recovers from panics
// into an error payload so the caller's goroutine cannot leak a failure into
// event-loop instability. The evaluation runs under JudgeEvaluationBudget,
// injects the session dump writer (judge-call observability), enriches the
// context with the session's security scope, and evaluates session-pinned
// (ADR-028) on the session's own provider/model — the same judge automatic
// escalations use.
func (s *JudgeService) Run(parentCtx context.Context, app *Application, req JudgeRequest, log *slog.Logger) (payload session.JudgeResponsePayload) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("judge: goroutine panicked", "confirm_id", req.ConfirmID, "panic", r)
			payload = session.JudgeResponsePayload{
				ConfirmID: req.ConfirmID,
				Error:     fmt.Sprintf("Internal error during judge evaluation: %v", r),
			}
		}
	}()

	log.Debug("judge: goroutine started", "confirm_id", req.ConfirmID, "tool", req.ToolName)

	if parentCtx == nil {
		parentCtx = context.Background()
	}
	judgeCtx, judgeCancel := context.WithTimeout(parentCtx, JudgeEvaluationBudget)
	defer judgeCancel()

	// Inject the session dump writer (judge-call observability), then enrich
	// the judge context with the session's security scope so the judge LLM
	// sees the session's directory scope (explicit + implicit work dirs).
	sess, sessOK := app.Manager().GetSession(req.SessionID)
	if sessOK {
		if dumpFile := sess.DumpFile(); dumpFile != nil {
			defer func() { _ = dumpFile.Close() }()
			judgeCtx = agent.WithDumpWriter(judgeCtx, dumpFile)
		}
	}
	judgeCtx = app.Manager().JudgeContext(judgeCtx, req.SessionID)

	payload = session.JudgeResponsePayload{ConfirmID: req.ConfirmID}

	// Session-pinned judge evaluation (ADR-028): the manual "Judge" action
	// evaluates on the session's own provider/model — the same judge
	// automatic escalations use. See specs/decisions/028-session-pinned-judge.md.
	_, reasoning, err := app.EvaluateJudgeForSession(judgeCtx, req.SessionID, req.ToolName, req.Input, req.TaskContext)
	if err != nil {
		log.Warn("judge: evaluation failed", "confirm_id", req.ConfirmID, "tool", req.ToolName, "error", err)
		payload.Error = fmt.Sprintf("Judge evaluation failed: %v", err)
		payload.Reasoning = reasoning
	} else {
		log.Debug("judge: evaluation completed", "confirm_id", req.ConfirmID, "tool", req.ToolName, "reasoning", reasoning)
		payload.Reasoning = reasoning
	}

	return payload
}
