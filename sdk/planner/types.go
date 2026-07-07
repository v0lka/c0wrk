package planner

import (
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// AgentProfile defines a specialized agent role for plan step execution.
type AgentProfile struct {
	// Role controls system prompt customization; tool filtering uses AllowedTools; strategy uses Domain.
	Role           string   `json:"role"`                      // "researcher", "coder", "tester", "executor" (default)
	SystemPrompt   string   `json:"system_prompt,omitempty"`   // role-specific prompt override (optional)
	AllowedTools   []string `json:"allowed_tools,omitempty"`   // subset of available tools (empty = all)
	Skills         []string `json:"skills,omitempty"`          // subset of router-matched skills (empty = use full task-scope pool)
	MaxSteps       int      `json:"max_steps,omitempty"`       // budget per agent (0 = use default)
	Domain         string   `json:"domain,omitempty"`          // "code" | "research" | "general" - affects compaction strategy
	KeepLastN      int      `json:"keep_last_n,omitempty"`     // per-step KeepLastN override (0 = use role default)
	ProtectedTools []string `json:"protected_tools,omitempty"` // per-step ProtectedTools override (nil = use role default)
}

// ToolLister provides access to available tool descriptors.
// Implementations include tool registries.
type ToolLister interface {
	List() []tools.ToolDescriptor
}

// Events is the minimal event interface needed by the planner.
// Implementations must be nil-safe.
type Events interface {
	agent.Events
	ServiceWithMeta(content string, meta map[string]any)
}

// ToolRegistry is the interface the planner needs for tool operations:
// executing tools during exploration and listing available tools.
type ToolRegistry interface {
	agent.ToolExecutor
	ToolLister
}

// ContextManagerFactory creates a ContextManager for the exploration loop.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) agent.ContextManager
