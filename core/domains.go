package core

// Domain values used by the Router and Planner to drive context compaction
// strategy and step profile defaults. Values match the strings emitted by the
// router LLM in its JSON output (see core/prompts/router_system.md) and the
// strings consumed by core/stepconfig.go and core/toolprofiles.go.
const (
	// DomainCode is for steps whose primary activity is modifying source files
	// or running build/test commands. Compacted with sliding-window strategy.
	DomainCode = "code"

	// DomainResearch is for steps whose primary activity is information
	// gathering (reading code, documentation, search). Compacted with
	// summarization strategy so synthesized findings survive long histories.
	DomainResearch = "research"

	// DomainGeneral is the default when the activity does not cleanly fit
	// "code" or "research". Sliding-window compaction; switches to hierarchical
	// at higher complexity thresholds.
	DomainGeneral = "general"

	// DomainMixed is treated as DomainGeneral by all current strategies; kept
	// distinct to preserve the router's expressive power.
	DomainMixed = "mixed"
)
