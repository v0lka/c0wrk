package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
)

// subscriptionAuthFixture builds a config whose chatgpt provider owns the
// default model, so ToBuilderConfig always produces a chatgpt entry.
func subscriptionAuthFixture() *config.Config {
	return &config.Config{LLM: config.LLMConfig{
		DefaultModel: "gpt-4o",
		ChatGPT: config.ChatGPTConfig{
			APIKey: "sk-static",
			Models: []string{"gpt-4o"},
		},
		Anthropic: config.AnthropicConfig{APIKey: "ak", Models: []string{"claude-sonnet-4-20250514"}},
	}}
}

// TestToBuilderConfig_APIKeyModeIsUnchanged is the AC: with the mode at its
// default (or explicitly api_key, or the empty bypassed-defaults reading) the
// chatgpt entry maps exactly as it did before the mode existed — no
// SubscriptionAuth marker, no BaseURL, the static key carried verbatim.
func TestToBuilderConfig_APIKeyModeIsUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		setMode bool
		mode    string
	}{
		{name: "bypassed-empty"},
		{name: "defaulted", setMode: true, mode: config.ChatGPTAuthModeAPIKey},
		{name: "explicit", setMode: true, mode: config.ChatGPTAuthModeAPIKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := subscriptionAuthFixture()
			if tc.setMode {
				cfg.LLM.ChatGPT.Auth.Mode = tc.mode
			}

			bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles())
			got := bc.LLM.ProviderConfigs["chatgpt"]
			if got.SubscriptionAuth {
				t.Error("SubscriptionAuth = true — api_key mode must never mark the entry")
			}
			if got.BaseURL != "" {
				t.Errorf("BaseURL = %q, want empty (the SDK default endpoint)", got.BaseURL)
			}
			if got.ProviderType != "openai" || got.APIKey != "sk-static" || len(got.Models) != 1 || got.Models[0] != "gpt-4o" {
				t.Errorf("entry = %+v, want the plain historical mapping", got)
			}
		})
	}
}

// TestToBuilderConfig_OAuthModeMarksAndPinsTheEntry verifies the oauth
// mapping: the chatgpt entry carries SubscriptionAuth and its BaseURL is
// pinned to the ChatGPT Codex backend (subscription credentials are accepted
// only there). The static api_key still travels on the entry — the token
// source overrides it on the wire, and a signed-out user gets an error
// rather than a silent key fallback.
func TestToBuilderConfig_OAuthModeMarksAndPinsTheEntry(t *testing.T) {
	cfg := subscriptionAuthFixture()
	cfg.LLM.ChatGPT.Auth.Mode = config.ChatGPTAuthModeOAuth

	bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles())

	got := bc.LLM.ProviderConfigs["chatgpt"]
	if !got.SubscriptionAuth {
		t.Error("SubscriptionAuth = false, want true in oauth mode")
	}
	if want := providerauth.ChatGPT().APIBaseURL; got.BaseURL != want {
		t.Errorf("BaseURL = %q, want the pinned Codex endpoint %q", got.BaseURL, want)
	}
	if got.APIKey != "sk-static" {
		t.Errorf("APIKey = %q, want it carried through untouched", got.APIKey)
	}

	// The pin and the marker are chatgpt-only: the sibling fixed provider
	// must map exactly as before.
	if sibling := bc.LLM.ProviderConfigs["anthropic"]; sibling.SubscriptionAuth {
		t.Error("anthropic SubscriptionAuth = true — the marker is chatgpt-only")
	}
}
