package llm

// FamilyReasoningOptions returns the native reasoning/thinking options available
// for a given model family. It also returns the recommended default (always the
// maximum available effort) and whether the family supports reasoning at all.
func FamilyReasoningOptions(family string) (options []string, preferred string, ok bool) {
	switch family {
	case "anthropic":
		return []string{"On", "Off"}, "On", true
	case "openai_flagship", "openai_standard":
		return []string{"minimal", "low", "medium", "high"}, "high", true
	case "openai_codex":
		return []string{"minimal", "low", "medium", "high", "max"}, "max", true
	case "google":
		return []string{"MINIMAL", "LOW", "MEDIUM", "HIGH"}, "HIGH", true
	case "deepseek":
		return []string{"Off", "High", "Max"}, "Max", true
	case "qwen":
		return []string{"On", "Off"}, "On", true
	case "glm":
		return []string{"On", "Off"}, "On", true
	default:
		return nil, "", false
	}
}
