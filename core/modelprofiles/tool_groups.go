package modelprofiles

// ToolGroupDef is one workflow cluster the always-present picker offers as
// a single entry pinning every member at once; members are validated
// against the live registry (specs/domains/model-profiles.md).
type ToolGroupDef struct {
	// ID is a stable, URL-safe cluster identifier.
	ID string
	// Title is the human-readable cluster name shown in the picker.
	Title string
	// Description explains the capability the cluster grants the agent; the
	// picker renders it (alongside the member list) in the entry's tooltip.
	Description string
	// Tools are the member names in display order; selecting the cluster
	// pins each still-selectable member.
	Tools []string
}

// toolGroupCatalog groups only tools useless in isolation; standalone areas
// stay ungrouped and goal-mode tools are excluded (never narrowable — a
// cluster would be inert; specs/domains/model-profiles.md).
var toolGroupCatalog = []ToolGroupDef{
	{
		ID:    "plan",
		Title: "Planning & steps",
		Description: "Declare a multi-step plan for complex tasks, execute its " +
			"steps, and mark each step finished (plus checklist progress). " +
			"Selected as a unit so a plan can always be executed and tracked.",
		Tools: []string{"declare_plan", "execute_plan", "declare_step_complete", "update_checklist"},
	},
	{
		ID:    "subagents",
		Title: "Subagents & reflection",
		Description: "Delegate work to isolated subagents, cancel a running " +
			"delegation, read delegated step outputs and final results, and " +
			"reflect on the trajectory. Selected as a unit so delegated work " +
			"can always be read back.",
		Tools: []string{"delegate", "cancel_delegation", "read_step_output", "list_step_outputs", "read_final_result", "reflect"},
	},
}

// ToolGroupCatalog returns a fresh deep copy of the catalog (including each
// Tools slice).
func ToolGroupCatalog() []ToolGroupDef {
	out := make([]ToolGroupDef, 0, len(toolGroupCatalog))
	for _, g := range toolGroupCatalog {
		cp := g
		cp.Tools = append([]string(nil), g.Tools...)
		out = append(out, cp)
	}
	return out
}
