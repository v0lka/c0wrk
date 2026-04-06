package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
		descriptors = append(descriptors, ToolDescriptor{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.InputSchema(),
			Source:      source,
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
		return ToolResult{}, fmt.Errorf("tool not found: %s", name)
	}
	return tool.Execute(ctx, input)
}
