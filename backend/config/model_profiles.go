package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Named ModelProfiles profiles: fixed presets over the 25 model-profile
// variant knobs (the five variants, never the master enabled toggle).
// Catalog semantics, the predefined/custom split, and per-entry values:
// specs/domains/model-profiles.md; value provenance per entry below.

// ModelProfileKind distinguishes the compiled-in predefined catalog from
// operator-authored custom profiles; metadata only (UI affordances,
// persistence routing), never value semantics —
// specs/domains/model-profiles.md.
type ModelProfileKind string

const (
	// ModelProfileKindPredefined marks the fixed catalog compiled into the binary.
	ModelProfileKindPredefined ModelProfileKind = "predefined"
	// ModelProfileKindCustom marks operator-authored profiles stored outside config.yaml.
	ModelProfileKindCustom ModelProfileKind = "custom"
)

// ModelProfilesGenericProfileID is the model-agnostic "generic" profile id —
// the default active profile and the soft fallback for a dangling
// active_profile (specs/domains/model-profiles.md).
const ModelProfilesGenericProfileID = "generic"

// ModelProfileConfig carries the 25 knob values of the five model-profile
// variants — ModelProfilesConfig minus the master Enabled toggle. Values are
// always concrete regardless of the variant gates (gate state controls
// application, not value presence): specs/domains/model-profiles.md.
type ModelProfileConfig struct {
	EssentialTools EssentialToolsConfig        `yaml:"essential_tools"`
	SystemPrompt   SystemPromptConfig          `yaml:"system_prompt"`
	Sampling       ModelProfilesSamplingConfig `yaml:"sampling"`
	LoopHardening  LoopHardeningConfig         `yaml:"loop_hardening"`
	Context        ModelProfilesContextConfig  `yaml:"context"`
}

// ModelProfile is one named, validated model-profile preset.
type ModelProfile struct {
	// ID is the stable slug identifying the profile (e.g. "qwen3.8-27b").
	ID string `yaml:"id"`
	// Name is the human-readable display name shown in the UI.
	Name string `yaml:"name"`
	// Kind distinguishes predefined catalog entries from custom ones.
	Kind ModelProfileKind `yaml:"kind"`
	// Config holds the 25 variant knob values.
	Config ModelProfileConfig `yaml:"config"`
}

// modelProfileIDPattern constrains profile ids to lowercase slugs built of
// [a-z0-9] segments joined by '.' or '-' (model-shaped ids such as
// "qwen3.8-27b", "gemma-4-26b-a4b-it", or the bare "generic").
var modelProfileIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

// validModelProfileReasoningEfforts is the allowed set for the sampling
// variant's reasoning_effort; empty inherits the model default (the "medium"
// fallback lives in the "generic" profile's values, not in ApplyDefaults) —
// specs/domains/model-profiles.md.
var validModelProfileReasoningEfforts = map[string]struct{}{
	"":       {},
	"off":    {},
	"low":    {},
	"medium": {},
}

// NewModelProfile is the single constructor-validator for ModelProfile (id
// slug, name, kind, all knob values via ValidateModelProfileConfig); it
// defensively clones always_present, and every catalog and custom-profile
// build goes through it — specs/domains/model-profiles.md.
func NewModelProfile(id, name string, kind ModelProfileKind, cfg ModelProfileConfig) (ModelProfile, error) {
	id = strings.TrimSpace(id)
	if !modelProfileIDPattern.MatchString(id) {
		return ModelProfile{}, fmt.Errorf("model profile id %q is invalid: must be a lowercase slug of [a-z0-9] segments joined by '.' or '-'", id)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ModelProfile{}, errors.New("model profile name must not be empty")
	}
	switch kind {
	case ModelProfileKindPredefined, ModelProfileKindCustom:
	default:
		return ModelProfile{}, fmt.Errorf("model profile kind %q is invalid (allowed: %q, %q)", kind, ModelProfileKindPredefined, ModelProfileKindCustom)
	}
	if err := ValidateModelProfileConfig(cfg); err != nil {
		return ModelProfile{}, fmt.Errorf("model profile %q: %w", id, err)
	}
	return ModelProfile{ID: id, Name: name, Kind: kind, Config: cloneModelProfileConfig(cfg)}, nil
}

// ValidateModelProfileConfig is the pure value validator for the 25 profile
// knobs. It runs at every write boundary regardless of the variant toggles,
// so a stored out-of-range value cannot go live the moment a gate flips on;
// sampling uses 0 as the "inherit the vendor preset" sentinel
// (specs/domains/model-profiles.md).
func ValidateModelProfileConfig(cfg ModelProfileConfig) error {
	// Essential tools: always_present may be empty — protected orchestration
	// tools (finish, fact memory, ask_user) and every MCP tool are always
	// kept implicitly; no tool-count budget exists (ADR-035).

	// Sampling.
	s := cfg.Sampling
	if s.Temperature < 0 {
		return fmt.Errorf("sampling.temperature must be > 0 when set, got %v (0 inherits the vendor preset)", s.Temperature)
	}
	if s.TopP < 0 || s.TopP > 1 {
		return fmt.Errorf("sampling.top_p must be in the range (0, 1] when set, got %v (0 inherits the vendor preset)", s.TopP)
	}
	if s.TopK < 0 {
		return fmt.Errorf("sampling.top_k must be >= 1 when set, got %d (0 inherits the vendor preset)", s.TopK)
	}
	if rp := s.RepetitionPenalty; rp != 0 && (rp < 1 || rp > 2) {
		return fmt.Errorf("sampling.repetition_penalty must be in the range [1, 2] when set, got %v (0 inherits the vendor preset)", rp)
	}
	// Qwen card: presence_penalty 0–2 is the sanctioned anti-repetition
	// lever (instruct default 1.5); values above 2 increase language mixing.
	if pp := s.PresencePenalty; pp != 0 && (pp < 0 || pp > 2) {
		return fmt.Errorf("sampling.presence_penalty must be in the range [0, 2] when set, got %v (0 inherits the vendor preset)", pp)
	}
	if _, ok := validModelProfileReasoningEfforts[s.ReasoningEffort]; !ok {
		return fmt.Errorf("sampling.reasoning_effort %q is invalid (allowed: off, low, medium; empty inherits the model default)", s.ReasoningEffort)
	}

	// Loop hardening.
	lh := cfg.LoopHardening
	thresholds := []int{
		lh.RepeatNudgeThreshold,
		lh.ParseErrorAbortThreshold,
		lh.FruitlessNudgeThreshold,
		lh.FruitlessAbortThreshold,
		lh.SameToolRepeatNudgeThreshold,
	}
	for _, threshold := range thresholds {
		if threshold < 0 {
			return errors.New("loop_hardening thresholds must be non-negative")
		}
	}
	if lh.Enabled {
		for _, threshold := range thresholds {
			if threshold < 1 {
				return errors.New("loop_hardening thresholds must be positive when loop_hardening is enabled")
			}
		}
	}

	// Context management.
	ctx := cfg.Context
	contextInts := []int{
		ctx.Compaction.KeepLast,
		ctx.Compaction.BlockSize,
		ctx.Compaction.TriggerPercent,
		ctx.ToolOutputKeepLastN,
		ctx.OutputTokenReserve,
	}
	for _, v := range contextInts {
		if v < 0 {
			return errors.New("context values must be non-negative")
		}
	}
	if ctx.Enabled {
		if ctx.Compaction.KeepLast < 2 {
			return fmt.Errorf("context.compaction.keep_last must be >= 2 when context is enabled, got %d", ctx.Compaction.KeepLast)
		}
		if ctx.Compaction.BlockSize < 2 {
			return fmt.Errorf("context.compaction.block_size must be >= 2 when context is enabled, got %d", ctx.Compaction.BlockSize)
		}
		if ctx.Compaction.TriggerPercent < 1 || ctx.Compaction.TriggerPercent >= 100 {
			return fmt.Errorf("context.compaction.trigger_percent must be in [1, 100) when context is enabled, got %d", ctx.Compaction.TriggerPercent)
		}
		if ctx.ToolOutputKeepLastN < 1 {
			return fmt.Errorf("context.tool_output_keep_last_n must be >= 1 when context is enabled, got %d", ctx.ToolOutputKeepLastN)
		}
		if ctx.OutputTokenReserve < 1 {
			return fmt.Errorf("context.output_token_reserve must be >= 1 when context is enabled, got %d", ctx.OutputTokenReserve)
		}
	}

	return nil
}

// ValidateModelProfilesUnique rejects a profile list that reuses an id or a
// display name — the predefined-catalog invariant, applied to the combined
// predefined ∪ custom namespace (specs/domains/model-profiles.md).
func ValidateModelProfilesUnique(profiles []ModelProfile) error {
	seenIDs := make(map[string]struct{}, len(profiles))
	seenNames := make(map[string]struct{}, len(profiles))
	for _, p := range profiles {
		if _, dup := seenIDs[p.ID]; dup {
			return fmt.Errorf("duplicate model profile id %q", p.ID)
		}
		seenIDs[p.ID] = struct{}{}
		if _, dup := seenNames[p.Name]; dup {
			return fmt.Errorf("duplicate model profile name %q", p.Name)
		}
		seenNames[p.Name] = struct{}{}
	}
	return nil
}

// predefinedModelProfiles is the fixed six-entry catalog, built through
// NewModelProfile + ValidateModelProfilesUnique at package init so an invalid
// entry panics at startup (programmer error) —
// specs/domains/model-profiles.md.
var predefinedModelProfiles = mustBuildPredefinedModelProfiles()

// PredefinedModelProfiles returns the hard-coded predefined catalog
// (specs/domains/model-profiles.md); the returned slice is a defensive copy.
func PredefinedModelProfiles() []ModelProfile {
	out := make([]ModelProfile, len(predefinedModelProfiles))
	for i, p := range predefinedModelProfiles {
		out[i] = cloneModelProfile(p)
	}
	return out
}

// FindPredefinedModelProfile resolves a predefined profile by id. The
// returned profile is a defensive copy.
func FindPredefinedModelProfile(id string) (ModelProfile, bool) {
	for _, p := range predefinedModelProfiles {
		if p.ID == id {
			return cloneModelProfile(p), true
		}
	}
	return ModelProfile{}, false
}

// legacyPredefinedModelProfileAliases maps predefined ids retired by a rename
// (the dashed pre-bonsai "bonsai-2-27b") to their replacements. It is
// consulted only AFTER a direct catalog lookup fails, so a custom profile
// owning the retired id keeps winning — specs/domains/model-profiles.md.
var legacyPredefinedModelProfileAliases = map[string]string{
	"bonsai-2-27b": "bonsai.2-27b",
}

// FindModelProfileResolvingLegacy resolves a stored profile id against a
// catalog, applying the legacy alias table only when no entry owns the id
// directly. resolvedID reports the id the lookup actually hit (the given id,
// or its replacement on an alias hit) so callers can surface the remap.
func FindModelProfileResolvingLegacy(catalog []ModelProfile, id string) (profile ModelProfile, resolvedID string, found bool) {
	if p, ok := FindModelProfile(catalog, id); ok {
		return p, id, true
	}
	replacement, aliased := legacyPredefinedModelProfileAliases[id]
	if !aliased {
		return ModelProfile{}, "", false
	}
	p, ok := FindModelProfile(catalog, replacement)
	return p, replacement, ok
}

func mustBuildPredefinedModelProfiles() []ModelProfile {
	profiles, err := buildPredefinedModelProfiles()
	if err != nil {
		panic(fmt.Sprintf("backend/config: invalid predefined ModelProfiles profile catalog: %v", err))
	}
	return profiles
}

func buildPredefinedModelProfiles() ([]ModelProfile, error) {
	// Shared value sets (docs/development/model-profiles-defaults-research.md):
	// loop-breaker shape 2/3/3/5/4 and the default always-present tool list.
	tools := func(enabled, compact bool) EssentialToolsConfig {
		return EssentialToolsConfig{
			Enabled:             enabled,
			AlwaysPresent:       slices.Clone(defaultModelProfilesAlwaysPresent),
			CompactDescriptions: compact,
		}
	}
	loopHardening := LoopHardeningConfig{
		Enabled:                      true,
		RepeatNudgeThreshold:         2,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      3,
		FruitlessAbortThreshold:      5,
		SameToolRepeatNudgeThreshold: 4,
	}
	// Tight context window for the weaker profiles (study: 6/5/80/2).
	contextTight := ModelProfilesContextConfig{
		Enabled:             true,
		Compaction:          ModelProfilesCompactionConfig{KeepLast: 6, BlockSize: 5, TriggerPercent: 80},
		ToolOutputKeepLastN: 2,
		OutputTokenReserve:  16384,
	}

	defs := []struct {
		id   string
		name string
		cfg  ModelProfileConfig
	}{
		{
			// Minimal: only loop hardening and the reasoning-effort trim pay.
			// Sampling inherits the sp4rk qwen preset (= the official
			// thinking recipe); context management stays off (262K window),
			// knobs seeded near-baseline for a later flip.
			id:   "qwen3.8-27b",
			name: "Qwen3.8-27B (minimal)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(false, false),
				Sampling: ModelProfilesSamplingConfig{
					Enabled:         true,
					ReasoningEffort: "medium", // native level vs the xhigh default: 60–90% fewer thinking tokens
				},
				LoopHardening: loopHardening,
				Context: ModelProfilesContextConfig{
					Compaction:          ModelProfilesCompactionConfig{KeepLast: 8, BlockSize: 5, TriggerPercent: 85},
					ToolOutputKeepLastN: 3,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			// Medium: a 3B-active MoE — tool narrowing, lite prompt, and
			// tight context all pay; sampling inherits the qwen preset plus
			// the card's sanctioned presence_penalty.
			id:   "qwen3.6-35b-a3b",
			name: "Qwen3.6-35B-A3B (medium)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(true, true),
				SystemPrompt:   SystemPromptConfig{Lite: true},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:         true,
					PresencePenalty: 1.5,      // general agent recipe (precise coding would want 0.6/0.0 instead)
					ReasoningEffort: "medium", // pre-3.8 binary mapping: thinking ON (no native level exists)
				},
				LoopHardening: loopHardening,
				Context:       contextTight,
			},
		},
		{
			// Maximal: the study's weakest agentic performer — every support
			// variant on. top_p/top_k seeded explicitly (the google preset
			// ships only temperature; Gemma's top_k standard is 64);
			// reasoning_effort unset (sp4rk has no google reasoning branch).
			id:   "gemma-4-26b-a4b-it",
			name: "Gemma-4-26B-A4B-it (maximal)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(true, true),
				SystemPrompt:   SystemPromptConfig{Lite: true},
				Sampling: ModelProfilesSamplingConfig{
					Enabled: true,
					TopP:    0.95,
					TopK:    64,
				},
				LoopHardening: loopHardening,
				Context:       contextTight,
			},
		},
		{
			// Moderate: handles the full prompt and tool schemas — tool
			// narrowing on (descriptions uncompacted), full system prompt,
			// near-baseline context. Same explicit Gemma sampling values as
			// its 26B sibling.
			id:   "gemma-4-31b-it",
			name: "Gemma-4-31B-it (moderate)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(true, false),
				Sampling: ModelProfilesSamplingConfig{
					Enabled: true,
					TopP:    0.95,
					TopK:    64,
				},
				LoopHardening: loopHardening,
				Context: ModelProfilesContextConfig{
					Enabled:             true,
					Compaction:          ModelProfilesCompactionConfig{KeepLast: 8, BlockSize: 5, TriggerPercent: 85},
					ToolOutputKeepLastN: 3,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			// Embedded: the backend-owned local model (Bonsai 2 27B). Knobs
			// target the measured prefill/token cost of local inference
			// (docs/development/embedded-llm-perf-diagnosis.md); the
			// load-bearing '.' in the id (custom-id collision avoidance, the
			// legacy dashed-id alias) is specified in
			// specs/domains/model-profiles.md.
			id:   "bonsai.2-27b",
			name: "Bonsai 2 27B (embedded)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(true, true),
				SystemPrompt:   SystemPromptConfig{Lite: true},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:         true,
					ReasoningEffort: "medium",
				},
				LoopHardening: loopHardening,
				Context:       contextTight,
			},
		},
		{
			// Generic: model-agnostic maximum support (research addendum);
			// the unmeasured prompt-content additions stay off. Sampling is
			// inherit-by-default (no seeded constant fits the whole class)
			// with reasoning_effort "medium" as the single safe seed.
			id:   ModelProfilesGenericProfileID,
			name: "Generic (model-agnostic)",
			cfg: ModelProfileConfig{
				EssentialTools: tools(true, true),
				SystemPrompt:   SystemPromptConfig{Lite: true},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:         true,
					ReasoningEffort: "medium",
				},
				LoopHardening: loopHardening,
				Context:       contextTight,
			},
		},
	}

	profiles := make([]ModelProfile, 0, len(defs))
	for _, def := range defs {
		p, err := NewModelProfile(def.id, def.name, ModelProfileKindPredefined, def.cfg)
		if err != nil {
			return nil, fmt.Errorf("predefined catalog entry %q: %w", def.id, err)
		}
		profiles = append(profiles, p)
	}
	if err := ValidateModelProfilesUnique(profiles); err != nil {
		return nil, fmt.Errorf("predefined catalog: %w", err)
	}
	return profiles, nil
}

func cloneModelProfile(p ModelProfile) ModelProfile {
	p.Config = cloneModelProfileConfig(p.Config)
	return p
}

func cloneModelProfileConfig(cfg ModelProfileConfig) ModelProfileConfig {
	cfg.EssentialTools.AlwaysPresent = slices.Clone(cfg.EssentialTools.AlwaysPresent)
	return cfg
}
