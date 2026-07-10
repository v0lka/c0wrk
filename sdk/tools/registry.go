package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// ToolRegistry stores all available tools and provides them to Executor.
// Thread-safe via sync.RWMutex.
//
// Execute applies fail-closed security policy enforcement based on each
// tool's DefaultPolicy() (or a per-tool override set via SetPolicyOverride):
//   - PolicyAlwaysAllow: executes directly. If the tool implements ToolJudger
//     and the judge flags the call, it is escalated to user confirmation.
//   - PolicyAlwaysDeny: the call is rejected.
//   - PolicyUserConfirm: the configured ConfirmFunc is consulted. If no
//     ConfirmFunc is set, the call is DENIED (fail-closed) — mutating tools
//     never execute silently without an explicit confirmation channel or an
//     explicit policy override.
//
// Hosts that implement their own enforcement layer on top (like a wrapping
// registry that shadows Execute) are unaffected as long as they do not route
// calls through this Execute method.
type ToolRegistry struct {
	tools           map[string]Tool
	toolSources     map[string]string
	toolCategories  map[string]ToolSourceCategory
	policyOverrides map[string]ToolPolicy
	confirmFunc     ConfirmFunc
	paramManager    ParamManager // optional param injector for execution-time injection
	logger          *slog.Logger
	mu              sync.RWMutex
}

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:           make(map[string]Tool),
		toolSources:     make(map[string]string),
		toolCategories:  make(map[string]ToolSourceCategory),
		policyOverrides: make(map[string]ToolPolicy),
	}
}

// SetLogger sets the logger used for registration warnings.
// If nil (or never called), slog.Default() is used.
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

// SetConfirmFunc sets the confirmation callback consulted for tools whose
// effective policy is PolicyUserConfirm (and for judge-escalated calls).
// If no ConfirmFunc is configured, such calls are DENIED (fail-closed).
func (r *ToolRegistry) SetConfirmFunc(fn ConfirmFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirmFunc = fn
}

// SetPolicyOverride sets an explicit per-tool policy that takes precedence
// over the tool's own DefaultPolicy(). Use this to deliberately relax
// (e.g. PolicyAlwaysAllow for bash_exec in CI) or tighten a tool's policy.
func (r *ToolRegistry) SetPolicyOverride(name string, policy ToolPolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyOverrides[name] = policy
}

// ClearPolicyOverride removes a per-tool policy override, restoring the
// tool's own DefaultPolicy() as the effective policy.
func (r *ToolRegistry) ClearPolicyOverride(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.policyOverrides, name)
}

// categoryForLocked resolves the source category of a registered tool.
// Explicitly stored categories win; otherwise falls back to the legacy
// source-prefix heuristic ("mcp*" → MCP) for backward compatibility with
// callers that registered via RegisterWithSource without a category.
// Caller must hold r.mu (read or write).
func (r *ToolRegistry) categoryForLocked(name string) ToolSourceCategory {
	if cat, ok := r.toolCategories[name]; ok {
		return cat
	}
	if source, ok := r.toolSources[name]; ok && strings.HasPrefix(source, "mcp") {
		return SourceCategoryMCP
	}
	return SourceCategoryCore
}

// registerLocked inserts a tool entry, enforcing the anti-shadowing rule:
// an MCP-sourced tool may NOT overwrite an already-registered tool of a
// different (non-MCP) category. Returns false if registration was skipped.
// Caller must hold r.mu for writing.
func (r *ToolRegistry) registerLocked(tool Tool, source string, hasSource bool, category ToolSourceCategory) bool {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		existingCategory := r.categoryForLocked(name)
		if category == SourceCategoryMCP && existingCategory != SourceCategoryMCP {
			return false
		}
	}
	r.tools[name] = tool
	if hasSource {
		r.toolSources[name] = source
	} else {
		// Overwriting registration without a source: clear any stale source
		// left by a previous RegisterWithSource for the same name.
		delete(r.toolSources, name)
	}
	r.toolCategories[name] = category
	return true
}

// Register adds a tool to the registry by its name.
// Re-registering an existing name replaces the previous entry (including an
// MCP-sourced one) and clears its stale source/category metadata.
func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerLocked(tool, "", false, SourceCategoryCore)
}

// RegisterWithSource adds a tool to the registry with an explicit source tag.
// The source category is inferred via the legacy heuristic: sources with the
// "mcp" prefix are classified as MCP, everything else as Core. New code that
// registers MCP tools should prefer RegisterWithSourceCategory so the
// category does not depend on the server name.
// If the inferred category is MCP and the name is already taken by a non-MCP
// tool, the registration is skipped with a warning (no shadowing).
func (r *ToolRegistry) RegisterWithSource(tool Tool, source string) {
	category := SourceCategoryCore
	if strings.HasPrefix(source, "mcp") {
		category = SourceCategoryMCP
	}
	r.mu.Lock()
	ok := r.registerLocked(tool, source, true, category)
	r.mu.Unlock()
	if !ok {
		r.log().Warn("MCP tool registration skipped: name shadows a non-MCP tool",
			"tool", tool.Name(), "source", source)
	}
}

// RegisterWithSourceCategory adds a tool with an explicit source tag and an
// explicit source category. This is the preferred registration path for MCP
// gateways: the category is stored verbatim and does not depend on the
// server name.
// Returns an error (and skips registration) when an MCP-categorized tool
// would shadow an already-registered non-MCP tool.
func (r *ToolRegistry) RegisterWithSourceCategory(tool Tool, source string, category ToolSourceCategory) error {
	r.mu.Lock()
	ok := r.registerLocked(tool, source, true, category)
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("tool %q from source %q not registered: MCP tools may not shadow non-MCP tools", tool.Name(), source)
	}
	return nil
}

// Unregister removes a tool from the registry by name.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.toolSources, name)
	delete(r.toolCategories, name)
}

// UnregisterBySource removes all tools that were registered with the given source.
func (r *ToolRegistry) UnregisterBySource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, s := range r.toolSources {
		if s == source {
			delete(r.tools, name)
			delete(r.toolSources, name)
			delete(r.toolCategories, name)
		}
	}
}

// Get returns a tool by name and a boolean indicating if it was found.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns a slice of ToolDescriptors for all registered tools.
func (r *ToolRegistry) List() []ToolDescriptor {
	return r.ListFiltered(nil)
}

// ListFiltered returns descriptors for all registered tools except those
// whose names appear in excludeNames. An empty or nil excludeNames returns all tools.
func (r *ToolRegistry) ListFiltered(excludeNames map[string]bool) []ToolDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	descriptors := make([]ToolDescriptor, 0, len(r.tools))
	for _, tool := range r.tools {
		if excludeNames != nil && excludeNames[tool.Name()] {
			continue
		}
		source := "core"
		if s, ok := r.toolSources[tool.Name()]; ok {
			source = s
		}
		descriptors = append(descriptors, ToolDescriptor{
			Name:           tool.Name(),
			Description:    tool.Description(),
			InputSchema:    tool.InputSchema(),
			Source:         source,
			SourceCategory: r.categoryForLocked(tool.Name()),
		})
	}
	return descriptors
}

// Execute looks up a tool by name and executes it with the given input,
// applying fail-closed policy enforcement (see the ToolRegistry doc comment).
// Returns a not-found error result if the tool is not registered.
// Param injection is applied if a ParamManager is configured.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok {
		return ToolResult{Content: "tool not found: " + name, IsError: true}, nil
	}

	r.mu.RLock()
	pm := r.paramManager
	source := r.toolSources[name]
	policy, hasOverride := r.policyOverrides[name]
	r.mu.RUnlock()

	// Apply param injection if configured (e.g., project scoping for MCP tools).
	if pm != nil {
		input = pm.InjectParams(ctx, name, source, input)
	}

	if !hasOverride {
		policy = tool.DefaultPolicy()
	}

	switch policy {
	case PolicyAlwaysDeny:
		return ToolResult{
			Content: fmt.Sprintf("tool %q blocked by security policy", name),
			IsError: true,
		}, nil

	case PolicyUserConfirm:
		return r.confirmAndExecute(ctx, tool, name, input, "")

	default: // PolicyAlwaysAllow
		// Tool-specific safety judge: when an always-allowed tool implements
		// ToolJudger and flags the call, escalate to user confirmation
		// (which fail-closes to deny if no ConfirmFunc is configured).
		if judger, ok := tool.(ToolJudger); ok {
			if allow, reasoning := judger.Judge(ctx, input); !allow && reasoning != "" {
				return r.confirmAndExecute(ctx, tool, name, input, reasoning)
			}
		}
		return tool.Execute(ctx, input)
	}
}

// confirmAndExecute requests user confirmation before executing a tool.
// FAIL-CLOSED: if no ConfirmFunc is configured, the call is denied with an
// actionable error instead of executing silently.
func (r *ToolRegistry) confirmAndExecute(ctx context.Context, tool Tool, name string, input json.RawMessage, reasoning string) (ToolResult, error) {
	r.mu.RLock()
	confirmFunc := r.confirmFunc
	r.mu.RUnlock()

	if confirmFunc == nil {
		return ToolResult{
			Content: fmt.Sprintf(
				"tool %q requires confirmation but no ConfirmFunc is configured; "+
					"set one via ToolRegistry.SetConfirmFunc (or sp4rk.Config.ConfirmFunc when using the Framework), "+
					"or explicitly override the policy via SetPolicyOverride(%q, PolicyAlwaysAllow)",
				name, name),
			IsError: true,
		}, nil
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
			msg += " Judge reasoning for flagging this call: " + reasoning
		}
		return ToolResult{Content: msg, IsError: true}, nil
	case ConfirmDenyAndStop:
		return ToolResult{}, context.Canceled
	default:
		return ToolResult{}, fmt.Errorf("unknown confirmation response: %d", resp)
	}
}

// GetToolSource returns the source of a tool (e.g., "core" or the MCP server name).
// Returns "core" for built-in tools, or the source tag for tools registered via RegisterWithSource.
// Returns empty string if the tool is not found.
func (r *ToolRegistry) GetToolSource(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if _, ok := r.tools[name]; !ok {
		return ""
	}

	if source, ok := r.toolSources[name]; ok {
		return source
	}
	return "core"
}

// IsToolUntrusted reports whether a tool's output is from an untrusted external source.
// Returns true if:
//   - The tool implements IsUntrusted() == true
//   - The tool is sourced from MCP (always untrusted)
//
// Returns false for unknown tools.
func (r *ToolRegistry) IsToolUntrusted(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, ok := r.tools[name]
	if !ok {
		return false
	}
	// Use the Tool interface's IsUntrusted() method for per-tool classification.
	if tool.IsUntrusted() {
		return true
	}
	// MCP-sourced tools are always untrusted regardless of their IsUntrusted() value.
	return r.categoryForLocked(name) == SourceCategoryMCP
}

// SetParamManager sets a ParamManager that handles param injection at execution time.
// When set, it is consulted during Execute() to inject auto-managed parameters
// (e.g., "project") before invoking the tool.
func (r *ToolRegistry) SetParamManager(pm ParamManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paramManager = pm
}
