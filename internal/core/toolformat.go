package core

import (
	"fmt"
	"strings"

	"github.com/user/agent/internal/tools"
)

// buildGroupedToolList formats tool descriptors into a tiered, priority-labeled
// text block for inclusion in LLM prompts. Tools are grouped into 4 tiers:
//   - Tier 1 (External): Source == "external"
//   - Tier 2 (Built-in): Source == "core" and not bash_exec/tool_creator
//   - Tier 3 (MCP): Source == "mcp"
//   - Tier 4 (Fallback): bash_exec and tool_creator
//
// Empty tiers are omitted from the output.
func buildGroupedToolList(descriptors []tools.ToolDescriptor) string {
	var externalTools, builtinTools, mcpTools, fallbackTools []tools.ToolDescriptor

	for _, t := range descriptors {
		switch {
		case t.Source == "external":
			externalTools = append(externalTools, t)
		case t.Name == "bash_exec" || t.Name == "tool_creator":
			fallbackTools = append(fallbackTools, t)
		case t.Source == "mcp":
			mcpTools = append(mcpTools, t)
		default:
			builtinTools = append(builtinTools, t)
		}
	}

	var b strings.Builder

	writeGroup := func(label string, group []tools.ToolDescriptor) {
		if len(group) == 0 {
			return
		}
		b.WriteString(label)
		b.WriteByte('\n')
		for _, t := range group {
			fmt.Fprintf(&b, "- %s: %s\n", t.Name, t.Description)
		}
	}

	writeGroup("External tools (TIER 1 — highest priority, use when applicable):", externalTools)
	writeGroup("Built-in tools (TIER 2):", builtinTools)
	writeGroup("MCP tools (TIER 3):", mcpTools)
	writeGroup("Fallback tools (TIER 4 — use only when no higher-tier tool fits):", fallbackTools)

	return b.String()
}
