package agent

import (
	"fmt"
	"strings"

	"github.com/v0lka/sp4rk/tools"
)

// BuildGroupedToolList formats tool descriptors into a tiered, priority-labeled
// text block for inclusion in LLM prompts. Tools are grouped into 3 tiers:
//   - Tier 1 (Built-in): Source == "core" and not bash_exec
//   - Tier 2 (MCP/External): SourceCategory is MCP
//   - Tier 3 (Fallback): bash_exec
//
// Empty tiers are omitted from the output.
func BuildGroupedToolList(descriptors []tools.ToolDescriptor) string {
	var builtinTools, mcpTools, fallbackTools []tools.ToolDescriptor

	for _, t := range descriptors {
		switch {
		case t.Name == "bash_exec":
			fallbackTools = append(fallbackTools, t)
		case t.SourceCategory == tools.SourceCategoryMCP:
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

	writeGroup("Built-in tools (TIER 1):", builtinTools)
	writeGroup("MCP tools (TIER 2):", mcpTools)
	writeGroup("Fallback tools (TIER 3 — use only when no higher-tier tool fits):", fallbackTools)

	return b.String()
}
