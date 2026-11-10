package config

import "github.com/v0lka/sp4rk/llm"

// kimiModelDefaults holds c0wrk-specific metadata for Kimi models that are not
// present (or are incorrectly specified) in the sp4rk built-in catalog.
// These values are applied as defaults: user overrides in config.yaml still win.
//
//   - k3.1-pro / k3.1-pro-highspeed: Kimi K3.1 Pro series, 1M context.
//   - kimi-for-coding: corrected to Kimi K2.8 Preview, 1M context (the sp4rk
//     built-in still describes the older K2.7 Code 256K variant).
var kimiModelDefaults = map[string]llm.ModelMetadata{
	"k3.1-pro": {
		ContextWindow: 1_000_000,
		OutputLimit:   131_072,
		TokenizerType: "approximate",
		Family:        string(llm.FamilyKimi),
		Protocol:      llm.ProtocolChatCompletions,
		Capabilities:  &llm.ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
	},
	"k3.1-pro-highspeed": {
		ContextWindow: 1_000_000,
		OutputLimit:   131_072,
		TokenizerType: "approximate",
		Family:        string(llm.FamilyKimi),
		Protocol:      llm.ProtocolChatCompletions,
		Capabilities:  &llm.ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
	},
	"kimi-for-coding": {
		ContextWindow: 1_000_000,
		OutputLimit:   131_072,
		TokenizerType: "approximate",
		Family:        string(llm.FamilyKimi),
		Protocol:      llm.ProtocolChatCompletions,
		Capabilities:  &llm.ModelCapabilities{Attachment: true, Reasoning: true, ToolCall: true},
	},
}

// KimiModelDefaults returns a copy of the c0wrk-specific Kimi model metadata
// defaults. Callers should treat these as factory defaults that sit above the
// sp4rk built-in catalog but below user overrides.
func KimiModelDefaults() map[string]llm.ModelMetadata {
	out := make(map[string]llm.ModelMetadata, len(kimiModelDefaults))
	for k, v := range kimiModelDefaults {
		out[k] = v
	}
	return out
}

// KimiModelDefault returns the c0wrk-specific default metadata for a single
// model name, or the zero value and false if c0wrk has no default for it.
func KimiModelDefault(name string) (llm.ModelMetadata, bool) {
	m, ok := kimiModelDefaults[name]
	return m, ok
}
