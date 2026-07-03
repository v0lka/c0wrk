package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sync"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// internalTools is the set of tool names that are always allowed,
// excluded from policy configuration, and bypass the tool judge entirely.
var internalTools = map[string]struct{}{
	"ask_user":            {},
	"finish":              {},
	"list_step_outputs":   {},
	"read_skill_resource": {},
	"read_step_output":    {},
	"search_facts":        {},
	"tool_result_read":    {},
	"semantic_search":     {},
	"set_step_status":     {},
	"store_fact":          {},
	sdktools.ToolBatch:    {},
}

// IsInternalTool returns true if the given tool name is an internal tool
// that is always allowed and bypasses policy/judge checks.
func IsInternalTool(name string) bool {
	_, ok := internalTools[name]
	return ok
}

// ToolFilter decides whether a tool should be registered. Return false to reject.
type ToolFilter func(toolName, source string) bool

// ToolRegistry stores all available tools and provides them to Executor.
// It embeds the SDK ToolRegistry for basic operations and adds policy enforcement on top.
// Thread-safe via sync.RWMutex.
//
// TODO(S-14): Replace embedding with composition — store *sdktools.ToolRegistry as a
// private field and explicitly delegate only the methods the core layer intends to
// expose. This gives full control over the public surface area and prevents
// accidental exposure of SDK-internal methods to callers. The refactor requires
// auditing all callers that access SDK methods through the embedded type.
type ToolRegistry struct {
	*sdktools.ToolRegistry
	mu                         sync.RWMutex
	confirmFunc                sdktools.ConfirmFunc
	judge                      *sdktools.ToolJudge
	policyOverrides            map[string]sdktools.ToolPolicy
	skillPolicyOverrides       map[string]sdktools.ToolPolicy
	defaultPolicy              sdktools.ToolPolicy
	hasDefaultPolicy           bool
	preExecuteHook             PreExecuteHook
	toolFilter                 ToolFilter
	paramManager               sdktools.ParamManager
	disabledTools              map[string]bool
	extraBashBlacklist         []*regexp.Regexp
	logger                     *slog.Logger
	autoApproveWorkspaceWrites bool
}

// PreExecuteHook is called before tool execution. It may block to wait for
// preconditions (e.g., indexing completion). If it returns an error, execution
// is aborted and the error is returned as a tool error result.
type PreExecuteHook func(ctx context.Context, toolName string, source string) error

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		ToolRegistry: sdktools.NewToolRegistry(),
	}
}

// Clone returns a copy of this ToolRegistry that shares the underlying SDK
// ToolRegistry (tools themselves are stateless and shared) but has independent
// policy state (policyOverrides, skillPolicyOverrides, defaultPolicy, judge,
// confirmFunc, hooks). This is used to give each session/orchestrator its own
// policy view so that runtime mutations — in particular skill-derived
// SetSkillPolicyOverrides — do not leak across concurrent sessions.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cloned := &ToolRegistry{
		ToolRegistry:               r.ToolRegistry, // shared SDK registry (tool definitions)
		confirmFunc:                r.confirmFunc,
		judge:                      r.judge,
		defaultPolicy:              r.defaultPolicy,
		hasDefaultPolicy:           r.hasDefaultPolicy,
		preExecuteHook:             r.preExecuteHook,
		toolFilter:                 r.toolFilter,
		paramManager:               r.paramManager,
		extraBashBlacklist:         r.extraBashBlacklist, // shared (compiled regexps are read-only)
		logger:                     r.logger,
		autoApproveWorkspaceWrites: r.autoApproveWorkspaceWrites,
	}
	if r.disabledTools != nil {
		cloned.disabledTools = make(map[string]bool, len(r.disabledTools))
		for k, v := range r.disabledTools {
			cloned.disabledTools[k] = v
		}
	}
	if r.policyOverrides != nil {
		cloned.policyOverrides = make(map[string]sdktools.ToolPolicy, len(r.policyOverrides))
		for k, v := range r.policyOverrides {
			cloned.policyOverrides[k] = v
		}
	}
	if r.skillPolicyOverrides != nil {
		cloned.skillPolicyOverrides = make(map[string]sdktools.ToolPolicy, len(r.skillPolicyOverrides))
		for k, v := range r.skillPolicyOverrides {
			cloned.skillPolicyOverrides[k] = v
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

// SetExtraBashBlacklist compiles and stores additional bash command blacklist
// patterns checked at execution time. This allows per-session blacklist
// augmentation (e.g., No Project mode blocks development tools) without
// re-registering the shared bash_exec tool instance.
// An empty or nil slice clears the extra blacklist.
func (r *ToolRegistry) SetExtraBashBlacklist(patterns []string) error {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("invalid extra bash blacklist pattern %q: %w", p, err)
		}
		compiled = append(compiled, re)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extraBashBlacklist = compiled
	return nil
}

// SetLogger sets the logger for the tool registry. If nil, slog.Default() is used.
func (r *ToolRegistry) SetLogger(l *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = l
}

func (r *ToolRegistry) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// SetConfirmFunc sets the confirmation callback for mutating tools.
// If nil, all tools execute without confirmation (CLI mode).
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

// SetPolicyOverrides sets per-tool policy overrides from configuration.
func (r *ToolRegistry) SetPolicyOverrides(overrides map[string]sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyOverrides = overrides
}

// SetDefaultPolicy sets the default policy for tools without explicit overrides.
func (r *ToolRegistry) SetDefaultPolicy(p sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultPolicy = p
	r.hasDefaultPolicy = true
}

// SetAutoApproveWorkspaceWrites enables or disables workspace-based auto-approval
// for file write tools with PolicyUserConfirm. When enabled, write_file, edit_file,
// delete_file, delete_directory, and create_directory auto-execute without confirmation
// when all paths in their input are within the session workspace.
func (r *ToolRegistry) SetAutoApproveWorkspaceWrites(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoApproveWorkspaceWrites = enabled
}

// SetPreExecuteHook sets a hook that is called before every non-internal tool execution.
func (r *ToolRegistry) SetPreExecuteHook(hook PreExecuteHook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preExecuteHook = hook
}

// SetToolFilter sets a filter that decides whether a tool should be registered.
// If the filter returns false, the tool is rejected during RegisterWithSource.
func (r *ToolRegistry) SetToolFilter(f ToolFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolFilter = f
}

// SetParamManager sets a ParamManager that handles both schema sanitization
// (for MCP gateway) and param injection (for tool execution).
func (r *ToolRegistry) SetParamManager(pm sdktools.ParamManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paramManager = pm
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
	r.ToolRegistry.RegisterWithSource(tool, source)
}

// resolvePolicy returns the effective policy for a tool.
// Resolution order: per-tool override > skill override > registry default > tool's own default.
func (r *ToolRegistry) resolvePolicy(name string, tool sdktools.Tool) sdktools.ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.policyOverrides[name]; ok {
		return p
	}
	if p, ok := r.skillPolicyOverrides[name]; ok {
		return p
	}
	if r.hasDefaultPolicy {
		return r.defaultPolicy
	}
	return tool.DefaultPolicy()
}

// SetSkillPolicyOverrides sets per-tool policy overrides derived from active skills.
// These have lower priority than config-sourced policyOverrides but higher than default.
func (r *ToolRegistry) SetSkillPolicyOverrides(overrides map[string]sdktools.ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skillPolicyOverrides = overrides
}

// Execute looks up a tool by name and executes it with the given input.
// Returns an error if the tool is not found.
// Security policy is resolved via resolvePolicy() and applied accordingly.
// Internal tools bypass policy and judge checks, but disabled-tool checks
// (No Project mode) are applied to all tools including internal ones.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok {
		return sdktools.ToolResult{Content: "tool not found: " + name, IsError: true}, nil
	}

	// Check disabled tools (No Project mode) — MUST precede internal bypass
	// so that tools like semantic_search are blocked at execution time too.
	r.mu.RLock()
	disabled := r.disabledTools
	extraBashBL := r.extraBashBlacklist
	r.mu.RUnlock()
	if disabled != nil && disabled[name] {
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q is not available in No Project mode", name),
			IsError: true,
		}, nil
	}

	// Internal tools bypass remaining policy/judge checks
	if IsInternalTool(name) {
		return tool.Execute(ctx, input)
	}

	// Extra bash blacklist check (per-session, e.g. No Project mode).
	// Parses the input to extract the command and checks it against
	// compiled patterns.
	if name == "bash_exec" && len(extraBashBL) > 0 {
		var params struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(input, &params); err == nil && params.Command != "" {
			for _, re := range extraBashBL {
				if re.MatchString(params.Command) {
					return sdktools.ToolResult{
						Content: fmt.Sprintf("command %q is not available in No Project mode", params.Command),
						IsError: true,
					}, nil
				}
			}
		}
	}

	// Get source for hooks
	source := r.GetToolSource(name)

	// Pre-execute hook (e.g., indexing gate for MCP tools)
	r.mu.RLock()
	hook := r.preExecuteHook
	r.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, name, source); err != nil {
			return sdktools.ToolResult{Content: fmt.Sprintf("pre-execute hook: %v", err), IsError: true}, nil
		}
	}

	// Param injection (e.g., project scoping for MCP tools)
	r.mu.RLock()
	pm := r.paramManager
	r.mu.RUnlock()
	if pm != nil {
		input = pm.InjectParams(ctx, name, source, input)
	}

	// Symlink detection gate: force-confirm when any tool input contains paths
	// that traverse symlinks, regardless of policy. Shows resolved paths in the
	// confirmation dialog so the user can make an informed decision.
	// sdktools.PolicyAlwaysDeny is still respected — symlinks don't bypass explicit denies.
	if intercepted, result, err := r.checkSymlinksAndConfirm(ctx, tool, name, input); intercepted {
		return result, err
	}

	policy := r.resolvePolicy(name, tool)

	// Workspace/temp auto-approval: if all paths in the input are within the
	// session workspace or temp directory, execute without confirmation.
	// Auto-approval ONLY applies to sdktools.PolicyAlwaysAllow (or no explicit policy
	// when the tool's own default permits it). sdktools.PolicyUserConfirm and
	// sdktools.PolicyAlwaysDeny are NEVER weakened by path-locality heuristics — a
	// user-controlled `working_directory` argument must not bypass an explicit
	// confirm policy (e.g., bash_exec running ./scripts/x.sh inside the
	// workspace).
	if policy == sdktools.PolicyAlwaysAllow {
		if tempDir := sdktools.TempDirFrom(ctx); tempDir != "" && sdktools.AllPathsInDir(input, tempDir) {
			r.log().Debug("auto-approved: all paths within session temp directory", "tool", name)
			return tool.Execute(ctx, input)
		}
		if sdktools.AllPathsInWorkspace(ctx, input) {
			r.log().Debug("auto-approved: all paths within workspace", "tool", name)
			return tool.Execute(ctx, input)
		}
	}

	switch policy {
	case sdktools.PolicyAlwaysAllow:
		// Safety filter: if the tool implements ToolJudger and flags the call, escalate to user confirmation.
		if judger, ok := tool.(sdktools.ToolJudger); ok {
			allow, reasoning := judger.Judge(ctx, input)
			if !allow && reasoning != "" {
				r.log().Debug("sdktools.PolicyAlwaysAllow: tool-specific judge flagged call", "tool", name, "reasoning", reasoning)
				return r.confirmAndExecute(ctx, tool, name, input, reasoning)
			}
		}
		return tool.Execute(ctx, input)

	case sdktools.PolicyAlwaysDeny:
		return sdktools.ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy", name),
			IsError: true,
		}, nil

	case sdktools.PolicyUserConfirm:
		// Workspace auto-approval: when enabled, allow file write tools to execute
		// without confirmation if all paths are within the session workspace.
		// Symlinks are already intercepted by checkSymlinksAndConfirm above,
		// so any path that reached this point is a real (non-symlink) path.
		r.mu.RLock()
		autoApprove := r.autoApproveWorkspaceWrites
		r.mu.RUnlock()
		if autoApprove {
			if judger, ok := tool.(sdktools.ToolJudger); ok {
				allow, reason := judger.Judge(ctx, input)
				if allow {
					r.log().Debug("workspace auto-approve: Judge allows", "tool", name, "reason", reason)
					return tool.Execute(ctx, input)
				}
			}
		}
		return r.confirmAndExecute(ctx, tool, name, input, "")

	default:
		return tool.Execute(ctx, input)
	}
}

// confirmAndExecute requests user confirmation before executing a tool.
// If confirmFunc is nil (CLI mode), executes without confirmation.
func (r *ToolRegistry) confirmAndExecute(ctx context.Context, tool sdktools.Tool, name string, input json.RawMessage, reasoning string) (sdktools.ToolResult, error) {
	r.mu.RLock()
	confirmFunc := r.confirmFunc
	r.mu.RUnlock()

	if confirmFunc == nil {
		return tool.Execute(ctx, input)
	}

	resp, err := confirmFunc(ctx, sdktools.ConfirmationRequest{
		ToolName:       name,
		Input:          input,
		JudgeReasoning: reasoning,
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
			msg += " LLM Judge reasoning for flagging this call: " + reasoning
		}
		return sdktools.ToolResult{Content: msg, IsError: true}, nil
	case sdktools.ConfirmDenyAndStop:
		return sdktools.ToolResult{}, context.Canceled
	default:
		return sdktools.ToolResult{}, fmt.Errorf("unknown confirmation response: %d", resp)
	}
}
