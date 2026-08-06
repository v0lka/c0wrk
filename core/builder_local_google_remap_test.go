package core

import (
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// noopExpand is an env-var expander that returns its input unchanged.
func noopExpand(s string) string { return s }

// TestRemapLocalGoogleProtocols verifies the auto-remap of the Google protocol
// to chat_completions for Gemma/Gemini checkpoints served by a local
// OpenAI-compatible server (LM Studio/vLLM/Ollama), which do NOT expose
// Google's :generateContent endpoint. See the acceptance matrix in the task.
func TestRemapLocalGoogleProtocols(t *testing.T) {
	t.Run("local gemma model remapped to chat_completions", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"lmstudio": {
				ProviderType: "openai",
				BaseURL:      "http://127.0.0.1:1234/v1",
				Models:       []string{"gemma-3-27b-it"},
			},
		}
		overrides := map[string]llm.ModelMetadata{}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		got, ok := overrides["gemma-3-27b-it"]
		if !ok {
			t.Fatalf("expected override for gemma-3-27b-it, got none")
		}
		if got.Protocol != llm.ProtocolChatCompletions {
			t.Fatalf("local gemma protocol = %q, want %q (chat_completions)",
				got.Protocol, llm.ProtocolChatCompletions)
		}
		// The override MUST be protocol-only: ContextWindow/OutputLimit/
		// TokenizerType left at zero so ModelRegistry.enrichPartialOverride can
		// inherit the model's REAL window from the lazy probe (cache tier). A
		// wholesale override carrying the fallback window (128000 for a catalog
		// miss) would shadow the probe forever — see the function doc comment.
		if got.ContextWindow != 0 {
			t.Errorf("override ContextWindow = %d, want 0 (protocol-only; "+
				"window must be inherited, not pinned)", got.ContextWindow)
		}
		if got.OutputLimit != 0 {
			t.Errorf("override OutputLimit = %d, want 0 (protocol-only)", got.OutputLimit)
		}
		if got.TokenizerType != "" {
			t.Errorf("override TokenizerType = %q, want empty (protocol-only)",
				got.TokenizerType)
		}
	})

	t.Run("LAN gemma model remapped to chat_completions", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"homelab": {
				ProviderType: "openai",
				BaseURL:      "http://192.168.1.50:8080/v1",
				Models:       []string{"gemma-2-27b"},
			},
		}
		overrides := map[string]llm.ModelMetadata{}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		got, ok := overrides["gemma-2-27b"]
		if !ok {
			t.Fatalf("expected override for gemma-2-27b, got none")
		}
		if got.Protocol != llm.ProtocolChatCompletions {
			t.Fatalf("LAN gemma protocol = %q, want %q (chat_completions)",
				got.Protocol, llm.ProtocolChatCompletions)
		}
	})

	// (b) Variant B: a Google-named model served by a public-host
	// OpenAI-compatible provider (a self-hosted vLLM behind a domain/Tailscale)
	// IS now remapped — the previous isLocalBaseURL gate dropped it, leaving
	// the model silently broken (200 OK + empty body from :generateContent).
	// The remap is safe for a genuine cloud gateway too: such gateways serve
	// /v1/chat/completions, so steering onto it still works.
	t.Run("public-host vllm gemma model remapped to chat_completions", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"vllm-public": {
				ProviderType: "openai",
				BaseURL:      "https://infer.mycompany.io/v1",
				Models:       []string{"gemma-2-27b"},
			},
		}
		overrides := map[string]llm.ModelMetadata{}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		got, ok := overrides["gemma-2-27b"]
		if !ok {
			t.Fatalf("expected override for public-host gemma model, got none")
		}
		if got.Protocol != llm.ProtocolChatCompletions {
			t.Fatalf("public-host gemma protocol = %q, want %q (chat_completions)",
				got.Protocol, llm.ProtocolChatCompletions)
		}
	})

	// (c) GPT-5 (Responses) and Claude (Anthropic) on the same local provider
	// are NOT remapped — those endpoints ARE served by local servers.
	t.Run("local gpt-5 and claude keep their protocols", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"lmstudio": {
				ProviderType: "openai",
				BaseURL:      "http://127.0.0.1:1234/v1",
				Models:       []string{"gpt-5.6", "claude-sonnet-4-20250514"},
			},
		}
		overrides := map[string]llm.ModelMetadata{}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		if _, ok := overrides["gpt-5.6"]; ok {
			t.Errorf("gpt-5.6 must NOT be remapped (Responses is served); found override")
		}
		if _, ok := overrides["claude-sonnet-4-20250514"]; ok {
			t.Errorf("claude-sonnet-4 must NOT be remapped (Anthropic is served); found override")
		}

		// Sanity: their built-in protocols are Responses / Anthropic.
		gpt5, _ := llm.ResolveBuiltInModel("gpt-5.6")
		if gpt5.Protocol != llm.ProtocolResponses {
			t.Fatalf("built-in gpt-5.6 protocol = %q, want %q (responses)",
				gpt5.Protocol, llm.ProtocolResponses)
		}
		claude, _ := llm.ResolveBuiltInModel("claude-sonnet-4-20250514")
		if claude.Protocol != llm.ProtocolAnthropic {
			t.Fatalf("built-in claude protocol = %q, want %q (anthropic)",
				claude.Protocol, llm.ProtocolAnthropic)
		}
	})

	// (d) An explicit user protocol override (from cfg.LLM.Models, already
	// seeded into overrides with a non-empty protocol) is respected and never
	// clobbered, even for a local Google-named model.
	t.Run("explicit user protocol override is respected", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"lmstudio": {
				ProviderType: "openai",
				BaseURL:      "http://127.0.0.1:1234/v1",
				Models:       []string{"gemma-3-27b-it"},
			},
		}
		overrides := map[string]llm.ModelMetadata{
			"gemma-3-27b-it": {Protocol: llm.ProtocolGoogle},
		}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		got := overrides["gemma-3-27b-it"]
		if got.Protocol != llm.ProtocolGoogle {
			t.Fatalf("explicit user override clobbered: protocol = %q, want %q (google)",
				got.Protocol, llm.ProtocolGoogle)
		}
	})

	// Non-openai providers are skipped even when local.
	t.Run("non-openai local provider is skipped", func(t *testing.T) {
		cfg := map[string]BuilderProviderConfig{
			"anthropic-local": {
				ProviderType: "anthropic",
				BaseURL:      "http://127.0.0.1:9999",
				Models:       []string{"gemini-2.5-pro"},
			},
		}
		overrides := map[string]llm.ModelMetadata{}

		remapLocalGoogleProtocols(overrides, cfg, noopExpand)

		if _, ok := overrides["gemini-2.5-pro"]; ok {
			t.Fatalf("non-openai local provider must not be remapped; found override")
		}
	})
}
