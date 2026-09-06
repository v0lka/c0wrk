package backend

import (
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/credentials"
	"github.com/v0lka/sp4rk/llm"
)

// GetSubscriptionStatuses returns status-only provider information. It never
// returns access tokens, refresh tokens, accounts, or credential paths.
func (f *FrontendAPI) GetSubscriptionStatuses() []SubscriptionStatus {
	if f.subscriptions == nil {
		return []SubscriptionStatus{{Provider: "chatgpt", Message: "Unavailable"}, {Provider: "kimi", Message: "Unavailable"}}
	}
	return f.subscriptions.statuses()
}

// ConnectSubscription begins a user-initiated local-loopback PKCE sign-in.
// The caller opens the returned authorization URL in the system browser.
func (f *FrontendAPI) ConnectSubscription(provider string) (SubscriptionLoginResponse, error) {
	if f.subscriptions == nil {
		return SubscriptionLoginResponse{}, errors.New("subscription sign-in is unavailable")
	}
	result, err := f.subscriptions.begin(f.ctx(), provider)
	if err != nil {
		return SubscriptionLoginResponse{}, err
	}
	return result, nil
}

// CancelSubscriptionLogin shuts down a pending local callback listener.
func (f *FrontendAPI) CancelSubscriptionLogin(provider string) error {
	if f.subscriptions == nil {
		return errors.New("subscription sign-in is unavailable")
	}
	f.subscriptions.cancel(provider)
	return nil
}

// LogoutSubscription removes local encrypted credentials and rebuilds the LLM
// router so subsequent requests cannot use a cached managed-provider token.
func (f *FrontendAPI) LogoutSubscription(provider string) error {
	if provider != "chatgpt" {
		return errors.New("unknown subscription provider")
	}
	if f.subscriptions == nil {
		return errors.New("subscription sign-in is unavailable")
	}
	if err := f.subscriptions.store.Logout(provider, subscriptionAccount); err != nil && !errors.Is(err, credentials.ErrNotFound) {
		return errors.New("could not remove sign-in")
	}

	// A managed model cannot remain the default after its credential record is
	// removed. Clear it atomically with the config file so future settings
	// updates do not inherit an unresolved subscription-only model.
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}
	previous := f.config.LLM.DefaultModel
	cleared := false
	providerName, _, managed := llm.ParseCompositeModelID(previous)
	if managed && providerName == "chatgpt_subscription" {
		f.config.LLM.DefaultModel = ""
		cleared = true
		if err := config.Save(f.config, f.configPath); err != nil {
			f.config.LLM.DefaultModel = previous
			f.configMu.Unlock()
			return fmt.Errorf("failed to persist cleared subscription default: %w", err)
		}
	}
	f.configMu.Unlock()
	if cleared {
		f.emitConfigUpdated()
	}
	return f.rebuildSubscriptionRouter()
}

func (f *FrontendAPI) rebuildSubscriptionRouter() error {
	b := f.builder()
	if b == nil {
		return nil
	}
	f.configMu.RLock()
	if f.config == nil {
		f.configMu.RUnlock()
		return errors.New("config not initialized")
	}
	cfg := ToBuilderConfig(f.config)
	f.subscriptions.augment(cfg)
	f.configMu.RUnlock()
	if err := b.RebuildRouter(cfg); err != nil {
		return fmt.Errorf("failed to rebuild LLM router: %w", err)
	}
	return nil
}
