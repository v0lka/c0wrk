package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
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
	extraShellBlacklist        []*regexp.Regexp
	logger                     *slog.Logger
	autoApproveWorkspaceWrites bool
	smartApprove               bool
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
// not leak across concurrent sessions.
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
		extraShellBlacklist:        r.extraShellBlacklist, // shared (compiled regexps are read-only)
		logger:                     r.logger,
		autoApproveWorkspaceWrites: r.autoApproveWorkspaceWrites,
		smartApprove:               r.smartApprove,
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
// Used by No Project mode to disable code-oriented tools.
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

// SetExtraShellBlacklist compiles and stores additional shell command blacklist
// patterns checked at execution time. This allows per-session blacklist
// augmentation (e.g., No Project mode blocks development tools) without
// re-registering the shared shell-exec tool instance (bash_exec/posh_exec).
// An empty or nil slice clears the extra blacklist.
func (r *ToolRegistry) SetExtraShellBlacklist(patterns []string) error {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid extra shell blacklist pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extraShellBlacklist = compiled
	return nil
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

// SetSmartApprove enables or disables strict automatic evaluation of calls
// whose effective policy is PolicyUserConfirm, and of soft tool-judge
// escalations under PolicyAlwaysAllow. Only a strict ALLOW executes without
// UI; every other outcome remains a user confirmation. Hard safety reasons
// never reach Smart Approve — they confirm directly.
func (r *ToolRegistry) SetSmartApprove(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.smartApprove = enabled
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
// pre-execute-hook errors, blacklist blocks, and user-confirmation outcomes
// (allow/deny). It does NOT fire when the tool is not found, disabled, or a
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
// Security is resolved by the tool's capability GROUP (Tool.Group()), never by
// tool name. Gate order:
//
//  1. required-field validation (schema "required" keys),
//  2. disabled tools (No Project mode) — applies to every tool, system included,
//  3. system group → execute directly (internal orchestration tools),
//  4. extra shell blacklist (No Project) — hard block; the reason names the
//     matched pattern,
//  5. pre-execute hook,
//  6. group policy deny → block,
//  7. group policy allow: tool Judge + symlink signals — a HARD reason
//     (command blacklist, SSRF, symlink escape) forces a confirmation the
//     advisory judge cannot weaken; a SOFT reason (path containment) goes to
//     Smart Approve and confirms unless the strict judge allows; a clean call
//     executes,
//  8. group policy user_confirm: local_write tools with
//     auto_approve_workspace_writes whose paths resolve inside the session
//     roots execute; everything else goes through Smart Approve (never around
//     a hard reason) and otherwise confirms.
//
// Hard reasons never pass Smart Approve: they confirm directly with the
// advisory Ask Agent action disabled.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (result sdktools.ToolResult, err error) {
	tool, ok := r.Get(name)
	if !ok {
		return sdktools.ToolResult{Content: "tool not found: " + name, IsError: true}, nil
	}

	// Gate 1: centralized required-field validation (ASI02-R2, defense-in-depth).
	// Ensures every tool — including new ones whose author forgot per-tool
	// validation — rejects inputs missing a JSON Schema "required" top-level
	// key. Fail-safe: schema parse errors or missing "required" are skipped,
	// so this never blocks a call that existing per-tool validation accepts.
	if missing := validateRequiredFields(tool.InputSchema(), input); len(missing) > 0 {
		return sdktools.ToolResult{
			Content: "validation error: missing required parameter(s): " + strings.Join(missing, ", "),
			IsError: true,
		}, nil
	}

	// Gate 2: disabled tools (No Project mode) — MUST precede the system-group
	// bypass so that tools like semantic_search are blocked at execution time too.
	r.mu.RLock()
	disabled := r.disabledTools
	extraShellBL := r.extraShellBlacklist
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
	if tool.Group() == sdktools.GroupSystem {
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

	// Gate 4: extra shell blacklist (per-session, e.g. No Project mode) — a
	// hard block that no policy can weaken. Applies to all shell-exec tools
	// (bash_exec, posh_exec); the reason names the matched pattern so the user
	// can see which rule fired.
	if pattern, command := matchExtraShellBlacklist(name, input, extraShellBL); pattern != "" {
		r.log().Warn("security: shell command blocked by No Project extra blacklist",
			"tool", name, "command", command, "pattern", pattern)
		return sdktools.ToolResult{
			Content: fmt.Sprintf("command %q is not available in No Project mode (matched blacklist pattern %q)", command, pattern),
			IsError: true,
		}, nil
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

	group := tool.Group()

	// Gate 6: group policy deny — a hard block.
	policy := r.groupPolicy(group)
	if policy == sdktools.PolicyAlwaysDeny {
		r.log().Warn("security: tool blocked by group policy (deny)", "tool", name, "group", string(group))
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy (group %q is set to deny)", name, group),
			IsError: true,
		}, nil
	}

	// Tool-local safety signals, gathered once and shared by every policy
	// branch below:
	//   - the tool's own Judge (command blacklist / SSRF = hard; path
	//     containment = soft),
	//   - symlink traversal detection (an escape out of the session roots or
	//     unresolvable input = hard; a symlink whose resolution stays inside
	//     the roots is NOT a concern — containment reasons about resolved
	//     paths).
	judgeOutcome := judgeToolCall(ctx, tool, input)
	symlinkReason := r.symlinkHardReason(ctx, name, tool, input)
	hardReason, softReason := splitSafetyReasons(judgeOutcome, symlinkReason)

	if policy == sdktools.PolicyAlwaysAllow {
		// Hard reasons are security-control triggers (command blacklist, SSRF,
		// symlink escapes). They force a confirmation the advisory Ask Agent
		// action cannot weaken (DisableJudge=true) and never pass Smart
		// Approve — path-locality or a lenient judge must not bypass a fired
		// security control.
		if hardReason != "" {
			r.log().Warn("security: allow-policy tool escalated by hard safety reason",
				"tool", name, "group", string(group), "reason", hardReason)
			return r.confirmAndExecuteWithOptions(ctx, tool, name, input, hardReason, true)
		}
		// Soft reasons are advisory scope questions (e.g. a read outside the
		// session roots): Smart Approve weighs them and confirms unless the
		// strict judge allows. Without Smart Approve they fall back to a plain
		// confirmation.
		if softReason != "" {
			r.log().Info("security: allow-policy tool escalated by soft safety reason",
				"tool", name, "group", string(group), "reason", softReason)
			return r.smartApproveOrConfirm(ctx, tool, name, source, input, softReason)
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
	if group == sdktools.GroupLocalWrite && autoApprove && hardReason == "" && judgeOutcome.Allow {
		r.log().Debug("workspace auto-approve: local_write target within session roots",
			"tool", name, "reason", judgeOutcome.Reason)
		return tool.Execute(ctx, input)
	}

	if hardReason != "" {
		r.log().Warn("security: user_confirm tool escalated by hard safety reason",
			"tool", name, "group", string(group), "reason", hardReason)
		return r.confirmAndExecuteWithOptions(ctx, tool, name, input, hardReason, true)
	}

	// Smart Approve is deliberately last among automatic gates and applies
	// only to the effective user_confirm policy. Workspace auto-approval above
	// therefore retains priority, while deny and hard-reason paths have
	// already returned before reaching this point.
	return r.smartApproveOrConfirm(ctx, tool, name, source, input, softReason)
}

// judgeToolCall runs the tool's optional local safety judge. Tools without a
// judge report no concern (zero outcome: no reason, not hard).
func judgeToolCall(ctx context.Context, tool sdktools.Tool, input json.RawMessage) sdktools.JudgeOutcome {
	if judger, ok := tool.(sdktools.ToolJudger); ok {
		return judger.Judge(ctx, input)
	}
	return sdktools.JudgeOutcome{}
}

// splitSafetyReasons folds the collected signals into (hardReason, softReason).
// At most one reason survives: a hard reason (symlink escape, command
// blacklist, SSRF) always wins; only a soft judge escalation (path
// containment) yields a soft reason. Empty strings mean "clean".
func splitSafetyReasons(judge sdktools.JudgeOutcome, symlinkReason string) (hard, soft string) {
	if !judge.Allow && judge.Reason != "" && judge.Severity == sdktools.JudgeSeverityHard {
		return judge.Reason, ""
	}
	if symlinkReason != "" {
		return symlinkReason, ""
	}
	if !judge.Allow && judge.Reason != "" {
		return "", judge.Reason
	}
	return "", ""
}

// smartApproveOrConfirm applies the Smart Approve gate. When enabled, the
// strict judge evaluates the call and only a strict ALLOW executes without UI;
// every other verdict (and a missing or failing judge) stays a user
// confirmation with the advisory Ask Agent action disabled (DisableJudge=true)
// — the advisory judge must not re-decide what the strict judge already ran
// on. When Smart Approve is off, the call goes straight to confirmation with
// the supplied reason (or the default per-tool reason when there is none).
func (r *ToolRegistry) smartApproveOrConfirm(ctx context.Context, tool sdktools.Tool, name, source string, input json.RawMessage, reason string) (sdktools.ToolResult, error) {
	r.mu.RLock()
	smartApprove := r.smartApprove
	strictJudge := r.judge
	r.mu.RUnlock()

	if !smartApprove {
		if reason == "" {
			reason = defaultConfirmReason(name)
		}
		return r.confirmAndExecute(ctx, tool, name, input, reason)
	}

	reasoning := "Strict judge is unavailable; requiring manual confirmation for safety"
	verdict := sdktools.VerdictConfirm
	if strictJudge != nil {
		var judgeErr error
		verdict, reasoning, judgeErr = strictJudge.JudgeStrict(ctx, sdktools.StrictJudgeRequest{
			ToolName:    name,
			Input:       input,
			TaskContext: sdktools.TaskContextFrom(ctx),
			ToolSource:  source,
		})
		if judgeErr != nil {
			verdict = sdktools.VerdictConfirm
			reasoning = "Strict judge evaluation failed; requiring manual confirmation for safety"
		}
	}

	verdictText := "CONFIRM"
	if verdict == sdktools.VerdictAllow {
		verdictText = "ALLOW"
	}
	r.log().Info("security: smart approve verdict",
		"tool", name,
		"source", source,
		"verdict", verdictText,
		"asi_scope", "ASI01,ASI02,ASI03,ASI05,ASI09")

	if verdict == sdktools.VerdictAllow {
		return tool.Execute(ctx, input)
	}
	return r.confirmAndExecuteWithOptions(ctx, tool, name, input, reasoning, true)
}

// matchExtraShellBlacklist checks a shell-exec tool's command against extra
// per-session blacklist patterns. It returns the matched pattern and the
// command, or empty strings when nothing matched (a command-less or
// unparseable input never matches).
func matchExtraShellBlacklist(name string, input json.RawMessage, patterns []*regexp.Regexp) (pattern, command string) {
	if !sdktools.IsShellExecTool(name) || len(patterns) == 0 {
		return "", ""
	}
	var params struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Command == "" {
		return "", ""
	}
	for _, re := range patterns {
		if re.MatchString(params.Command) {
			return re.String(), params.Command
		}
	}
	return "", ""
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

// validateRequiredFields extracts the top-level "required" property names from
// a tool's JSON Schema and reports which are absent from the input object.
// It is fail-safe: any schema/input parse error, a non-object input, or a
// schema without a "required" array yields an empty result (no missing fields),
// so it never blocks a call that existing per-tool validation accepts. This is
// defense-in-depth (ASI02-R2) so a newly added tool that forgets per-tool
// validation still rejects inputs missing a declared required parameter.
func validateRequiredFields(schema, input json.RawMessage) []string {
	if len(schema) == 0 || len(input) == 0 {
		return nil
	}
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Required) == 0 {
		return nil // no schema, unparseable, or nothing required → skip
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return nil // input is not a JSON object (e.g. a raw value) → skip
	}
	var missing []string
	for _, field := range s.Required {
		if _, present := obj[field]; !present {
			missing = append(missing, field)
		}
	}
	return missing
}
