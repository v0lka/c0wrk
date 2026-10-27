package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrGoalBlockedByModelProfiles is returned when a goal request — fresh
// (HandleMessage) or resumed (resumeGoalLoop) — is attempted while the Model
// Profiles essential-tools narrowing is active: goal mode is refused rather
// than run narrowed. The session API rejects the same requests earlier; see
// specs/domains/goal-mode.md.
var ErrGoalBlockedByModelProfiles = errors.New("goal mode is unavailable while the Model Profiles essential-tools profile is active")

// deriveGoal runs a full-context Conductor pass that derives a crisp
// {condition, verify} goal from the user's message and submits it for user
// sign-off via propose_goal — a Conductor run with a different instruction
// set, not a separate engine. Returns the approved (possibly user-edited)
// GoalState, or an error if the agent never proposed, the user cancelled, or
// the run failed before a proposal. See specs/domains/goal-mode.md.
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

	capturer := &capturingProposer{delegate: deps.goalProposer}
	ctx = tools.WithGoalProposer(ctx, capturer)

	availableTools = ensureProposeGoalTool(availableTools, deps.toolRegistry)

	deps.systemPromptOverride = func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		return buildSpecializedSystemPromptWithLite(ctx, msg, modelMeta,
			prompts.GoalDerivation, prompts.GoalDerivationLite)
	}
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

	// What matters is whether the agent called propose_goal and the user
	// approved — read the captured outcome regardless of the conductor result.
	proposal, response, proposed := capturer.outcome()
	if !proposed {
		if err != nil {
			return nil, fmt.Errorf("deriveGoal: agent did not propose a goal and the conductor run failed: %w", err)
		}
		return nil, errors.New("deriveGoal: agent completed without calling propose_goal")
	}

	gs, gerr := buildGoalState(proposal, response, time.Now())
	if gerr != nil {
		// Surface the conductor error too when both failed, so the caller sees
		// why the run ended as well as why the goal was rejected.
		if err != nil {
			return nil, fmt.Errorf("%w (conductor run also errored: %w)", gerr, err)
		}
		return nil, gerr
	}
	return gs, nil
}

// errGoalDerivationCancelled is returned by buildGoalState when the user
// cancels the propose_goal card. It is a CONTROLLED outcome, not a derivation
// failure: match it with errors.Is and end the task cleanly — never fall back
// to executing the task by other means. See specs/domains/goal-mode.md.
var errGoalDerivationCancelled = errors.New("goal derivation cancelled by user")

// derivationFailureOutcome classifies how the goal loop must treat a
// derivation error — the fork the suggested-entry path introduced, kept out
// of runGoalLoopRouted so both entry modes stay honest about one behavior: a
// USER CANCEL is always a clean exit, in BOTH entry modes. See
// specs/domains/goal-mode.md (suggested activation).
type derivationFailureOutcome int

const (
	derivationBubble        derivationFailureOutcome = iota // the wait was interrupted (context cancelled / deadline exceeded); the error bubbles
	derivationCleanExit                                     // end the task with the request acknowledged as the output (user cancel, or a genuine failure on the explicit entry)
	derivationFallbackPlain                                 // suggested entry + a genuine failure: leave the result unbuilt so HandleMessage falls back to the plain Conductor path
)

// classifyDerivationFailure maps a derivation error onto the outcome. The
// user-cancel sentinel is checked BEFORE the suggested-entry fallback so a
// cancel can never ride the fallback into a plain execution.
func classifyDerivationFailure(derr error, fallbackOnDeriveFailure bool) derivationFailureOutcome {
	switch {
	case errors.Is(derr, context.Canceled) || errors.Is(derr, context.DeadlineExceeded):
		return derivationBubble
	case errors.Is(derr, errGoalDerivationCancelled):
		return derivationCleanExit
	case fallbackOnDeriveFailure:
		return derivationFallbackPlain
	default:
		return derivationCleanExit
	}
}

// capturingProposer wraps a GoalProposer and records the agent's proposal and
// the user's response so deriveGoal can rebuild the approved goal after the
// run; see specs/domains/goal-mode.md. propose_goal is called at most once per
// derivation; the mutex is re-entrancy insurance.
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
// GoalState: "approve" prefers the user's edits over the agent's wording,
// "cancel" returns errGoalDerivationCancelled (a controlled outcome — see its
// doc), anything else is an untyped error. The budget stays unlimited — the
// turn cap is applied at activation, not derivation. See specs/domains/goal-mode.md.
func buildGoalState(proposal tools.GoalProposal, resp tools.GoalProposalResponse, now time.Time) (*goal.GoalState, error) {
	switch resp.Decision {
	case "approve":
		condition := resp.Condition
		if condition == "" {
			condition = proposal.Condition
		}
		verify := resp.Verify
		if verify == "" {
			verify = proposal.Verify
		}
		// Fall back to the agent-proposed mode, then normalize so the stored
		// value is always canonical even when both are empty.
		verificationMode := resp.VerificationMode
		if verificationMode == "" {
			verificationMode = proposal.VerificationMode
		}
		verificationMode, err := goal.NormalizeVerificationMode(verificationMode)
		if err != nil {
			// Unknown mode — default gracefully instead of erroring: propose_goal
			// validates at proposal time, so only future values land here.
			verificationMode = goal.VerificationModeExecutable
		}
		return &goal.GoalState{
			Condition:        condition,
			VerifyClause:     verify,
			VerificationMode: verificationMode,
			Budget:           goal.GoalBudget{}, // unlimited; caps applied at activation
			Status:           goal.StatusActive,
			CreatedAt:        now,
		}, nil
	case "cancel":
		return nil, errGoalDerivationCancelled
	default:
		return nil, fmt.Errorf("goal derivation: unknown proposer decision %q", resp.Decision)
	}
}

// ensureProposeGoalTool guarantees the propose_goal descriptor is present in
// availableTools, rebuilding it from the registry when the caller's list omits
// it. If the tool is not registered at all the list is returned unchanged and
// a stray invocation fails with the "no goal proposer in context" error.
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
				// Group is load-bearing (ADR-024): an undeclared-group
				// descriptor matches no allow-list. propose_goal is an
				// orchestration tool, i.e. GroupSystem.
				Group: sdktools.GroupSystem,
			})
		}
	}
	return list
}

// ----------------------------------------------------------------------------
// Multi-turn goal loop (runGoalLoop)
// ----------------------------------------------------------------------------

// runGoalLoop is the multi-turn goal-mode driver, entered from HandleMessage
// whenever opts.Goal is set — fresh task or continuation alike (the
// single-flight guard is already held). It routes once, derives the goal, then
// iterates turns (runGoalTurns) until the goal reaches an outcome. See
// specs/domains/goal-mode.md for the full loop contract.
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

	// Route once (routing invariant): continuation fast-path or full router.
	// The RAW `message` goes to the router — it augments skill context itself,
	// and the attachment-augmented form would double-prefix the skill reference.
	ctx, routing, _, _, err := o.routeOrContinue(ctx, message, opts, bb, availableTools)
	if err != nil {
		return nil, err
	}

	result, _, err := o.runGoalLoopRouted(ctx, message, opts, bb, availableTools, plansDir, routing, false)
	return result, err
}

// runGoalLoopRouted is runGoalLoop's post-routing body, split out so the
// goal-mode SUGGESTION entry can reuse the routing decision the normal path
// already established (one routing decision per goal task, no second router
// call). fallbackOnDeriveFailure changes ONLY the genuine-failure branch (a
// user cancel is classified before it and stays a clean exit in both entry
// modes): when true, a failed derivation returns deriveFailed=true with a nil
// result and no side effects beyond routing persistence, so HandleMessage can
// fall back to the plain Conductor run. See specs/domains/goal-mode.md.
func (o *Orchestrator) runGoalLoopRouted(
	ctx context.Context,
	message string,
	opts HandleOptions,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	routing *router.RoutingDecision,
	fallbackOnDeriveFailure bool,
) (result *HandleResult, deriveFailed bool, err error) {
	// The enriched ctx flows into deriveGoal AND every goal turn.
	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// `message` feeds routing; the attachment-augmented form feeds deriveGoal,
	// runGoalTurns, and the derivation-failure fallback.
	conductorMessage := o.augmentWithAttachments(message, bb)

	// Images travel as ContentBlocks through deriveGoal and every goal turn,
	// never as blackboard text.
	contentBlocks := buildContentBlocks(conductorMessage, opts.PendingImages)

	// Persist the real routing decision so finalizeResult and resume see it.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	// The model-profile essential-tools filter is NOT applied in goal mode —
	// narrowing a goal run would drop the goal-loop tooling (see
	// specs/domains/goal-mode.md). The Lite prompt profile IS honored: ctx
	// carries it into the derivation and verification passes, fresh or resumed.

	deriveDeps := o.buildConductorDeps(nil, nil)
	deriveDeps.contentBlocks = contentBlocks
	gs, derr := o.deriveGoal(ctx, conductorMessage, bb, availableTools, deriveDeps)
	if derr != nil {
		o.logInfo("goal_loop: derivation failed, exiting", "error", derr)
		switch classifyDerivationFailure(derr, fallbackOnDeriveFailure) {
		case derivationBubble:
			return nil, false, derr
		case derivationFallbackPlain:
			// GENUINE failure on the suggested entry: leave the result unbuilt
			// (no task-outcome persist) so HandleMessage can fall back to the
			// plain path — the accepted task must still get done. A user cancel
			// never reaches this branch (classified to a clean exit first).
			return nil, true, nil
		default:
			// User cancel = clean exit in BOTH entry modes. Pass the computed
			// routing so finalizeResult does not clobber the decision
			// persisted above (the other call sites pass the real value too).
			return o.goalLoopResult(conductorMessage, bb, routing, goal.StatusActive, "", false, nil), false, nil
		}
	}

	// Turn-only budget, no config defaults: MaxTurns from the per-message
	// override, else unlimited (the user governs it via pause/stop; the only
	// non-numeric guard is the anti-spin halt).
	gs.Budget = resolveGoalBudget(opts.GoalBudgetOverride)
	if gs.CreatedAt.IsZero() {
		gs.CreatedAt = time.Now()
	}
	gs.Status = goal.StatusActive
	o.logInfo("goal_loop: goal activated", "condition", gs.Condition, "verify", gs.VerifyClause, "budget", gs.Budget)

	conversationHistory := truncateHistory(o.historySnapshot(), o.config.ConductorHistoryWindow)

	turnRunner := o.goalTurnRunner
	if turnRunner == nil {
		turnRunner = o.defaultGoalTurnRunner
	}

	// Each turn is a fresh RunConductor call, so the image blocks must be
	// re-injected into the deps every turn.
	if len(contentBlocks) > 0 {
		inner := turnRunner
		turnRunner = func(ctx context.Context, turn int, msg string, b orchestration.Blackboard, tl []sdktools.ToolDescriptor, pd string, hist []llm.Message, deps conductorDeps) (int, *orchestration.ExecutionResult, error) {
			deps.contentBlocks = contentBlocks
			return inner(ctx, turn, msg, b, tl, pd, hist, deps)
		}
	}

	gs, paused := o.runGoalTurns(ctx, conductorMessage, bb, availableTools, plansDir, conversationHistory, gs, turnRunner)

	o.persistGoalStateBestEffort(bb, gs)
	out := message
	if gs.LastVerdict != nil && gs.LastVerdict.Reason != "" {
		out = gs.LastVerdict.Reason
	}
	return o.goalLoopResult(out, bb, routing, gs.Status, gs.Condition, paused, gs.LastErrorTyped), false, nil
}

// resumeGoalLoop re-enters the goal loop from a persisted, non-terminal
// GoalState, mirroring runGoalLoop's post-derivation body but skipping
// deriveGoal (condition and verify clause are known) and seeding the prior
// trajectory on the first resumed turn so the interrupted turn is CONTINUED,
// not restarted. nudge lands as that turn's final user message
// (PendingUserInterjection); forceCompactionStrategy force-compacts the merged
// trajectory on the same turn only. See specs/domains/goal-mode.md (persistence
// & resume), specs/domains/session-lifecycle.md (nudge),
// specs/domains/memory/compaction.md (resume compaction).
func (o *Orchestrator) resumeGoalLoop(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	routing *router.RoutingDecision,
	gs *goal.GoalState,
	resumeSteps []agent.Step,
	nudge string,
	forceCompactionStrategy string,
) (*HandleResult, error) {
	// Defense-in-depth (see ErrGoalBlockedByModelProfiles): direct callers hit
	// the same wall the session API and Orchestrator.Resume already enforce —
	// before any side effect.
	if o.modelProfilesEssentialToolsEnabled() {
		return nil, ErrGoalBlockedByModelProfiles
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// A paused goal was left active; re-activation also covers the blocked_idle
	// path so the turn loop's `for gs.Status == active` guard enters. Terminal
	// goals never reach here (Resume guards on IsTerminal).
	gs.Status = goal.StatusActive
	if gs.CreatedAt.IsZero() {
		gs.CreatedAt = time.Now()
	}
	o.logInfo("goal_loop: resuming", "condition", gs.Condition, "verify", gs.VerifyClause, "turn", gs.TurnCount, "budget", gs.Budget, "nudge", nudge != "")

	conversationHistory := truncateHistory(o.historySnapshot(), o.config.ConductorHistoryWindow)

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
	// and carries the one-shot nudge; the counter continues from gs.TurnCount,
	// so the once-flags fire exactly once.
	//
	// Image blocks are re-derived from the ORIGINAL request every turn — the
	// stored history's user message carries it (the wave summary / fallback note
	// never enter stored history), mirroring the plain-Conductor resume path.
	// See specs/domains/session-lifecycle.md (§ Resume-side image-block
	// re-injection).
	// The attachment augmentation is UNCONDITIONAL: the "## Attached files"
	// section is the only place the model learns an attachment_id, so a resumed
	// goal must carry it exactly like a fresh send.
	goalMessage := o.augmentWithAttachments(message, bb)
	resumeContentBlocks := buildContentBlocks(
		goalMessage,
		imageBlocksForRequest(o.historySnapshot(), bb.GetOriginalRequest()),
	)
	seed := resumeSteps
	seeded := false
	nudged := false
	forceCompacted := false
	wrapped := func(ctx context.Context, turn int, msg string, b orchestration.Blackboard, tl []sdktools.ToolDescriptor, pd string, hist []llm.Message, deps conductorDeps) (int, *orchestration.ExecutionResult, error) {
		if !seeded && len(seed) > 0 {
			deps.resumeSteps = seed
			seeded = true
		}
		// One-shot nudge: each turn is a fresh conductor run, so it must be
		// re-set per-turn — the once-flag keeps it to the first turn only.
		if !nudged && nudge != "" {
			deps.nudge = nudge
			nudged = true
		}
		// One-shot resume compaction (see specs/domains/memory/compaction.md),
		// scoped to the first resumed turn: that is the only turn seeding the
		// prior trajectory; later turns start fresh context managers.
		if !forceCompacted && forceCompactionStrategy != "" {
			deps.forceCompactionStrategy = forceCompactionStrategy
			forceCompacted = true
		}
		if len(resumeContentBlocks) > 0 {
			deps.contentBlocks = resumeContentBlocks
		}
		return turnRunner(ctx, turn, msg, b, tl, pd, hist, deps)
	}

	gs, paused := o.runGoalTurns(ctx, goalMessage, bb, availableTools, plansDir, conversationHistory, gs, wrapped, withResumeContinuation())

	o.persistGoalStateBestEffort(bb, gs)
	// Fallback is the clean original request, not the augmented `message`: the
	// wave summary / fallback note are per-run conductor context, not
	// user-visible task output.
	out := bb.GetOriginalRequest()
	if gs.LastVerdict != nil && gs.LastVerdict.Reason != "" {
		out = gs.LastVerdict.Reason
	}
	return o.goalLoopResult(out, bb, routing, gs.Status, gs.Condition, paused, gs.LastErrorTyped), nil
}

// persistGoalStateBestEffort persists the goal state so a paused/active goal
// survives app restart. Best-effort by contract: absent store/task ID is a
// no-op, and a persistence failure is logged, never propagated — losing the
// checkpoint degrades only resumability. See specs/domains/goal-mode.md.
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

// goalVerifierDefaultRejectReason is the synthesized reason for a rejected met
// verdict whose verifier supplied no concrete reason. See specs/domains/goal-mode.md.
const goalVerifierDefaultRejectReason = "the independent verifier could not confirm the goal's success condition is met"

// goalTurnMaxErrorRetries bounds the CONSECUTIVE errored turns before the loop
// halts as a resumable failure (specs/domains/goal-mode.md, anti-spin error
// branch). Per-incident: a clean turn resets the counter.
const goalTurnMaxErrorRetries = 2

// goalTurnBudgetSpent is the single budget predicate, consulted by the loop's
// tail check and the turn-error retry guard (a retry spends a real turn and
// must not push past MaxTurns). MaxTurns == 0 is never spent.
func goalTurnBudgetSpent(gs *goal.GoalState) bool {
	return gs.Budget.MaxTurns > 0 && gs.TurnCount >= gs.Budget.MaxTurns
}

// goalTurnOption configures a runGoalTurns invocation (variadic so existing
// fresh-run callers and tests need no change). See specs/domains/goal-mode.md.
type goalTurnOption func(*goalTurnConfig)

// goalTurnConfig is the resolved runGoalTurns option set.
type goalTurnConfig struct {
	// resumeContinuesTurn marks a RESUME re-entry: the first iteration
	// CONTINUES the interrupted turn (no budget re-charge) and tolerates an
	// idle first turn — a resume artifact, not a stuck agent. See
	// specs/domains/goal-mode.md (resume continuity).
	resumeContinuesTurn bool
}

// withResumeContinuation marks a runGoalTurns call as a resume re-entry (see
// goalTurnConfig.resumeContinuesTurn).
func withResumeContinuation() goalTurnOption {
	return func(c *goalTurnConfig) { c.resumeContinuesTurn = true }
}

// runGoalTurns is the turn-iteration core of the goal loop, extracted so it
// can be unit-tested with a mock turn runner and a pre-built GoalState. It
// mutates and returns gs, returning paused=true when a turn's conductor run
// came back ExecutionStatusPaused (universal pause signal — pausing is
// session-level) so the goal stays ACTIVE for resume. Per-turn ordering — turn
// run under counting proxies, then error retry, verdict, anti-spin, budget,
// goal_progress/goal_status emission — follows specs/domains/goal-mode.md
// (per-turn invariant, anti-spin, resume continuity). With
// withResumeContinuation the first iteration continues the interrupted turn.
func (o *Orchestrator) runGoalTurns(
	ctx context.Context,
	message string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	plansDir string,
	conversationHistory []llm.Message,
	gs *goal.GoalState,
	turnRunner func(ctx context.Context, turn int, message string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, plansDir string, conversationHistory []llm.Message, deps conductorDeps) (toolCallCount int, result *orchestration.ExecutionResult, err error),
	opts ...goalTurnOption,
) (*goal.GoalState, bool) {
	var cfg goalTurnConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	resumeContinuation := cfg.resumeContinuesTurn
	// Flips after the first iteration so only it re-uses the interrupted turn's number.
	consumedResumeTurn := false
	var paused bool
	// Consecutive errored turns, for the bounded retry below; a clean turn resets it.
	consecutiveErrors := 0
	// A resumed goal loads gs.LastError from persistence; clear it so it
	// reflects only THIS invocation's turns.
	gs.LastError = ""
	gs.LastErrorTyped = nil

	for gs.Status == goal.StatusActive {
		if ctx.Err() != nil {
			// Cancelled (user CancelTask or app shutdown): break WITHOUT
			// terminalizing — the goal stays active and the manager layer
			// decides (user cancel abandons it, shutdown leaves it resumable).
			// The orchestrator cannot tell the two apart.
			break
		}

		// Resume contract: the first iteration CONTINUES the interrupted turn
		// (already charged before the pause) — no re-charge per pause/resume cycle.
		firstResumedTurn := resumeContinuation && !consumedResumeTurn
		consumedResumeTurn = true
		turn := gs.TurnCount + 1
		if firstResumedTurn && gs.TurnCount > 0 {
			turn = gs.TurnCount
		}
		// Charge up front so the per-turn budget line renders the CURRENT turn
		// (an after-run increment rendered "turn 0" for every turn). A retried
		// errored turn charges again: the increment is at the top of the loop.
		gs.TurnCount = turn
		o.logInfo("goal_loop: starting turn", "turn", turn)

		// Build per-turn deps with a fresh verdict sink and counting wrappers.
		sink := &memGoalStatusSink{}
		deps := o.buildConductorDeps(conversationHistory, nil)
		counter := &turnUsageCounter{}
		deps.toolExec = &countingToolExec{inner: deps.toolExec, counter: counter}
		// verify_on_edit never runs on goal-loop turns — see
		// specs/domains/verify-on-edit.md (suppression points).
		deps.verifyOnEdit = nil
		// declare_goal_status is the turn's STOP TOOL: one bounded attempt per
		// turn. Without it the agent runs the whole goal (verification loop
		// included) inside one unbounded turn and the budget never engages —
		// see specs/domains/goal-mode.md (per-turn Executor.Run invariant).
		deps.stopTools = []string{"declare_goal_status"}

		toolCalls, execResult, terr := turnRunner(
			tools.WithGoalStatusSink(WithGoalState(ctx, gs), sink),
			turn, message, bb, availableTools, plansDir, conversationHistory, deps,
		)

		// Mid-turn pause (universal signal): break, goal stays ACTIVE — the
		// conductor already flushed the partial trajectory, so Resume seeds
		// the checkpoint and continues the interrupted turn. Detected before
		// the verdict/anti-spin logic so a paused idle turn is never
		// misclassified as blocked_idle.
		if execResult != nil && execResult.Status == orchestration.ExecutionStatusPaused {
			o.logInfo("goal_loop: paused by signal mid-turn (goal stays active)", "turn", gs.TurnCount)
			paused = true
			break
		}
		// Cancelled mid-turn: same contract as the top-of-loop break (goal
		// stays active, manager decides). Checked before the outcome logic so
		// a cancel takes precedence. A cancel is not a turn failure — clear
		// any retry's error marker, or goalLoopResult would report it as
		// "stopped after a turn error".
		if ctx.Err() != nil {
			gs.LastError = ""
			gs.LastErrorTyped = nil
			break
		}
		// Turn error: branched BEFORE the outcome logic — an errored turn is
		// never blocked_idle or a bare "partial". Retried a bounded number of
		// times (never past the budget: a retry spends a real turn), then the
		// loop halts leaving the goal ACTIVE with the cause recorded — a
		// resumable failure a later Resume retries. See specs/domains/goal-mode.md.
		if terr != nil {
			gs.LastError = terr.Error()
			// The error VALUE rides along for goalLoopResult → HandleResult.Err
			// (the manager's auto-retry classifier, ADR-065); only the final,
			// halt-deciding error matters — a cleaned retry resets both below.
			gs.LastErrorTyped = terr
			consecutiveErrors++
			budgetSpent := goalTurnBudgetSpent(gs)
			if consecutiveErrors <= goalTurnMaxErrorRetries && !budgetSpent {
				o.logInfo("goal_loop: turn errored, retrying", "turn", turn, "attempt", consecutiveErrors, "max_retries", goalTurnMaxErrorRetries, "error", terr)
				// A retried turn reports progress like any other; `continue`
				// skips only the tail budget check (already honoured above).
				o.emitGoalProgress(ctx, gs)
				o.emitGoalStatus(ctx, gs)
				continue
			}
			o.logInfo("goal_loop: turn errored, halting as a resumable failure", "turn", turn, "attempts", consecutiveErrors, "budget_spent", budgetSpent, "error", terr)
			o.emitGoalStatus(ctx, gs)
			break
		}
		consecutiveErrors = 0
		gs.LastError = ""
		gs.LastErrorTyped = nil

		// The rejection notice is one-shot: the marker rendered into THIS
		// turn's prompt is cleared here; a new met attempt re-populates it
		// below before emitGoalStatus.
		gs.LastVerification = ""
		gs.LastVerificationReason = ""
		gs.LastVerificationEvidence = nil

		// Read the agent's verdict (nil if declare_goal_status was not called).
		if v := sink.Last(); v != nil {
			gs.LastVerdict = v
			if v.Status == "met" {
				// Independent verification gate (specs/domains/goal-mode.md):
				// "off" or a nil verifier terminates as met on the agent's
				// word; otherwise the isolated verifier pass adjudicates —
				// confirmed → met; rejected → a synthesized not_met verdict
				// replaces the claim and the loop CONTINUES without re-charging
				// the turn. A rejected met never terminates the goal as met.
				verificationMode := o.config.GoalLoop.Verification
				if verificationMode == "off" {
					gs.LastVerification = "off"
					gs.Status = goal.StatusMet
					o.logInfo("goal_loop: goal met (verification off)", "turn", turn, "evidence", len(v.Evidence))
					o.emitGoalStatus(ctx, gs)
					break
				}
				verifier := o.resolveGoalVerifier()
				if verifier == nil {
					// Defensive: a nil seam with verification enabled counts
					// as confirmed (same as "off").
					gs.LastVerification = "confirmed"
					gs.Status = goal.StatusMet
					o.logInfo("goal_loop: goal met (no verifier configured)", "turn", turn, "evidence", len(v.Evidence))
					o.emitGoalStatus(ctx, gs)
					break
				}
				outcome, verr := verifier(ctx, gs, v, message, execResultOutput(execResult), bb, availableTools, deps)
				// A pause inside the verification pass INTERRUPTED it — the
				// condition was neither confirmed nor refuted. Suspend the
				// request (goal stays ACTIVE, Resume re-runs verification);
				// never synthesize not_met from an interrupted pass. Checked
				// before the confirm/reject branches for exactly that reason.
				if isPaused(verr) {
					o.logInfo("goal_loop: verifier paused by signal (goal stays active)", "turn", turn)
					paused = true
					break
				}
				if outcome != nil && outcome.Confirmed {
					gs.LastVerification = "confirmed"
					// Keep the verifier's structured outcome next to the agent's
					// own evidence (which stays on gs.LastVerdict.Evidence) so
					// emitGoalStatus can carry WHY the goal was confirmed.
					gs.LastVerificationReason = outcome.Reason
					gs.LastVerificationEvidence = outcome.Evidence
					gs.Status = goal.StatusMet
					o.logInfo("goal_loop: goal met (verification confirmed)", "turn", turn, "evidence", len(v.Evidence))
					o.emitGoalStatus(ctx, gs)
					break
				}
				// Rejected: synthesize a not_met verdict carrying the rejection
				// reason (visible to the next turn via renderGoalModeVolatile);
				// the agent's original evidence is kept for context.
				rejectReason := goalVerifierDefaultRejectReason
				if outcome != nil && strings.TrimSpace(outcome.Reason) != "" {
					rejectReason = outcome.Reason
				}
				gs.LastVerdict = &goal.Verdict{
					Status:     "not_met",
					Reason:     rejectReason,
					Evidence:   v.Evidence,
					DeclaredAt: time.Now(),
				}
				gs.LastVerification = "rejected"
				if verr != nil {
					o.logInfo("goal_loop: met verdict rejected by verifier (verifier error)", "turn", turn, "error", verr, "reason", rejectReason)
				} else {
					o.logInfo("goal_loop: met verdict rejected by verifier", "turn", turn, "reason", rejectReason)
				}
				// Fall through WITHOUT re-incrementing the turn counter (this
				// turn already counted) so a rejected claim is retried next
				// turn; goal_status is emitted by the bottom-of-loop pair —
				// emitting here would double-emit (there is no break).
			} else if v.Status == "blocked" {
				gs.Status = goal.StatusBlockedIdle
				o.logInfo("goal_loop: agent declared blocked", "turn", turn, "reason", v.Reason)
				o.emitGoalStatus(ctx, gs)
				break
			}
			// "not_met" (or any other): keep iterating.
		}

		// Anti-spin: zero tool calls AND no verdict is an idle turn — halt as
		// blocked_idle rather than spin. Exception (resume continuity): the
		// first turn of a RESUMED loop tolerates idleness (the interrupted
		// turn's work lives in the seeded checkpoint), bounded to that one
		// turn; without budget headroom it still terminates as exhausted.
		if toolCalls == 0 && sink.Last() == nil {
			if firstResumedTurn {
				if goalTurnBudgetSpent(gs) {
					gs.Status = goal.StatusExhausted
					o.logInfo("goal_loop: idle resumed turn with no budget headroom (exhausted)", "turn", turn)
					o.emitGoalStatus(ctx, gs)
					break
				}
				o.logInfo("goal_loop: idle first resumed turn tolerated (goal stays active)", "turn", turn)
				o.emitGoalProgress(ctx, gs)
				o.emitGoalStatus(ctx, gs)
				continue
			}
			gs.Status = goal.StatusBlockedIdle
			o.logInfo("goal_loop: blocked_idle (zero tool calls, no verdict)", "turn", turn)
			o.emitGoalStatus(ctx, gs)
			break
		}

		if goalTurnBudgetSpent(gs) {
			gs.Status = goal.StatusExhausted
			o.logInfo("goal_loop: turn budget exhausted", "turn", gs.TurnCount, "max", gs.Budget.MaxTurns)
			o.emitGoalStatus(ctx, gs)
			break
		}

		o.emitGoalProgress(ctx, gs)
		o.emitGoalStatus(ctx, gs)
	}
	return gs, paused
}

// execResultOutput extracts the met turn's work product (Summary preferred —
// a stop-tool turn's Output holds only the verdict confirmation; falls back
// to Output; nil → ""). The verifier seeds it onto its fresh blackboard via
// SetFinalResult so its read_final_result returns the real work. See
// specs/domains/goal-mode.md (independent verification).
func execResultOutput(r *orchestration.ExecutionResult) string {
	if r == nil {
		return ""
	}
	if r.Summary != "" {
		return r.Summary
	}
	return r.Output
}

// defaultGoalTurnRunner runs ONE goal-loop turn via the real Conductor over
// the already-enriched ctx (routing was established once upstream; no turn
// re-routes). It reports the turn's tool-call count by reading the counting
// wrapper AFTER the run — countingToolExec increments it only as the
// Conductor executes tools, so an earlier read would always be zero.
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

	// The wrapper is always present in the goal loop; the guard covers
	// standalone calls.
	toolCalls := 0
	if ce, ok := deps.toolExec.(*countingToolExec); ok {
		toolCalls = int(ce.counter.toolCalls.Load())
	}
	return toolCalls, result, err
}

// resolveGoalBudget applies the optional per-message override: MaxTurns when
// set, otherwise unlimited — the budget is turn-only, with no config-level
// defaults. See specs/domains/goal-mode.md (configuration).
func resolveGoalBudget(override *goal.GoalBudget) goal.GoalBudget {
	if override == nil {
		return goal.GoalBudget{}
	}
	if override.MaxTurns > 0 {
		return goal.GoalBudget{MaxTurns: override.MaxTurns}
	}
	return goal.GoalBudget{}
}

// goalLoopResult builds the HandleResult for the loop's outcome. met/cancelled/
// exhausted map onto execution statuses; blocked_idle and a cooperative pause
// map to ExecutionStatusPaused (a resumable SUSPENSION, never a failed
// session); a halted turn error maps to a resumable ExecutionStatusFailed with
// the reason as the output and the error VALUE preserved on HandleResult.Err
// for the manager's auto-retry classifier (ADR-065 — see
// specs/decisions/065-auto-resend-retryable-errors.md, specs/domains/goal-mode.md).
func (o *Orchestrator) goalLoopResult(output string, bb orchestration.Blackboard, routing *router.RoutingDecision, status goal.GoalStatus, condition string, paused bool, turnErr error) *HandleResult {
	execResult := &orchestration.ExecutionResult{Output: output}
	var cause error
	switch status {
	case goal.StatusMet:
		execResult.Status = orchestration.ExecutionStatusSuccess
	case goal.StatusExhausted:
		execResult.Status = orchestration.ExecutionStatusFailed
	case goal.StatusCancelled:
		execResult.Status = orchestration.ExecutionStatusCancelled
	case goal.StatusBlockedIdle:
		// NOT a failure: the goal stays non-terminal and resumable. Surfaces
		// as a session suspension ("Goal blocked" badge), not the misleading
		// task_failed_resumable banner (Issue #94 AC3).
		execResult.Status = orchestration.ExecutionStatusPaused
	default: // active (pause / derivation / turn-error path)
		switch {
		case paused:
			execResult.Status = orchestration.ExecutionStatusPaused
		case turnErr != nil:
			// Halted on a turn error: resumable failure with the concrete
			// cause as the output (mirroring E2S's explicit-output
			// convention); the goal stays active for Resume to retry.
			execResult.Status = orchestration.ExecutionStatusFailed
			execResult.Output = fmt.Sprintf("Goal run stopped after a turn error: %s\n\nThe goal is still active — resume to retry from the checkpoint.", turnErr)
			cause = turnErr
		default:
			execResult.Status = orchestration.ExecutionStatusPartial
		}
	}
	result := o.finalizeResult(bb, routing, execResult)
	result.Err = cause
	return result
}

// emitGoalStatus emits the dedicated goal_status session event — its own type
// (not the phase-discriminated `service` channel) so the frontend's live
// subscription reliably reaches the goal store. Payload contract (verdict,
// verification marker/evidence, created_at identity): specs/domains/goal-mode.md
// (events) and specs/contracts/event-catalog.md.
func (o *Orchestrator) emitGoalStatus(_ context.Context, gs *goal.GoalState) {
	data := map[string]any{
		"status":            string(gs.Status),
		"turn":              gs.TurnCount,
		"condition":         gs.Condition,
		"max_turns":         gs.Budget.MaxTurns,
		"verification_mode": gs.VerificationMode,
	}
	// Zero CreatedAt (state predating the field) is omitted so the frontend
	// falls back to its turn-only heuristic.
	if !gs.CreatedAt.IsZero() {
		data["created_at"] = gs.CreatedAt.UnixMilli()
	}
	if gs.LastVerdict != nil {
		data["verdict"] = gs.LastVerdict.Status
		data["reason"] = gs.LastVerdict.Reason
		data["evidence"] = gs.LastVerdict.Evidence
	}
	if v := gs.LastVerification; v == "confirmed" || v == "rejected" || v == "off" {
		data["verification"] = v
	}
	if gs.LastVerification == "confirmed" {
		data["verification_reason"] = gs.LastVerificationReason
		data["verification_evidence"] = gs.LastVerificationEvidence
	}
	o.emitter.GoalStatus(data)
}

// emitGoalProgress emits the dedicated goal_progress event (mid-loop
// turn/budget telemetry; transient — the paired goal_status is persisted).
func (o *Orchestrator) emitGoalProgress(_ context.Context, gs *goal.GoalState) {
	o.emitter.GoalProgress(map[string]any{
		"turn":      gs.TurnCount,
		"max_turns": gs.Budget.MaxTurns,
		"condition": gs.Condition,
	})
}

// ----------------------------------------------------------------------------
// Per-turn counting wrappers (anti-spin tool-call tally)
// ----------------------------------------------------------------------------

// turnUsageCounter tallies one goal-loop turn's tool calls for the anti-spin
// (idle-turn) check; installed fresh per turn, read after the turn completes.
type turnUsageCounter struct {
	toolCalls atomic.Int64
}

// countingToolExec wraps an agent.ToolExecutor, counting Execute calls so the
// loop can detect an idle (zero-tool-call) turn. See specs/domains/goal-mode.md.
type countingToolExec struct {
	inner   agent.ToolExecutor
	counter *turnUsageCounter
}

func (c *countingToolExec) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	c.counter.toolCalls.Add(1)
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

// memGoalStatusSink holds the most recent declare_goal_status verdict;
// Last returns nil until one is declared.
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

// ----------------------------------------------------------------------------
// Verification sink (in-memory implementation)
// ----------------------------------------------------------------------------

// memVerificationSink holds the most recent declare_verification outcome;
// Last returns nil until one is declared.
type memVerificationSink struct {
	mu      sync.Mutex
	outcome *tools.VerificationOutcome
}

func (s *memVerificationSink) Declare(v tools.VerificationOutcome) {
	s.mu.Lock()
	s.outcome = &v
	s.mu.Unlock()
}

func (s *memVerificationSink) Last() *tools.VerificationOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcome
}

// verifierIncludeGroups is the verification pass's include set: system + reads
// + execute (re-run the verify clause) + MCP (user-installed checkers). The
// mutating groups are absent, hard-excluded via verifierExcludedGroups, and
// backstopped by name via verifierMutatingToolNames. See specs/domains/goal-mode.md
// (read-only mandate).
var verifierIncludeGroups = map[sdktools.ToolGroup]struct{}{
	sdktools.GroupSystem:     {},
	sdktools.GroupLocalRead:  {},
	sdktools.GroupRemoteRead: {},
	sdktools.GroupExecute:    {},
	sdktools.GroupLocalMCP:   {},
	sdktools.GroupRemoteMCP:  {},
}

// verifierExcludedGroups is the mutating set the verification pass must never
// see. Disjoint from the include set (behaviorally redundant) — it states the
// read-only invariant at the exclusion site.
var verifierExcludedGroups = map[sdktools.ToolGroup]struct{}{
	sdktools.GroupLocalWrite:  {},
	sdktools.GroupRemoteWrite: {},
}

// verifierMutatingToolNames are the classic filesystem-mutating builtins,
// hard-excluded BY NAME: if one were ever mis-tagged into an included group it
// would pass both group checks — the name exclusion still strips it.
var verifierMutatingToolNames = map[string]struct{}{
	// Mutating file tools.
	ToolWriteFile: {}, ToolEditFile: {},
	"delete_file": {}, "delete_directory": {}, "create_directory": {},
}

// verifierExcludedToolNames are the goal-control / coordination tools stripped
// even though their group (system) is included — the verifier's ONLY output
// channel is declare_verification, and execute_plan would let it mutate state
// by proxy (plan-step subagents carry full toolsets). See specs/domains/goal-mode.md
// (read-only mandate). Names without a core constant are sp4rk builtins the
// SDK does not export constants for.
var verifierExcludedToolNames = map[string]struct{}{
	// Goal-control / coordination tools.
	"declare_goal_status": {}, ToolDeclareStepComplete: {},
	"declare_plan": {}, "execute_plan": {}, "delegate": {}, ToolSubAgent: {},
	"propose_goal": {}, "reflect": {}, "cancel_delegation": {},
}

// verifierReDerivationExcludedToolNames is verifierExcludedToolNames MINUS
// "delegate" — the one coordination tool re_derivation grants, so the
// verifier can delegate a fresh read-only run of the goal's process.
var verifierReDerivationExcludedToolNames = map[string]struct{}{
	// Goal-control / coordination tools — MINUS delegate.
	"declare_goal_status": {}, ToolDeclareStepComplete: {},
	"declare_plan": {}, "execute_plan": {}, ToolSubAgent: {},
	"propose_goal": {}, "reflect": {}, "cancel_delegation": {},
}

// buildVerifierToolset is the shared core of the two verification-mode tool
// filters: include by group (verifierIncludeGroups), plus declare_verification
// by name (kept name-based so the verdict channel survives any future group
// re-tagging), plus delegate only when allowDelegate. The excluded set, the
// mutating groups, the name backstop, and mode-disabled tools are then
// hard-excluded; undeclared (zero) groups match no include criterion.
func buildVerifierToolset(all []sdktools.ToolDescriptor, disabled map[string]bool, excluded map[string]struct{}, allowDelegate bool) []sdktools.ToolDescriptor {
	out := make([]sdktools.ToolDescriptor, 0, len(all))
	seen := make(map[string]struct{}, len(all))
	for _, d := range all {
		if _, ok := seen[d.Name]; ok {
			continue
		}
		if disabled[d.Name] {
			continue
		}
		if _, isExcluded := excluded[d.Name]; isExcluded {
			continue
		}
		if _, isExcludedGroup := verifierExcludedGroups[d.Group]; isExcludedGroup {
			continue
		}
		if _, isMutating := verifierMutatingToolNames[d.Name]; isMutating {
			continue
		}
		_, groupIncluded := verifierIncludeGroups[d.Group]
		isVerdict := d.Name == "declare_verification" // the verifier's verdict channel
		isDelegate := allowDelegate && d.Name == "delegate"
		if !groupIncluded && !isVerdict && !isDelegate {
			continue
		}
		seen[d.Name] = struct{}{}
		out = append(out, d)
	}
	return out
}

// verifierToolFilter builds the executable mode's read-only/test toolset (the
// default); declare_verification is guaranteed present.
func verifierToolFilter(all []sdktools.ToolDescriptor, disabled map[string]bool) []sdktools.ToolDescriptor {
	return buildVerifierToolset(all, disabled, verifierExcludedToolNames, false)
}

// verifierReDerivationToolFilter builds the re_derivation toolset: the
// executable set PLUS delegate (the delegated run's result is read via
// read_step_output, already present via the system group). Only delegate is
// added to the coordination set.
func verifierReDerivationToolFilter(all []sdktools.ToolDescriptor, disabled map[string]bool) []sdktools.ToolDescriptor {
	return buildVerifierToolset(all, disabled, verifierReDerivationExcludedToolNames, true)
}

// renderReportedEvidence formats the agent's self-reported verdict for the
// {reported_evidence} placeholder: UNVERIFIED claims the verifier must
// re-check — presented, never vouched for. A nil verdict yields a placeholder
// note so the directive still renders cleanly.
func renderReportedEvidence(verdict *goal.Verdict) string {
	if verdict == nil {
		return "(The agent reported no self-evaluation verdict. Verify the condition by inspection and cite concrete evidence.)"
	}
	var b strings.Builder
	if reason := strings.TrimSpace(verdict.Reason); reason != "" {
		fmt.Fprintf(&b, "Agent's reported reason: %s\n", reason)
	}
	if len(verdict.Evidence) == 0 {
		b.WriteString("The agent reported NO concrete evidence for its verdict.\n")
	} else {
		b.WriteString("The agent's reported evidence (treat as UNVERIFIED claims — re-check each one):\n")
		for i, ev := range verdict.Evidence {
			fmt.Fprintf(&b, "  %d. [%s] %s — %s\n", i+1, ev.Type, ev.Ref, ev.Summary)
		}
	}
	return b.String()
}

// defaultGoalVerifier is the production independent verifier: an ISOLATED
// Conductor pass on the same complexity-derived step budget as a working turn,
// reporting through declare_verification into a fresh memVerificationSink. It
// runs on a FRESH blackboard seeded with the met turn's work product
// (execResultOutput → SetFinalResult); directive + toolset follow
// gs.VerificationMode; a pass ending without a verdict is a REJECT; a
// cooperative pause inside the pass returns the agent.ErrPaused sentinel (nil
// outcome) so an interrupted verification is never misread as a rejection — a
// CONFIRMED verdict still wins. See specs/domains/goal-mode.md.
func (o *Orchestrator) defaultGoalVerifier(
	ctx context.Context,
	gs *goal.GoalState,
	verdict *goal.Verdict,
	message string,
	lastTurnOutput string,
	bb orchestration.Blackboard,
	availableTools []sdktools.ToolDescriptor,
	deps conductorDeps,
) (*tools.VerificationOutcome, error) {
	// Fresh deps isolate the verifier from the loop's per-turn wiring; the
	// prior conversation history is preserved so it sees the agent's dialogue
	// context. Verification never resumes from a checkpoint.
	deps = o.buildConductorDeps(deps.conversationHistory, nil)
	deps.resumeSteps = nil

	// Ledger-bound unit sink: the pass and its delegations persist as durable
	// records under the judged goal task (ADR-048) even though the run itself
	// stays on the fresh blackboard below. Nil without a task id — RunConductor
	// then falls back to its blackboard-derived wiring.
	deps.unitSink = o.verifierUnitSink(bb, gs, deps)

	verifierBB := orchestration.NewMapBlackboard()
	verifierBB.SetOriginalRequest(message)
	if lastTurnOutput != "" {
		verifierBB.SetFinalResult(lastTurnOutput)
	}

	verifierTools := verifierToolFilter(availableTools, deps.disabledTools)
	if gs.VerificationMode == goal.VerificationModeReDerivation {
		verifierTools = verifierReDerivationToolFilter(availableTools, deps.disabledTools)
	}
	// Single mode→directive mapping in the prompts layer — the verbose and
	// Lite branches cannot drift apart.
	evidence := renderReportedEvidence(verdict)
	directive := prompts.GoalVerificationDirectiveByMode(
		gs.VerificationMode, gs.Condition, gs.VerifyClause, evidence,
	)
	// Lite counterpart: swapped in only when the model-profile Lite profile is
	// active; otherwise the verbose directive above is used verbatim.
	liteDirective := prompts.GoalVerificationLiteDirectiveByMode(
		gs.VerificationMode, gs.Condition, gs.VerifyClause, evidence,
	)
	deps.systemPromptOverride = func(ctx context.Context, msg string, modelMeta llm.ModelMetadata) string {
		return buildSpecializedSystemPromptWithLite(ctx, msg, modelMeta, directive, liteDirective)
	}

	// Inject a fresh sink so this pass's outcome is captured in isolation.
	sink := &memVerificationSink{}
	verifierCtx := tools.WithVerificationSink(ctx, sink)

	runResult, runErr := RunConductor(verifierCtx, message, verifierBB, verifierTools, deps, "")

	// A CONFIRMED verdict wins outright — a trailing pause cannot undo it.
	if outcome := sink.Last(); outcome != nil && outcome.Confirmed {
		return outcome, nil
	}

	// No CONFIRMED verdict + cooperative pause (in this pass or a spawned
	// delegate): interrupted, not refuted — report the pause sentinel so
	// runGoalTurns suspends instead of synthesizing a rejection. Checked
	// BEFORE the reject so a paused pass can never surface as rejected.
	if pausedRun(runErr, runResult) {
		if o.logger != nil {
			o.logger.Info("goal verification conductor run paused (goal stays active)", "error", runErr)
		}
		return nil, agent.ErrPaused
	}

	// A declared (non-confirmed) verdict stands — an explicit reject is a
	// genuine verdict, not a pause artifact.
	if outcome := sink.Last(); outcome != nil {
		return outcome, nil
	}

	if runErr != nil {
		if o.logger != nil {
			o.logger.Debug("goal verification conductor run returned error", "error", runErr)
		}
	}

	// No verdict declared: REJECT — the condition could not be independently
	// confirmed (step budget exhausted, or an error before declaring).
	reason := "verification pass ended without a verdict: the condition could not be independently confirmed"
	if err := ctx.Err(); err != nil {
		reason = fmt.Sprintf("verification pass ended without a verdict (context error: %s)", err)
	}
	return &tools.VerificationOutcome{
		Confirmed:  false,
		Reason:     reason,
		DeclaredAt: time.Now(),
	}, nil
}

// verifierUnitSink builds the ledger-bound unit sink for one verification
// pass, scoped to the judged goal task. Nil without a task id (an in-memory
// run has nothing to persist under); units.StoreFrom degrades a store without
// unit persistence to a best-effort in-memory ledger.
func (o *Orchestrator) verifierUnitSink(bb orchestration.Blackboard, gs *goal.GoalState, deps conductorDeps) *unitSink {
	pbb, ok := bb.(PersistableBlackboard)
	if !ok {
		return nil
	}
	return newVerifierUnitSink(pbb.TaskID(), units.StoreFrom(deps.taskStore), gs, deps.logger)
}

// resolveGoalVerifier resolves the verifier nil→default (the goal loop's
// injection seam, mirroring goalTurnRunner): an injected verifier is honored
// verbatim, a nil field yields defaultGoalVerifier.
func (o *Orchestrator) resolveGoalVerifier() func(ctx context.Context, gs *goal.GoalState, verdict *goal.Verdict, message string, lastTurnOutput string, bb orchestration.Blackboard, availableTools []sdktools.ToolDescriptor, deps conductorDeps) (*tools.VerificationOutcome, error) {
	if o.goalVerifier != nil {
		return o.goalVerifier
	}
	return o.defaultGoalVerifier
}

// compile-time assertions that the wrappers satisfy their interfaces.
var (
	_ agent.ToolExecutor     = (*countingToolExec)(nil)
	_ tools.GoalStatusSink   = (*memGoalStatusSink)(nil)
	_ tools.VerificationSink = (*memVerificationSink)(nil)
)
