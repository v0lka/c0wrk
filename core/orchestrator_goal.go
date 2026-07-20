package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// deriveGoal runs a full-context Conductor pass whose only job is to derive a
// crisp {condition, verify} goal from the user's message and submit it for
// user sign-off via propose_goal. It reuses the entire Conductor toolset
// (read/search/probe tools) so the agent grounds the goal in the actual
// codebase, but swaps in the specialized derivation system prompt and injects
// a GoalProposer that captures the agent's proposal.
//
// The flow:
//  1. Wrap deps.goalProposer in a capturingProposer (records the agent's
//     proposal + the user's response) and inject it into the Conductor ctx.
//  2. Ensure the propose_goal tool is exposed to the agent.
//  3. Override the Conductor system prompt with the derivation prompt.
//  4. Run the Conductor (reusing all wiring — context injection, trajectory,
//     tool executor/registry).
//  5. Build a GoalState from the captured approved response (preferring the
//     user's edits over the agent's original wording).
//
// Returns the approved (possibly user-edited) GoalState, or an error if the
// agent never proposed, the user cancelled, or the run failed before a
// proposal.
func (o *Orchestrator) deriveGoal(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	deps conductorDeps,
) (*goal.GoalState, error) {
	if deps.goalProposer == nil {
		return nil, errors.New("deriveGoal: no goal proposer configured (deps.goalProposer is nil)")
	}

	// Wrap the proposer so the agent's {condition, verify} is captured for
	// building the GoalState after the run, even if the user edits the values.
	capturer := &capturingProposer{delegate: deps.goalProposer}
	ctx = tools.WithGoalProposer(ctx, capturer)

	// Ensure the propose_goal tool is exposed. It is registered as a builtin,
	// but the caller's availableTools list may not include it; the derivation
	// agent cannot complete its mission without it.
	availableTools = ensureProposeGoalTool(availableTools, deps.toolRegistry)

	// Swap the standard orchestrator system prompt for the derivation prompt
	// while KEEPING the shared project-context prefix (family overlay,
	// verification, injection defense, workspace, work dirs, environment,
	// AGENTS.md, active skills, vector hints) so the derivation agent sees the
	// same project context a normal Conductor run does. All other Conductor
	// wiring (context injection, trajectory, toolset) is reused via
	// RunConductor — derivation is a Conductor run with a different instruction
	// set, not a separate engine.
	deps.systemPromptOverride = func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		return buildSpecializedSystemPrompt(ctx, msg, modelMeta, prompts.GoalDerivation)
	}
	// Bound the derivation loop independently of routing complexity.
	deps.resumeSteps = nil // derivation never resumes from a checkpoint

	result, err := RunConductor(ctx, message, bb, availableTools, deps, "")
	if o.logger != nil {
		switch {
		case err != nil:
			o.logger.Debug("goal derivation conductor run returned error", "error", err)
		case result != nil:
			o.logger.Debug("goal derivation conductor run completed", "status", result.Status)
		}
	}

	// The conductor run may succeed or fail; what matters for the goal is
	// whether the agent called propose_goal and the user approved. Read the
	// captured proposal+response regardless of the conductor outcome.
	proposal, response, proposed := capturer.outcome()
	if !proposed {
		if err != nil {
			return nil, fmt.Errorf("deriveGoal: agent did not propose a goal and the conductor run failed: %w", err)
		}
		return nil, errors.New("deriveGoal: agent completed without calling propose_goal")
	}

	gs, gerr := buildGoalState(proposal, response, time.Now())
	if gerr != nil {
		// Surface the conductor error too if both failed, so the caller sees
		// why the run ended as well as why the goal was rejected.
		if err != nil {
			return nil, fmt.Errorf("%w (conductor run also errored: %w)", gerr, err)
		}
		return nil, gerr
	}
	return gs, nil
}

// capturingProposer wraps a GoalProposer and records the agent's proposal and
// the user's response so deriveGoal can reconstruct the approved goal after the
// Conductor run ends. It is concurrency-safe: the propose_goal tool may be
// called at most once per derivation, but the guard keeps it correct under any
// re-entrancy.
type capturingProposer struct {
	delegate tools.GoalProposer

	mu       sync.Mutex
	proposal tools.GoalProposal
	response tools.GoalProposalResponse
	captured bool
}

// Propose records the agent's proposal, delegates to the real proposer (which
// blocks for the desktop approval flow), then records the response.
func (c *capturingProposer) Propose(ctx context.Context, p tools.GoalProposal) (tools.GoalProposalResponse, error) {
	c.mu.Lock()
	c.proposal = p
	c.mu.Unlock()

	resp, err := c.delegate.Propose(ctx, p)

	c.mu.Lock()
	c.response = resp
	c.captured = true
	c.mu.Unlock()
	return resp, err
}

// outcome returns the captured proposal and response, plus whether propose_goal
// was ever called.
func (c *capturingProposer) outcome() (tools.GoalProposal, tools.GoalProposalResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proposal, c.response, c.captured
}

// buildGoalState maps an approved goal proposal + the user's response into a
// GoalState. On "approve" the returned GoalState prefers the user's edited
// condition/verify over the agent's original wording (edits arrive populated
// in the response; unedited fields fall back to the proposal). On "cancel" or
// any non-approve decision it returns an error.
//
// The budget is left unlimited here; the turn cap from config is applied when
// the goal is activated (turn 1), not at derivation time — derivation only
// decides WHAT the goal is, not how much it may cost.
func buildGoalState(proposal tools.GoalProposal, resp tools.GoalProposalResponse, now time.Time) (*goal.GoalState, error) {
	switch resp.Decision {
	case "approve":
		// Prefer the user's edits; fall back to the agent's original wording
		// when the response omits a field (user approved without editing it).
		condition := resp.Condition
		if condition == "" {
			condition = proposal.Condition
		}
		verify := resp.Verify
		if verify == "" {
			verify = proposal.Verify
		}
		return &goal.GoalState{
			Condition:    condition,
			VerifyClause: verify,
			Budget:       goal.GoalBudget{}, // unlimited; caps applied at activation
			Status:       goal.StatusActive,
			CreatedAt:    now,
		}, nil
	case "cancel":
		return nil, errors.New("goal derivation cancelled by user")
	case "clarify":
		// Reaching here means the Conductor run ended on a clarification
		// without ever resolving to an approval — the goal could not be
		// finalized from the available back-and-forth.
		return nil, fmt.Errorf("goal derivation ended on a clarification without approval: %s", resp.Clarification)
	default:
		return nil, fmt.Errorf("goal derivation: unknown proposer decision %q", resp.Decision)
	}
}

// ensureProposeGoalTool guarantees the propose_goal tool descriptor is present
// in the availableTools list. If the caller already included it, the list is
// returned unchanged. Otherwise the descriptor is rebuilt from the tool
// registry (where it is registered as a builtin) and appended. If the tool is
// not registered at all, the list is returned unchanged and the agent will get
// a clear "no goal proposer in context" error if it somehow invokes the tool.
func ensureProposeGoalTool(list []sdktools.ToolDescriptor, registry *sdktools.ToolRegistry) []sdktools.ToolDescriptor {
	for _, t := range list {
		if t.Name == "propose_goal" {
			return list
		}
	}
	if registry != nil {
		if t, ok := registry.Get("propose_goal"); ok {
			return append(list, sdktools.ToolDescriptor{
				Name:        t.Name(),
				Description: t.Description(),
				InputSchema: t.InputSchema(),
				Source:      "core",
			})
		}
	}
	return list
}

// ----------------------------------------------------------------------------
// Multi-turn goal loop (runGoalLoop)
// ----------------------------------------------------------------------------

// goalLoopMaxTurns is the hard ceiling on goal-loop iterations applied when no
// budget override sets MaxTurns. It is a safety net against an accidental
// infinite loop, not a user-facing default — a goal without an explicit turn
// cap is "unlimited" per the goal.GoalBudget contract, but no loop is truly
// infinite.
const goalLoopMaxTurns = 50

// runGoalLoop is the multi-turn goal-mode driver. It is entered from
// HandleMessage when opts.Goal is set — on BOTH a fresh task (TaskID == "")
// and on a continuation (TaskID != ""). The single-flight guard in
// HandleMessage is already held, so the loop runs exclusively.
//
// Flow:
//  0. Route and activate skills via routeOrContinue: on a continuation it
//     reuses the restored routing decision (mirroring the normal continuation
//     fast-path), and on a fresh task it runs the full router. Enrich ctx with
//     domain/complexity/user-skills and persist the routing. Derivation and
//     every turn inherit this routing.
//  1. Derive the goal (deriveGoal): a full-context Conductor pass that grounds
//     the {condition, verify} in the actual codebase and submits it for user
//     sign-off via propose_goal. On cancel/error, the loop exits with the
//     original message as output.
//  2. Resolve the budget: opts.GoalBudgetOverride sets MaxTurns when present
//     (otherwise unlimited). Persist the active GoalState.
//  3. Install the pause signal (PauseGoal flips the atomic; the loop polls it).
//  4. Iterate turns while Status == active:
//     - top of turn: if paused → Status=paused, break (release single-flight).
//     - run one turn via the turn runner (one Conductor turn over the
//     already-enriched ctx — routing was established once in step 0 and is
//     inherited unchanged by every turn, including turn 1; no turn re-routes),
//     wrapped in counting proxies so the loop learns the turn's tool-call count.
//     - read the verdict sink: a "met" verdict → Status=met, break.
//     - anti-spin: a turn that made ZERO tool calls AND declared no verdict →
//     Status=blocked_idle, break (the agent is stuck/idling).
//     - budget: increment the turn counter; if MaxTurns is hit →
//     Status=exhausted, break.
//     - emit goal_progress (turn/budget) and goal_status events.
//  5. Return a HandleResult carrying the final goal outcome.
func (o *Orchestrator) runGoalLoop(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
) (*HandleResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 0. Route and activate skills. On a continuation (opts.TaskID != "") this
	// reuses the restored routing via routeOrContinue's fast-path (the router is
	// blind to the restored plan and would misclassify a continuation message);
	// on a fresh task it runs the full router. The router augments the skill
	// context itself, so the RAW `message` is passed (not the
	// attachment-augmented conductorMessage) to avoid double-prefixing the
	// skill reference.
	ctx, routing, _, _, err := o.routeOrContinue(ctx, message, opts, bb, availableTools)
	if err != nil {
		return nil, err
	}

	// Enrich context with domain/complexity/user-skills (mirrors HandleMessage).
	// routeAndActivateSkills already set WithActiveSkills internally; this ctx
	// now flows into deriveGoal AND every goal turn.
	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// Augment the Conductor's task message with the session's attached files.
	// `message` is used for routing above; the augmented conductorMessage is
	// used for deriveGoal, runGoalTurns, and the derivation-failure fallback.
	conductorMessage := o.augmentWithAttachments(message, bb)

	// Persist the real routing decision so finalizeResult and resume see it.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	// Derive the goal. On error/cancel, surface the conductor message as the
	// output so the user sees their request acknowledged, not an empty result.
	gs, derr := o.deriveGoal(ctx, conductorMessage, bb, availableTools, o.buildConductorDeps(nil, nil))
	if derr != nil {
		o.logInfo("goal_loop: derivation failed, exiting", "error", derr)
		if errors.Is(derr, context.Canceled) || errors.Is(derr, context.DeadlineExceeded) {
			return nil, derr
		}
		// A user cancel of the proposal is a clean exit, not an error to bubble.
		return o.goalLoopResult(conductorMessage, bb, nil, goal.StatusActive, ""), nil
	}

	// Resolve the budget: the per-message override sets MaxTurns when present;
	// otherwise the goal is unlimited (the only cap is the goalLoopMaxTurns
	// hard ceiling). There are no config-level defaults now — the budget is
	// turn-only.
	gs.Budget = resolveGoalBudget(opts.GoalBudgetOverride)
	if gs.CreatedAt.IsZero() {
		gs.CreatedAt = time.Now()
	}
	gs.Status = goal.StatusActive
	o.logInfo("goal_loop: goal activated", "condition", gs.Condition, "verify", gs.VerifyClause, "budget", gs.Budget)

	// Pause signal: installed for the duration of this loop and cleared on
	// exit so a stale signal cannot pause a future request.
	pause := &atomic.Bool{}
	o.activeGoalPause.Store(pause)
	defer func() { o.activeGoalPause.Store(nil) }()

	// Truncate conversation history once; the trajectory accumulates across
	// turns via the blackboard so the agent keeps dialogue context.
	conversationHistory := truncateHistory(o.conversationHistory, o.config.ConductorHistoryWindow)

	turnRunner := o.goalTurnRunner
	if turnRunner == nil {
		turnRunner = o.defaultGoalTurnRunner
	}

	gs = o.runGoalTurns(ctx, conductorMessage, bb, availableTools, plansDir, conversationHistory, gs, pause, turnRunner)

	o.persistGoalStateBestEffort(bb, gs)
	out := message
	if gs.LastVerdict != nil && gs.LastVerdict.Reason != "" {
		out = gs.LastVerdict.Reason
	}
	return o.goalLoopResult(out, bb, routing, gs.Status, gs.Condition), nil
}

// resumeGoalLoop re-enters the goal loop from a persisted, non-terminal
// GoalState (paused or still active). It mirrors runGoalLoop's post-derivation
// body but skips deriveGoal — the goal condition and verify clause are already
// known — and seeds the prior trajectory (resumeSteps) into the executor on the
// first turn so the resumed run continues the step counter/history from the
// checkpoint rather than starting fresh.
//
// A paused goal is re-activated so runGoalTurns's `for gs.Status == active`
// guard enters the loop; terminal statuses must never reach here (Resume guards
// on IsTerminal before delegating).
func (o *Orchestrator) resumeGoalLoop(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	routing *router.RoutingDecision,
	gs *goal.GoalState,
	resumeSteps []agent.Step,
) (*HandleResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Re-activate a paused goal so the turn loop continues. Active goals are
	// already in the right state; terminal ones never reach here.
	gs.Status = goal.StatusActive
	if gs.CreatedAt.IsZero() {
		gs.CreatedAt = time.Now()
	}
	o.logInfo("goal_loop: resuming", "condition", gs.Condition, "verify", gs.VerifyClause, "turn", gs.TurnCount, "budget", gs.Budget)

	// Pause signal for the duration of this resumed loop.
	pause := &atomic.Bool{}
	o.activeGoalPause.Store(pause)
	defer func() { o.activeGoalPause.Store(nil) }()

	conversationHistory := truncateHistory(o.conversationHistory, o.config.ConductorHistoryWindow)

	// Reuse the persisted routing decision (default to general when absent).
	if routing == nil {
		routing = &router.RoutingDecision{Domain: "general", Complexity: defaultResumeComplexity}
	}
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	turnRunner := o.goalTurnRunner
	if turnRunner == nil {
		turnRunner = o.defaultGoalTurnRunner
	}

	// Wrap the turn runner so the FIRST resumed turn seeds the prior trajectory
	// into the executor deps. The turn counter continues from gs.TurnCount (not
	// reset to 1), so a once-flag seeds exactly the first invocation. Subsequent
	// turns rely on the Conductor's own accumulated trajectory (the same
	// convention runGoalLoop uses).
	seed := resumeSteps
	seeded := false
	wrapped := func(ctx context.Context, turn int, msg string, b orchestration.Blackboard, tl []sdktools.ToolDescriptor, pd string, hist []llm.Message, deps conductorDeps) (int, *orchestration.ExecutionResult, error) {
		if !seeded && len(seed) > 0 {
			deps.resumeSteps = seed
			seeded = true
		}
		return turnRunner(ctx, turn, msg, b, tl, pd, hist, deps)
	}

	gs = o.runGoalTurns(ctx, message, bb, availableTools, plansDir, conversationHistory, gs, pause, wrapped)

	o.persistGoalStateBestEffort(bb, gs)
	out := message
	if gs.LastVerdict != nil && gs.LastVerdict.Reason != "" {
		out = gs.LastVerdict.Reason
	}
	return o.goalLoopResult(out, bb, routing, gs.Status, gs.Condition), nil
}

// persistGoalStateBestEffort persists the goal state for the current task so a
// paused/active goal survives app restart. It is best-effort: the task store or
// task ID may be absent (e.g. tests, non-persistent sessions), in which case it
// is a no-op. A persistence failure is logged but never propagates — losing the
// goal-state checkpoint degrades only resumability, not the current run.
func (o *Orchestrator) persistGoalStateBestEffort(bb orchestration.Blackboard, gs *goal.GoalState) {
	if o.taskStore == nil || gs == nil {
		return
	}
	pbb, ok := bb.(PersistableBlackboard)
	if !ok {
		return
	}
	taskID := pbb.TaskID()
	if taskID == "" {
		return
	}
	if err := o.taskStore.PersistGoalState(taskID, gs); err != nil {
		o.logDebug("goal_loop: failed to persist goal state (best-effort)", "taskID", taskID, "error", err)
	}
}

// runGoalTurns is the turn-iteration core of the goal loop, extracted so it can
// be unit-tested with a mock turn runner and a pre-built GoalState (bypassing
// the LLM-driven deriveGoal). It mutates and returns gs.
//
// Invariants per turn:
//   - pause signal (top of turn) → Status=paused, break.
//   - run one turn via turnRunner (wrapped in counting proxies so the loop
//     learns the turn's tool-call count).
//   - verdict sink "met" → Status=met, break.
//   - anti-spin: zero tool calls AND no verdict → Status=blocked_idle, break.
//   - budget (MaxTurns) or hard ceiling hit → Status=exhausted, break.
//   - emit goal_progress + goal_status each iteration.
func (o *Orchestrator) runGoalTurns(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	conversationHistory []llm.Message,
	gs *goal.GoalState,
	pause *atomic.Bool,
	turnRunner func(ctx context.Context, turn int, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, deps conductorDeps) (toolCallCount int, result *orchestration.ExecutionResult, err error),
) *goal.GoalState {
	for gs.Status == goal.StatusActive {
		// Pause check (top of turn): a paused goal suspends the loop and
		// releases the single-flight lock so the user can Resume later.
		if pause.Load() {
			gs.Status = goal.StatusPaused
			o.logInfo("goal_loop: paused by signal", "turn", gs.TurnCount)
			o.emitGoalStatus(ctx, gs)
			break
		}
		if ctx.Err() != nil {
			gs.Status = goal.StatusPaused
			break
		}

		turn := gs.TurnCount + 1
		o.logInfo("goal_loop: starting turn", "turn", turn)

		// Build per-turn deps with a fresh verdict sink and counting wrappers.
		sink := &memGoalStatusSink{}
		deps := o.buildConductorDeps(conversationHistory, nil)
		// Count tool calls for this turn only (anti-spin detection).
		counter := &turnUsageCounter{}
		deps.toolExec = &countingToolExec{inner: deps.toolExec, counter: counter}

		toolCalls, execResult, terr := turnRunner(
			tools.WithGoalStatusSink(WithGoalState(ctx, gs), sink),
			turn, message, bb, availableTools, plansDir, conversationHistory, deps,
		)
		gs.TurnCount = turn

		// Read the agent's verdict (nil if declare_goal_status was not called).
		if v := sink.Last(); v != nil {
			gs.LastVerdict = v
			if v.Status == "met" {
				gs.Status = goal.StatusMet
				o.logInfo("goal_loop: goal met", "turn", turn, "evidence", len(v.Evidence))
				o.emitGoalStatus(ctx, gs)
				break
			}
			if v.Status == "blocked" {
				gs.Status = goal.StatusBlockedIdle
				o.logInfo("goal_loop: agent declared blocked", "turn", turn, "reason", v.Reason)
				o.emitGoalStatus(ctx, gs)
				break
			}
			// "not_met" (or any other): keep iterating.
		}

		// Anti-spin: a turn that made NO tool calls AND declared no verdict is
		// idle — the agent is stuck and further turns would likely repeat the
		// same non-action. Halt as blocked_idle rather than spinning.
		if toolCalls == 0 && sink.Last() == nil {
			gs.Status = goal.StatusBlockedIdle
			o.logInfo("goal_loop: blocked_idle (zero tool calls, no verdict)", "turn", turn)
			o.emitGoalStatus(ctx, gs)
			break
		}

		// Budget checks. A hit transitions to exhausted (terminal failure).
		if gs.Budget.MaxTurns > 0 && gs.TurnCount >= gs.Budget.MaxTurns {
			gs.Status = goal.StatusExhausted
			o.logInfo("goal_loop: turn budget exhausted", "turn", gs.TurnCount, "max", gs.Budget.MaxTurns)
			o.emitGoalStatus(ctx, gs)
			break
		}

		// Hard safety ceiling for "unlimited" budgets (MaxTurns == 0). It does
		// NOT override an explicitly-set MaxTurns — the override contract ("the
		// override wins for any field it sets") means a caller that sets
		// MaxTurns > goalLoopMaxTurns is entitled to that many turns. The
		// ceiling only guards the no-cap case so no loop is truly infinite.
		if gs.Budget.MaxTurns == 0 && gs.TurnCount >= goalLoopMaxTurns {
			gs.Status = goal.StatusExhausted
			o.logInfo("goal_loop: hard turn ceiling hit", "turn", gs.TurnCount, "ceiling", goalLoopMaxTurns)
			o.emitGoalStatus(ctx, gs)
			break
		}

		o.emitGoalProgress(ctx, gs)
		o.emitGoalStatus(ctx, gs)
		_ = terr // a turn error does not abort the loop; the agent may recover next turn
		_ = execResult
	}
	return gs
}

// defaultGoalTurnRunner runs ONE turn of the goal loop via the real Conductor.
// Routing and active skills are established once by runGoalLoop (before the
// turn loop begins) and inherited by every turn; the runner therefore just
// runs one Conductor turn over the already-enriched ctx (turn 1 like any other
// turn). The Conductor's conversation history accumulates across turns via the
// blackboard trajectory, so dialogue context is preserved.
//
// It reports the number of tool calls the turn made by reading the counting
// wrappers installed in deps AFTER the run completes. The counter is shared
// between the wrapper and this reader via the deps struct: runGoalTurns
// installs a fresh turnUsageCounter (starting at zero) each turn, and
// countingToolExec increments it only as the Conductor executes tools — so the
// count must be read AFTER RunConductor returns, not before.
func (o *Orchestrator) defaultGoalTurnRunner(
	ctx context.Context,
	turn int,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	conversationHistory []llm.Message,
	deps conductorDeps,
) (int, *orchestration.ExecutionResult, error) {
	result, err := RunConductor(ctx, message, bb, availableTools, deps, plansDir)
	if result == nil {
		result = &orchestration.ExecutionResult{}
	}

	// Read the tool-call count AFTER the run so it reflects the turn's actual
	// usage. The wrapper is always present in the goal loop (runGoalTurns
	// installs it); the guard keeps the runner correct if called standalone.
	toolCalls := 0
	if ce, ok := deps.toolExec.(*countingToolExec); ok {
		toolCalls = ce.counter.toolCalls
	}
	return toolCalls, result, err
}

// resolveGoalBudget applies the optional per-message override. The budget is
// turn-only: when the override sets MaxTurns to a non-zero value it is used; a
// nil override (or MaxTurns == 0) means unlimited. There are no config-level
// defaults — the only safety net for an unlimited goal is the goalLoopMaxTurns
// hard ceiling.
func resolveGoalBudget(override *goal.GoalBudget) goal.GoalBudget {
	if override == nil {
		return goal.GoalBudget{}
	}
	if override.MaxTurns > 0 {
		return goal.GoalBudget{MaxTurns: override.MaxTurns}
	}
	return goal.GoalBudget{}
}

// goalLoopResult builds a HandleResult summarizing the goal loop's outcome.
// The status is encoded into the HandleResult.Status where it maps cleanly
// (met→success; paused/blocked_idle→partial; exhausted→failed); non-mappable
// statuses default to success for met and partial otherwise.
func (o *Orchestrator) goalLoopResult(output string, bb orchestration.Blackboard, routing *router.RoutingDecision, status goal.GoalStatus, condition string) *HandleResult {
	execResult := &orchestration.ExecutionResult{Output: output}
	switch status {
	case goal.StatusMet:
		execResult.Status = orchestration.ExecutionStatusSuccess
	case goal.StatusExhausted:
		execResult.Status = orchestration.ExecutionStatusFailed
	default: // paused, blocked_idle, active (derivation path)
		execResult.Status = orchestration.ExecutionStatusPartial
	}
	return o.finalizeResult(bb, routing, execResult)
}

// emitGoalStatus emits a goal_status service event carrying the current goal
// state snapshot. Uses ServiceWithMeta (the existing generic channel) rather
// than adding new Emitter methods, so the frontend can subscribe to
// {"phase":"goal_status", ...} without a core Emitter-interface change.
func (o *Orchestrator) emitGoalStatus(_ context.Context, gs *goal.GoalState) {
	meta := map[string]any{
		"phase":     "goal_status",
		"status":    string(gs.Status),
		"turn":      gs.TurnCount,
		"condition": gs.Condition,
		"max_turns": gs.Budget.MaxTurns,
	}
	if gs.LastVerdict != nil {
		meta["verdict"] = gs.LastVerdict.Status
		meta["reason"] = gs.LastVerdict.Reason
	}
	o.emitter.ServiceWithMeta("Goal status: "+string(gs.Status), meta)
}

// emitGoalProgress emits a goal_progress service event with turn/budget
// telemetry, emitted mid-loop (after a non-terminal turn) so the frontend can
// show live progress toward the budget.
func (o *Orchestrator) emitGoalProgress(_ context.Context, gs *goal.GoalState) {
	o.emitter.ServiceWithMeta(
		fmt.Sprintf("Goal turn %d complete", gs.TurnCount),
		map[string]any{
			"phase":     "goal_progress",
			"turn":      gs.TurnCount,
			"max_turns": gs.Budget.MaxTurns,
			"condition": gs.Condition,
		},
	)
}

// PauseGoal signals the currently-running goal loop (if any) to pause at the
// top of its next turn. It is a no-op when no goal loop is active. The pause is
// cooperative: the loop polls the signal and transitions Status→paused, then
// releases the single-flight lock so a later Resume can re-enter.
func (o *Orchestrator) PauseGoal() {
	if p := o.activeGoalPause.Load(); p != nil {
		p.Store(true)
	}
}

// ----------------------------------------------------------------------------
// Counting wrappers (per-turn tool-call + token usage)
// ----------------------------------------------------------------------------

// turnUsageCounter tallies the tool calls of a single goal-loop turn. It is
// shared with the countingToolExec wrapper installed for that turn, then read
// by the loop after the turn completes for the anti-spin (idle-turn) check.
type turnUsageCounter struct {
	toolCalls int
}

// countingToolExec wraps an agent.ToolExecutor, incrementing a counter on each
// Execute call so the goal loop can detect an idle (zero-tool-call) turn — the
// anti-spin condition. All other methods delegate unchanged.
type countingToolExec struct {
	inner   agent.ToolExecutor
	counter *turnUsageCounter
}

func (c *countingToolExec) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	c.counter.toolCalls++
	return c.inner.Execute(ctx, name, input)
}
func (c *countingToolExec) GetToolSource(name string) string { return c.inner.GetToolSource(name) }
func (c *countingToolExec) IsToolUntrusted(name string) bool { return c.inner.IsToolUntrusted(name) }
func (c *countingToolExec) CacheStrategy(ctx context.Context, name string, input json.RawMessage) sdktools.CacheMode {
	return c.inner.CacheStrategy(ctx, name, input)
}

// ----------------------------------------------------------------------------
// Goal status sink (in-memory implementation)
// ----------------------------------------------------------------------------

// memGoalStatusSink is the concrete GoalStatusSink used by the goal loop. It
// holds the most recent verdict behind a mutex (declare_goal_status may be
// called at most once per turn, but the guard keeps it correct under any
// re-entrancy). Last returns nil until a verdict is declared.
type memGoalStatusSink struct {
	mu      sync.Mutex
	verdict *goal.Verdict
}

func (s *memGoalStatusSink) Declare(v goal.Verdict) {
	s.mu.Lock()
	s.verdict = &v
	s.mu.Unlock()
}

func (s *memGoalStatusSink) Last() *goal.Verdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verdict
}

// compile-time assertions that the wrappers satisfy their interfaces.
var (
	_ agent.ToolExecutor   = (*countingToolExec)(nil)
	_ tools.GoalStatusSink = (*memGoalStatusSink)(nil)
)
