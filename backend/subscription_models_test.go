package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/credentials"
	"github.com/v0lka/sp4rk/llm"
)

func TestSubscriptionCatalogContainsAllSupportedChatGPTModels(t *testing.T) {
	manager := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) {
		return config.SubscriptionProvidersConfig{}, true
	})
	catalog := manager.catalog("chatgpt")
	if len(catalog) != 16 {
		t.Fatalf("ChatGPT subscription catalogue has %d models, want 16: %v", len(catalog), catalog)
	}
	for _, model := range []string{"gpt-5.1-codex", "gpt-5.3-codex-spark", "gpt-5.4-pro", "gpt-5.5-pro", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra"} {
		found := false
		for _, candidate := range catalog {
			if candidate == model {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ChatGPT subscription catalogue misses %q", model)
		}
	}
}

func TestSubscriptionCatalogModelsHaveBuiltInRegistryMetadata(t *testing.T) {
	for _, model := range chatGPTCodexModels {
		metadata, ok := llm.ResolveBuiltInModel(model)
		if !ok {
			t.Errorf("ChatGPT subscription model %q is missing from the sp4rk built-in registry", model)
			continue
		}
		if metadata.ContextWindow == 0 || metadata.OutputLimit == 0 || metadata.Capabilities == nil {
			t.Errorf("ChatGPT subscription model %q has incomplete registry metadata: %+v", model, metadata)
		}
	}
}

func TestSubscriptionLegacyModelSelectionExpandsToFullCatalog(t *testing.T) {
	manager := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) {
		return config.SubscriptionProvidersConfig{ChatGPT: config.SubscriptionProviderConfig{EnabledModels: append([]string(nil), legacyChatGPTCodexModels...)}}, true
	})
	manager.store = credentials.NewWithKeyStore(t.TempDir(), &testKeyStore{})
	if err := manager.store.Save("chatgpt", subscriptionAccount, credentials.Credential{AccessToken: "test-access"}); err != nil {
		t.Fatal(err)
	}
	if got := manager.models("chatgpt"); len(got) != len(chatGPTCodexModels) {
		t.Fatalf("legacy selection produced %d models, want %d: %v", len(got), len(chatGPTCodexModels), got)
	}
}

func TestConnectedSubscriptionModelsAreSelectableAndActivateRouter(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	manager := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) {
		return config.SubscriptionProvidersConfig{}, true
	})
	manager.profile = func() config.SubscriptionProviderConfig {
		return config.SubscriptionProviderConfig{
			Enabled: true,
			BaseURL: "https://example.invalid/codex",
			Models:  []string{"gpt-5.4", "gpt-5.4-mini"},
		}
	}
	manager.store = credentials.NewWithKeyStore(t.TempDir(), &testKeyStore{})
	f.subscriptions = manager

	if got := f.collectAllModels(nil); len(got) != 1 || got[0].Provider != "anthropic" {
		t.Fatalf("models before subscription credential = %+v, want only configured models", got)
	}
	if err := manager.store.Save("chatgpt", subscriptionAccount, credentials.Credential{AccessToken: "test-access"}); err != nil {
		t.Fatal(err)
	}

	models := f.collectAllModels(nil)
	if len(models) != 3 {
		t.Fatalf("models after subscription credential = %+v, want configured plus two subscription models", models)
	}
	for _, model := range models[1:] {
		if model.Provider != "chatgpt_subscription" {
			t.Errorf("subscription model provider = %q, want chatgpt_subscription", model.Provider)
		}
	}

	if err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "chatgpt_subscription/gpt-5.4"}); err != nil {
		t.Fatalf("UpdateLLMConfig(subscription default) error = %v", err)
	}
	if got := f.config.LLM.DefaultModel; got != "chatgpt_subscription/gpt-5.4" {
		t.Errorf("default model = %q, want subscription composite id", got)
	}
	mock.mu.Lock()
	if mock.rebuildRouterCalls != 1 {
		t.Errorf("router rebuild calls = %d, want 1", mock.rebuildRouterCalls)
	}
	mock.mu.Unlock()

	if err := f.LogoutSubscription("chatgpt"); err != nil {
		t.Fatalf("LogoutSubscription error = %v", err)
	}
	if got := f.config.LLM.DefaultModel; got != "" {
		t.Errorf("default model after logout = %q, want cleared managed default", got)
	}
	for _, model := range f.collectAllModels(nil) {
		if model.Provider == "chatgpt_subscription" {
			t.Fatalf("subscription model %q remained visible after logout", model.Name)
		}
	}
}

func TestSubscriptionModelSelectionFiltersModelsAndProtectsDefault(t *testing.T) {
	f, _, _ := newTestAPI(t)
	manager := newSubscriptionManager(t.TempDir(), func() (config.SubscriptionProvidersConfig, bool) {
		return config.SubscriptionProvidersConfig{}, true
	})
	manager.profile = func() config.SubscriptionProviderConfig {
		return config.SubscriptionProviderConfig{
			Enabled: true,
			BaseURL: "https://example.invalid/codex",
			Models:  []string{"gpt-5.4", "gpt-5.4-mini"},
		}
	}
	manager.store = credentials.NewWithKeyStore(t.TempDir(), &testKeyStore{})
	if err := manager.store.Save("chatgpt", subscriptionAccount, credentials.Credential{AccessToken: "test-access"}); err != nil {
		t.Fatal(err)
	}
	f.subscriptions = manager

	if err := f.UpdateLLMConfig(LLMFullConfigRequest{SubscriptionModels: map[string][]string{"chatgpt_subscription": {"gpt-5.4-mini"}}}); err != nil {
		t.Fatalf("UpdateLLMConfig(subscription models) error = %v", err)
	}
	models := f.collectAllModels(nil)
	if got := models[len(models)-1]; got.Provider != "chatgpt_subscription" || got.Name != "gpt-5.4-mini" {
		t.Errorf("selected subscription model = %+v, want chatgpt_subscription/gpt-5.4-mini", got)
	}
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{DefaultModel: "chatgpt_subscription/gpt-5.4"}); err == nil {
		t.Error("expected disabled subscription model to be rejected as default")
	}
	if err := f.UpdateLLMConfig(LLMFullConfigRequest{SubscriptionModels: map[string][]string{"chatgpt_subscription": {"unrecognized"}}}); err == nil {
		t.Error("expected unknown subscription model to be rejected")
	}
}
