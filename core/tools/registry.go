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
	"batch":             {},
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

// ToolRegistry stores all available tools and provides them to Executor.
// It embeds the SDK ToolRegistry for basic operations and adds policy enforcement on top.
// Thread-safe via sync.RWMutex.
type ToolRegistry struct {
	*sdktools.ToolRegistry
	mu               sync.RWMutex
	confirmFunc      ConfirmFunc
	judge            *ToolJudge
	policyOverrides  map[string]ToolPolicy
	defaultPolicy    ToolPolicy
	hasDefaultPolicy bool
}

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		ToolRegistry: sdktools.NewToolRegistry(),
	}
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

// resolvePolicy returns the effective policy for a tool.
// Resolution order: per-tool override > registry default > tool's own default.
func (r *ToolRegistry) resolvePolicy(name string, tool Tool) ToolPolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.policyOverrides[name]; ok {
		return p
	}
	if r.hasDefaultPolicy {
		return r.defaultPolicy
	}
	return tool.DefaultPolicy()
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

	policy := r.resolvePolicy(name, tool)

	// Workspace/temp auto-approval: if all paths in the input are within
	// the session workspace or temp directory, execute without confirmation
	// regardless of policy (except AlwaysDeny which is always respected).
	if policy != PolicyAlwaysDeny {
		if tempDir := sdktools.TempDirFrom(ctx); tempDir != "" && allPathsInDir(input, tempDir) {
			slog.Debug("auto-approved: all paths within session temp directory", "tool", name)
			return tool.Execute(ctx, input)
		}
		if allPathsInWorkspace(ctx, input) {
			slog.Debug("auto-approved: all paths within workspace", "tool", name)
			return tool.Execute(ctx, input)
		}
	}

	switch policy {
	case PolicyAlwaysAllow:
		// Safety filter: if the tool implements ToolJudger and flags the call, escalate to user confirmation.
		if judger, ok := tool.(ToolJudger); ok {
			allow, reasoning := judger.Judge(ctx, input)
			if !allow && reasoning != "" {
				slog.Debug("PolicyAlwaysAllow: tool-specific judge flagged call", "tool", name, "reasoning", reasoning)
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

