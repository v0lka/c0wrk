package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// goalModeTools is the set of system-group tools that exist ONLY for goal mode:
// they have no meaning (and must not be offered to the agent) outside an
// active goal pursuit.
//   - propose_goal          starts a goal (derivation phase)
//   - declare_goal_status   reports the agent's self-evaluation verdict
//   - declare_verification  reports the independent verifier's verdict
//
// All three are already system tools (always allowed, policy/judge-exempt,
// hidden from the security UI). This set is concerned purely with
// AVAILABILITY: it tells the orchestrator which system tools to strip from a
// non-goal Conductor run's available-tool list, so the agent never sees
// goal-only tools when goal mode is off. The goal loop and the independent
// verifier deliberately receive the UNSTRIPPED list — verifierToolFilter/
// verifierReDerivationToolFilter build their read-only toolset (which must
// include declare_verification) from it.
var goalModeTools = map[string]struct{}{
	"propose_goal":         {},
	"declare_goal_status":  {},
	"declare_verification": {},
}

// IsGoalModeTool returns true if the given tool name exists ONLY for goal
// mode. Such tools are system tools (policy-exempt) but are additionally
// gated: they are offered to the agent only when the session is running a
// goal loop (HandleMessage/ResumeTask strip them from the available-tool list
// on the non-goal path).
func IsGoalModeTool(name string) bool {
	_, ok := goalModeTools[name]
	return ok
}

// StripGoalModeTools removes goal-mode-only tools from a tool-descriptor list.
// It is the single helper the orchestrator uses to build the available-tool
// view for a NON-goal Conductor run (HandleMessage/ResumeTask normal path) so
// goal-specific tools never reach an agent outside a goal pursuit.
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

// Autonomy-mode values — the shared vocabulary between core's
// BuilderSecurityConfig.AutonomyMode and the registry's interpretation of it
// (core/tools cannot import core, so the strings are re-declared here, the
// same pattern as the SilentToolConfirm* constants below; the two
// dictionaries are pinned against each other by core/silent_mode_test.go). An
// empty or unrecognized value behaves as standard — fail-safe: no automatic
// evaluation, every gated call opens a confirmation card.
const (
	// AutonomyModeStandard is the interactive posture: no automatic gate,
	// every confirmation-gated call opens a user confirmation card.
	AutonomyModeStandard = "standard"
	// AutonomyModeAssisted enables the strict judge (the former Smart
	// Approve): a strict ALLOW executes without UI, a strict DENY terminates
	// the call with the judge's justification (no card), everything else —
	// CONFIRM, a missing/failing/unparseable judge — opens a card.
	AutonomyModeAssisted = "assisted"
	// AutonomyModeSilent is the unattended posture: no card can open; the
	// silent-mode sub-policies (SilentModeState below) resolve every gated
	// call to a terminal execute-or-deny.
	AutonomyModeSilent = "silent"
)

// SilentModeState is the registry's snapshot of the security.silent_mode
// sub-policies — the unattended posture's policy container. Whether the
// posture itself is LIVE is decided by the autonomy mode
// (AutonomyModeSilent), never by a bool here: the sub-policies are inert in
// the standard and assisted modes. It is a plain value type (no pointers,
// slices, or maps) so Clone may copy it verbatim, and so ApplySecurityState
// replaces it atomically alongside the rest of the security state under one
// lock. The mode strings are the config enum values passed through from the
// builder; the registry stores them without interpreting them (consumers read
// the fields they need).
type SilentModeState struct {
	ToolConfirm  string
	StepLimit    string
	AskUser      string
	ReviewPrompt string
}

// ToolRegistry stores all available tools and provides them to Executor.
// It embeds the sp4rk ToolRegistry for basic operations and adds group-based
// policy enforcement on top. Thread-safe via sync.RWMutex.
//
// TODO(S-14): Replace embedding with composition — store *sdktools.ToolRegistry as a
// private field and explicitly delegate only the methods the core layer intends
// to expose. This gives full control over the public surface area and prevents
// accidental exposure of sp4rk-internal methods to callers. The refactor requires
// auditing all callers that access sp4rk methods through the embedded type.
type ToolRegistry struct {
	// Deprecated: embedded for backward compatibility. TODO(S-14): Replace with
	// composition — store *sdktools.ToolRegistry as a private field and
	// explicitly delegate only the methods the core layer intends to expose.
	*sdktools.ToolRegistry
	mu                         sync.RWMutex
	confirmFunc                sdktools.ConfirmFunc
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
	// judgeMemoMu guards judgeMemo. It is deliberately independent of mu: the
	// memo has its own lifecycle (task-scoped, never touched by the config
	// setters mu protects), and silent-path reads must not contend with
	// Settings pushes for it.
	judgeMemoMu sync.Mutex
	// judgeMemo memoizes silent-mode strict-judge verdicts by the shell
	// EFFECT signature (ShellAnalysisDigest.Signature) — Track D of the
	// silent-mode deny-accuracy audit
	// (silent-mode-deny-accuracy-recommendations.md §3): the first verdict
	// for an effect is fixed and replayed verbatim on re-escalation, so
	// judge non-determinism on identical input (audit pair 961130 allow vs
	// 961162 deny — the same vitest run, tail -20 vs -15) can no longer flip
	// a retry, and retries stop re-paying the strict-judge call. Deliberately
	// NOT copied by Clone — but clone lifetime is NOT task lifetime: a
	// session's registry clone is created once
	// (OrchestratorBuilder.registerSessionRegistry) and reused for every task
	// in that session, so an unreset memo would leak a verdict across
	// distinct tasks. The memo is therefore scoped to a task by an explicit
	// reset at each task-launch boundary — ResetJudgeMemo, called from
	// Manager.refreshAutonomyPosture — not by the clone's lifetime.
	judgeMemo map[string]judgeMemoEntry
	// parent is the shared builder registry this clone was cut from (nil on
	// the shared registry itself). Clones pin their autonomy posture at task
	// launch — a runtime Settings save must not flip the posture of a task
	// already running (see RefreshAutonomyPosture) — so the clone keeps a
	// link to the authoritative posture source instead.
	parent *ToolRegistry
}

// PreExecuteHook is called before tool execution. It may block to wait for
// preconditions (e.g., indexing completion). If it returns an error, execution
// is aborted and the error is returned as a tool error result.
type PreExecuteHook func(ctx context.Context, toolName string, source string) error

// PostExecuteHook is called after a non-system tool execution path completes
// (regardless of success or error). It receives the tool name, the result, and
// the execution error so it can react to file mutations (e.g., triggering
// vector index refresh) while distinguishing genuine successes from failed
// attempts (user confirmation denied, context cancellation, confirm-func
// failure, etc.). The hook must not block — long-running work should be
// dispatched to a goroutine. If the hook panics, the panic propagates to the
// caller; hooks should be defensive.
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

// JudgeObserver is invoked around the strict judge (Smart Approve) evaluation
// of an escalated tool call. It lets the host surface an honest "judge is
// working" session status while the evaluation runs — a confirmation card does
// not exist yet at JudgePhaseStarted (it is only issued when the judge does
// not return a strict ALLOW, so a plain "waiting for your response" status
// would mislead the user). The observer must not block; it receives the
// executor context (which carries the session ID) and the tool name.
type JudgeObserver func(ctx context.Context, phase JudgePhase, toolName string)

// AutonomyDecision is one autonomous (no-human) security decision the registry
// took: a confirmation-gated tool call resolved without a confirmation card
// (tool_confirm, in silent mode), a strict-judge DENY that terminated a call
// in the assisted mode before any card opened (assisted_deny), or — filled in
// by the host — a step-limit boundary resolved without the blocking
// step-limit prompt (step_limit). It is the auditable record of a gate a
// human would otherwise have answered, so the run's trajectory stays
// reconstructable (OWASP ASI10). The strict-judge phase telemetry
// (JudgeObserver) reports only that a judge RAN; this records WHAT it
// decided. The struct is JSON-tagged because the host serializes it verbatim
// into the `autonomy_decision` session event.
type AutonomyDecision struct {
	// Kind is the gate resolved autonomously: "tool_confirm",
	// "assisted_deny", or "step_limit".
	Kind string `json:"kind"`
	// Mode is the autonomy posture that took the decision: "assisted" or
	// "silent" (AutonomyModeAssisted/AutonomyModeSilent) — which automatic
	// posture answered the gate.
	Mode string `json:"mode,omitempty"`
	// Policy is the posture's sub-policy that decided: the silent-mode
	// tool_confirm mode ("judge"|"allow"|"deny"), the step_limit mode
	// ("auto" or a pinned response), or "judge" for assisted_deny — the
	// strict judge itself is the decider there. Guaranteed non-empty on
	// every emitted decision: all registry/step-limit emitters fill it, and
	// the host funnel (Manager.EmitAutonomyDecision) defaults an empty
	// policy from the decision kind (audit §6.3 — 5 of 8 TRUE_DENY corpus
	// events carried policy=None, losing the deciding mechanism).
	Policy string `json:"policy,omitempty"`
	// Verdict is the decision: "allow"|"deny" for tool_confirm, or the
	// step-limit response ("allow_once"|"allow_more"|"allow_always"|"deny").
	Verdict string `json:"verdict"`
	// Tool is the tool name for a tool_confirm decision; empty for step_limit.
	Tool string `json:"tool,omitempty"`
	// Source is the tool source ("core" or an MCP server name) for a
	// tool_confirm decision.
	Source string `json:"source,omitempty"`
	// Signature is the deterministic effect signature of the analyzed call
	// (sdktools.ShellAnalysisDigest.Signature: resolved driver binaries,
	// canonical effects, fired criteria, the workspace-scoping marker). It
	// rides shell-exec tool decisions, where it doubles as the silent-mode
	// strict-judge memoization key — identical signature within a task
	// resolves identically — so an audit reader can tell which decisions
	// adjudicated the same effect. Empty for non-shell tools, failed
	// analyses, and non-tool gates (step_limit).
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
	// PlanStepID locates the decision in the transcript: the delegation or
	// plan-step block whose executor took it. Host-filled from the executor
	// context (agent.StepIDFromContext — RunSubAgent stamps every subagent
	// launch) with the root emitter's current inline step as the fallback, so
	// the notice nests under the matching subagent/plan-step block instead of
	// the main chat stream. Empty for root-level decisions.
	PlanStepID string `json:"plan_step_id,omitempty"`
}

// AutonomyDecisionObserver is invoked once per automatic (no-human) decision,
// after the registry has resolved the outcome. It lets the host emit an
// auditable, non-blocking `autonomy_decision` session event for a gate that was
// answered without a human — in either automatic posture (assisted or silent).
// The observer must not block; it receives the executor context (which carries
// the session ID) and the decision.
type AutonomyDecisionObserver func(ctx context.Context, decision AutonomyDecision)

// Autonomy-decision vocabulary the registry fills in (the host serializes
// these verbatim into the autonomy_decision event). The step_limit
// kind/verdicts are produced by the host's step-limit resolver, not here.
const (
	autonomyDecisionKindToolConfirm = "tool_confirm"
	// autonomyDecisionKindAssistedDeny is the assisted-mode counterpart of
	// tool_confirm: the strict judge terminated a confirmation-gated call
	// with a deliberate DENY before any card opened. It shares the
	// autonomy_decision audit channel so the trajectory stays reconstructable
	// (ASI10) for BOTH automatic postures — assisted and silent.
	autonomyDecisionKindAssistedDeny = "assisted_deny"
	autonomyDecisionVerdictAllow     = "allow"
	autonomyDecisionVerdictDeny      = "deny"
)

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		ToolRegistry: sdktools.NewToolRegistry(),
	}
}

// Clone returns a copy of this ToolRegistry that shares the underlying sp4rk
// ToolRegistry (tools themselves are stateless and shared) but has independent
// policy state (groupPolicies, judge, confirmFunc, hooks). This gives each
// session/orchestrator its own policy view so runtime mutations on a clone do
// not leak across concurrent sessions. The clone records its parent (the
// registry it was cut from) so RefreshAutonomyPosture can re-sync the pinned
// autonomy posture from the authoritative source at task launch.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := &ToolRegistry{
		ToolRegistry:               r.ToolRegistry, // shared sp4rk registry (tool definitions)
		confirmFunc:                r.confirmFunc,
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

// SetDisabledTools sets tool names that are blocked from execution.
// Used by No Project mode to disable index-dependent tools (semantic_search —
// no vector index exists without a project).
// An empty or nil map clears all disabled tools.
// The caller's map is deep-copied so future mutations to it do not affect the registry.
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

// DisabledTools returns a copy of the current set of disabled tool names.
// Returns nil if no tools are disabled.
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

// SetJudge sets the tool judge for evaluating mutating tool calls.
func (r *ToolRegistry) SetJudge(j *sdktools.ToolJudge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judge = j
}

// SetJudgeObserver registers the strict-judge phase observer (Smart Approve).
// Nil disables observation. The observer is only invoked while Smart Approve
// is enabled and a strict judge is configured, always in started/finished
// pairs around the judge's LLM call.
func (r *ToolRegistry) SetJudgeObserver(fn JudgeObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judgeObserver = fn
}

// SetAutonomyDecisionObserver registers the automatic-decision observer. Nil
// disables observation. It is invoked once per automatic decision the registry
// takes without a human (silentToolTerminal in silent mode; an assisted-mode
// strict-judge DENY), regardless of the verdict, so the host can record an
// auditable `autonomy_decision` event.
func (r *ToolRegistry) SetAutonomyDecisionObserver(fn AutonomyDecisionObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyDecisionObserver = fn
}

// observeAutonomyDecision notifies the registered autonomy-decision observer,
// if any, of an autonomous (no-human) decision. Best-effort: a nil observer is
// a no-op, and it is safe to call with no lock held (it takes its own RLock).
func (r *ToolRegistry) observeAutonomyDecision(ctx context.Context, d AutonomyDecision) {
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

// SetGroupPolicies sets the group→policy map that Execute consults for every
// non-system tool (security.groups in config.yaml). It replaces any previous
// map. Groups without an entry resolve to PolicyUserConfirm (fail-safe); the
// reserved system group is never configurable and ignores entries.
func (r *ToolRegistry) SetGroupPolicies(policies map[sdktools.ToolGroup]sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupPolicies = policies
}

// ApplySecurityState atomically replaces the registry's global security
// state: the group→policy map, session-root write auto-approval, the autonomy
// mode, and the silent-mode sub-policies. It is the push API for runtime
// security-settings updates (applySecurityPolicies), used for the shared
// builder registry; the live per-session clones are updated with group
// policies and auto-approval only (ApplyGroupPolicies) — their autonomy
// posture is re-synced from the shared registry at each task launch
// (RefreshAutonomyPosture), never mid-run. The
// policies map is deep-copied so the caller's map never aliases registry
// state — a broadcast push may pass the same map to many registries, and each
// must stay independently mutable (Clone contract). Replacing the whole map
// and every scalar under one lock acquisition also means a concurrently
// executing tool never observes a torn update (e.g. new group policies with
// the old autonomy mode or silent-mode state).
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

// ApplyGroupPolicies atomically replaces ONLY the group→policy map and the
// session-root write auto-approval, preserving the autonomy mode and the
// silent-mode sub-policies. It is the runtime-push counterpart of
// ApplySecurityState for live per-session clones: group policies and
// auto-approval are shared, fail-closed posture that a Settings save must
// deliver to already-running sessions (a deny set in the UI must not fail
// open on a session created before the save), while the autonomy posture is
// pinned per task at launch (see RefreshAutonomyPosture) and — in the
// escalation direction — must never be flipped under a running task. A
// tightening (de-escalation) save is the one exception: it is delivered to
// live clones separately via ApplyAutonomyPostureIfTightening, which
// applySecurityPolicies also calls on each clone in the same push. Like
// ApplySecurityState, the policies map is deep-copied so a broadcast push may
// pass the same map to many registries.
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

// RefreshAutonomyPosture re-syncs this registry's autonomy mode and
// silent-mode sub-policies from its parent (the shared builder registry the
// clone was cut from). It is the task-launch half of the per-task posture
// pinning contract and carries the ESCALATION direction: a Settings save
// updates the shared registry immediately, but a session clone picks a
// between-tasks posture change up only here — at fresh-task start and at
// resume — so a task that started interactive can never silently turn
// unattended mid-run, and a paused task resumed after a Settings change runs
// under the posture the user sees in Settings. The DE-ESCALATION direction is
// handled mid-run instead by ApplyAutonomyPostureIfTightening: a tightening
// Settings save (revoking an unattended posture) reaches a live clone
// immediately, so a running task stops auto-approving the moment the operator
// reasserts human control. A no-op on the shared registry itself (no parent)
// and on clones whose parent is nil. Both values are read from the parent
// under one lock acquisition, so a concurrent Settings save cannot tear the
// pair.
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

// autonomyModeRank orders the autonomy postures by automaticity for the
// tightening check of ApplyAutonomyPostureIfTightening: silent (2) is the most
// unattended, assisted (1) is next, and standard — or any unrecognized/empty
// value — is the human-in-the-loop baseline (0). A LOWER rank is a TIGHTER
// (less automatic) posture.
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

// ApplyAutonomyPostureIfTightening applies the autonomy mode and silent-mode
// sub-policies to this registry only when they are a TIGHTENING — a
// less-permissive posture than the one the registry currently holds
// (autonomyModeRank(autonomyMode) < autonomyModeRank(r.autonomyMode)). It
// returns true when it applied the change, or false when it left the registry
// untouched (the incoming posture ranks at or above the current one).
//
// It is the exception to the per-task posture pinning (see
// RefreshAutonomyPosture and applySecurityPolicies). Pinning exists so a task
// can never silently BECOME unattended mid-run — an escalation the operator
// did not intend for a task already in flight — and this method preserves
// that: an equal or looser posture is ignored. But the reverse direction must
// not fail open: when an operator revokes an unattended posture (Security back
// to Assisted/Standard) while a task runs, the running clone must stop
// auto-approving immediately, so a tightening save reaches it here. Both
// values are set under one lock acquisition so a concurrently executing tool
// never observes a torn posture pair.
func (r *ToolRegistry) ApplyAutonomyPostureIfTightening(autonomyMode string, silentMode SilentModeState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if autonomyModeRank(autonomyMode) >= autonomyModeRank(r.autonomyMode) {
		return false
	}
	r.autonomyMode = autonomyMode
	r.silentMode = silentMode
	return true
}

// AutonomyMode returns the registry's current autonomy posture
// (security.autonomy_mode): standard, assisted, or silent. Empty means
// standard.
func (r *ToolRegistry) AutonomyMode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.autonomyMode
}

// SilentMode returns the registry's current silent-mode posture (security.
// silent_mode). The returned value is a copy — the caller cannot mutate
// registry state through it.
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

// groupPolicy returns the effective policy for a tool group. A group without a
// configured entry fails safe to PolicyUserConfirm — the same posture as the
// config group defaults (reads may be widened to allow; everything else
// confirms).
func (r *ToolRegistry) groupPolicy(group sdktools.ToolGroup) sdktools.ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.groupPolicies[group]; ok {
		return p
	}
	return sdktools.PolicyUserConfirm
}

// SetAutoApproveWorkspaceWrites enables or disables session-root-based
// auto-approval for tools in the local_write group with an effective
// PolicyUserConfirm. When enabled, write_file, edit_file, delete_file,
// delete_directory, and create_directory auto-execute without confirmation
// when their targets resolve inside the session roots (workspace, temp
// directory, and any additional allowed roots — equal peers). Symlink
// traversals that resolve out of the session roots still force confirmation.
func (r *ToolRegistry) SetAutoApproveWorkspaceWrites(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoApproveWorkspaceWrites = enabled
}

// SetAutonomyMode sets the autonomy posture (security.autonomy_mode):
//
//   - AutonomyModeStandard — every gated call opens a confirmation card;
//   - AutonomyModeAssisted — the strict judge resolves gated calls (the
//     former Smart Approve): ALLOW executes, DENY terminates with the judge's
//     justification, everything else opens a card;
//   - AutonomyModeSilent — unattended: the silent-mode sub-policies resolve
//     every gated call (see silentToolTerminal).
//
// An empty or unrecognized value is treated as standard (fail-safe).
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

// SetPostExecuteHook sets a hook that is called after every non-system tool
// execution path completes. The hook receives the tool name, result, and
// execution error. The hook is registered via defer after the tool-not-found,
// disabled-tool, and system-group early returns, so it fires only for tools
// that reach policy/security resolution: successful execution, policy denials,
// pre-execute-hook errors, and user-confirmation outcomes (allow/deny). It
// does NOT fire when the tool is not found, disabled, or a
// system tool. The hook should filter on err, result.IsError, and toolName to
// avoid unnecessary work.
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

// Execute looks up a tool by name and executes it with the given input.
// Returns an error if the tool is not found.
//
// Security is resolved by the tool's capability GROUP (sdktools.ToolGroupOf), never by
// tool name. Gate order:
//
//  1. structural input validation (sdktools.ValidateToolInput — required
//     keys, JSON types, unknown keys, recursively into nested objects and
//     array items),
//  2. disabled tools (No Project mode) — applies to every tool, system included,
//  3. system group → execute directly (internal orchestration tools),
//  4. pre-execute hook,
//  5. group policy deny → block,
//  6. group policy allow: tool Judge + symlink signals — a HARD reason
//     (command blocklist, the flowsh shell-analysis controls, SSRF, symlink
//     escape) escalates through the autonomy gate, where the canonical
//     backstop (isCanonicalHardReason) forces a user confirmation even on a
//     strict ALLOW in the assisted mode (rule 2); a SOFT reason (path
//     containment) goes to the autonomy gate and confirms unless the strict
//     judge allows; a clean call executes,
//  7. group policy user_confirm: local_write tools with
//     auto_approve_workspace_writes whose paths resolve inside the session
//     roots execute; everything else goes through the autonomy gate (never
//     around a hard reason) and otherwise confirms.
//
// Hard reasons never pass Smart Approve: they confirm directly with the
// advisory Ask Agent action disabled.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (result sdktools.ToolResult, err error) {
	tool, ok := r.Get(name)
	if !ok {
		return sdktools.ToolResult{Content: "tool not found: " + name, IsError: true}, nil
	}

	// Gate 1: centralized structural input validation (ASI02-R2,
	// defense-in-depth) via the SDK's general validator
	// (sdktools.ValidateToolInput). Every call — including tools whose author
	// forgot per-tool validation — is checked against the tool's InputSchema
	// BEFORE dispatch: required keys, JSON types of declared properties, and
	// unknown keys, RECURSIVELY into nested objects and array items, so a
	// schema-violating payload (e.g. declare_plan tasks without ids) is
	// rejected up front with an actionable message naming the offending path
	// (tasks[2].id) and the valid parameters. Fail-open ONLY on unmodeled
	// constructs: empty/unparseable schemas, $ref subtrees, and levels
	// without a declared property set are skipped — but a level WITH a
	// declared property set is closed (unknown keys rejected) and declared
	// types are enforced, so a call carrying extra keys or off-type values
	// is rejected here even when the tool body would have tolerated it
	// (json.Unmarshal ignores unknown fields and coerces nulls).
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

	// Gate 3: system group — internal orchestration/state tools bypass the
	// remaining policy and judge checks.
	if sdktools.ToolGroupOf(tool) == sdktools.GroupSystem {
		return tool.Execute(ctx, input)
	}

	// Post-execute hook: deferred so it runs on every return path below
	// (policy denials, pre-execute-hook errors, successful execution, etc.).
	// The hook filters on err, result.IsError, and toolName to skip irrelevant
	// calls. It does not cover the early returns above (tool not found,
	// disabled tool, system tool) — see SetPostExecuteHook docs.
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

	// Shell-exec tools (bash_exec/posh_exec): run the deterministic flowsh
	// analysis ONCE per call and attach the digest to ctx. The tool's own
	// Judge consumes it through sdktools.ShellJudgeOutcome (its blocklist
	// stage aside, its criteria come exclusively from the attached analysis),
	// and smartApproveOrConfirm forwards the same digest to the strict judge
	// as static-analysis evidence — one analysis, every judge.
	ctx = AttachShellAnalysis(ctx, name, input, r.log())

	// Tool-local safety signals, gathered once and shared by every policy
	// branch below:
	//   - the tool's own Judge (command blocklist / SSRF = hard; path
	//     containment = soft),
	//   - symlink traversal detection (an escape out of the session roots =
	//     hard; a symlink whose resolution stays inside the roots is NOT a
	//     concern — containment reasons about resolved paths; shell variable
	//     expansions are flowsh's domain, not the symlink gate's).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason, symlinkCode := r.symlinkHardReason(ctx, name, tool, input)
	reasons := splitSafetyReasons(judgeOutcome, symlinkReason, symlinkCode)

	if policy == sdktools.PolicyAlwaysAllow {
		// Hard reasons are security-control triggers (command blocklist, SSRF,
		// symlink escapes) or unassessable inputs. They now consult the
		// strict judge like any other escalation, but the deterministic
		// backstop in smartApproveOrConfirm still forces a user confirmation
		// for canonical reasons even when the strict judge returns ALLOW in
		// the assisted mode (rule 2) — path-locality or a lenient judge must
		// not bypass a fired security control, and an unassessable call is
		// not for the judge to resolve. In silent mode the judge's decision
		// is final (decision 1c): its ALLOW executes and is audited.
		if reasons.hard != "" {
			r.log().Warn("security: allow-policy tool escalated by hard safety reason",
				"tool", name, "group", string(group), "reason", reasons.hard)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.hard, reasons.hardCode, sdktools.JudgeSeverityHard)
		}
		// Soft reasons are advisory scope questions (e.g. a read outside the
		// session roots): Smart Approve weighs them and confirms unless the
		// strict judge allows. Without Smart Approve they fall back to a plain
		// confirmation.
		if reasons.soft != "" {
			r.log().Info("security: allow-policy tool escalated by soft safety reason",
				"tool", name, "group", string(group), "reason", reasons.soft)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.soft, reasons.softCode, sdktools.JudgeSeveritySoft)
		}
		return tool.Execute(ctx, input)
	}

	// Effective policy: PolicyUserConfirm (the fail-safe default for any group
	// without a configured entry).

	// Session-root auto-approval: local_write tools whose targets resolve
	// inside the session roots (workspace, temp directory, and additional
	// allowed roots — equal peers) execute without confirmation when
	// auto_approve_workspace_writes is enabled. The Judge's containment check
	// resolves symlinks and normalizes ".." (pathutil underneath), so a
	// symlink whose resolution stays inside the roots auto-approves while an
	// escape fails containment. Hard reasons preempt auto-approval.
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

	// Smart Approve is deliberately last among automatic gates and applies
	// only to the effective user_confirm policy. Workspace auto-approval above
	// therefore retains priority, while deny and hard-reason paths have
	// already returned before reaching this point.
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

// isShellToolName reports whether the named tool is one of the shell-exec
// pair (sdktools' isShellTool equivalent — unexported there, so this local
// name-keyed check mirrors it via the exported name constants). Shell-exec
// tools are the only ones with a deterministic flowsh analysis digest.
func isShellToolName(name string) bool {
	return name == sdktools.ToolBashExec || name == sdktools.ToolPoshExec
}

// AttachShellAnalysis runs the deterministic flowsh analysis
// (sdktools.AnalyzeShellCommandForJudge) for a shell-exec call ONCE and
// attaches the digest to ctx via sdktools.WithShellAnalysis, so every judge
// downstream sees the same evidence without re-running the engine: the
// tool's own SDK Judge (sdktools.ShellJudgeOutcome), the strict judge
// (StrictJudgeRequest.AnalysisContext, see smartApproveOrConfirm), and the
// advisory judge (its in-prompt analysis block). Non-shell tools return ctx
// unchanged.
//
// A failed analysis (e.g. the flowsh analyzer's knowledge base could not be
// loaded — a sticky, process-lifetime failure) is attached as (nil, err) and
// logged. The shell tools' Judge then FAILS CLOSED: sdktools.ShellJudgeOutcome
// turns the attachment error into a hard canonical
// ReasonCodeCommandAnalysisUnavailable outcome (see sp4rk tools/shellanalysis.go),
// so the call still escalates under an `allow` policy and blocks under
// verify-on-edit's unattended path rather than running with the deterministic
// floor (C1–C8) silently absent. The strict judge's AnalysisContext stays ""
// in that case — the escalation is carried by the Judge, not the digest.
// Exported for the backend advisory path (backend/application.go
// evaluateJudgeWith).
//
// Before analysing, NO host-known variable binding is attached. The executed
// shell is a fresh, stateless `bash -c` process whose environment carries no
// D=… assignment (sp4rk runs it via exec.CommandContext without setting D),
// so a cross-command expansion such as `$D` is genuinely UNSET at runtime —
// `$D/foo` expands to the absolute `/foo`, outside the session roots. Seeding
// a binding for `$D` would make the analyzer resolve an UNASSIGNED `$D` to an
// in-root temp directory the command never actually targets: a fail-open that
// downgrades the fail-closed ⊤ escalation (C6, command_unbounded_analysis) to
// a criterion-free in-root allow. An unassigned `$D` must therefore keep
// degrading to ⊤ — the flowsh analysis models the command exactly as the
// shell will run it (silent-mode-deny-accuracy-recommendations.md §2C).
func AttachShellAnalysis(ctx context.Context, name string, input json.RawMessage, log *slog.Logger) context.Context {
	if !isShellToolName(name) {
		return ctx
	}
	analysis, err := sdktools.AnalyzeShellCommandForJudge(ctx, name, input)
	if err != nil {
		if log != nil {
			log.Warn("security: shell command analysis failed; shell judge fails closed", "tool", name, "error", err)
		}
	}
	return sdktools.WithShellAnalysis(ctx, analysis, err)
}

// shellAnalysisContext extracts the marshaled digest for the strict judge's
// AnalysisContext field (a single-line JSON document, see
// sdktools.ShellAnalysisDigest): "" for non-shell tools, when no analysis is
// attached, or when the attached one carries an error (the strict judge then
// evaluates without the static-analysis evidence, same as the SDK judges).
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

// shellEffectSignature returns the effect signature of the shell analysis
// attached to ctx (ShellAnalysisDigest.Signature — a pure function of the
// resolved driver binaries, the canonical effects, the fired criteria and
// the workspace-scoping marker; see sp4rk tools/shellanalysis.go): "" for
// non-shell tools, a missing analysis, or a failed one. It keys the silent
// judge's task-scoped verdict memo (silentJudgeDecide) and rides the
// autonomy_decision audit event, so an audit reader can tell which decisions
// adjudicated the same effect.
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

// safetyReasons carries the folded tool-local safety signals through the
// policy branches: at most one hard and one soft reason survive, each with
// its typed classification code (sdktools.ReasonCode*) — hosts key
// deterministic policy off the code (see isCanonicalHardReason), never off
// the prose.
type safetyReasons struct {
	hard     string
	hardCode sdktools.JudgeReasonCode
	soft     string
	softCode sdktools.JudgeReasonCode
}

// splitSafetyReasons folds the collected signals into a safetyReasons pair.
// At most one reason survives per severity: a hard reason (symlink escape,
// command blocklist, SSRF) always wins; only a soft judge escalation (path
// containment) yields a soft reason. Empty strings mean "clean".
//
// One hard-vs-hard refinement: when the judge's hard reason carries a
// NON-canonical code (e.g. the flowsh ⊤ limitation,
// ReasonCodeCommandUnboundedAnalysis, which the strict judge may positively
// clear) while the symlink gate independently produced a CANONICAL hard code
// (a symlink escape out of the session roots), the canonical symlink
// reason+code is kept. Otherwise the non-canonical judge code would mask the
// canonical signal and isCanonicalHardReason — the deterministic backstop
// consulted by smartApproveOrConfirm and verify-on-edit — would not force a
// confirmation for a call that actually traversed a symlink out of the roots.
// Every other combination is unchanged: a canonical judge hard reason still
// wins, and a non-canonical judge hard reason still wins over a non-canonical
// symlink reason.
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

// smartApproveOrConfirm applies the autonomy-mode gate (security.
// autonomy_mode) to a confirmation-escalated call:
//
//   - silent: the unattended terminal resolves the call without a human —
//     the tool_confirm sub-policy selects how (see silentToolTerminal).
//   - standard: the call goes straight to a confirmation card (with the
//     advisory judge disabled for hard reasons) using the supplied reason,
//     or the default per-tool reason when there is none.
//   - assisted (the former Smart Approve): the strict judge evaluates the
//     call. A strict ALLOW executes without UI — except for a canonical hard
//     reason, where the deterministic backstop (isCanonicalHardReason)
//     forces a user confirmation (rule 2: a fired security control must
//     never become judge-waivable). A strict DENY TERMINATES the call: the
//     tool is not executed, ConfirmFunc is never invoked, and the ToolResult
//     carries the judge's justification — the judge positively assessed the
//     call as dangerous, so there is nothing for a human to weigh in on. The
//     decision is reported through the autonomy-decision audit channel
//     (Kind "assisted_deny") so the trajectory stays reconstructable
//     (ASI10). CONFIRM — and a missing, failing, or unparseable judge —
//     stays a user confirmation with the advisory Ask Agent action disabled
//     (DisableJudge=true): the advisory judge must not re-decide what the
//     strict judge already ran on.
//
// The user-facing reasoning always keeps the concrete cause (the supplied
// reason or the per-tool default): when the strict judge is missing, fails, or
// returns a Confirm without a reasoning, only a short note explaining why
// Smart Approve could not decide is prepended, so the confirmation card never
// degrades to a generic "judge unavailable" text that hides WHY this call
// needs confirmation.
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

	// Silent mode (security.autonomy_mode=silent) is the unattended-operation
	// posture: a call that would open a confirmation card must resolve without
	// a human, so it never reaches ConfirmFunc — the tool_confirm sub-policy
	// selects the terminal (see silentToolTerminal). It takes precedence over
	// the assisted path because enabling silent mode is an explicit operator
	// decision to run unattended, and a confirmation card would contradict
	// that.
	if autonomyMode == AutonomyModeSilent {
		return r.silentToolTerminal(ctx, tool, name, source, input, reason, code, severity, silentMode.ToolConfirm, strictJudge, judgeObserver)
	}

	if autonomyMode != AutonomyModeAssisted {
		// A hard security control fired: keep the advisory Ask Agent action
		// disabled (DisableJudge=true) so the advisory judge cannot weaken a
		// fired control. A soft escalation keeps the advisory judge available.
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
		// Shell-exec calls (clean and escalated alike) carry the
		// host-precomputed flowsh digest in ctx (AttachShellAnalysis ran
		// before the tool's own Judge); forward it as the strict judge's
		// static-analysis evidence. Non-shell tools and failed analyses
		// yield "" and the envelope omits the field.
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
	// Deterministic backstop (ASI02, ASI05): a canonical hard reason must
	// never be auto-approved, even when the strict judge returns ALLOW — a
	// fired security control on unmistakably dangerous behavior, or an input
	// whose safety the judge is structurally unable to assess (degraded SSRF
	// protection, an undeterminable URL/path), is not for an advisory judge
	// to waive. Only non-canonical hard reasons (e.g. the flowsh analyzer's
	// ⊤ limitation, command_unbounded_analysis) that the strict judge
	// positively clears may auto-approve. (The silent path drops this
	// backstop by design — see silentJudgeDecide.)
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
		// Assisted-mode auto-deny: the strict judge positively rejected the
		// call, so it terminates without executing and without a card. The
		// decision rides the same auditable autonomy_decision channel with a
		// distinct kind (assisted_deny) — a gate a human would otherwise
		// have answered was decided automatically (ASI10). Policy names the
		// deciding sub-policy ("judge" — the strict judge is the decider in
		// the assisted posture) and Signature carries the effect signature
		// when the call was a shell execution (audit §6.3: every decision
		// names its mechanism and its effect).
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

// Silent-mode sub-policy mode values — the shared vocabulary between
// backend/config's Silent* enum constants and the registry's interpretation of
// them (core cannot import backend, so the strings are re-declared here).
// backend/configadapter_test.go (TestToBuilderConfig_SilentModeEnumPin) pins
// the two dictionaries against each other, so a rename on either side fails
// the backend test run instead of silently desynchronizing the security
// posture (an unrecognized tool_confirm mode falls back to the judge default;
// an unrecognized ask_user mode keeps the tool live in unattended runs).
const (
	// SilentToolConfirmJudge routes a confirmation-gated call through the strict
	// judge. The default mode.
	SilentToolConfirmJudge = "judge"
	// SilentToolConfirmAllow executes a confirmation-gated call with no hard
	// safety reason without consulting the judge.
	SilentToolConfirmAllow = "allow"
	// SilentToolConfirmDeny blocks every confirmation-gated call.
	SilentToolConfirmDeny = "deny"

	// SilentAskUserDisable registers the ask_user tool in a form that reports
	// itself as unavailable, so an unattended run can never block on a question.
	SilentAskUserDisable = "disable"
)

// silentToolTerminal resolves a confirmation-gated tool call without a human
// when silent mode is active (security.autonomy_mode=silent). It NEVER calls
// ConfirmFunc — there is no one to answer — and returns a terminal
// ToolResult instead. The tool_confirm sub-policy selects how:
//
//   - "judge" (default): the strict judge decides, with NO canonical
//     backstop (decision 1c — the operator delegated the decision to the
//     judge, and no human is available to overrule it either way): a strict
//     ALLOW executes, including for a canonical hard reason, and the
//     audited event carries the judge's justification. Every other outcome —
//     a deliberate DENY, CONFIRM, a missing judge, an error or timeout, an
//     unparseable verdict — auto-denies carrying the reasoning, with the
//     Justification distinguishing the judge's DENY from the fail-closed
//     causes (see silentJudgeDecide).
//   - "allow": a call carrying NO hard safety reason executes; a call carrying
//     a HARD safety reason — canonical or not — escalates to the strict
//     judge, which decides (its ALLOW executes, canonical included). A fired
//     control is never auto-executed without the judge's explicit approval,
//     even in the permissive mode.
//   - "deny": every confirmation-gated call is denied without consulting the
//     judge.
//
// An empty or unrecognized mode falls back to the "judge" default (the value
// ApplySilentModeDefaults seeds).
func (r *ToolRegistry) silentToolTerminal(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity, mode string, strictJudge *sdktools.ToolJudge, judgeObserver JudgeObserver) (sdktools.ToolResult, error) {
	if reason == "" {
		reason = defaultConfirmReason(name)
	}
	// The effect signature rides every decision this terminal emits (audit
	// §6.3), whether the sub-policy decides outright (deny/allow) or the
	// strict judge does (silentJudgeDecide re-derives it itself).
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
		// mode: escalate it to the strict judge, which decides. A call with no
		// hard reason runs unattended.
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
		return r.silentJudgeDecide(ctx, tool, name, source, input, reason, code, severity, mode, strictJudge, judgeObserver)
	default: // SilentToolConfirmJudge, the empty mode, and any unrecognized value
		return r.silentJudgeDecide(ctx, tool, name, source, input, reason, code, severity, SilentToolConfirmJudge, strictJudge, judgeObserver)
	}
}

// silentJudgeDecide runs the strict judge for a silent-mode escalation and
// returns the terminal outcome: a strict ALLOW executes, anything else
// auto-denies carrying the reasoning. There is deliberately NO canonical
// backstop here (decision 1c): in silent mode the operator explicitly
// delegated the decision to the judge and no human can override it either
// way, so the judge's ALLOW executes even for a canonical hard reason — the
// executed decision is fully audited (the autonomy_decision event carries the
// judge's justification for allowing a fired control). A deliberate DENY and
// the fail-closed outcomes (CONFIRM, a missing judge, an error/timeout, an
// unparseable verdict) all deny — there is no human to fall back to and no
// confirmation card to open — but their Justification is explicitly
// distinguishable: a judge DENY is tagged "strict judge verdict DENY", the
// fail-closed causes carry their own prefixes, so the audit trail says WHO
// refused the call. mode is the governing tool_confirm sub-policy, recorded on
// the emitted autonomy-decision event (its Policy field, with Mode carrying
// the silent posture) for the audit trail.
//
// Verdicts are memoized by EFFECT signature (ShellAnalysisDigest.Signature)
// for the life of this registry's task, keying on the signature plus the
// escalation severity (see judgeMemoKey): a re-escalation of an
// already-adjudicated effect — the identical command, or a retry re-spelled
// so the deterministic analysis lands on the same drivers, canonical effects
// and fired criteria — replays the FIRST verdict and its reasoning verbatim
// without consulting the judge again (audit Track D: pair 961130 allow vs
// 961162 deny is exactly a judge flip on identical input; retries must not
// re-pay the call either). Only a judge that SPOKE is memoized — a provider
// error/timeout or an unparseable response is infrastructure failure, not a
// verdict about the effect, so the next occurrence retries the judge. A
// missing signature (non-shell tool, failed analysis) never memoizes.
func (r *ToolRegistry) silentJudgeDecide(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity, mode string, strictJudge *sdktools.ToolJudge, judgeObserver JudgeObserver) (sdktools.ToolResult, error) {
	reasoning := "Strict judge is unavailable; " + reason
	verdict := sdktools.VerdictConfirm
	// The effect signature of the attached analysis: the memo key and the
	// audit event's Signature field. "" for non-shell tools and failed
	// analyses — those never memoize.
	signature := shellEffectSignature(ctx, name)
	if strictJudge != nil {
		// Consult the task-scoped memo BEFORE the judge runs. A hit replays
		// the first verdict verbatim — no judge call and no judge-observer
		// phases (no judge is working).
		if entry, ok := r.consultJudgeMemo(signature, severity); ok {
			verdict, reasoning = entry.verdict, entry.reasoning
			r.log().Debug("security: silent mode judge verdict reused (memoized effect signature)",
				"tool", name, "source", source)
		} else {
			var judgeErr error
			if judgeObserver != nil {
				judgeObserver(ctx, JudgePhaseStarted, name)
			}
			// Shell-exec calls carry the host-precomputed flowsh digest in ctx
			// (AttachShellAnalysis ran before the tool's own Judge); forward it as
			// the strict judge's static-analysis evidence, exactly as the
			// non-silent Smart Approve path does.
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
			} else if judgeSpoke(verdict, reasoning) {
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
	// Terminal denial — no human is available. The Justification distinguishes
	// a deliberate judge rejection (DENY: the judge positively assessed the
	// call as dangerous) from the fail-closed outcomes (CONFIRM, a missing
	// judge, an error/timeout, an unparseable verdict), so an operator reading
	// the audit trail can tell WHO refused the call.
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

// Strict-judge fail-safe reason markers. JudgeStrict NEVER returns a non-nil
// error: every infrastructure failure (provider error/timeout, an envelope
// marshal failure) and a twice-unparseable response comes back as a
// fail-safe CONFIRM carrying one of these exact reason strings (sp4rk
// tools/judge.go — unexported there, mirrored here and PINNED behaviorally by
// TestStrictJudgeFailureReasonMarkers_Pin so the mirror cannot drift).
const (
	strictJudgeFailureReason = "Strict judge evaluation failed; requiring manual confirmation for safety"
	judgeUnparsedReason      = "Unable to parse judge response; requiring manual confirmation for safety"
)

// judgeSpoke reports whether a strict-judge result is an actual adjudication
// rather than a fail-safe CONFIRM about the judge infrastructure: a parsed
// ALLOW/DENY always spoke, and a CONFIRM spoke unless its reasoning is one of
// the fail-safe markers. Only spoken verdicts are memoized — an
// infrastructure failure is not a verdict about the effect, so the next
// occurrence of the effect retries the judge instead of inheriting the
// failure for the rest of the task.
func judgeSpoke(verdict sdktools.JudgeVerdict, reasoning string) bool {
	if verdict != sdktools.VerdictConfirm {
		return true
	}
	return reasoning != strictJudgeFailureReason && reasoning != judgeUnparsedReason
}

// judgeMemoEntry is one memoized silent-mode strict-judge verdict: the
// terminal verdict the judge returned plus its reasoning. A memo hit replays
// both verbatim, so the second occurrence of the same effect produces a
// byte-identical justification — the terminal is exactly reproducible.
type judgeMemoEntry struct {
	verdict   sdktools.JudgeVerdict
	reasoning string
}

// judgeMemoKey builds the memo key from the effect signature and the
// escalation severity. The signature carries everything the deterministic
// analysis says about the call (resolved driver binaries, canonical effects,
// fired criteria, the workspace-scoping marker); the severity is the one
// envelope dimension that changes what the judge is ASKED (a fired hard
// control and a soft escalation are different questions), so the two
// together bound what "identical effect" means for reusing a verdict.
func judgeMemoKey(signature string, severity sdktools.JudgeSeverity) string {
	return strconv.Itoa(int(severity)) + "\x00" + signature
}

// consultJudgeMemo returns the memoized verdict for the effect signature, if
// this task's memo holds one. An empty signature (non-shell tool, failed
// analysis) never consults — without an effect identity there is nothing to
// key on, so every such escalation goes to the judge.
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

// recordJudgeMemo fixes the FIRST verdict for the effect signature in this
// task's memo (audit Track D: "первый вердикт в рамках задачи фиксируется").
// A later recording for the same key never overwrites, so a retried effect
// keeps resolving the way it first resolved even if a concurrent
// adjudication of the same effect returned differently. An empty signature
// never records.
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

// ResetJudgeMemo clears the task-scoped silent-mode verdict memo so a task
// launched on this registry clone starts with no verdict replayed from a
// previous task. It is called at the task-launch boundary
// (Manager.refreshAutonomyPosture) because a session's clone is created once
// and reused for every task in that session: the memo is scoped to one task
// by this explicit reset, not by the clone's lifetime (see the judgeMemo
// field doc). Also called by tests to simulate a fresh task launch.
func (r *ToolRegistry) ResetJudgeMemo() {
	r.judgeMemoMu.Lock()
	defer r.judgeMemoMu.Unlock()
	r.judgeMemo = nil
}

// silentDenial builds the auto-denial ToolResult for silent mode. It carries
// the concrete reasoning so the caller — and the task transcript — can see WHY
// the call was blocked without any confirmation card ever being shown.
func silentDenial(reasoning string) sdktools.ToolResult {
	return sdktools.ToolResult{
		Content: "Automatic denial (silent mode): " + reasoning,
		IsError: true,
	}
}

// assistedDenial builds the terminal ToolResult for an assisted-mode
// strict-judge DENY (security.autonomy_mode=assisted). The judge positively
// assessed the call as dangerous, so it terminates without executing and
// without a confirmation card; the ToolResult carries the judge's
// justification so the task transcript shows WHY the call was rejected.
func assistedDenial(justification string) sdktools.ToolResult {
	return sdktools.ToolResult{
		Content: "Denied by strict judge (assisted mode): " + justification,
		IsError: true,
	}
}

// isCanonicalHardReason reports whether a fired hard safety reason must never
// be auto-approved by the strict judge. Canonical codes cover two classes:
// a security control that fired on unmistakably dangerous behavior (a command
// blocklist match — wire code command_blacklist, the historical spelling kept
// deliberately as a stable contract while the config key is `blocklist` — a
// flowsh shell-analysis control — exfiltration flow, privilege escalation, a
// system-path/raw-device write, an irreversible destructive write outside the
// session roots, a download cradle — an SSRF escape target, a symlink escape
// out of the session roots, a write into git internals) AND an input whose
// safety the judge is structurally unable to assess — degraded SSRF
// protection, an undeterminable URL or path, or a deterministic shell
// analysis that could not run at all — because the judge sees only the prose,
// not the DNS resolution or filesystem state the deterministic control
// lacked. The flowsh ⊤ limitation
// (ReasonCodeCommandUnboundedAnalysis, "the analyzer could not bound this
// command") is deliberately NON-canonical: it is an analysis limitation the
// strict judge may positively clear, not a fired control. Codes are the
// typed cross-repo contract from sp4rk (sdktools.JudgeReasonCode): prose
// matching would silently break when sp4rk rewords a reason, an empty/unknown
// code stays non-canonical (the strict judge may positively clear it).
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
		sdktools.ReasonCodeGitInternal:
		return true
	default:
		return false
	}
}

// defaultConfirmReason returns a human-readable explanation of why a tool whose
// effective policy is PolicyUserConfirm requires the user's approval before it
// can run. It is used when there is no richer reason available (e.g. no
// symlink traversal, no judge flag, no auto-approve denial) — previously this
// case surfaced an empty string, leaving the confirmation dialog without any
// explanation of what led to the prompt.
//
// The mapping covers the built-in mutating tools (which default to
// PolicyUserConfirm). Any other tool falls back to a generic statement. The
// strings are UI-facing and kept in English to match the rest of the
// confirmation card ("Tool Confirmation", "Allow Once", ...).
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

	resp, err := confirmFunc(ctx, sdktools.ConfirmationRequest{
		ToolName:       name,
		Input:          input,
		JudgeReasoning: reasoning,
		DisableJudge:   disableJudge,
	})
	if err != nil {
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
