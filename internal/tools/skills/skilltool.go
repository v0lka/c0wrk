package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	tools "github.com/user/agent/internal/tools"
)

// SkillTool wraps a Skill as a Tool interface implementation.
type SkillTool struct {
	manifest  *SkillManifest
	container *SkillContainer
	skillDir  string
	builder   *DockerBuilder
	mu        sync.Mutex
}

// NewSkillTool creates a new SkillTool from a manifest.
func NewSkillTool(manifest *SkillManifest, skillDir string, builder *DockerBuilder) *SkillTool {
	return &SkillTool{
		manifest: manifest,
		skillDir: skillDir,
		builder:  builder,
	}
}

// Name returns the skill name.
func (st *SkillTool) Name() string {
	return st.manifest.Name
}

// Description returns the skill description.
func (st *SkillTool) Description() string {
	return st.manifest.Description
}

// InputSchema returns the skill input schema as JSON.
func (st *SkillTool) InputSchema() json.RawMessage {
	data, err := json.Marshal(st.manifest.InputSchema)
	if err != nil {
		return json.RawMessage("{}")
	}
	return data
}

// DefaultPolicy returns PolicyAlwaysAllow because skills are Docker-sandboxed.
func (st *SkillTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute runs the skill with the given input.
func (st *SkillTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Lazy initialization: build container if not built
	if st.container == nil {
		st.container = NewSkillContainer(st.manifest, st.builder)
	}

	if !st.container.IsBuilt() {
		if err := st.container.Build(ctx, st.skillDir); err != nil {
			return tools.ToolResult{
				Content: fmt.Sprintf("failed to build skill container: %v", err),
				IsError: true,
			}, nil
		}
	}

	// Parse input JSON to map
	var params map[string]interface{}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to parse input: %v", err),
			IsError: true,
		}, nil
	}

	// Run the container
	output, err := st.container.Run(ctx, params)
	if err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("skill execution failed: %v", err),
			IsError: true,
		}, nil
	}

	return tools.ToolResult{
		Content: output,
		IsError: false,
	}, nil
}

// Compile-time check that SkillTool implements Tool interface.
var _ tools.Tool = (*SkillTool)(nil)
