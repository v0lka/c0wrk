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

// confirmCeilingGuard: the confirmation deadline derives from the tool-CALL
// start (shared with the executor's watchdog), not from the confirmation start.
const confirmCeilingGuard = time.Second

// callStartCtxKey keys the tool-call start instant Execute publishes, so the
// confirmation wait budgets against the watchdog's deadline, not a fresh span.
type callStartCtxKey struct{}

// callStartFrom returns the tool-call start published by Execute, or the zero
// time when the context did not come from Execute (direct test callers).
func callStartFrom(ctx context.Context) time.Time {
	if t, ok := ctx.Value(callStartCtxKey{}).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// Goal-mode-only system tools; StripGoalModeTools strips them from non-goal
// runs. See specs/domains/tool-system/builtins.md (Goal-Mode Tools).
var goalModeTools = map[string]struct{}{
	"propose_goal":         {},
	"declare_goal_status":  {},
	"declare_verification": {},
}

// IsGoalModeTool reports whether the tool is a goal-mode-only system tool.
func IsGoalModeTool(name string) bool {
	_, ok := goalModeTools[name]
	return ok
}

// StripGoalModeTools removes goal-mode-only tools from a descriptor list.
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

// Autonomy-mode values — re-declared because core/tools cannot import core
// (pinned by core/silent_mode_test.go); empty/unrecognized behaves as standard.
// See specs/architecture/security-model.md (the autonomy axis).
const (
	AutonomyModeStandard = "standard" // interactive: every gated call opens a user confirmation card
	AutonomyModeAssisted = "assisted" // the strict judge gates calls; non-terminal outcomes open a card
	AutonomyModeSilent   = "silent"   // unattended: SilentModeState sub-policies resolve every gated call
)

// SilentModeState snapshots the security.silent_mode sub-policies (ADR-053,
// ADR-072); inert unless silent; a plain value Clone copies verbatim.
type SilentModeState struct {
	ToolConfirm string
	// UserConfirm refines the fail-closed judge outcome; it never governs a
	// deliberate judge DENY (specs/decisions/072-silent-mode-user-confirm-policy.md).
	UserConfirm string
	StepLimit   string
	AskUser     string
}

// ToolRegistry layers group policy enforcement on the embedded sp4rk registry.
// TODO(S-14): compose instead of embedding; delegate only core's methods.
type ToolRegistry struct {
	// Deprecated: embedded for backward compatibility (TODO(S-14), see the type doc).
	*sdktools.ToolRegistry
	mu          sync.RWMutex
	confirmFunc sdktools.ConfirmFunc
	// toolCallTimeout mirrors the executor's per-tool-call ceiling for the
	// confirmation wait; see SetToolCallTimeout.
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
	// judgeMemo memoizes silent-mode strict-judge fail-closed verdicts by shell
	// effect signature: the first verdict for an effect replays verbatim. Only
	// fail-closed verdicts are stored — an ALLOW would let a materially different
	// command inherit a verdict the judge never gave (the signature is not
	// injective). Not cloned: task-scoped via ResetJudgeMemo, not clone lifetime.
	judgeMemo map[string]judgeMemoEntry
	// parent is the shared builder registry this clone was cut from (nil on the
	// shared registry itself): the posture source RefreshAutonomyPosture re-syncs.
	parent *ToolRegistry
}

// PreExecuteHook is called before tool execution; it may block on preconditions
// (e.g. indexing). A returned error aborts execution as a tool error result.
type PreExecuteHook func(ctx context.Context, toolName string, source string) error

// PostExecuteHook runs after a non-system tool path completes, success or
// error alike, so the host can react to file mutations; must not block (a
// panic propagates to the caller).
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

// JudgeObserver is invoked around the strict judge's evaluation so the host
// can surface an honest "judge is working" status; must not block.
type JudgeObserver func(ctx context.Context, phase JudgePhase, toolName string)

// AutonomyDecision is one autonomous (no-human) security decision — the
// auditable record (ASI10) serialized into the `autonomy_decision` event;
// field contract: specs/contracts/event-catalog.md.
type AutonomyDecision struct {
	Kind          string           `json:"kind"`
	Mode          string           `json:"mode,omitempty"`
	Policy        string           `json:"policy,omitempty"`
	Verdict       string           `json:"verdict"`
	Tool          string           `json:"tool,omitempty"`
	Source        string           `json:"source,omitempty"`
	Signature     string           `json:"signature,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Justification string           `json:"justification,omitempty"`
	Category      string           `json:"category,omitempty"`
	CurrentStep   int              `json:"current_step,omitempty"`
	MaxSteps      int              `json:"max_steps,omitempty"`
	PlanStepID    string           `json:"plan_step_id,omitempty"`
	Network       *NetworkDecision `json:"network,omitempty"`
}

// AutonomyDecisionObserver is invoked once per automatic (no-human) decision,
// in either posture, so the host can emit the auditable event. Must not block.
type AutonomyDecisionObserver func(ctx context.Context, decision AutonomyDecision)

// Autonomy-decision vocabulary the registry fills in (serialized verbatim into
// the autonomy_decision event); step_limit kind/verdicts come from the host.
const (
	autonomyDecisionKindToolConfirm   = "tool_confirm"
	autonomyDecisionKindAssistedDeny  = "assisted_deny"
	autonomyDecisionVerdictAllow      = "allow"
	autonomyDecisionVerdictDeny       = "deny"
	autonomyDecisionPolicyUserConfirm = "user_confirm"
)

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		ToolRegistry: sdktools.NewToolRegistry(),
	}
}

// Clone returns a copy sharing the sp4rk registry (tools are stateless and
// shared) with independent policy state, so mutations never leak across
// sessions; parent records the posture source for RefreshAutonomyPosture.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := &ToolRegistry{
		ToolRegistry:               r.ToolRegistry,
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

// SetToolCallTimeout mirrors the executor's per-tool-call ceiling: an
// outlasting confirmation wait yields a clean denial, not the watchdog's
// run-aborting ErrToolTimeout (specs/domains/tool-system/builtins.md).
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
func (r *ToolRegistry) SetJudgeObserver(fn JudgeObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judgeObserver = fn
}

// SetAutonomyDecisionObserver registers the automatic-decision observer; nil disables.
func (r *ToolRegistry) SetAutonomyDecisionObserver(fn AutonomyDecisionObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autonomyDecisionObserver = fn
}

// observeAutonomyDecision notifies the observer, deriving the Network summary
// from ctx (AttachShellAnalysis) when the caller left it unset.
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
// non-system tool (security.groups).
func (r *ToolRegistry) SetGroupPolicies(policies map[sdktools.ToolGroup]sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groupPolicies = policies
}

// ApplySecurityState atomically replaces the whole global security state — the
// runtime-settings push for the shared registry; clones get group policies
// only, posture re-syncs at task launch (specs/architecture/security-model.md,
// Silent Mode). Deep-copies the map.
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

// ApplyGroupPolicies is ApplySecurityState's live-clone counterpart: replaces
// only the group→policy map and write auto-approval, preserving the pinned
// posture (re-synced at task launch, or mid-run only when tightening).
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
// sub-policies from the parent at task launch — posture pinning's task-launch
// half (tightening: ApplyAutonomyPostureIfTightening). No-op without a parent.
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

// ApplyAutonomyPostureIfTightening applies the posture only when it tightens —
// the mid-run exception to posture pinning; a same-mode save must tighten every
// silent sub-policy (silentModeAtLeastAsStrict). One lock, no torn pair.
func (r *ToolRegistry) ApplyAutonomyPostureIfTightening(autonomyMode string, silentMode SilentModeState) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	incomingRank := autonomyModeRank(autonomyMode)
	currentRank := autonomyModeRank(r.autonomyMode)
	switch {
	case incomingRank < currentRank:
		r.autonomyMode = autonomyMode
		r.silentMode = silentMode
		return true
	case incomingRank > currentRank:
		return false
	}
	// Same rank: sub-policies carry policy only in the silent posture, so only
	// a silent→silent save is a candidate — deliver it on per-field tightening.
	if currentRank != autonomyModeRank(AutonomyModeSilent) {
		return false
	}
	if !silentModeAtLeastAsStrict(silentMode, r.silentMode) {
		return false
	}
	if silentMode == r.silentMode {
		return false
	}
	r.silentMode = silentMode
	return true
}

// silentToolConfirmPermissiveness ranks tool_confirm.mode by unattendedness
// (HIGHER is more permissive; unknown ranks as the documented default, judge).
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

// silentUserConfirmPermissiveness ranks user_confirm by unattendedness:
// confirm > deny > escalate; unknown ranks as the documented default (deny).
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

// silentStepLimitPermissiveness ranks step_limit.mode by unattendedness;
// literals mirror backend/config (core cannot import backend).
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
// least as strict — the condition for delivering a same-mode change.
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

// SilentMode returns a copy of the current silent-mode posture (security.silent_mode).
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
// local_write tools (mechanics in Execute; hard reasons preempt).
func (r *ToolRegistry) SetAutoApproveWorkspaceWrites(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoApproveWorkspaceWrites = enabled
}

// SetAutonomyMode sets the autonomy posture (security.autonomy_mode);
// empty/unrecognized behaves as standard.
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

// SetPostExecuteHook sets the post-execution hook; it fires only for calls
// that reach policy resolution (registered via defer after the early returns).
func (r *ToolRegistry) SetPostExecuteHook(hook PostExecuteHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.postExecuteHook = hook
}

// SetToolFilter sets the registration filter (see ToolFilter).
func (r *ToolRegistry) SetToolFilter(f ToolFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolFilter = f
}

// RegisterWithSource registers a tool with the given source, unless the filter
// rejects it (logged at Debug and skipped).
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

// Execute looks up a tool by name and executes it with the given input; a
// missing tool is an error. Security resolves by capability GROUP through the
// ordered gate pipeline of specs/domains/tool-system/README.md.
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

	// Gate 1: structural input validation (ASI02-R2); fail-open only on
	// unmodeled constructs (specs/domains/tool-system/README.md).
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

	// Gate 2b: per-server MCP mode gating (defense-in-depth) — descriptor filters
	// already hide gated-off servers, but a hallucinated or stale call must fail
	// closed at dispatch; precedes the system-group bypass like Gate 2.
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

	// Gate 3: system group bypasses the remaining policy and judge checks.
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

	// Run the deterministic flowsh analysis once per call; every judge
	// downstream consumes the attached digest (AttachShellAnalysisForTool).
	ctx = AttachShellAnalysisForTool(ctx, tool, name, input, r.log())

	// Tool-local safety signals, gathered once for every policy branch below
	// (severity semantics: specs/architecture/security-model.md).
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
		// Hard reasons escalate like any user_confirm call (smartApproveOrConfirm).
		if reasons.hard != "" {
			r.log().Warn("security: allow-policy tool escalated by hard safety reason",
				"tool", name, "group", string(group), "reason", reasons.hard)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.hard, reasons.hardCode, sdktools.JudgeSeverityHard)
		}
		// Soft reasons are advisory scope questions.
		if reasons.soft != "" {
			r.log().Info("security: allow-policy tool escalated by soft safety reason",
				"tool", name, "group", string(group), "reason", reasons.soft)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, reasons.soft, reasons.softCode, sdktools.JudgeSeveritySoft)
		}
		return tool.Execute(ctx, input)
	}

	// Effective policy: PolicyUserConfirm (the fail-safe default for any group
	// without a configured entry).

	// Session-root auto-approval under auto_approve_workspace_writes; hard
	// reasons preempt (specs/architecture/security-model.md).
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

	// Smart Approve is deliberately last among automatic gates: auto-approval
	// kept priority, and the deny/hard-reason paths already returned.
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
// unexported, mirrored via the exported name constants).
func isShellToolName(name string) bool {
	return name == sdktools.ToolBashExec || name == sdktools.ToolPoshExec
}

// AttachShellAnalysis runs the deterministic flowsh analysis for a shell-exec
// call once and attaches the digest to ctx for every judge downstream. A failed
// analysis attaches as (nil, err) and fails the shell Judge closed; no
// host-known variable binding is seeded (see specs/architecture/security-model.md).
func AttachShellAnalysis(ctx context.Context, name string, input json.RawMessage, log *slog.Logger) context.Context {
	return AttachShellAnalysisForTool(ctx, nil, name, input, log)
}

// AttachShellAnalysisForTool is AttachShellAnalysis with the registered tool
// instance: the declared shell kind (ADR-058) picks the analysis dialect.
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
// AnalysisContext field: "" when there is no analysis evidence to show.
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
// judge-memo key and autonomy_decision audit field. "" when not analyzable.
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
// most one hard and one soft reason, each with its typed code.
type safetyReasons struct {
	hard     string
	hardCode sdktools.JudgeReasonCode
	soft     string
	softCode sdktools.JudgeReasonCode
}

// splitSafetyReasons folds the collected signals: at most one reason per
// severity, a hard reason wins, empty strings mean clean — except a
// NON-canonical judge hard code (judge-clearable, e.g. flowsh ⊤) must not mask
// a CANONICAL symlink escape, or the backstop would miss a real traversal.
// See specs/architecture/security-model.md (Safety-Signal Folding).
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

// smartApproveOrConfirm is the unified autonomy funnel (ADR-026) for a
// confirmation-escalated call: silent → silentToolTerminal, standard → card,
// assisted → strict judge with the deterministic backstop (full semantics:
// specs/architecture/security-model.md). The user-facing reasoning keeps the
// concrete cause, never a generic "judge unavailable" text.
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

	// Silent mode never reaches ConfirmFunc (see silentToolTerminal).
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
		// Surface the judge run before any card exists (see JudgeObserver).
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
		}
	}
	// Deterministic backstop: a canonical hard reason is never judge-waivable
	// here; silent drops it by design (specs/architecture/security-model.md).
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
		// Assisted-mode auto-deny, audited as assisted_deny.
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
// backend (pinned by TestToBuilderConfig_SilentModeEnumPin). Per-value semantics:
// specs/decisions/053-silent-mode.md, specs/decisions/072-silent-mode-user-confirm-policy.md.
const (
	SilentToolConfirmJudge = "judge" // strict judge decides gated calls; the default mode
	SilentToolConfirmAllow = "allow" // executes gated calls lacking a hard safety reason
	SilentToolConfirmDeny  = "deny"  // blocks every gated call

	SilentUserConfirmConfirm  = "confirm"  // executes a fail-closed judge outcome unattended (audited as an allow)
	SilentUserConfirmDeny     = "deny"     // the fail-closed default
	SilentUserConfirmEscalate = "escalate" // falls back to the blocking card

	SilentAskUserDisable = "disable" // ask_user registers as unavailable for unattended runs
)

// silentToolTerminal resolves a confirmation-gated call without a human — it
// never calls ConfirmFunc. The tool_confirm sub-policy selects the terminal;
// userConfirm refines the judge terminal's fail-closed CONFIRM outcome
// (specs/decisions/053-silent-mode.md, specs/decisions/072-silent-mode-user-confirm-policy.md).
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

// silentJudgeDecide runs the strict judge for a silent-mode escalation with no
// canonical backstop (ADR-053 D4); user_confirm resolves the fail-closed
// outcomes (ADR-072). Memoization is DENY-only — protocol on the judgeMemo field.
func (r *ToolRegistry) silentJudgeDecide(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string, code sdktools.JudgeReasonCode, severity sdktools.JudgeSeverity, mode, userConfirm string, strictJudge *sdktools.ToolJudge, judgeObserver JudgeObserver) (sdktools.ToolResult, error) {
	reasoning := "Strict judge is unavailable; " + reason
	verdict := sdktools.VerdictConfirm
	// The effect signature of the attached analysis: the memo key and the
	// audit event's Signature field. "" never memoizes.
	signature := shellEffectSignature(ctx, name)
	if strictJudge != nil {
		// A memo hit replays the first verdict verbatim, with no judge phases.
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

	// judgeRan: the escalated card disables the advisory judge whenever the
	// strict judge spoke (matching smartApproveOrConfirm).
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
	// A deliberate judge DENY never routes here — it takes the fail-closed
	// terminal below.
	if verdict == sdktools.VerdictConfirm && (userConfirm == SilentUserConfirmConfirm || userConfirm == SilentUserConfirmEscalate) {
		return r.silentConfirmTerminal(ctx, tool, name, source, input, reason, reasoning, verdictText, signature, judgeRan, userConfirm)
	}
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

// silentConfirmTerminal resolves a CONFIRM verdict per user_confirm
// (specs/decisions/072-silent-mode-user-confirm-policy.md): "confirm" executes
// unattended (audited as an allow), "escalate" restores the blocking card.
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

// Strict-judge fail-safe reason markers — the exact fail-safe CONFIRM strings
// (mirrored from unexported sp4rk constants; pinned by TestStrictJudgeFailureReasonMarkers_Pin).
const (
	strictJudgeFailureReason = "Strict judge evaluation failed; requiring manual confirmation for safety"
	judgeUnparsedReason      = "Unable to parse judge response; requiring manual confirmation for safety"
)

// judgeSpoke reports an actual adjudication vs a fail-safe infrastructure
// CONFIRM; only spoken verdicts are memoized.
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
// one envelope dimension that changes what the judge is asked.
func judgeMemoKey(signature string, severity sdktools.JudgeSeverity) string {
	return strconv.Itoa(int(severity)) + "\x00" + signature
}

// consultJudgeMemo returns the memoized verdict; only a DENY is ever recorded,
// so a hit is always fail-closed. An empty signature never consults.
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

// recordJudgeMemo fixes the FIRST verdict for an effect; only a DENY may be
// passed, and a later recording never overwrites. Empty signature: no-op.
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
// (Manager.refreshAutonomyPosture) — not clone lifetime. Tests: fresh launch.
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

// isCanonicalHardReason reports whether a fired hard reason is never
// judge-waivable on the interactive paths; the typed-code list and its guard
// test live in specs/architecture/security-model.md (Deterministic backstop).
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
// richer reason is available (UI-facing; English, matching the card).
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

// confirmAndExecuteWithOptions requests user confirmation, optionally
// disabling the advisory Ask Agent action; missing infra denies (fail-closed).
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

	// Bound the human wait to yield its clean denial before the executor's
	// ceiling fires (see confirmCeilingGuard, SetToolCallTimeout).
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
		// An outlasted ceiling is a clean denial, not a run failure; every other
		// error, including genuine cancellation, propagates unchanged.
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
