package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	sdktools "github.com/user/agent/sdk/tools"
)

// internalTools is the set of tool names that are always allowed,
// excluded from policy configuration, and bypass the tool judge entirely.
var internalTools = map[string]struct{}{
	"ask_user":          {},
	"finish":            {},
	"list_step_outputs": {},
	"read_step_output":  {},
}

// IsInternalTool returns true if the given tool name is an internal tool
// that is always allowed and bypasses policy/judge checks.
func IsInternalTool(name string) bool {
	_, ok := internalTools[name]
	return ok
}

// ToolFilter decides whether a tool should be registered. Return false to reject.
type ToolFilter func(toolName, source string) bool

// ParamInjector transforms tool input before execution (e.g., injecting scoping params).
type ParamInjector func(toolName, source string, input json.RawMessage) json.RawMessage

// ToolRegistry stores all available tools and provides them to Executor.
// It embeds the SDK ToolRegistry for basic operations and adds policy enforcement on top.
// Thread-safe via sync.RWMutex.
type ToolRegistry struct {
	*sdktools.ToolRegistry
	mu                  sync.RWMutex
	confirmFunc         ConfirmFunc
	judge               *ToolJudge
	policyOverrides     map[string]ToolPolicy
	skillPolicyOverrides map[string]ToolPolicy
	defaultPolicy       ToolPolicy
	hasDefaultPolicy    bool
	preExecuteHook      PreExecuteHook
	toolFilter          ToolFilter
	paramInjector       ParamInjector
	logger              *slog.Logger
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
func (r *ToolRegistry) SetConfirmFunc(fn ConfirmFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirmFunc = fn
}

// SetJudge sets the tool judge for evaluating mutating tool calls.
func (r *ToolRegistry) SetJudge(j *ToolJudge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.judge = j
}

// GetJudge returns the current tool judge, or nil if not set.
func (r *ToolRegistry) GetJudge() *ToolJudge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.judge
}

// SetPolicyOverrides sets per-tool policy overrides from configuration.
func (r *ToolRegistry) SetPolicyOverrides(overrides map[string]ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyOverrides = overrides
}

// SetDefaultPolicy sets the default policy for tools without explicit overrides.
func (r *ToolRegistry) SetDefaultPolicy(p ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultPolicy = p
	r.hasDefaultPolicy = true
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

// SetParamInjector sets an injector that transforms tool input before execution.
func (r *ToolRegistry) SetParamInjector(fn ParamInjector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paramInjector = fn
}

// RegisterWithSource registers a tool with the given source, subject to the tool filter.
// If the filter rejects the tool, it is silently dropped.
func (r *ToolRegistry) RegisterWithSource(tool Tool, source string) {
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
func (r *ToolRegistry) resolvePolicy(name string, tool Tool) ToolPolicy {
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
func (r *ToolRegistry) SetSkillPolicyOverrides(overrides map[string]ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skillPolicyOverrides = overrides
}

// Execute looks up a tool by name and executes it with the given input.
// Returns an error if the tool is not found.
// Security policy is resolved via resolvePolicy() and applied accordingly.
// Internal tools bypass all policy and judge checks.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok {
		return ToolResult{}, fmt.Errorf("tool not found: %s", name)
	}

	// Internal tools bypass all policy/judge checks
	if IsInternalTool(name) {
		return tool.Execute(ctx, input)
	}

	// Get source for hooks
	source := r.GetToolSource(name)

	// Pre-execute hook (e.g., indexing gate for MCP tools)
	r.mu.RLock()
	hook := r.preExecuteHook
	r.mu.RUnlock()
	if hook != nil {
		if err := hook(ctx, name, source); err != nil {
			return ToolResult{Content: fmt.Sprintf("pre-execute hook: %v", err), IsError: true}, nil
		}
	}

	// Param injection (e.g., project scoping for MCP tools)
	r.mu.RLock()
	injector := r.paramInjector
	r.mu.RUnlock()
	if injector != nil {
		input = injector(name, source, input)
	}

	policy := r.resolvePolicy(name, tool)

	// Workspace/temp auto-approval: if all paths in the input are within
	// the session workspace or temp directory, execute without confirmation
	// regardless of policy (except AlwaysDeny which is always respected).
	if policy != PolicyAlwaysDeny {
		if tempDir := sdktools.TempDirFrom(ctx); tempDir != "" && allPathsInDir(input, tempDir) {
			r.log().Debug("auto-approved: all paths within session temp directory", "tool", name)
			return tool.Execute(ctx, input)
		}
		if allPathsInWorkspace(ctx, input) {
			r.log().Debug("auto-approved: all paths within workspace", "tool", name)
			return tool.Execute(ctx, input)
		}
	}

	switch policy {
	case PolicyAlwaysAllow:
		// Safety filter: if the tool implements ToolJudger and flags the call, escalate to user confirmation.
		if judger, ok := tool.(ToolJudger); ok {
			allow, reasoning := judger.Judge(ctx, input)
			if !allow && reasoning != "" {
				r.log().Debug("PolicyAlwaysAllow: tool-specific judge flagged call", "tool", name, "reasoning", reasoning)
				return r.confirmAndExecute(ctx, tool, name, input, reasoning)
			}
		}
		return tool.Execute(ctx, input)

	case PolicyAlwaysDeny:
		return ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy", name),
			IsError: true,
		}, nil

	case PolicyUserConfirm:
		return r.confirmAndExecute(ctx, tool, name, input, "")

	default:
		return tool.Execute(ctx, input)
	}
}

// confirmAndExecute requests user confirmation before executing a tool.
// If confirmFunc is nil (CLI mode), executes without confirmation.
func (r *ToolRegistry) confirmAndExecute(ctx context.Context, tool Tool, name string, input json.RawMessage, reasoning string) (ToolResult, error) {
	r.mu.RLock()
	confirmFunc := r.confirmFunc
	r.mu.RUnlock()

	if confirmFunc == nil {
		return tool.Execute(ctx, input)
	}

	resp, err := confirmFunc(ctx, ConfirmationRequest{
		ToolName:       name,
		Input:          input,
		JudgeReasoning: reasoning,
	})
	if err != nil {
		return ToolResult{}, err
	}

	switch resp {
	case ConfirmAllowOnce:
		return tool.Execute(ctx, input)
	case ConfirmDeny:
		msg := "Tool execution denied by user."
		if reasoning != "" {
			msg += " LLM Judge reasoning for flagging this call: " + reasoning
		}
		return ToolResult{Content: msg, IsError: true}, nil
	case ConfirmDenyAndStop:
		return ToolResult{}, context.Canceled
	default:
		return ToolResult{}, fmt.Errorf("unknown confirmation response: %d", resp)
	}
}
