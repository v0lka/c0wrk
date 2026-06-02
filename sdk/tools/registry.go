package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// ToolRegistry stores all available tools and provides them to Executor.
// Thread-safe via sync.RWMutex.
type ToolRegistry struct {
	tools       map[string]Tool
	toolSources map[string]string
	mu          sync.RWMutex
}

// NewToolRegistry creates a new ToolRegistry with an empty tool map.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:       make(map[string]Tool),
		toolSources: make(map[string]string),
	}
}

// Register adds a tool to the registry by its name.
func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// RegisterWithSource adds a tool to the registry with an explicit source tag.
func (r *ToolRegistry) RegisterWithSource(tool Tool, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
	r.toolSources[tool.Name()] = source
}

// Unregister removes a tool from the registry by name.
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.toolSources, name)
}

// UnregisterBySource removes all tools that were registered with the given source.
func (r *ToolRegistry) UnregisterBySource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, s := range r.toolSources {
		if s == source {
			delete(r.tools, name)
			delete(r.toolSources, name)
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	descriptors := make([]ToolDescriptor, 0, len(r.tools))
	for _, tool := range r.tools {
		source := "core"
		if s, ok := r.toolSources[tool.Name()]; ok {
			source = s
		}
		sourceCategory := SourceCategoryCore
		if strings.HasPrefix(source, "mcp") {
			sourceCategory = SourceCategoryMCP
		}
		descriptors = append(descriptors, ToolDescriptor{
			Name:           tool.Name(),
			Description:    tool.Description(),
			InputSchema:    tool.InputSchema(),
			Source:         source,
			SourceCategory: sourceCategory,
		})
	}
	return descriptors
}

// Execute looks up a tool by name and executes it with the given input.
// Returns an error if the tool is not found.
// This is a simple execute with NO policy enforcement, NO confirmation, NO judge.
func (r *ToolRegistry) Execute(ctx context.Context, name string, input json.RawMessage) (ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok {
		return ToolResult{Content: "tool not found: " + name, IsError: true}, nil
	}
	return tool.Execute(ctx, input)
}

// GetToolSource returns the source of a tool (e.g., "core", "mcp:<server>").
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

// HasSourceContaining reports whether any registered tool has a source
// whose name contains the given substring (case-insensitive).
func (r *ToolRegistry) HasSourceContaining(substr string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lower := strings.ToLower(substr)
	seen := make(map[string]struct{})
	for _, src := range r.toolSources {
		if _, ok := seen[src]; ok {
			continue
		}
		seen[src] = struct{}{}
		if strings.Contains(strings.ToLower(src), lower) {
			return true
		}
	}
	return false
}
