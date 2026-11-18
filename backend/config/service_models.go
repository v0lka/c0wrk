package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/v0lka/sp4rk/llm"
)

// Service-model override keys: the fixed set of one-shot service LLM calls
// (title generation, commit messages, prompt optimization, …) that
// `llm.service_models` can individually re-route onto a dedicated composite
// "provider/model" id. The set is deliberately closed — an unrecognized key
// is a hand-editing typo, not a forward-compat concern, so load drops it
// with a warning (the valid set is named in the warning to self-document).
const (
	ServiceModelJudge               = "judge"
	ServiceModelRouter              = "router"
	ServiceModelReflector           = "reflector"
	ServiceModelTitle               = "title"
	ServiceModelCommitMessage       = "commit_message"
	ServiceModelOptimizeExtract     = "optimize_extract"
	ServiceModelOptimizeRewrite     = "optimize_rewrite"
	ServiceModelCompactionSummarize = "compaction_summarize"
	ServiceModelGoalSuggest         = "goal_suggest"
	ServiceModelPromptSuggest       = "prompt_suggest"
)

// serviceModelKeys is the membership set behind normalizeServiceModels; the
// string slice is the deterministic rendering order for warnings.
var (
	serviceModelKeys = map[string]struct{}{
		ServiceModelJudge:               {},
		ServiceModelRouter:              {},
		ServiceModelReflector:           {},
		ServiceModelTitle:               {},
		ServiceModelCommitMessage:       {},
		ServiceModelOptimizeExtract:     {},
		ServiceModelOptimizeRewrite:     {},
		ServiceModelCompactionSummarize: {},
		ServiceModelGoalSuggest:         {},
		ServiceModelPromptSuggest:       {},
	}
	serviceModelKeyList = []string{
		ServiceModelJudge,
		ServiceModelRouter,
		ServiceModelReflector,
		ServiceModelTitle,
		ServiceModelCommitMessage,
		ServiceModelOptimizeExtract,
		ServiceModelOptimizeRewrite,
		ServiceModelCompactionSummarize,
		ServiceModelGoalSuggest,
		ServiceModelPromptSuggest,
	}
)

// IsServiceModelKey reports whether key is one of the fixed service-model
// override keys accepted in llm.service_models.
func IsServiceModelKey(key string) bool {
	_, ok := serviceModelKeys[key]
	return ok
}

// ValidServiceModelKeys returns the accepted service-model keys in their
// canonical (declaration) order — the list warnings and docs name.
func ValidServiceModelKeys() []string {
	out := make([]string, len(serviceModelKeyList))
	copy(out, serviceModelKeyList)
	return out
}

// normalizeServiceModels validates every llm.service_models entry and returns
// a load warning for each invalid one — fail-soft: the entry is DROPPED in
// place so the next save rewrites the section without it (the "rewritten in
// place" convention shared with the legacy-key migrations). Validation is
// format-only: the key must be one of the fixed service keys and the value a
// composite "provider/model" id (split on the FIRST "/" only — the model
// name may itself contain "/"; both parts non-empty). The provider/model
// pair is deliberately NOT resolved here — that is the session-build runtime
// check, which fails soft to the session default (an override pointing at a
// missing provider or disabled model degrades to the default instead of
// blocking the load). An absent/empty section is the no-override behavior
// and warns about nothing. Runs after ApplyDefaults and before validate,
// mirroring normalizeSessionArtifacts; entries are visited in sorted order
// so the warning order — surfaced verbatim in the UI — is deterministic.
func normalizeServiceModels(cfg *Config) []string {
	models := cfg.LLM.ServiceModels
	if len(models) == 0 {
		return nil
	}
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var warnings []string
	for _, key := range keys {
		if !IsServiceModelKey(key) {
			warnings = append(warnings, fmt.Sprintf(
				"llm.service_models.%q ignored (dropped): unknown service model key; valid keys: %s",
				key, strings.Join(ValidServiceModelKeys(), ", ")))
			delete(models, key)
			continue
		}
		if reason := serviceModelValueProblem(models[key]); reason != "" {
			warnings = append(warnings, fmt.Sprintf(
				"llm.service_models.%q ignored (dropped): %s", key, reason))
			delete(models, key)
		}
	}
	return warnings
}

// serviceModelValueProblem describes why a service-model override value is
// not a usable composite "provider/model" id, or "" when the format is fine.
// Empty, bare (no "/"), empty-provider ("/model") and empty-model
// ("provider/") values are all rejected; anything else — including a model
// part containing further "/" — passes. The provider name itself is never
// checked against the configured providers.
func serviceModelValueProblem(value string) string {
	if value == "" {
		return `value is empty; expected a composite "provider/model" id`
	}
	provider, model, ok := llm.ParseCompositeModelID(value)
	switch {
	case !ok:
		return fmt.Sprintf("value %q is not a composite \"provider/model\" id (missing the provider prefix)", value)
	case provider == "":
		return fmt.Sprintf("value %q has an empty provider part; expected \"provider/model\"", value)
	case model == "":
		return fmt.Sprintf("value %q has an empty model part; expected \"provider/model\"", value)
	}
	return ""
}
