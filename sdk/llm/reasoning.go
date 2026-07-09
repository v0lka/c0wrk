package llm

import (
	"regexp"
	"strconv"
	"strings"
)

// FamilyReasoningOptions returns the native reasoning/thinking options available
// for a given model family. It also returns the recommended default (always the
// maximum available effort) and whether the family supports reasoning at all.
//
// Note: this is the family-level view. When the specific model version matters
// (e.g. GLM 5.2+ introduced reasoning_effort), use ModelReasoningOptions.
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

// glmVersionRe captures the GLM major (and optional minor) version from a bare
// model name such as "glm-5.2" or "glm-4.7". It anchors on the "glm-" prefix so
// older/unversioned names (e.g. "glm-z1-32b", "chatglm-4") do not match.
var glmVersionRe = regexp.MustCompile(`^glm-(\d+)(?:\.(\d+))?`)

// IsGLM52OrLater reports whether model is a GLM model version 5.2 or later.
// GLM 5.2+ introduced the reasoning_effort parameter (values "max"/"high"),
// which is honored when thinking is enabled. The model argument may be a bare
// name ("glm-5.2") or a composite "provider/name" identifier.
func IsGLM52OrLater(model string) bool {
	bare := strings.ToLower(strings.TrimSpace(BareModel(model)))
	m := glmVersionRe.FindStringSubmatch(bare)
	if m == nil {
		return false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return false
	}
	minor := 0
	if m[2] != "" {
		if minor, err = strconv.Atoi(m[2]); err != nil {
			minor = 0
		}
	}
	return major > 5 || (major == 5 && minor >= 2)
}

// ModelReasoningOptions is the model-aware counterpart of
// FamilyReasoningOptions. It returns the reasoning options for a specific model
// when the model version matters (e.g. GLM 5.2+), and falls back to the
// family-level options otherwise.
//
// GLM 5.2+ exposes three options:
//
//   - "none": thinking disabled
//   - "max":  thinking enabled with reasoning_effort=max (the GLM default)
//   - "high": thinking enabled with reasoning_effort=high
//
// Older GLM models keep the family-level "On"/"Off" options.
func ModelReasoningOptions(family, model string) (options []string, preferred string, ok bool) {
	if family == "glm" && IsGLM52OrLater(model) {
		return []string{"none", "max", "high"}, "max", true
	}
	return FamilyReasoningOptions(family)
}
