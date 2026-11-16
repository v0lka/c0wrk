package core

import (
	"encoding/json"
	"strings"

	"github.com/v0lka/sp4rk/llm"
)

// metadataDetector parses one raw /models listing entry into partial
// ModelMetadata; ok=false when the entry's shape is not the detector's.
type metadataDetector func(entry []byte) (llm.ModelMetadata, bool)

// modelMetadataDetectors is the first-wins detector chain over /models
// listing entries, ordered most-specific shape first; contract:
// specs/domains/llm-providers.md ("Lazy local-model probe").
func modelMetadataDetectors() []metadataDetector {
	return []metadataDetector{
		detectLMStudioMetadata,
		detectAggregatorMetadata,
		detectOpenAIStandardMetadata,
	}
}

// detectModelMetadata runs the chain on one entry: the first matching
// detector's partial metadata wins; unset fields inherit from the registry's
// lower tiers.
func detectModelMetadata(entry []byte) (llm.ModelMetadata, bool) {
	for _, detect := range modelMetadataDetectors() {
		if meta, ok := detect(entry); ok {
			return meta, true
		}
	}
	return llm.ModelMetadata{}, false
}

// detectLMStudioMetadata parses the native LM Studio REST API entry: runtime
// window when loaded (a non-positive loaded_context_length counts as not
// loaded — the advertised capacity still applies), advertised capacity
// otherwise; capacity is claimed only without max_model_len (the
// OpenAI-standard detector owns its priority); no capabilities.
// Field-level contract: specs/domains/llm-providers.md.
func detectLMStudioMetadata(entry []byte) (llm.ModelMetadata, bool) {
	var e struct {
		MaxModelLen         int `json:"max_model_len"`
		MaxContextLength    int `json:"max_context_length"`
		LoadedContextLength int `json:"loaded_context_length"`
		LoadedInstances     []struct {
			Config struct {
				ContextLength int `json:"context_length"`
			} `json:"config"`
		} `json:"loaded_instances"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return llm.ModelMetadata{}, false
	}
	// A non-positive loaded_context_length is an unset runtime window, not a
	// declaration: reset it so the advertised-capacity branch below still
	// applies instead of a negative field poisoning the entry into declining.
	window := max(e.LoadedContextLength, 0)
	for _, li := range e.LoadedInstances {
		if li.Config.ContextLength > 0 {
			window = li.Config.ContextLength
			break
		}
	}
	if window == 0 && e.MaxModelLen == 0 {
		window = e.MaxContextLength
	}
	if window <= 0 {
		return llm.ModelMetadata{}, false
	}
	return llm.ModelMetadata{ContextWindow: window}, true
}

// detectAggregatorMetadata parses the aggregator-style listing entry
// (the gateway-shaped limit/capabilities/modalities form, matched
// structurally — no live aggregator's response shape has been validated
// against it yet); capabilities
// are emitted only on a FULL declaration (both blocks AND the tools/reasoning
// sub-objects AND the temperature key inside capabilities), a partial one
// inherits — the authoritative all-false must not come from a sparse block's
// omission or a missing temperature key.
// Semantics: specs/domains/llm-providers.md ("Lazy local-model probe").
func detectAggregatorMetadata(entry []byte) (llm.ModelMetadata, bool) {
	var e struct {
		Limit *struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		Capabilities *struct {
			Tools *struct {
				Supported bool `json:"supported"`
			} `json:"tools"`
			Reasoning *struct {
				Supported bool `json:"supported"`
			} `json:"reasoning"`
			Temperature json.RawMessage `json:"temperature"`
		} `json:"capabilities"`
		Modalities *struct {
			Input []string `json:"input"`
		} `json:"modalities"`
	}
	if err := json.Unmarshal(entry, &e); err != nil || e.Limit == nil || e.Limit.Context <= 0 {
		return llm.ModelMetadata{}, false
	}
	// A declared output above the window drives EffectiveMax negative and
	// silently disables compaction, so contradictory listing data
	// (output > context) is clamped to the window at the source; a
	// non-positive output is undeclared — normalized to 0 here so the
	// wire contract is flatly "0 == undeclared" and no consumer ever has
	// to re-guard a negative (the write loop's heuristic applies when 0).
	meta := llm.ModelMetadata{
		ContextWindow: e.Limit.Context,
		OutputLimit:   max(min(e.Limit.Output, e.Limit.Context), 0),
	}
	if e.Capabilities != nil && e.Capabilities.Tools != nil && e.Capabilities.Reasoning != nil &&
		e.Modalities != nil {
		// The temperature flag is presence-checked, not defaulted: the full
		// declaration requires the key itself. RawMessage stays nil when the
		// key is omitted, so the omission cannot masquerade as an
		// authoritative "temperature unsupported" — the same omission→false
		// hazard the tools/reasoning sub-objects are guarded against. A
		// non-bool value counts as undeclared (inherit).
		tempDeclared, tempSupports := false, false
		if raw := e.Capabilities.Temperature; len(raw) > 0 && string(raw) != "null" {
			if json.Unmarshal(raw, &tempSupports) == nil {
				tempDeclared = true
			}
		}
		if tempDeclared {
			meta.Capabilities = &llm.ModelCapabilities{
				Attachment:  aggregatorDeclaresAttachment(e.Modalities.Input),
				Reasoning:   e.Capabilities.Reasoning.Supported,
				Temperature: tempSupports,
				ToolCall:    e.Capabilities.Tools.Supported,
			}
		}
	}
	return meta, true
}

// aggregatorDeclaresAttachment reports image/PDF input modalities — the
// Attachment flag's contract on llm.ModelCapabilities.
func aggregatorDeclaresAttachment(input []string) bool {
	for _, m := range input {
		if m == "application/pdf" || strings.HasPrefix(m, "image/") {
			return true
		}
	}
	return false
}

// detectOpenAIStandardMetadata parses the standard OpenAI listing entry as
// extended by self-hosted servers: the first non-zero of max_model_len /
// max_context_length / context_length. Capabilities stay unset.
func detectOpenAIStandardMetadata(entry []byte) (llm.ModelMetadata, bool) {
	var e struct {
		MaxModelLen   int `json:"max_model_len"`
		MaxContextLen int `json:"max_context_length"`
		ContextLen    int `json:"context_length"`
	}
	if err := json.Unmarshal(entry, &e); err != nil {
		return llm.ModelMetadata{}, false
	}
	window := e.MaxModelLen
	if window == 0 {
		window = e.MaxContextLen
	}
	if window == 0 {
		window = e.ContextLen
	}
	if window <= 0 {
		return llm.ModelMetadata{}, false
	}
	return llm.ModelMetadata{ContextWindow: window}, true
}
