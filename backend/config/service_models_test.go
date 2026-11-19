package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestServiceModelValueProblem pins the format rule for llm.service_models
// values: a composite "provider/model" id with BOTH parts non-empty (split
// on the FIRST "/" only, so a nested model name passes). The provider name
// itself is never checked — that is the session-build runtime resolution.
func TestServiceModelValueProblem(t *testing.T) {
	cases := []struct {
		value   string
		wantBad bool
	}{
		{"", true},
		{"claude-3-haiku", true},  // bare name, no provider prefix
		{"anthropic/", true},      // empty model part
		{"/claude-3-haiku", true}, // empty provider part
		{"anthropic/claude-3-haiku", false},
		{"lmstudio/Qwen/Qwen3-Coder-30B", false}, // nested "/" in the model name
	}
	for _, tc := range cases {
		got := serviceModelValueProblem(tc.value)
		if (got != "") != tc.wantBad {
			t.Errorf("serviceModelValueProblem(%q) = %q, wantBad %v", tc.value, got, tc.wantBad)
		}
	}
}

// TestIsServiceModelKey pins the fixed key set and its exact spelling.
func TestIsServiceModelKey(t *testing.T) {
	for _, key := range ValidServiceModelKeys() {
		if !IsServiceModelKey(key) {
			t.Errorf("IsServiceModelKey(%q) = false, want true", key)
		}
	}
	if IsServiceModelKey("summarizer") {
		t.Error(`IsServiceModelKey("summarizer") = true, want false (unknown key)`)
	}
	if IsServiceModelKey("") {
		t.Error(`IsServiceModelKey("") = true, want false`)
	}
}

// TestNormalizeServiceModels_DropsUnknownAndBroken pins the direct normalizer
// behavior: unknown keys and broken values warn and are DELETED in place,
// valid entries survive untouched.
func TestNormalizeServiceModels_DropsUnknownAndBroken(t *testing.T) {
	cfg := &Config{}
	cfg.LLM.ServiceModels = map[string]string{
		ServiceModelJudge:     "anthropic/claude-3-haiku",
		ServiceModelTitle:     "bare-model",
		ServiceModelReflector: "anthropic/",
		ServiceModelRouter:    "",
		"summarizer":          "anthropic/claude-3-haiku",
	}

	warnings := normalizeServiceModels(cfg)
	if len(warnings) != 4 {
		t.Fatalf("warnings = %v, want exactly 4 (unknown key + empty/bare/provider-only values)", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		`llm.service_models."summarizer"`,
		`llm.service_models."router"`,
		`llm.service_models."reflector"`,
		`llm.service_models."title"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings must name %s, got: %v", want, warnings)
		}
	}
	// The valid entry survives; the invalid ones are dropped in place.
	if got := cfg.LLM.ServiceModels[ServiceModelJudge]; got != "anthropic/claude-3-haiku" {
		t.Errorf("judge override = %q, want it preserved", got)
	}
	if len(cfg.LLM.ServiceModels) != 1 {
		t.Errorf("ServiceModels = %v, want only the valid judge entry left", cfg.LLM.ServiceModels)
	}
}

// TestNormalizeServiceModels_EmptySectionSilent pins "absent/empty section =
// behavior without changes, zero warnings".
func TestNormalizeServiceModels_EmptySectionSilent(t *testing.T) {
	cfg := &Config{}
	if w := normalizeServiceModels(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none for an absent section", w)
	}
	cfg.LLM.ServiceModels = map[string]string{}
	if w := normalizeServiceModels(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none for an empty section", w)
	}
}

// TestLoad_ServiceModels_ParsesValidSection runs the full pipeline on a valid
// section: parsed, warning-free, values preserved.
func TestLoad_ServiceModels_ParsesValidSection(t *testing.T) {
	content := securityGroupsTestBase + `
  service_models:
    judge: "anthropic/claude-3-haiku"
    title: "lmstudio/Qwen/Qwen3-Coder-30B"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Fatalf("LoadErrors = %v, want none", result.LoadErrors)
	}
	want := map[string]string{
		ServiceModelJudge: "anthropic/claude-3-haiku",
		ServiceModelTitle: "lmstudio/Qwen/Qwen3-Coder-30B",
	}
	for key, wantModel := range want {
		if got := result.Config.LLM.ServiceModels[key]; got != wantModel {
			t.Errorf("service_models[%s] = %q, want %q", key, got, wantModel)
		}
	}
	if len(result.Config.LLM.ServiceModels) != len(want) {
		t.Errorf("ServiceModels = %v, want exactly %v", result.Config.LLM.ServiceModels, want)
	}
}

// TestLoad_ServiceModels_MissingSectionIsNil pins the unchanged-behavior
// default: no section → nil map, zero warnings.
func TestLoad_ServiceModels_MissingSectionIsNil(t *testing.T) {
	result, err := LoadWithResult(writeTestConfig(t, securityGroupsTestBase))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if result.Config.LLM.ServiceModels != nil {
		t.Errorf("ServiceModels = %v, want nil when the section is absent", result.Config.LLM.ServiceModels)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("LoadErrors = %v, want none", result.LoadErrors)
	}
}

// TestLoad_ServiceModels_WarnsAndDropsInvalid runs the full pipeline on a
// mixed section: the invalid entries warn (through LoadErrors, the UI's
// configLoadErrors channel) and are gone from the loaded map.
func TestLoad_ServiceModels_WarnsAndDropsInvalid(t *testing.T) {
	content := securityGroupsTestBase + `
  service_models:
    judge: "anthropic/claude-3-haiku"
    title: "no-slash"
    reflector: "anthropic/"
    future_thing: "anthropic/claude-3-haiku"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	joined := strings.Join(result.LoadErrors, "\n")
	for _, want := range []string{
		`llm.service_models."title"`,
		`llm.service_models."reflector"`,
		`llm.service_models."future_thing"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("LoadErrors must warn about %s, got: %v", want, result.LoadErrors)
		}
	}
	if _, ok := result.Config.LLM.ServiceModels[ServiceModelTitle]; ok {
		t.Error("invalid title override survived the load, want dropped")
	}
	if _, ok := result.Config.LLM.ServiceModels[ServiceModelReflector]; ok {
		t.Error("invalid reflector override survived the load, want dropped")
	}
	if _, ok := result.Config.LLM.ServiceModels["future_thing"]; ok {
		t.Error("unknown future_thing key survived the load, want dropped")
	}
	if got := result.Config.LLM.ServiceModels[ServiceModelJudge]; got != "anthropic/claude-3-haiku" {
		t.Errorf("judge override = %q, want it preserved", got)
	}
}

// TestServiceModels_YAMLRoundTrip pins Save→Load fidelity: a valid section
// survives a save/reload cycle with identical entries and zero warnings.
func TestServiceModels_YAMLRoundTrip(t *testing.T) {
	content := securityGroupsTestBase + `
  service_models:
    judge: "anthropic/claude-3-haiku"
    commit_message: "lmstudio/deepseek-chat"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	savedPath := writeTestConfig(t, "")
	if err := Save(result.Config, savedPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	reloaded, err := LoadWithResult(savedPath)
	if err != nil {
		t.Fatalf("reload after Save failed: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("reload LoadErrors = %v, want none", reloaded.LoadErrors)
	}
	want := map[string]string{
		ServiceModelJudge:         "anthropic/claude-3-haiku",
		ServiceModelCommitMessage: "lmstudio/deepseek-chat",
	}
	if len(reloaded.Config.LLM.ServiceModels) != len(want) {
		t.Fatalf("reloaded ServiceModels = %v, want exactly %v", reloaded.Config.LLM.ServiceModels, want)
	}
	for key, wantModel := range want {
		if got := reloaded.Config.LLM.ServiceModels[key]; got != wantModel {
			t.Errorf("reloaded service_models[%s] = %q, want %q", key, got, wantModel)
		}
	}
}

// TestServiceModels_UnknownLegacyKeyVanishesOnSave pins the "rewritten in
// place" convention: an unknown key loaded from a hand-edited config warns,
// is dropped in memory, and the next save rewrites the section without it —
// the saved file reloads warning-free.
func TestServiceModels_UnknownLegacyKeyVanishesOnSave(t *testing.T) {
	content := securityGroupsTestBase + `
  service_models:
    judge: "anthropic/claude-3-haiku"
    deprecated_call: "anthropic/claude-3-haiku"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	joined := strings.Join(result.LoadErrors, "\n")
	if !strings.Contains(joined, `llm.service_models."deprecated_call"`) {
		t.Errorf("LoadErrors must warn about the unknown deprecated_call key, got: %v", result.LoadErrors)
	}

	savedPath := writeTestConfig(t, "")
	if err := Save(result.Config, savedPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	llmSection, _ := doc["llm"].(map[string]any)
	serviceModels, _ := llmSection["service_models"].(map[string]any)
	if serviceModels == nil {
		t.Fatalf("saved config lost llm.service_models entirely: %s", data)
	}
	if _, has := serviceModels["deprecated_call"]; has {
		t.Error("saved config still serializes the unknown deprecated_call key, want rewritten away")
	}
	if serviceModels["judge"] != "anthropic/claude-3-haiku" {
		t.Errorf("saved service_models.judge = %v, want it preserved", serviceModels["judge"])
	}

	// The rewritten file reloads warning-free.
	reloaded, err := LoadWithResult(savedPath)
	if err != nil {
		t.Fatalf("reload after Save failed: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("reload LoadErrors = %v, want none", reloaded.LoadErrors)
	}
}

// TestCleanServiceModels pins the RPC-side cleaner (UpdateLLMConfig): the
// same format rule as the load pipeline, fail-soft drops with the same
// warning texts in deterministic (sorted-key) order, and nil-out for
// empty/fully-invalid input — a cleared or fully-invalid section vanishes at
// the next save instead of persisting garbage.
func TestCleanServiceModels(t *testing.T) {
	cleaned, warnings := CleanServiceModels(map[string]string{
		ServiceModelJudge:               "anthropic/claude-3-haiku",
		"future_thing":                  "anthropic/claude-3-haiku",
		ServiceModelTitle:               "no-slash",
		ServiceModelCompactionSummarize: "",
	})
	if len(cleaned) != 1 || cleaned[ServiceModelJudge] != "anthropic/claude-3-haiku" {
		t.Errorf("cleaned = %v, want only the valid judge entry", cleaned)
	}
	// Sorted-key order: compaction_summarize < future_thing < judge < title,
	// so the dropped keys warn as compaction_summarize, future_thing, title.
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		`llm.service_models."compaction_summarize"`,
		`llm.service_models."future_thing"`,
		`llm.service_models."title"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %q, got: %v", want, warnings)
		}
	}
	if idx := strings.Index(joined, "compaction_summarize"); idx == -1 || idx > strings.Index(joined, "future_thing") || strings.Index(joined, "future_thing") > strings.Index(joined, `"title"`) {
		t.Errorf("warnings are not in sorted-key order: %v", warnings)
	}

	// Nil/empty input cleans to nil with no warnings (the no-pin default).
	if c, w := CleanServiceModels(nil); c != nil || len(w) != 0 {
		t.Errorf("CleanServiceModels(nil) = %v, %v, want nil, none", c, w)
	}
	if c, w := CleanServiceModels(map[string]string{}); c != nil || len(w) != 0 {
		t.Errorf("CleanServiceModels({}) = %v, %v, want nil, none", c, w)
	}

	// Fully-invalid input cleans to nil (not an empty map) with warnings.
	c, w := CleanServiceModels(map[string]string{ServiceModelRouter: "no-slash"})
	if c != nil {
		t.Errorf("fully-invalid input cleaned to %v, want nil", c)
	}
	if len(w) != 1 {
		t.Errorf("fully-invalid input warnings = %v, want exactly one", w)
	}
}
