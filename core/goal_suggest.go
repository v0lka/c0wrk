package core

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/oneshot"
)

// errGoalSuggestNoCaller is returned when the orchestrator has no LLM caller
// to classify with (direct-constructed test orchestrators); the gate
// fail-opens to no-suggestion.
var errGoalSuggestNoCaller = errors.New("goal suggest: no LLM caller")

// GoalSuggestion is the goal-mode suitability classifier's verdict for one
// fresh user message; suggest=true is honored only with explicit_criteria
// (the request must itself state a done-criterion) and the gate fail-closes
// otherwise. Contract: specs/domains/goal-mode.md (suggestion flow).
type GoalSuggestion struct {
	Suggest          bool   `json:"suggest"`
	ExplicitCriteria bool   `json:"explicit_criteria"`
	Reason           string `json:"reason"`
}

// GoalSuggester is the classifier seam: production rides the session's LLM
// caller through the shared serviceCall pipeline; a nil Orchestrator field
// resolves to it (mirroring the goalVerifier seam); tests inject a stub.
type GoalSuggester func(ctx context.Context, message string, routing *router.RoutingDecision) (GoalSuggestion, error)

// GoalSuggestionProposer bridges the post-routing suggestion gate to the
// desktop UI: emits a goal_suggestion event, renders a pending-action card,
// and BLOCKS until the user answers. Err (cancelled wait, UI unavailable,
// shutdown drain) fails the gate OPEN with no suppression; (false, nil) is a
// user decline and engages session-scoped suppression.
// specs/decisions/086-goal-mode-suggestion.md.
type GoalSuggestionProposer interface {
	ProposeGoalSuggestion(ctx context.Context, sessionID string, suggestion GoalSuggestion) (accepted bool, err error)
}

// goalSuggestGateOutcome classifies what the post-routing suggestion gate
// decided for one fresh task message.
type goalSuggestGateOutcome int

const (
	// gateNoSuggestion — the classifier was not consulted (preconditions),
	// judged the message not goal-suited, or failed; the plain path continues
	// and future suggestions stay armed.
	gateNoSuggestion goalSuggestGateOutcome = iota
	// gateAccepted — the user accepted the offer; the caller must enter the
	// goal loop with the already-established routing.
	gateAccepted
	// gateDeclined — the user explicitly declined; the session suppresses
	// further suggestions.
	gateDeclined
)

// parseGoalSuggestion extracts the classifier's JSON verdict. The zero value
// is a valid "no suggestion" (absent/malformed fields decode false), so a
// garbage answer fail-closes to no-offer; a response with no JSON at all
// still errors, which the caller fail-opens.
func parseGoalSuggestion(resp *llm.ChatResponse) (GoalSuggestion, error) {
	s, err := oneshot.ParseJSON[GoalSuggestion](resp)
	if err != nil {
		return GoalSuggestion{}, err
	}
	s.Reason = strings.TrimSpace(s.Reason)
	if !s.Suggest {
		s.Reason = ""
	}
	return s, nil
}

// goalSuggestRoutingContext renders the advisory routing line appended to the
// user message so the classifier sees the already-computed classification.
func goalSuggestRoutingContext(routing *router.RoutingDecision) string {
	if routing == nil {
		return "Routing classification (advisory): unavailable"
	}
	return "Routing classification (advisory): domain=" + routing.Domain +
		", complexity=" + strconv.Itoa(routing.Complexity) + "/5"
}

// suggestGoalModeWithCaller is the production classifier pass: one one-shot
// service call (ServiceKindGoalSuggest) over the embedded goal_suggest prompt.
// Errors are returned to the caller, which fails OPEN — the feature can never
// break a send.
func suggestGoalModeWithCaller(
	ctx context.Context,
	caller oneshot.Caller,
	metrics *ServiceMetrics,
	logger *slog.Logger,
	model string,
	message string,
	routing *router.RoutingDecision,
) (GoalSuggestion, error) {
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.GoalSuggest},
			{Role: "user", Content: "Request:\n" + message + "\n\n" + goalSuggestRoutingContext(routing)},
		},
		MaxTokens:       200,
		Temperature:     &oneshotTempGoalSuggest, // oneshot service policy — explicit value wins over any profile
		ReasoningEffort: serviceReasoningEffort(model, oneshotTierGoalSuggest),
		// Classification call — same budget class as routing itself.
		CallPurpose: llm.CallPurposeRouting,
	}
	return serviceCall(ctx, metrics, logger, ServiceKindGoalSuggest, model, caller, req, parseGoalSuggestion, oneshot.Options[GoalSuggestion]{})
}

// SuggestGoalMode runs the goal-mode suitability classifier for a fresh,
// already-routed user message; the verdict gates the pre-task activation
// offer (HandleMessage's post-routing gate). It rides the session's own LLM
// caller under the shared oneshot service pipeline; a nil goalSuggester seam
// resolves to the production implementation and a nil caller fails open.
// Gate contract: specs/domains/goal-mode.md (suggestion flow).
func (o *Orchestrator) SuggestGoalMode(ctx context.Context, message string, routing *router.RoutingDecision) (GoalSuggestion, error) {
	if fn := o.goalSuggester; fn != nil {
		return fn(ctx, message, routing)
	}
	if o.llm == nil {
		return GoalSuggestion{}, errGoalSuggestNoCaller
	}
	return suggestGoalModeWithCaller(ctx, o.llm, o.serviceMetrics, o.logger, bareActiveModel(o.modelSwitcher), message, routing)
}

// goalSuggestionApplicable reports the cheap, LLM-free preconditions of the
// post-routing suggestion gate (fresh plain-Conductor tasks only). The full
// precondition list: specs/decisions/086-goal-mode-suggestion.md (Decision 2).
func (o *Orchestrator) goalSuggestionApplicable(opts HandleOptions) bool {
	if opts.Goal || opts.ReviewMode || opts.TaskID != "" {
		return false
	}
	// The suggestion would offer a mode that is hard-blocked: Model Profiles
	// essential-tools narrowing refuses every goal entry (fresh and resumed),
	// so offering it would only produce a refusal after acceptance.
	if o.modelProfilesEssentialToolsEnabled() {
		return false
	}
	if !o.config.GoalLoop.Suggest.Enabled {
		return false
	}
	// Session-scoped suppression: the user already declined once on this
	// session (the orchestrator instance is per-session, so the flag's
	// lifetime IS the session's).
	if o.goalSuggestionDeclined.Load() {
		return false
	}
	return o.goalSuggestionProposer != nil
}

// goalSuggestRoutingEligible applies the advisory routing preconditions
// (domain ≠ general, complexity floor) — cost gates before the LLM call.
func (o *Orchestrator) goalSuggestRoutingEligible(routing *router.RoutingDecision) bool {
	if routing == nil {
		return false
	}
	if routing.Domain == router.DomainGeneral {
		return false
	}
	return routing.Complexity >= o.config.GoalLoop.Suggest.MinComplexity
}

// runGoalSuggestionGate runs the classifier and, on an obvious fit backed by
// explicit done-criteria, blocks on the user's answer. Fail-open contract
// (errors → gateNoSuggestion, suppression stays armed): an explicit decline
// is the only gateDeclined. specs/decisions/086-goal-mode-suggestion.md.
func (o *Orchestrator) runGoalSuggestionGate(ctx context.Context, sessionID, message string, routing *router.RoutingDecision) goalSuggestGateOutcome {
	suggestion, err := o.SuggestGoalMode(ctx, message, routing)
	if err != nil {
		o.logDebug("goal suggest: classifier failed — continuing without a suggestion", "error", err)
		return gateNoSuggestion
	}
	if !suggestion.Suggest {
		o.logDebug("goal suggest: classifier judged the message not goal-suited")
		return gateNoSuggestion
	}
	if !suggestion.ExplicitCriteria {
		// Fail closed (not a user decline — no suppression).
		o.logDebug("goal suggest: classifier suggested, but the request states no explicit done-criteria")
		return gateNoSuggestion
	}
	accepted, err := o.goalSuggestionProposer.ProposeGoalSuggestion(ctx, sessionID, suggestion)
	if err != nil {
		// Fail open, and NOT as a decline (no suppression).
		o.logDebug("goal suggest: proposal wait failed — continuing as a plain task", "error", err)
		return gateNoSuggestion
	}
	if !accepted {
		o.goalSuggestionDeclined.Store(true)
		o.logInfo("goal suggest: user declined — suppressing suggestions for this session")
		return gateDeclined
	}
	o.logInfo("goal suggest: user accepted — entering the goal loop with the established routing")
	return gateAccepted
}
