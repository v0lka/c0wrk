// Silent-mode step-limit autonomy.
//
// When security.silent_mode is enabled and its step_limit sub-policy is "auto",
// the blocking step-limit card must be replaced by an autonomous decision. This
// file resolves that decision: it gathers the host-side trajectory (the
// session emitter's recent-execution window: last steps + run counters + plan
// progress), enriches it with the boundary category and the task context, and
// asks the session-pinned loop judge (sp4rk tools.ToolJudge.JudgeStepLimit).
// The typed verdict is mapped onto agent.StepLimitResponse, failing CLOSED
// (deny) whenever silent+auto is on but the judge is missing or fails.
package backend

import (
	"context"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// stepLimitRecentEntries caps how many trailing execution-window entries are
// offered to the judge as the recent trajectory. The window itself is larger;
// the cap keeps the prompt compact.
const stepLimitRecentEntries = 16

// Compile-time guard: the desktop HITL adapter injects an interface with this
// exact signature. Declaring it here fails the build if the two ever drift.
var _ interface {
	ResolveSilentStepLimit(ctx context.Context, sessionID string, currentStep, maxSteps int, abortReason string) (agent.StepLimitResponse, string, bool)
} = (*Application)(nil)

// ResolveSilentStepLimit resolves a step-limit boundary autonomously when
// silent mode is enabled AND the step_limit sub-policy is "auto". It returns
// (response, reasoning, handled): handled is false when the boundary must
// follow the existing interactive path — silent mode off, sub-policy "stop",
// or no session registry — so the caller falls back to the blocking prompt
// (non-silent behavior is unchanged). When handled is true the decision is
// FINAL and every failure inside is fail-closed to StepLimitDeny.
//
// The trajectory provider is the session emitter's recent-execution window; the
// loop judge is the session-pinned ToolJudge carried by the session registry.
func (app *Application) ResolveSilentStepLimit(ctx context.Context, sessionID string, currentStep, maxSteps int, abortReason string) (agent.StepLimitResponse, string, bool) {
	registry := app.sessionRegistry(sessionID)
	if registry == nil {
		// No registry ⇒ the posture cannot be read. Do not invent autonomy:
		// fall back to the existing (human) path.
		return agent.StepLimitDeny, "", false
	}

	var win session.ExecutionWindowSnapshot
	if app.manager != nil {
		win, _ = app.manager.ExecutionWindow(sessionID)
	}
	req := buildStepLimitJudgeRequest(sdktools.TaskContextFrom(ctx), win, currentStep, maxSteps, abortReason)

	resp, reasoning, handled := resolveSilentStepLimitDecision(ctx, registry.AutonomyMode(), registry.SilentMode(), registry.GetJudge(), req)
	if handled {
		app.log().Info("silent-mode step-limit decision",
			"session_id", sessionID,
			"response", string(resp),
			"category", req.AbortCategory,
			"current_step", currentStep,
			"max_steps", maxSteps,
		)
		// Every automatic decision must leave an auditable, non-blocking trace
		// (OWASP ASI10). The step-limit boundary is answered without a human
		// here, so emit the autonomy_decision event carrying the verdict, the
		// boundary category and the deciding justification.
		if app.manager != nil {
			app.manager.EmitAutonomyDecision(sessionID, autonomyStepLimitDecisionData(req, resp, reasoning, registry.SilentMode().StepLimit))
		}
	}
	return resp, reasoning, handled
}

// autonomyDecisionKindStepLimit is the autonomy_decision kind for a step-limit
// boundary resolved autonomously (mirrors the tool registry's tool_confirm
// kind). LoopBoundary* categories reuse the sdktools constants.
const autonomyDecisionKindStepLimit = "step_limit"

// autonomyStepLimitDecisionData builds the auditable autonomy-decision record
// for a resolved step-limit boundary. The step-limit gate only resolves
// without a human in the silent posture, so Mode is pinned to silent and the
// step_limit sub-policy rides in Policy. Pure — no locks or I/O — so the
// emitted fields are unit-testable without a live Manager/Application.
func autonomyStepLimitDecisionData(req sdktools.StepLimitJudgeRequest, resp agent.StepLimitResponse, reasoning, policy string) coretools.AutonomyDecision {
	return coretools.AutonomyDecision{
		Kind:          autonomyDecisionKindStepLimit,
		Mode:          coretools.AutonomyModeSilent,
		Policy:        policy,
		Verdict:       string(resp),
		Reason:        req.AbortReason,
		Justification: reasoning,
		Category:      req.AbortCategory,
		CurrentStep:   req.CurrentStep,
		MaxSteps:      req.MaxSteps,
	}
}

// sessionRegistry resolves the SESSION-pinned tool registry for a session (the
// same one that holds the session-bound judge and the pushed silent-mode
// posture), or nil when the session is unknown or has no orchestrator yet.
func (app *Application) sessionRegistry(sessionID string) *coretools.ToolRegistry {
	if app.manager == nil || sessionID == "" {
		return nil
	}
	sess, ok := app.manager.GetSession(sessionID)
	if !ok || sess == nil {
		return nil
	}
	orch := sess.GetOrchestrator()
	if orch == nil {
		return nil
	}
	return orch.ToolRegistry()
}

// resolveSilentStepLimitDecision is the seam-free core of the resolver: given
// the session's autonomy mode and silent-mode sub-policies, its judge, and the
// assembled request, it decides the boundary. Split out from
// ResolveSilentStepLimit so the gate and the fail-closed policy are
// unit-testable without a live Manager/Application.
func resolveSilentStepLimitDecision(ctx context.Context, autonomyMode string, sm coretools.SilentModeState, judge *sdktools.ToolJudge, req sdktools.StepLimitJudgeRequest) (agent.StepLimitResponse, string, bool) {
	if autonomyMode != coretools.AutonomyModeSilent {
		return agent.StepLimitDeny, "", false
	}
	// A fixed sub-policy resolves the boundary deterministically (no judge).
	// "stop" is NOT a fixed resolution — it opts this gate out of silent mode
	// by keeping the interactive card — so it falls through below.
	if fixed, reasoning, ok := fixedStepLimitResponse(sm.StepLimit); ok {
		return fixed, reasoning, true
	}
	if sm.StepLimit != config.SilentStepLimitAuto {
		// "stop", or an empty/unknown value: keep the blocking card (fall back
		// to the unchanged interactive path).
		return agent.StepLimitDeny, "", false
	}
	// Silent + auto: the boundary must be decided without a human. From here
	// every failure is fail-closed DENY (handled = true), never a prompt.
	if judge == nil {
		return agent.StepLimitDeny, "step-limit judge unavailable in silent mode; stopping for safety", true
	}
	verdict, reasoning, err := judge.JudgeStepLimit(ctx, req)
	if err != nil {
		return agent.StepLimitDeny, "step-limit judge failed; stopping for safety", true
	}
	return stepLimitResponseFromVerdict(verdict), reasoning, true
}

// fixedStepLimitResponse maps a PINNED (non-auto) step_limit sub-policy to its
// deterministic response, so a fixed operator choice never consults the judge.
// It reports ok=false for "auto" (the judge decides) and for "stop"/"" (the
// boundary is not resolved without a human — the caller keeps the card).
func fixedStepLimitResponse(mode string) (agent.StepLimitResponse, string, bool) {
	reason := "step-limit boundary pinned to " + mode + " by security.silent_mode.step_limit.mode"
	switch mode {
	case config.SilentStepLimitAllowOnce:
		return agent.StepLimitAllowOnce, reason, true
	case config.SilentStepLimitAllowMore:
		return agent.StepLimitAllowMore, reason, true
	case config.SilentStepLimitAllowAlways:
		return agent.StepLimitAllowAlways, reason, true
	case config.SilentStepLimitDeny:
		return agent.StepLimitDeny, reason, true
	default:
		return agent.StepLimitDeny, "", false
	}
}

// buildStepLimitJudgeRequest assembles the loop judge's decision context from
// the host-side evidence: the task context, the boundary position and category
// (derived from the abort reason), and the execution-window trajectory (recent
// steps digest, run counters, plan progress). Pure — no locks, no I/O.
func buildStepLimitJudgeRequest(taskContext string, win session.ExecutionWindowSnapshot, currentStep, maxSteps int, abortReason string) sdktools.StepLimitJudgeRequest {
	category := sdktools.LoopBoundaryBudget
	if strings.TrimSpace(abortReason) != "" {
		category = sdktools.LoopBoundaryCircuitBreaker
	}
	return sdktools.StepLimitJudgeRequest{
		TaskContext:   taskContext,
		PlanSnapshot:  renderPlanSnapshot(win),
		CurrentStep:   currentStep,
		MaxSteps:      maxSteps,
		AbortCategory: category,
		AbortReason:   abortReason,
		RecentSteps:   recentStepDigests(win.Entries, stepLimitRecentEntries),
		Metrics: sdktools.StepLimitMetrics{
			Steps:            win.Counters.Steps,
			ToolCalls:        win.Counters.ToolCalls,
			ToolErrors:       win.Counters.ToolErrors,
			Nudges:           win.Counters.Nudges,
			Aborts:           win.Counters.Aborts,
			ParseErrors:      win.Counters.ParseErrors,
			InvalidToolCalls: win.Counters.InvalidToolCalls,
		},
	}
}

// renderPlanSnapshot renders the plan-progress view for the judge. Returns ""
// when the run has no plan (a standalone checklist or no plan at all), so the
// prompt omits the block entirely.
func renderPlanSnapshot(win session.ExecutionWindowSnapshot) string {
	if win.PlanTotal <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d plan steps complete", win.PlanDone, win.PlanTotal)
	if win.PlanStep != "" {
		fmt.Fprintf(&b, "; current step %s", win.PlanStep)
	}
	return b.String()
}

// recentStepDigests converts the trailing `limit` execution-window entries into
// the judge's step-digest shape (oldest first). Every entry carries its step
// index; tool calls contribute tool+args, tool results contribute result+error,
// and diagnostics contribute their event name — so the judge can see both the
// actions and the loop-detector signals.
func recentStepDigests(entries []session.ExecutionWindowEntry, limit int) []sdktools.StepLimitStepDigest {
	if len(entries) == 0 {
		return nil
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := make([]sdktools.StepLimitStepDigest, 0, len(entries))
	for _, e := range entries {
		d := sdktools.StepLimitStepDigest{Step: e.Step, IsError: e.IsError}
		switch e.Kind {
		case session.ExecWindowToolCallKind:
			d.Tool = e.Tool
			d.Args = e.Detail
		case session.ExecWindowDiagnosticKind:
			d.Result = "diagnostic: " + e.Detail
		default: // tool_result, step_complete
			d.Tool = e.Tool
			d.Result = e.Detail
		}
		out = append(out, d)
	}
	return out
}

// stepLimitResponseFromVerdict maps the typed loop verdict onto the executor's
// step-limit response enum. The zero/unknown verdict maps to deny (fail-closed).
func stepLimitResponseFromVerdict(v sdktools.LoopVerdict) agent.StepLimitResponse {
	switch v {
	case sdktools.LoopVerdictAllowOnce:
		return agent.StepLimitAllowOnce
	case sdktools.LoopVerdictAllowMore:
		return agent.StepLimitAllowMore
	case sdktools.LoopVerdictAllowAlways:
		return agent.StepLimitAllowAlways
	default:
		return agent.StepLimitDeny
	}
}
