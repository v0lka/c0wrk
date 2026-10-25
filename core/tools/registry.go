package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// confirmCeilingGuard is how far ahead of the executor's per-tool-call ceiling
// the confirmation wait must yield its clean denial. Both timers start at the
// tool-CALL start, so the guard arms against the CALL deadline, not the (later)
// confirmation start whose pre-confirmation work already consumed part of the
// ceiling — see SetToolCallTimeout / confirmAndExecuteWithOptions.
const confirmCeilingGuard = time.Second

// callStartCtxKey is the context key under which Execute publishes the tool-call
// start instant, so the confirmation wait budgets against the SAME deadline as
// the executor's watchdog instead of a fresh span of its own.
type callStartCtxKey struct{}

// callStartFrom returns the tool-call start published by Execute, or the zero
// time when the context did not come from Execute (direct test callers).
func callStartFrom(ctx context.Context) time.Time {
	if t, ok := ctx.Value(callStartCtxKey{}).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// goalModeTools is the system-group tools that exist ONLY for a goal pursuit
// (propose_goal, declare_goal_status, declare_verification). This set is purely
// about AVAILABILITY: StripGoalModeTools removes them from a non-goal Conductor
// run's catalog, while the goal loop and the verifier keep the unstripped list.
// See specs/domains/goal-mode.md.
var goalModeTools = map[string]struct{}{
	"propose_goal":         {},
	"declare_goal_status":  {},
	"declare_verification": {},
}

// IsGoalModeTool reports whether the tool exists only for goal mode: a
// policy-exempt system tool additionally gated to goal-loop runs.
func IsGoalModeTool(name string) bool {
	_, ok := goalModeTools[name]
	return ok
}

// StripGoalModeTools removes goal-mode-only tools from a descriptor list — the
// single helper building the available-tool view for a non-goal Conductor run.
func StripGoalModeTools(in []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	if len(in) == 0 {
		return in
	}
	out := make([]sdktools.ToolDescriptor, 0, len(in))
	for _, t := range in {
		if IsGoalModeTool(t.Name) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ToolFilter decides whether a tool should be registered. Return false to reject.
type ToolFilter func(toolName, source string) bool

// Autonomy-mode values — shared vocabulary re-declared here because core/tools
// cannot import core (pinned against backend by core/silent_mode_test.go).
// Empty/unrecognized behaves as standard — fail-safe: every gated call opens a
// confirmation card. See specs/architecture/security-model.md.
const (
	// AutonomyModeStandard is the interactive posture: no automatic gate,
	// every confirmation-gated call opens a user confirmation card.
	AutonomyModeStandard = "standard"
	// AutonomyModeAssisted enables the strict judge (the former Smart Approve):
	// strict ALLOW executes, strict DENY terminates with the justification (no
	// card), everything else opens a card.
	AutonomyModeAssisted = "assisted"
	// AutonomyModeSilent is the unattended posture: no card can open; the
	// silent-mode sub-policies (SilentModeState below) resolve every gated call
	// to a terminal execute-or-deny.
	AutonomyModeSilent = "silent"
)

// SilentModeState is the registry's snapshot of the security.silent_mode
// sub-policies (ADR-053/ADR-072). Inert unless the autonomy mode is silent —
// liveness is the autonomy mode's decision, never a bool here. A plain value
// type so Clone copies it verbatim and ApplySecurityState swaps it atomically.
type SilentModeState struct {
	ToolConfirm string
	// UserConfirm refines the tool_confirm terminal for a fail-closed
	// (CONFIRM) outcome — a spoken CONFIRM, a missing judge, a judge
	// error/timeout, or an unparseable verdict — selecting whether the silent
	// terminal auto-denies (the "deny" default), executes the call unattended
	// ("confirm"), or falls back to the blocking confirmation card
	// ("escalate"). It never governs a deliberate judge DENY: a positive
	// rejection is neither auto-approved nor escalated.
	UserConfirm string
	StepLimit   string
	AskUser     string
}

// ToolRegistry stores all available tools and provides them to Executor: the
// sp4rk registry plus group-based policy enforcement on top. Thread-safe via
// sync.RWMutex. TODO(S-14): replace the embedding with composition and
// delegate only the methods the core layer intends to expose.
type ToolRegistry struct {
	// Deprecated: embedded for backward compatibility (TODO(S-14), see the type doc).
	*sdktools.ToolRegistry
	mu          sync.RWMutex
	confirmFunc sdktools.ConfirmFunc
	// toolCallTimeout mirrors the executor's per-tool-call ceiling so the
	// confirmation wait yields a clean denial just under it. Zero disables the
	// bound. See SetToolCallTimeout.
	toolCallTimeout            time.Duration
	judge                      *sdktools.ToolJudge
	groupPolicies              map[sdktools.ToolGroup]sdktools.ToolPolicy
	preExecuteHook             PreExecuteHook
	postExecuteHook            PostExecuteHook
	toolFilter                 ToolFilter
	disabledTools              map[string]bool
	logger                     *slog.Logger
	autoApproveWorkspaceWrites bool
	autonomyMode               string
	silentMode                 SilentModeState
	judgeObserver              JudgeObserver
	autonomyDecisionObserver   AutonomyDecisionObserver
	// judgeMemoMu guards judgeMemo — deliberately independent of mu: the memo is
	// task-scoped and silent-path reads must not contend with Settings pushes.
	judgeMemoMu sync.Mutex
	// judgeMemo memoizes silent-mode strict-judge FAIL-CLOSED verdicts (a DENY,
	// and a spoken CONFIRM) by the shell EFFECT signature
	// (ShellAnalysisDigest.Signature): the first fail-closed verdict for an
	// effect is replayed verbatim on re-escalation, so judge non-determinism or
	// a re-spelled retry can neither flip a retry DENY back to ALLOW nor re-pay
	// the strict-judge call. An ALLOW is deliberately NEVER memoized: the
	// signature is NOT injective (every command the analyzer cannot see through
	// collapses to command_unbounded_analysis), so a replayed ALLOW would let a
	// materially different command inherit a verdict the judge never gave — a
	// judge bypass; a replayed DENY is fail-closed and safe. Deliberately NOT
	// copied by Clone — and clone lifetime is NOT task lifetime: the memo is
	// task-scoped by the explicit ResetJudgeMemo at each task launch
	// (Manager.refreshAutonomyPosture), not by the clone's lifetime.
	judgeMemo map[string]judgeMemoEntry
	// parent is the shared builder registry this clone was cut from (nil on the
	// shared registry itself) — the authoritative posture source for the
	// per-task posture pinning (see RefreshAutonomyPosture).
	parent *ToolRegistry
}

// PreExecuteHook is called before tool execution; it may block on preconditions
// (e.g. indexing). A returned error aborts execution as a tool error result.
type PreExecuteHook func(ctx context.Context, toolName string, source string) error

// PostExecuteHook is called after a non-system tool execution path completes,
// success or error alike, so the host can react to file mutations (e.g. vector
// index refresh) while distinguishing genuine successes from denied/failed
// attempts. It must not block; a panic propagates to the caller.
type PostExecuteHook func(ctx context.Context, toolName string, result sdktools.ToolResult, err error)

// JudgePhase marks a strict-judge (Smart Approve) evaluation boundary reported
// to a registered JudgeObserver.
type JudgePhase string

const (
	// JudgePhaseStarted fires immediately before the strict judge's LLM call.
	JudgePhaseStarted JudgePhase = "started"
	// JudgePhaseFinished fires immediately after the strict judge's LLM call
	// returns, on success and on error alike.
	JudgePhaseFinished JudgePhase = "finished"
)

// JudgeObserver is invoked around the strict judge's evaluation of an escalated
// call so the host can surface an honest "judge is working" status — no card
// exists yet at JudgePhaseStarted. The observer must not block.
type JudgeObserver func(ctx context.Context, phase JudgePhase, toolName string)

// AutonomyDecision is one autonomous (no-human) security decision the registry
// took — tool_confirm (silent), assisted_deny, or the host-filled step_limit —
// the auditable record of a gate a human would otherwise have answered (ASI10);
// the host serializes it verbatim into the `autonomy_decision` session event.
type AutonomyDecision struct {
	// Kind is the gate resolved autonomously: "tool_confirm",
	// "assisted_deny", or "step_limit".
	Kind string `json:"kind"`
	// Mode is the autonomy posture that took the decision ("assisted"/"silent").
	Mode string `json:"mode,omitempty"`
	// Policy is the sub-policy that decided (the silent tool_confirm mode, the
	// step_limit mode, or "judge" for assisted_deny). Guaranteed non-empty on
	// every emitted decision: the host funnel (Manager.EmitAutonomyDecision)
	// defaults an empty policy from the decision kind (audit §6.3).
	Policy string `json:"policy,omitempty"`
	// Verdict is the decision: "allow"|"deny" for tool_confirm, or the
	// step-limit response ("allow_once"|"allow_more"|"allow_always"|"deny").
	Verdict string `json:"verdict"`
	// Tool is the tool name for a tool_confirm decision; empty for step_limit.
	Tool string `json:"tool,omitempty"`
	// Source is the tool source ("core" or an MCP server name) for a
	// tool_confirm decision.
	Source string `json:"source,omitempty"`
	// Signature is the deterministic effect signature of an analyzed shell call
	// (sdktools.ShellAnalysisDigest.Signature); it doubles as the silent-mode
	// judge-memo key and lets an audit reader tell which decisions adjudicated
	// the same effect. Empty for non-shell tools, failed analyses, step_limit.
	Signature string `json:"signature,omitempty"`
	// Reason is WHY the call/boundary was escalated in the first place (the
	// confirmation reason, or the circuit-breaker reason; empty for a plain
	// step-budget exhaustion).
	Reason string `json:"reason,omitempty"`
	// Justification is the deciding rationale — the strict judge's reasoning,
	// a fail-closed cause, or the policy that denied/allowed outright.
	Justification string `json:"justification,omitempty"`
	// Category is the step-limit boundary category ("budget" or
	// "circuit_breaker"); empty for tool_confirm.
	Category string `json:"category,omitempty"`
	// CurrentStep/MaxSteps locate a step-limit decision; zero for tool_confirm.
	CurrentStep int `json:"current_step,omitempty"`
	MaxSteps    int `json:"max_steps,omitempty"`
	// PlanStepID locates the decision under its delegation/plan-step block in
	// the transcript (host-filled from agent.StepIDFromContext, the root
	// emitter's inline step as fallback). Empty for root-level decisions.
	PlanStepID string `json:"plan_step_id,omitempty"`
	// Network summarizes the network data-flow of a shell-exec decision (the
	// established cradle/ingest/fetch flow, resolved egress host(s), affected
	// operands), making a silent-mode network read diagnosable — and a canonical
	// cradle (fired control) distinguishable from a judge-clearable ingest. Nil
	// for non-shell tools, failed analyses, non-network commands, step_limit.
	Network *NetworkDecision `json:"network,omitempty"`
}

// AutonomyDecisionObserver is invoked once per automatic (no-human) decision,
// in either posture, so the host can emit the auditable event. Must not block.
type AutonomyDecisionObserver func(ctx context.Context, decision AutonomyDecision)

// Autonomy-decision vocabulary the registry fills in (serialized verbatim into
// the autonomy_decision event); step_limit kind/verdicts come from the host.
const (
	autonomyDecisionKindToolConfirm = "tool_confirm"
	// autonomyDecisionKindAssistedDeny: the assisted-mode strict-judge DENY that
	// terminated a call before any card opened — same audit channel (ASI10).
	autonomyDecisionKindAssistedDeny = "assisted_deny"
	autonomyDecisionVerdictAllow     = "allow"
	autonomyDecisionVerdictDeny      = "deny"
	// autonomyDecisionPolicyUserConfirm: recorded when the user_confirm
	// sub-policy auto-approves a fail-closed terminal — Policy names the
	// mechanism that answered the gate.
	autonomyDecisionPolicyUserConfirm = "user_confirm"
)

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		ToolRegistry: sdktools.NewToolRegistry(),
	}
}

// Clone returns a copy sharing the underlying sp4rk registry (tools are
// stateless and shared) with independent policy state, so runtime mutations on
// a clone never leak across concurrent sessions. The clone records its parent —
// the posture source RefreshAutonomyPosture re-syncs from at task launch.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := &ToolRegistry{
		ToolRegistry:               r.ToolRegistry, // shared sp4rk registry (tool definitions)
		confirmFunc:                r.confirmFunc,
		toolCallTimeout:            r.toolCallTimeout,
		judge:                      r.judge,
		preExecuteHook:             r.preExecuteHook,
		postExecuteHook:            r.postExecuteHook,
		toolFilter:                 r.toolFilter,
		logger:                     r.logger,
		autoApproveWorkspaceWrites: r.autoApproveWorkspaceWrites,
		autonomyMode:               r.autonomyMode,
		silentMode:                 r.silentMode,
		judgeObserver:              r.judgeObserver,
		autonomyDecisionObserver:   r.autonomyDecisionObserver,
		parent:                     r,
	}
	if r.disabledTools != nil {
		cloned.disabledTools = make(map[string]bool, len(r.disabledTools))
		for k, v := range r.disabledTools {
			cloned.disabledTools[k] = v
		}
	}
	if r.groupPolicies != nil {
		cloned.groupPolicies = make(map[sdktools.ToolGroup]sdktools.ToolPolicy, len(r.groupPolicies))
		for k, v := range r.groupPolicies {
			cloned.groupPolicies[k] = v
		}
	}
	return cloned
}

// SetDisabledTools sets tool names blocked from execution (No Project mode's
// index-dependent tools). Empty/nil clears; the map is deep-copied.
func (r *ToolRegistry) SetDisabledTools(names map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(names) == 0 {
		r.disabledTools = nil
		return
	}
	r.disabledTools = make(map[string]bool, len(names))
	for k, v := range names {
		r.disabledTools[k] = v
	}
}

// DisabledTools returns a copy of the disabled set, or nil when nothing is disabled.
func (r *ToolRegistry) DisabledTools() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.disabledTools == nil {
		return nil
	}
	out := make(map[string]bool, len(r.disabledTools))
	for k, v := range r.disabledTools {
		out[k] = v
	}
	return out
}

// SetLogger sets the logger for the tool registry. If nil, slog.Default() is used.
func (r *ToolRegistry) SetLogger(l *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = l
}

func (r *ToolRegistry) log() *slog.Logger {
	r.mu.RLock()
	logger := r.logger
	r.mu.RUnlock()
	if logger != nil {
		return logger
	}
	return slog.Default()
}

// SetConfirmFunc sets the confirmation callback for mutating tools.
// If nil, tools requiring confirmation are denied (fail-closed).
func (r *ToolRegistry) SetConfirmFunc(fn sdktools.ConfirmFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirmFunc = fn
}

// SetToolCallTimeout mirrors the executor's per-tool-call ceiling
// (timeouts.toolCallTimeout) so a user-confirmation wait is bounded just under
// it: an outlasting wait yields a CLEAN denial — the tool counts as denied, the
// model is told, the run continues — instead of the watchdog's run-aborting
// ErrToolTimeout. Only the human wait is bounded; a zero value (the default)
// leaves the confirmation unbounded. Safe to call at any time.
func (r *ToolRegistry) SetToolCallTimeout(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCallTimeout = d
}

// SetJudge sets the tool judge for evaluating mutating tool calls.
func (r *ToolRegistry) SetJudge(j *sdktools.ToolJudge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judge = j
}

// SetJudgeObserver registers the strict-judge phase observer; nil disables.
// Invoked in started/finished pairs around the judge's LLM call.
func (r *ToolRegistry) SetJudgeObserver(fn JudgeObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judgeObserver = fn
}

// SetAutonomyDecisionObserver registers the automatic-decision observer; nil
// disables. Invoked once per no-human decision, verdict regardless.
func (r *ToolRegistry) SetAutonomyDecisionObserver(fn AutonomyDecisionObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyDecisionObserver = fn
}

// observeAutonomyDecision notifies the registered observer of a no-human
// decision (best-effort; safe without the lock). It derives the Network
// summary from the per-call ctx (the digest attached by AttachShellAnalysis)
// when the caller left it unset, so every emission path carries the same
// diagnosable summary without deriving it itself.
func (r *ToolRegistry) observeAutonomyDecision(ctx context.Context, d AutonomyDecision) {
	if d.Network == nil {
		d.Network = shellNetworkDecision(ctx, d.Tool)
	}
	r.mu.RLock()
	observer := r.autonomyDecisionObserver
	r.mu.RUnlock()
	if observer != nil {
		observer(ctx, d)
	}
}

// GetJudge returns the current tool judge, or nil if not set.
func (r *ToolRegistry) GetJudge() *sdktools.ToolJudge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.judge
}

// SetGroupPolicies sets the group→policy map Execute consults for every
// non-system tool (security.groups). Missing entries fail safe to
// PolicyUserConfirm; the reserved system group ignores entries.
func (r *ToolRegistry) SetGroupPolicies(policies map[sdktools.ToolGroup]sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupPolicies = policies
}

// ApplySecurityState atomically replaces the whole global security state
// (group→policy map, write auto-approval, autonomy mode, silent-mode
// sub-policies) — the runtime-settings push for the shared builder registry.
// Clones get group policies only (ApplyGroupPolicies); their posture re-syncs
// at task launch. The map is deep-copied (a broadcast push may pass the same
// map to many registries) and everything swaps under one lock, so a
// concurrently executing tool never observes a torn update.
func (r *ToolRegistry) ApplySecurityState(
	policies map[sdktools.ToolGroup]sdktools.ToolPolicy,
	autoApproveWorkspaceWrites bool,
	autonomyMode string,
	silentMode SilentModeState,
) {
	copied := make(map[sdktools.ToolGroup]sdktools.ToolPolicy, len(policies))
	for g, p := range policies {
		copied[g] = p
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupPolicies = copied
	r.autoApproveWorkspaceWrites = autoApproveWorkspaceWrites
	r.autonomyMode = autonomyMode
	r.silentMode = silentMode
}

// ApplyGroupPolicies is ApplySecurityState's live-clone counterpart: it
// replaces ONLY the group→policy map and write auto-approval, preserving the
// pinned autonomy posture — a deny saved in the UI must not fail open on a
// session created before the save. Posture changes reach clones at task launch
// (RefreshAutonomyPosture), or mid-run only when tightening
// (ApplyAutonomyPostureIfTightening). The map is deep-copied as above.
func (r *ToolRegistry) ApplyGroupPolicies(
	policies map[sdktools.ToolGroup]sdktools.ToolPolicy,
	autoApproveWorkspaceWrites bool,
) {
	copied := make(map[sdktools.ToolGroup]sdktools.ToolPolicy, len(policies))
	for g, p := range policies {
		copied[g] = p
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupPolicies = copied
	r.autoApproveWorkspaceWrites = autoApproveWorkspaceWrites
}

// RefreshAutonomyPosture re-syncs the autonomy mode and silent-mode
// sub-policies from the parent — the task-launch, ESCALATION half of posture
// pinning: a clone picks posture changes up only at fresh-task start/resume,
// so a task that started interactive never silently turns unattended mid-run;
// a tightening save reaches a live clone mid-run instead
// (ApplyAutonomyPostureIfTightening). No-op without a parent; both values read
// under one lock so a concurrent save cannot tear the pair.
func (r *ToolRegistry) RefreshAutonomyPosture() {
	if r.parent == nil {
		return
	}
	r.parent.mu.RLock()
	autonomyMode := r.parent.autonomyMode
	silentMode := r.parent.silentMode
	r.parent.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyMode = autonomyMode
	r.silentMode = silentMode
}

// autonomyModeRank orders postures by automaticity for the tightening check:
// silent (2) > assisted (1) > standard/unrecognized (0). LOWER is TIGHTER.
func autonomyModeRank(mode string) int {
	switch mode {
	case AutonomyModeSilent:
		return 2
	case AutonomyModeAssisted:
		return 1
	default:
		return 0
	}
}

// ApplyAutonomyPostureIfTightening applies the posture only when it is a
// TIGHTENING (returns true when applied) — the mid-run exception to posture
// pinning: an operator revoking an unattended posture must stop a running
// clone's auto-approvals immediately, while an equal-or-looser posture is
// ignored. A same-mode save counts as tightening when every incoming silent
// sub-policy is at least as strict (silentModeAtLeastAsStrict). Both values
// are set under one lock, so no torn posture pair is observable.
func (r *ToolRegistry) ApplyAutonomyPostureIfTightening(autonomyMode string, silentMode SilentModeState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	incomingRank := autonomyModeRank(autonomyMode)
	currentRank := autonomyModeRank(r.autonomyMode)
	switch {
	case incomingRank < currentRank:
		// An autonomy de-escalation (tightening) is always delivered.
		r.autonomyMode = autonomyMode
		r.silentMode = silentMode
		return true
	case incomingRank > currentRank:
		// An escalation (loosening) never reaches a running task (pinned).
		return false
	}
	// Same autonomy rank. The sub-policies carry policy only in the silent
	// posture, so only a silent→silent save is a candidate; deliver it when it
	// is a per-field tightening.
	if currentRank != autonomyModeRank(AutonomyModeSilent) {
		return false
	}
	if !silentModeAtLeastAsStrict(silentMode, r.silentMode) {
		return false
	}
	if silentMode == r.silentMode {
		// Nothing actually changed.
		return false
	}
	r.silentMode = silentMode
	return true
}

// silentToolConfirmPermissiveness ranks tool_confirm.mode by unattendedness:
// HIGHER is more permissive; a transition to a lower-or-equal rank is a
// TIGHTENING. Unknown/empty ranks as the documented default (judge).
func silentToolConfirmPermissiveness(mode string) int {
	switch mode {
	case SilentToolConfirmAllow:
		return 2
	case SilentToolConfirmDeny:
		return 0
	default: // judge, empty, unknown
		return 1
	}
}

// silentUserConfirmPermissiveness ranks the user_confirm refinement by
// unattendedness: confirm > deny > escalate; unknown ranks as the documented
// default (deny). Lower-or-equal is a TIGHTENING.
func silentUserConfirmPermissiveness(mode string) int {
	switch mode {
	case SilentUserConfirmConfirm:
		return 2
	case SilentUserConfirmEscalate:
		return 0
	default: // deny, empty, unknown
		return 1
	}
}

// silentStepLimitPermissiveness ranks step_limit.mode by unattendedness:
// allow_always the most, stop (the human decides) the least; unknown ranks as
// the documented default (auto). Literals mirror backend/config's
// SilentStepLimit* constants (core cannot import backend).
func silentStepLimitPermissiveness(mode string) int {
	switch mode {
	case "allow_always":
		return 5
	case "allow_more":
		return 4
	case "allow_once":
		return 3
	case "deny":
		return 1
	case "stop":
		return 0
	default: // auto, empty, unknown
		return 2
	}
}

// silentAskUserPermissiveness ranks ask_user.mode: "disable" (the agent can
// never block on a question) is more permissive than "enable"/unknown.
func silentAskUserPermissiveness(mode string) int {
	if mode == "enable" {
		return 0
	}
	return 1 // disable, empty, unknown
}

// silentModeAtLeastAsStrict reports whether every incoming sub-policy is at
// least as strict as the current one — the condition for delivering a
// same-mode sub-policy change to a running clone.
func silentModeAtLeastAsStrict(incoming, current SilentModeState) bool {
	return silentToolConfirmPermissiveness(incoming.ToolConfirm) <= silentToolConfirmPermissiveness(current.ToolConfirm) &&
		silentUserConfirmPermissiveness(incoming.UserConfirm) <= silentUserConfirmPermissiveness(current.UserConfirm) &&
		silentStepLimitPermissiveness(incoming.StepLimit) <= silentStepLimitPermissiveness(current.StepLimit) &&
		silentAskUserPermissiveness(incoming.AskUser) <= silentAskUserPermissiveness(current.AskUser)
}

// AutonomyMode returns the current autonomy posture; empty means standard.
func (r *ToolRegistry) AutonomyMode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.autonomyMode
}

// SilentMode returns a copy of the current silent-mode posture (security.
// silent_mode); the caller cannot mutate registry state through it.
func (r *ToolRegistry) SilentMode() SilentModeState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.silentMode
}

// GroupPolicies returns a copy of the current group→policy map.
func (r *ToolRegistry) GroupPolicies() map[sdktools.ToolGroup]sdktools.ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[sdktools.ToolGroup]sdktools.ToolPolicy, len(r.groupPolicies))
	for k, v := range r.groupPolicies {
		out[k] = v
	}
	return out
}

// groupPolicy returns the effective policy for a group; a missing entry fails
// safe to PolicyUserConfirm.
func (r *ToolRegistry) groupPolicy(group sdktools.ToolGroup) sdktools.ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.groupPolicies[group]; ok {
		return p
	}
	return sdktools.PolicyUserConfirm
}

// SetAutoApproveWorkspaceWrites toggles session-root auto-approval for
// local_write tools (mechanics enforced in Execute); symlink escapes still
// force confirmation.
func (r *ToolRegistry) SetAutoApproveWorkspaceWrites(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoApproveWorkspaceWrites = enabled
}

// SetAutonomyMode sets the autonomy posture (security.autonomy_mode):
// standard, assisted, or silent — per-posture behavior on the AutonomyMode*
// constants and in smartApproveOrConfirm. Empty/unrecognized is standard.
func (r *ToolRegistry) SetAutonomyMode(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyMode = mode
}

// SetPreExecuteHook sets a hook that is called before every non-system tool execution.
func (r *ToolRegistry) SetPreExecuteHook(hook PreExecuteHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preExecuteHook = hook
}

// SetPostExecuteHook sets the post-execution hook. Registered via defer after
// the tool-not-found/disabled/system early returns, it fires only for calls
// that reach policy resolution; filter on err, result.IsError, and toolName.
func (r *ToolRegistry) SetPostExecuteHook(hook PostExecuteHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.postExecuteHook = hook
}

// SetToolFilter sets a filter that decides whether a tool should be registered.
// If the filter returns false, the tool is rejected during RegisterWithSource.
func (r *ToolRegistry) SetToolFilter(f ToolFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolFilter = f
}

// RegisterWithSource registers a tool with the given source, subject to the tool filter.
// If the filter rejects the tool, it is silently dropped.
func (r *ToolRegistry) RegisterWithSource(tool sdktools.Tool, source string) {
	r.mu.RLock()
	filter := r.toolFilter
	r.mu.RUnlock()
	if filter != nil && !filter(tool.Name(), source) {
		r.log().Debug("tool filtered out during registration", "tool", tool.Name(), "source", source)
		return
	}
	if err := r.ToolRegistry.RegisterWithSource(tool, source); err != nil {
		r.log().Warn("tool registration skipped", "tool", tool.Name(), "source", source, "error", err)
	}
}

// Execute looks up a tool by name and executes it with the given input;
// a missing tool is an error. Security is resolved by the tool's capability
// GROUP, never by name, through the ordered gate pipeline documented in
// specs/domains/tool-system/README.md: structural input validation → disabled
// tools (No Project) → per-server MCP mode gating → system-group bypass →
// pre-execute hook → policy deny → allow/user_confirm branches, where hard
// safety reasons escalate through the autonomy gate under the canonical
// backstop (isCanonicalHardReason) and soft ones confirm unless the strict
// judge allows. Hard reasons never pass Smart Approve: they confirm directly
// with the advisory Ask Agent action disabled.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (result sdktools.ToolResult, err error) {
	// Publish the call start so the confirmation guard shares the watchdog's
	// budget instead of restarting it (see callStartCtxKey).
	ctx = context.WithValue(ctx, callStartCtxKey{}, time.Now())

	tool, ok := r.Get(name)
	if !ok {
		return sdktools.ToolNotFoundResult(ctx, name, func(candidate string) bool {
			_, registered := r.Get(candidate)
			r.mu.RLock()
			disabled := r.disabledTools[candidate]
			r.mu.RUnlock()
			return registered && !disabled
		}), nil
	}

	// Gate 1: structural input validation (ASI02-R2) — every call, including
	// tools whose author forgot per-tool validation, is checked against the
	// tool's InputSchema before dispatch, recursively into nested objects.
	// Fail-open ONLY on unmodeled constructs (empty/unparseable schemas, $ref
	// subtrees); a level with declared properties is closed and declared types
	// are enforced. See specs/domains/tool-system/README.md.
	if verr := sdktools.ValidateToolInput(name, tool.InputSchema(), input); verr != nil {
		return sdktools.ErrorResult("%s", verr), nil
	}

	// Gate 2: disabled tools (No Project mode) — MUST precede the system-group
	// bypass so that tools like semantic_search are blocked at execution time too.
	r.mu.RLock()
	disabled := r.disabledTools
	r.mu.RUnlock()
	if disabled != nil && disabled[name] {
		r.log().Warn("security: tool blocked in No Project mode", "tool", name, "reason", "disabled_in_no_project")
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q is not available in No Project mode", name),
			IsError: true,
		}, nil
	}

	// Gate 2b: per-server MCP mode gating (defense-in-depth). The task-level
	// descriptor filters already hide gated-off servers' tools, but a
	// hallucinated call or a stale registration left by a reconfigure race must
	// still fail closed at dispatch. An absent gated set disables the check;
	// the same category/source metadata as the descriptor filters keeps true
	// built-ins exempt. Must precede the system-group bypass like Gate 2.
	if gated := GatedMCPServersFromContext(ctx); len(gated) > 0 {
		for _, descriptor := range r.List() {
			if descriptor.Name != name {
				continue
			}
			if descriptor.SourceCategory == sdktools.SourceCategoryMCP && gated[descriptor.Source] {
				r.log().Warn("security: tool blocked by MCP server mode gating", "tool", name, "server", descriptor.Source)
				return sdktools.ToolResult{
					Content: fmt.Sprintf("tool %q is unavailable: MCP server %q is not enabled for this task (manual mode without a mention, or disabled)", name, descriptor.Source),
					IsError: true,
				}, nil
			}
			break
		}
	}

	// Gate 3: system group — internal orchestration/state tools bypass the
	// remaining policy and judge checks.
	if sdktools.ToolGroupOf(tool) == sdktools.GroupSystem {
		return tool.Execute(ctx, input)
	}

	// Post-execute hook: deferred so it runs on every return path below; the
	// early returns above are excluded by design (see SetPostExecuteHook).
	r.mu.RLock()
	postHook := r.postExecuteHook
	r.mu.RUnlock()
	if postHook != nil {
		defer func() {
			postHook(ctx, name, result, err)
		}()
	}

	// Get source for hooks and the strict judge.
	source := r.GetToolSource(name)

	// Pre-execute hook (e.g., indexing gate for MCP tools).
	r.mu.RLock()
	hook := r.preExecuteHook
	r.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, name, source); err != nil {
			return sdktools.ToolResult{Content: fmt.Sprintf("pre-execute hook: %v", err), IsError: true}, nil
		}
	}

	group := sdktools.ToolGroupOf(tool)

	// Gate 5: group policy deny — a hard block.
	policy := r.groupPolicy(group)
	if policy == sdktools.PolicyAlwaysDeny {
		r.log().Warn("security: tool blocked by group policy (deny)", "tool", name, "group", string(group))
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy (group %q is set to deny)", name, group),
			IsError: true,
		}, nil
	}

	// Shell-exec tools: run the deterministic flowsh analysis ONCE per call and
	// attach the digest to ctx — one analysis, every judge downstream (see
	// AttachShellAnalysisForTool).
	ctx = AttachShellAnalysisForTool(ctx, tool, name, input, r.log())

	// Tool-local safety signals, gathered once for every policy branch below:
	// the tool's own Judge (command blocklist / SSRF = hard; path containment =
	// soft) and symlink detection (an escape out of the session roots is hard;
	// containment reasons about resolved paths only).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason, symlinkCode := r.symlinkHardReason(ctx, name, tool, input)
	worktreeReason, worktreeCode := worktreeTraversalHardReason(ctx, input)
	reasons := splitSafetyReasons(judgeOutcome, symlinkReason, symlinkCode)
	// The managed-worktree traversal gate is an independent hard signal: it
	// yields only to an already-canonical hard reason, never silently cleared.
	if worktreeReason != "" && !isCanonicalHardReason(reasons.hardCode) {
		reasons.hard, reasons.hardCode = worktreeReason, worktreeCode
	}

	if policy == sdktools.PolicyAlwaysAllow {
		// Hard reasons consult the strict judge like any other escalation, but
		// the deterministic backstop still forces a user confirmation for
		// canonical reasons on the interactive paths (smartApproveOrConfirm);
		// in silent mode the judge's decision is final and audited.
		if reasons.hard != "" {
			r.log().Warn("security: allow-policy tool escalated by hard safety reason",
				"tool", name, "group", string(group), "reason", reasons.hard)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.hard, reasons.hardCode, sdktools.JudgeSeverityHard)
		}
		// Soft reasons are advisory scope questions (e.g. a read outside the
		// session roots): they confirm unless the strict judge allows.
		if reasons.soft != "" {
			r.log().Info("security: allow-policy tool escalated by soft safety reason",
				"tool", name, "group", string(group), "reason", reasons.soft)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.soft, reasons.softCode, sdktools.JudgeSeveritySoft)
		}
		return tool.Execute(ctx, input)
	}

	// Effective policy: PolicyUserConfirm (the fail-safe default for any group
	// without a configured entry).

	// Session-root auto-approval: local_write targets resolving inside the
	// session roots execute without confirmation under
	// auto_approve_workspace_writes — the containment check resolves symlinks
	// and normalizes "..", so an in-root resolution approves while an escape
	// fails. Hard reasons preempt auto-approval.
	r.mu.RLock()
	autoApprove := r.autoApproveWorkspaceWrites
	r.mu.RUnlock()
	if group == sdktools.GroupLocalWrite && autoApprove && reasons.hard == "" && judgeOutcome.Allow {
		r.log().Debug("workspace auto-approve: local_write target within session roots",
			"tool", name, "reason", judgeOutcome.Reason)
		return tool.Execute(ctx, input)
	}

	if reasons.hard != "" {
		r.log().Warn("security: user_confirm tool escalated by hard safety reason",
			"tool", name, "group", string(group), "reason", reasons.hard)
		return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.hard, reasons.hardCode, sdktools.JudgeSeverityHard)
	}

	// Smart Approve is deliberately last among automatic gates: workspace
	// auto-approval kept priority, and the deny/hard-reason paths already
	// returned.
	return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.soft, reasons.softCode, sdktools.JudgeSeveritySoft)
}

// judgeToolCall runs the tool's optional local safety judge. Tools without a
// judge report no concern (zero outcome: no reason, not hard).
func judgeToolCall(ctx context.Context, tool sdktools.Tool, input json.RawMessage) sdktools.JudgeOutcome {
	if judger, ok := tool.(sdktools.ToolJudger); ok {
		return judger.Judge(ctx, input)
	}
	return sdktools.JudgeOutcome{}
}

// isShellToolName reports the shell-exec pair (sdktools' equivalent is
// unexported, mirrored here via the exported name constants) — the only tools
// with a deterministic flowsh analysis digest.
func isShellToolName(name string) bool {
	return name == sdktools.ToolBashExec || name == sdktools.ToolPoshExec
}

// AttachShellAnalysis runs the deterministic flowsh analysis for a shell-exec
// call ONCE and attaches the digest to ctx, so every judge downstream — the
// tool's own SDK Judge, the strict judge, the advisory judge — sees the same
// evidence without re-running the engine
// (specs/decisions/052-flowsh-command-analysis.md). Exported for the backend
// advisory path (backend/application.go evaluateJudgeWith).
//
// A failed analysis is attached as (nil, err): the shell Judge then FAILS
// CLOSED to the canonical command_analysis_unavailable outcome, so the call
// still escalates (and blocks under verify-on-edit) rather than running with
// the deterministic floor silently absent.
//
// NO host-known variable binding is attached before analysis: the executed
// shell is a fresh stateless `bash -c` whose environment carries no D=…, so
// `$D` is genuinely UNSET and `$D/foo` expands to the absolute /foo. Seeding a
// binding would resolve an unassigned `$D` to an in-root directory the command
// never targets — a fail-open downgrading the fail-closed ⊤ escalation to an
// in-root allow. The unassigned `$D` must keep degrading to ⊤
// (specs/architecture/security-model.md, Shell Command Analysis).
func AttachShellAnalysis(ctx context.Context, name string, input json.RawMessage, log *slog.Logger) context.Context {
	return AttachShellAnalysisForTool(ctx, nil, name, input, log)
}

// AttachShellAnalysisForTool is AttachShellAnalysis with the registered tool
// instance: a declared shell kind (an ADR-058 launch-shape override) picks the
// analysis dialect; a nil/kindless tool falls back to the legacy name mapping.
func AttachShellAnalysisForTool(ctx context.Context, tool sdktools.Tool, name string, input json.RawMessage, log *slog.Logger) context.Context {
	if !isShellToolName(name) {
		return ctx
	}
	var (
		analysis *sdktools.ShellAnalysis
		err      error
	)
	switch lang, ok := sdktools.ShellAnalysisLangForTool(tool, name); {
	case ok:
		analysis, err = sdktools.AnalyzeShellCommandForJudgeWithDialect(ctx, lang, input)
	default:
		analysis, err = sdktools.AnalyzeShellCommandForJudge(ctx, name, input)
	}
	if err != nil {
		if log != nil {
			log.Warn("security: shell command analysis failed; shell judge fails closed", "tool", name, "error", err)
		}
	}
	return sdktools.WithShellAnalysis(ctx, analysis, err)
}

// shellAnalysisContext extracts the marshaled digest for the strict judge's
// AnalysisContext field: "" for non-shell tools, a missing analysis, or a
// failed one (the judge then evaluates without static-analysis evidence).
func shellAnalysisContext(ctx context.Context, name string) string {
	if !isShellToolName(name) {
		return ""
	}
	analysis, err := sdktools.ShellAnalysisFrom(ctx)
	if err != nil || analysis == nil {
		return ""
	}
	digest, mErr := json.Marshal(analysis.Digest)
	if mErr != nil {
		// Defensive: ShellAnalysisDigest contains only marshalable fields.
		return ""
	}
	return string(digest)
}

// shellEffectSignature returns the attached analysis's effect signature — the
// silent judge-memo key and an autonomy_decision field, so an audit reader can
// tell which decisions adjudicated the same effect. "" when not analyzable.
func shellEffectSignature(ctx context.Context, name string) string {
	if !isShellToolName(name) {
		return ""
	}
	analysis, err := sdktools.ShellAnalysisFrom(ctx)
	if err != nil || analysis == nil {
		return ""
	}
	return analysis.Digest.Signature
}

// safetyReasons carries the folded signals through the policy branches: at
// most one hard and one soft reason, each with its typed code — deterministic
// policy keys off the code, never the prose (isCanonicalHardReason).
type safetyReasons struct {
	hard     string
	hardCode sdktools.JudgeReasonCode
	soft     string
	softCode sdktools.JudgeReasonCode
}

// splitSafetyReasons folds the collected signals: at most one reason per
// severity, a hard reason always wins, empty strings mean clean. One
// refinement — a NON-canonical judge hard code (e.g. the flowsh ⊤ limitation,
// clearable by the strict judge) must not mask a CANONICAL symlink escape, or
// the deterministic backstop would not force confirmation for an actual
// out-of-roots traversal; every other combination keeps judge precedence.
func splitSafetyReasons(judge sdktools.JudgeOutcome, symlinkReason string, symlinkCode sdktools.JudgeReasonCode) safetyReasons {
	if !judge.Allow && judge.Reason != "" && judge.Severity == sdktools.JudgeSeverityHard {
		if !isCanonicalHardReason(judge.ReasonCode) && symlinkReason != "" && isCanonicalHardReason(symlinkCode) {
			return safetyReasons{hard: symlinkReason, hardCode: symlinkCode}
		}
		return safetyReasons{hard: judge.Reason, hardCode: judge.ReasonCode}
	}
	if symlinkReason != "" {
		return safetyReasons{hard: symlinkReason, hardCode: symlinkCode}
	}
	if !judge.Allow && judge.Reason != "" {
		return safetyReasons{soft: judge.Reason, softCode: judge.ReasonCode}
	}
	return safetyReasons{}
}

// smartApproveOrConfirm applies the autonomy-mode gate (security.autonomy_mode)
// to a confirmation-escalated call — the unified funnel (ADR-026): silent
// resolves through silentToolTerminal with no human; standard opens a
// confirmation card; assisted consults the strict judge, whose ALLOW executes
// except for a canonical hard reason (the deterministic backstop), whose DENY
// terminates the call with the justification (audited as assisted_deny), and
// whose CONFIRM — like a missing/failing/unparseable judge — stays a user
// confirmation with the advisory Ask Agent action disabled. Full semantics:
// specs/architecture/security-model.md.
//
// The user-facing reasoning always keeps the concrete cause: when the strict
// judge is missing, fails, or returns a Confirm without reasoning, only a
// short note is prepended, so the card never degrades to a generic
// "judge unavailable" text hiding WHY the call needs confirmation.
func (r *ToolRegistry) smartApproveOrConfirm(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity) (sdktools.ToolResult, error) {
	r.mu.RLock()
	autonomyMode := r.autonomyMode
	silentMode := r.silentMode
	strictJudge := r.judge
	judgeObserver := r.judgeObserver
	r.mu.RUnlock()

	if reason == "" {
		reason = defaultConfirmReason(name)
	}

	// Silent mode never reaches ConfirmFunc — enabling it is an explicit
	// operator decision to run unattended (see silentToolTerminal).
	if autonomyMode == AutonomyModeSilent {
		return r.silentToolTerminal(ctx, tool, name, source, input, reason, code, severity, silentMode.ToolConfirm, silentMode.UserConfirm, strictJudge, judgeObserver)
	}

	if autonomyMode != AutonomyModeAssisted {
		// A fired hard control keeps DisableJudge=true so the advisory judge
		// cannot weaken it; a soft escalation keeps the advisory judge.
		if severity == sdktools.JudgeSeverityHard {
			return r.confirmAndExecuteWithOptions(ctx, tool, name, input, reason, true)
		}
		return r.confirmAndExecute(ctx, tool, name, input, reason)
	}

	reasoning := "Strict judge is unavailable; " + reason
	verdict := sdktools.VerdictConfirm
	if strictJudge != nil {
		var judgeErr error
		// Surface the judge run to the host so the session status can say a
		// judge is working — at this point no confirmation card exists yet,
		// so a "waiting for your response" label would be misleading.
		if judgeObserver != nil {
			judgeObserver(ctx, JudgePhaseStarted, name)
		}
		// Forward the ctx-carried flowsh digest (AttachShellAnalysis) as the
		// strict judge's static-analysis evidence.
		verdict, reasoning, judgeErr = strictJudge.JudgeStrict(ctx, sdktools.StrictJudgeRequest{
			ToolName:        name,
			Input:           input,
			TaskContext:     sdktools.TaskContextFrom(ctx),
			ToolSource:      source,
			JudgeReasoning:  reason,
			JudgeSeverity:   severity,
			AnalysisContext: shellAnalysisContext(ctx, name),
		})
		if judgeObserver != nil {
			judgeObserver(ctx, JudgePhaseFinished, name)
		}
		if judgeErr != nil {
			verdict = sdktools.VerdictConfirm
			reasoning = "Strict judge evaluation failed; " + reason
		}
	}
	// Deterministic backstop (ASI02, ASI05): a canonical hard reason is never
	// judge-waivable on the interactive paths; the silent path drops this
	// backstop by design (silentJudgeDecide). See
	// specs/architecture/security-model.md (Deterministic backstop).
	if verdict == sdktools.VerdictAllow && severity == sdktools.JudgeSeverityHard && isCanonicalHardReason(code) {
		verdict = sdktools.VerdictConfirm
		reasoning = "A security control fired on this destructive call and cannot be waived by an advisory judge; manual confirmation required. " + reason
	}
	if verdict != sdktools.VerdictAllow && reasoning == "" {
		reasoning = reason
	}

	verdictText := "CONFIRM"
	switch verdict {
	case sdktools.VerdictAllow:
		verdictText = "ALLOW"
	case sdktools.VerdictDeny:
		verdictText = "DENY"
	}
	r.log().Info("security: smart approve verdict",
		"tool", name,
		"source", source,
		"verdict", verdictText,
		"asi_scope", "ASI01,ASI02,ASI03,ASI05,ASI09")

	switch verdict {
	case sdktools.VerdictAllow:
		return tool.Execute(ctx, input)
	case sdktools.VerdictDeny:
		// Assisted-mode auto-deny: terminates without executing, audited as
		// assisted_deny — Policy names the decider ("judge"), Signature the
		// effect (audit §6.3).
		r.observeAutonomyDecision(ctx, AutonomyDecision{
			Kind:          autonomyDecisionKindAssistedDeny,
			Mode:          AutonomyModeAssisted,
			Policy:        SilentToolConfirmJudge,
			Verdict:       autonomyDecisionVerdictDeny,
			Tool:          name,
			Source:        source,
			Signature:     shellEffectSignature(ctx, name),
			Reason:        reason,
			Justification: reasoning,
		})
		return assistedDenial(reasoning), nil
	default:
		return r.confirmAndExecuteWithOptions(ctx, tool, name, input, reasoning, true)
	}
}

// Silent-mode sub-policy mode values — re-declared because core cannot import
// backend; pinned against backend/config by
// TestToBuilderConfig_SilentModeEnumPin, so a rename fails a test run instead
// of silently desynchronizing the security posture.
const (
	// SilentToolConfirmJudge routes a confirmation-gated call through the strict
	// judge. The default mode.
	SilentToolConfirmJudge = "judge"
	// SilentToolConfirmAllow executes a confirmation-gated call with no hard
	// safety reason without consulting the judge.
	SilentToolConfirmAllow = "allow"
	// SilentToolConfirmDeny blocks every confirmation-gated call.
	SilentToolConfirmDeny = "deny"

	// SilentUserConfirmConfirm executes a fail-closed (CONFIRM) outcome
	// unattended, audited as an allow — the operator accepts the flagged risk.
	SilentUserConfirmConfirm = "confirm"
	// SilentUserConfirmDeny is the fail-closed default: a CONFIRM outcome
	// auto-denies with the reasoning, exactly as before the user_confirm
	// refinement existed.
	SilentUserConfirmDeny = "deny"
	// SilentUserConfirmEscalate falls back to the blocking confirmation card —
	// the least permissive value, the only one that may block an unattended run.
	SilentUserConfirmEscalate = "escalate"

	// SilentAskUserDisable registers the ask_user tool in a form that reports
	// itself as unavailable, so an unattended run can never block on a question.
	SilentAskUserDisable = "disable"
)

// silentToolTerminal resolves a confirmation-gated call without a human — it
// NEVER calls ConfirmFunc. The tool_confirm sub-policy selects the terminal
// (ADR-053): "judge" (default) decides through the strict judge with NO
// canonical backstop (decision 1c — the operator delegated to the judge and no
// human can overrule it either way), auto-denying every non-ALLOW outcome;
// "allow" executes calls with no hard reason and escalates any hard reason to
// the judge; "deny" refuses everything unconditionally. Empty/unrecognized
// falls back to "judge". userConfirm (security.silent_mode.user_confirm,
// ADR-072) selects how the judge terminal's fail-closed CONFIRM outcome
// resolves; the deny/allow modes decide outright.
func (r *ToolRegistry) silentToolTerminal(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity, mode, userConfirm string, strictJudge *sdktools.ToolJudge, judgeObserver JudgeObserver) (sdktools.ToolResult, error) {
	if reason == "" {
		reason = defaultConfirmReason(name)
	}
	// The effect signature rides every decision this terminal emits (audit §6.3).
	signature := shellEffectSignature(ctx, name)

	switch mode {
	case SilentToolConfirmDeny:
		r.log().Warn("security: silent mode denied confirmation-gated call",
			"tool", name, "group", string(sdktools.ToolGroupOf(tool)), "mode", mode)
		r.observeAutonomyDecision(ctx, AutonomyDecision{
			Kind:          autonomyDecisionKindToolConfirm,
			Mode:          AutonomyModeSilent,
			Policy:        mode,
			Verdict:       autonomyDecisionVerdictDeny,
			Tool:          name,
			Source:        source,
			Signature:     signature,
			Reason:        reason,
			Justification: "denied outright by security.silent_mode.tool_confirm.mode=deny (no human available)",
		})
		return silentDenial(reason), nil
	case SilentToolConfirmAllow:
		// A fired hard reason is never auto-executed even in the permissive
		// mode — it escalates to the strict judge.
		if severity != sdktools.JudgeSeverityHard {
			result, execErr := tool.Execute(ctx, input)
			r.observeAutonomyDecision(ctx, AutonomyDecision{
				Kind:          autonomyDecisionKindToolConfirm,
				Mode:          AutonomyModeSilent,
				Policy:        mode,
				Verdict:       autonomyDecisionVerdictAllow,
				Tool:          name,
				Source:        source,
				Signature:     signature,
				Reason:        reason,
				Justification: "ran unattended: no hard safety reason (security.silent_mode.tool_confirm.mode=allow)",
			})
			return result, execErr
		}
		return r.silentJudgeDecide(ctx, tool, name, source, input, reason, code, severity, mode, userConfirm, strictJudge, judgeObserver)
	default: // SilentToolConfirmJudge, the empty mode, and any unrecognized value
		return r.silentJudgeDecide(ctx, tool, name, source, input, reason, code, severity, SilentToolConfirmJudge, userConfirm, strictJudge, judgeObserver)
	}
}

// silentJudgeDecide runs the strict judge for a silent-mode escalation: a
// strict ALLOW executes — deliberately with NO canonical backstop (decision
// 1c, ADR-053: the operator delegated to the judge and no human can overrule
// it either way) — and everything else is terminal. A deliberate DENY always
// takes the fail-closed terminal and is NEVER governed by the user_confirm
// refinement; the fail-closed outcomes (a spoken CONFIRM, a missing judge, an
// error/timeout, an unparseable verdict) auto-deny by default but may execute
// ("confirm") or open the card ("escalate") per the refinement (ADR-072).
// Justification prefixes distinguish WHO refused the call (a judge DENY vs the
// fail-closed causes); mode rides the emitted event's Policy field.
//
// Memoization is DENY-only, keyed by effect signature + severity — the
// protocol and its fail-open rationale live on the judgeMemo field. Only a
// judge that SPOKE is memoized (an infrastructure failure is not a verdict
// about the effect); an empty signature never memoizes.
func (r *ToolRegistry) silentJudgeDecide(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity, mode, userConfirm string, strictJudge *sdktools.ToolJudge, judgeObserver JudgeObserver) (sdktools.ToolResult, error) {
	reasoning := "Strict judge is unavailable; " + reason
	verdict := sdktools.VerdictConfirm
	// The effect signature of the attached analysis: the memo key and the
	// audit event's Signature field. "" never memoizes.
	signature := shellEffectSignature(ctx, name)
	if strictJudge != nil {
		// Consult the task-scoped memo before the judge runs: a hit replays
		// the first verdict verbatim, with no judge phases.
		if entry, ok := r.consultJudgeMemo(signature, severity); ok {
			verdict, reasoning = entry.verdict, entry.reasoning
			r.log().Debug("security: silent mode judge verdict reused (memoized effect signature)",
				"tool", name, "source", source)
		} else {
			var judgeErr error
			if judgeObserver != nil {
				judgeObserver(ctx, JudgePhaseStarted, name)
			}
			// Forward the ctx-carried flowsh digest as static-analysis evidence.
			verdict, reasoning, judgeErr = strictJudge.JudgeStrict(ctx, sdktools.StrictJudgeRequest{
				ToolName:        name,
				Input:           input,
				TaskContext:     sdktools.TaskContextFrom(ctx),
				ToolSource:      source,
				JudgeReasoning:  reason,
				JudgeSeverity:   severity,
				AnalysisContext: shellAnalysisContext(ctx, name),
			})
			if judgeObserver != nil {
				judgeObserver(ctx, JudgePhaseFinished, name)
			}
			if judgeErr != nil {
				verdict = sdktools.VerdictConfirm
				reasoning = "Strict judge evaluation failed; " + reason
			} else if verdict == sdktools.VerdictDeny {
				// Only a DENY is memoized — see the judgeMemo field doc.
				r.recordJudgeMemo(signature, severity, verdict, reasoning)
			}
		}
	}
	if reasoning == "" {
		reasoning = reason
	}

	verdictText := "CONFIRM"
	switch verdict {
	case sdktools.VerdictAllow:
		verdictText = "ALLOW"
	case sdktools.VerdictDeny:
		verdictText = "DENY"
	}
	r.log().Info("security: silent mode tool verdict",
		"tool", name,
		"source", source,
		"verdict", verdictText,
		"asi_scope", "ASI01,ASI02,ASI03,ASI05,ASI09")

	// judgeRan distinguishes a real adjudication from the fail-safe CONFIRMs;
	// the escalated card disables the advisory judge whenever the strict judge
	// spoke (matching smartApproveOrConfirm).
	judgeRan := strictJudge != nil && judgeSpoke(verdict, reasoning)

	if verdict == sdktools.VerdictAllow {
		result, execErr := tool.Execute(ctx, input)
		r.observeAutonomyDecision(ctx, AutonomyDecision{
			Kind:          autonomyDecisionKindToolConfirm,
			Mode:          AutonomyModeSilent,
			Policy:        mode,
			Verdict:       autonomyDecisionVerdictAllow,
			Tool:          name,
			Source:        source,
			Signature:     signature,
			Reason:        reason,
			Justification: reasoning,
		})
		return result, execErr
	}
	// A CONFIRM terminal is governed by the user_confirm sub-policy; a
	// deliberate judge DENY never routes here — it always takes the fail-closed
	// terminal below.
	if verdict == sdktools.VerdictConfirm && (userConfirm == SilentUserConfirmConfirm || userConfirm == SilentUserConfirmEscalate) {
		return r.silentConfirmTerminal(ctx, tool, name, source, input, reason, reasoning, verdictText, signature, judgeRan, userConfirm)
	}
	// Terminal denial — the Justification distinguishes a deliberate judge
	// rejection from the fail-closed causes, so the audit trail says WHO
	// refused the call.
	justification := "fail-closed auto-denial (no human available; judge verdict " + verdictText + "): " + reasoning
	if verdict == sdktools.VerdictDeny {
		justification = "strict judge verdict DENY: " + reasoning
	}
	r.observeAutonomyDecision(ctx, AutonomyDecision{
		Kind:          autonomyDecisionKindToolConfirm,
		Mode:          AutonomyModeSilent,
		Policy:        mode,
		Verdict:       autonomyDecisionVerdictDeny,
		Tool:          name,
		Source:        source,
		Signature:     signature,
		Reason:        reason,
		Justification: justification,
	})
	return silentDenial(justification), nil
}

// silentConfirmTerminal resolves a CONFIRM verdict (spoken, missing, errored,
// or unparseable judge) per the user_confirm sub-policy (ADR-072): "confirm"
// executes unattended, audited as an allow naming user_confirm as the policy;
// "escalate" falls back to the blocking card — no autonomy_decision (a human
// decided) — with the advisory judge disabled (DisableJudge=true) whenever the
// strict judge actually ran (judgeRan). The "deny" default never routes here;
// the defensive default below preserves fail-closed behavior if routing
// changes.
func (r *ToolRegistry) silentConfirmTerminal(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason, reasoning, verdictText, signature string, judgeRan bool, userConfirm string) (sdktools.ToolResult, error) {
	switch userConfirm {
	case SilentUserConfirmEscalate:
		return r.confirmAndExecuteWithOptions(ctx, tool, name, input, reasoning, judgeRan)
	case SilentUserConfirmConfirm:
		result, execErr := tool.Execute(ctx, input)
		r.observeAutonomyDecision(ctx, AutonomyDecision{
			Kind:          autonomyDecisionKindToolConfirm,
			Mode:          AutonomyModeSilent,
			Policy:        autonomyDecisionPolicyUserConfirm,
			Verdict:       autonomyDecisionVerdictAllow,
			Tool:          name,
			Source:        source,
			Signature:     signature,
			Reason:        reason,
			Justification: "ran unattended: the fail-closed judge verdict " + verdictText + " was auto-approved by security.silent_mode.user_confirm=confirm (no human available): " + reasoning,
		})
		return result, execErr
	default:
		// Defensive: unreachable via silentJudgeDecide's routing, but a
		// misroute must still fail closed.
		return silentDenial(reasoning), nil
	}
}

// Strict-judge fail-safe reason markers: JudgeStrict reports infrastructure
// failures and twice-unparseable responses as fail-safe CONFIRMs carrying
// these exact strings (mirrored from unexported sp4rk constants, pinned by
// TestStrictJudgeFailureReasonMarkers_Pin so the mirror cannot drift).
const (
	strictJudgeFailureReason = "Strict judge evaluation failed; requiring manual confirmation for safety"
	judgeUnparsedReason      = "Unable to parse judge response; requiring manual confirmation for safety"
)

// judgeSpoke reports an actual adjudication vs a fail-safe infrastructure
// CONFIRM. Only spoken verdicts are memoized — an infrastructure failure is
// not a verdict about the effect, so the next occurrence retries the judge.
func judgeSpoke(verdict sdktools.JudgeVerdict, reasoning string) bool {
	if verdict != sdktools.VerdictConfirm {
		return true
	}
	return reasoning != strictJudgeFailureReason && reasoning != judgeUnparsedReason
}

// judgeMemoEntry is one memoized verdict; a hit replays verdict and reasoning
// verbatim, so a repeat occurrence is byte-identical.
type judgeMemoEntry struct {
	verdict   sdktools.JudgeVerdict
	reasoning string
}

// judgeMemoKey keys the memo on effect signature + escalation severity — the
// signature is everything the analysis says about the call, the severity the
// one envelope dimension that changes what the judge is ASKED.
func judgeMemoKey(signature string, severity sdktools.JudgeSeverity) string {
	return strconv.Itoa(int(severity)) + "\x00" + signature
}

// consultJudgeMemo returns the memoized verdict; only a DENY is ever recorded,
// so a hit is always fail-closed. An empty signature never consults — without
// an effect identity every escalation goes to the judge.
func (r *ToolRegistry) consultJudgeMemo(signature string, severity sdktools.JudgeSeverity) (judgeMemoEntry, bool) {
	if signature == "" {
		return judgeMemoEntry{}, false
	}
	key := judgeMemoKey(signature, severity)
	r.judgeMemoMu.Lock()
	defer r.judgeMemoMu.Unlock()
	entry, ok := r.judgeMemo[key]
	return entry, ok
}

// recordJudgeMemo fixes the FIRST verdict for an effect (audit Track D);
// callers must pass only a DENY — the sole fail-closed verdict. A later
// recording never overwrites, so a retried effect keeps resolving the way it
// first resolved. An empty signature never records.
func (r *ToolRegistry) recordJudgeMemo(signature string, severity sdktools.JudgeSeverity, verdict sdktools.JudgeVerdict, reasoning string) {
	if signature == "" {
		return
	}
	key := judgeMemoKey(signature, severity)
	r.judgeMemoMu.Lock()
	defer r.judgeMemoMu.Unlock()
	if r.judgeMemo == nil {
		r.judgeMemo = make(map[string]judgeMemoEntry)
	}
	if _, exists := r.judgeMemo[key]; exists {
		return
	}
	r.judgeMemo[key] = judgeMemoEntry{verdict: verdict, reasoning: reasoning}
}

// ResetJudgeMemo clears the task-scoped memo at the task-launch boundary
// (Manager.refreshAutonomyPosture): the session clone outlives a task, so this
// explicit reset — not clone lifetime — scopes the memo (see the judgeMemo
// field doc). Tests call it to simulate a fresh task launch.
func (r *ToolRegistry) ResetJudgeMemo() {
	r.judgeMemoMu.Lock()
	defer r.judgeMemoMu.Unlock()
	r.judgeMemo = nil
}

// silentDenial builds the auto-denial ToolResult carrying the concrete reasoning.
func silentDenial(reasoning string) sdktools.ToolResult {
	return sdktools.ToolResult{
		Content: "Automatic denial (silent mode): " + reasoning,
		IsError: true,
	}
}

// assistedDenial builds the terminal ToolResult for an assisted-mode
// strict-judge DENY; it carries the judge's justification.
func assistedDenial(justification string) sdktools.ToolResult {
	return sdktools.ToolResult{
		Content: "Denied by strict judge (assisted mode): " + justification,
		IsError: true,
	}
}

// isCanonicalHardReason reports whether a fired hard reason must never be
// auto-approved by the strict judge: canonical fired controls (blocklist,
// flowsh controls, SSRF, symlink escape, git internals) plus unassessable
// inputs, with the flowsh ⊤ and external-content-ingest codes as the
// deliberate judge-clearable counterexamples. The typed-code list and its
// cross-repo guard test are documented in
// specs/architecture/security-model.md (Deterministic backstop).
func isCanonicalHardReason(code sdktools.JudgeReasonCode) bool {
	switch code {
	case sdktools.ReasonCodeCommandBlacklist,
		sdktools.ReasonCodeCommandExfilFlow,
		sdktools.ReasonCodeCommandPrivilegeEscalation,
		sdktools.ReasonCodeCommandSystemWrite,
		sdktools.ReasonCodeCommandDestructiveOutsideRoots,
		sdktools.ReasonCodeCommandDownloadCradle,
		sdktools.ReasonCodeCommandAnalysisUnavailable,
		sdktools.ReasonCodeSSRFPrivateAddress,
		sdktools.ReasonCodeSSRFDegraded,
		sdktools.ReasonCodeUnassessableURL,
		sdktools.ReasonCodeUnassessablePath,
		sdktools.ReasonCodeSymlinkEscape,
		sdktools.ReasonCodeGitInternal,
		ReasonCodeManagedWorktreeTraversal:
		return true
	default:
		return false
	}
}

// defaultConfirmReason returns the per-tool confirmation text used when no
// richer reason (symlink, judge flag) is available — previously this case
// surfaced an empty prompt explanation. Covers the built-in mutating tools;
// anything else gets the generic statement. UI-facing strings, kept in English
// to match the confirmation card.
func defaultConfirmReason(name string) string {
	switch name {
	case "bash_exec", "posh_exec":
		return "This tool runs a shell command on your system."
	case "write_file":
		return "This tool creates or overwrites a file."
	case "edit_file":
		return "This tool modifies an existing file."
	case "delete_file":
		return "This tool deletes a file."
	case "create_directory":
		return "This tool creates a directory."
	case "delete_directory":
		return "This tool deletes a directory and all of its contents."
	default:
		return "This tool can modify your system and requires your approval before running."
	}
}

// confirmAndExecute requests user confirmation before executing a tool.
func (r *ToolRegistry) confirmAndExecute(ctx context.Context, tool sdktools.Tool, name string, input json.RawMessage, reasoning string) (sdktools.ToolResult, error) {
	return r.confirmAndExecuteWithOptions(ctx, tool, name, input, reasoning, false)
}

// confirmAndExecuteWithOptions requests user confirmation and optionally
// disables the advisory Ask Agent action when strict judging already ran or a
// hard security control fired. Missing confirmation infrastructure is a
// denial, never implicit approval.
func (r *ToolRegistry) confirmAndExecuteWithOptions(ctx context.Context, tool sdktools.Tool, name string, input json.RawMessage, reasoning string, disableJudge bool) (sdktools.ToolResult, error) {
	r.mu.RLock()
	confirmFunc := r.confirmFunc
	ceiling := r.toolCallTimeout
	r.mu.RUnlock()

	if confirmFunc == nil {
		r.log().Warn("security: tool confirmation unavailable; execution denied",
			"tool", name,
			"reason", "confirm_func_nil",
			"asi_scope", "ASI02,ASI09")
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q requires user confirmation, but confirmation is unavailable", name),
			IsError: true,
		}, nil
	}

	// Bound the human wait to yield its clean denial strictly before the
	// executor's ceiling fires: armed against the CALL deadline (start + ceiling
	// − guard) when the call start is known, else a fresh span from now. See
	// confirmCeilingGuard and SetToolCallTimeout.
	confirmCtx := ctx
	confirmBounded := false
	if ceiling > confirmCeilingGuard {
		var cancel context.CancelFunc
		if callStart := callStartFrom(ctx); !callStart.IsZero() {
			confirmCtx, cancel = context.WithDeadline(ctx, callStart.Add(ceiling-confirmCeilingGuard))
		} else {
			confirmCtx, cancel = context.WithTimeout(ctx, ceiling-confirmCeilingGuard)
		}
		defer cancel()
		confirmBounded = true
	}

	resp, err := confirmFunc(confirmCtx, sdktools.ConfirmationRequest{
		ToolName:       name,
		Input:          input,
		JudgeReasoning: reasoning,
		DisableJudge:   disableJudge,
	})
	if err != nil {
		// An outlasted ceiling is a clean denial, not a run failure: the run
		// context is still live — only this wait's derived deadline fired.
		// Every other error, including genuine cancellation/shutdown, propagates
		// unchanged.
		if confirmBounded && ctx.Err() == nil && errors.Is(confirmCtx.Err(), context.DeadlineExceeded) {
			r.log().Warn("security: tool confirmation timed out; execution denied",
				"tool", name,
				"reason", "confirmation_timeout",
				"asi_scope", "ASI02,ASI09")
			return sdktools.ToolResult{
				Content: fmt.Sprintf("tool %q was not executed: the confirmation request was not answered within the per-tool-call time limit", name),
				IsError: true,
			}, nil
		}
		return sdktools.ToolResult{}, err
	}

	switch resp {
	case sdktools.ConfirmAllowOnce:
		return tool.Execute(ctx, input)
	case sdktools.ConfirmDeny:
		msg := "Tool execution denied by user."
		if reasoning != "" {
			msg += " Reason for confirmation request: " + reasoning
		}
		return sdktools.ToolResult{Content: msg, IsError: true}, nil
	case sdktools.ConfirmDenyAndStop:
		return sdktools.ToolResult{}, context.Canceled
	default:
		return sdktools.ToolResult{}, fmt.Errorf("unknown confirmation response: %d", resp)
	}
}
