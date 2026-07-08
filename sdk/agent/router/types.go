package router

// Domain constants used by the Router and Planner to drive context compaction
// strategy and step profile defaults. Values match the strings emitted by the
// router LLM in its JSON output and consumed by downstream orchestration.
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

// RoutingDecision is the result of Router classification.
type RoutingDecision struct {
	Domain             string   `json:"domain"`     // "code" | "research" | "general" | "mixed"
	Complexity         int      `json:"complexity"` // 1-5
	NeedsClarification bool     `json:"needs_clarification"`
	MatchedSkills      []string `json:"matched_skills,omitempty"` // skills selected by the router
}

// SkillDescriptor is the minimal skill metadata needed by the router.
type SkillDescriptor struct {
	Name        string
	Description string
}

// applyCompactionStrategy returns the compaction strategy name based on
// domain and complexity.
func applyCompactionStrategy(domain string, complexity int) string {
	switch domain {
	case DomainCode:
		return "sliding_window"
	case DomainResearch:
		return "summarization"
	case DomainMixed, DomainGeneral:
		if complexity >= 4 {
			return "hierarchical"
		}
		return "sliding_window"
	default:
		return "sliding_window"
	}
}
