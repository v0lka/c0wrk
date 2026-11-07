package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
	"github.com/v0lka/sp4rk/llm"
)

func (f *FrontendAPI) kimiAuthDescriptor() providerAuthDescriptor {
	return providerAuthDescriptor{
		providerName:    "kimi",
		displayName:     "Kimi",
		notifySignedOut: true,
		profile: func() providerauth.Profile {
			f.configMu.RLock()
			defer f.configMu.RUnlock()
			return providerauth.KimiForRegion(f.config.LLM.Kimi.Auth.Region)
		},
		authMode: func() string {
			f.configMu.RLock()
			defer f.configMu.RUnlock()
			if f.config == nil {
				return config.KimiAuthModeAPIKey
			}
			return kimiAuthModeOrAPIKey(f.config.LLM.Kimi.Auth.Mode)
		},
		emit: func(event providerAuthEvent) {
			f.emitKimiAuthState(KimiAuthEventData{
				State: event.state, AuthURL: event.authURL, Error: event.err,
				Email: event.email, ExpiresAt: event.expiresAt,
			})
		},
	}
}

func kimiAuthModeOrAPIKey(mode string) string {
	if mode == "" {
		return config.KimiAuthModeAPIKey
	}
	return mode
}

func kimiAuthRegionOrGlobal(region string) string {
	if region == "" {
		return config.KimiRegionGlobal
	}
	return region
}

// rebindKimiAuthRegion forces reauthorization after a region switch.
func (f *FrontendAPI) rebindKimiAuthRegion(region string) error {
	state := &f.kimiAuth
	state.mu.Lock()
	if state.restoring {
		state.mu.Unlock()
		return errors.New("changing the Kimi region is unavailable while the account restore is still reading the OS keychain — try again in a moment")
	}
	if state.rebinding {
		state.mu.Unlock()
		return errors.New("changing the Kimi region is already in progress — try again in a moment")
	}
	state.rebinding = true
	m := state.manager
	if state.inFlight {
		state.cancelRequested = true
		state.cancel()
		done := state.runDone
		timeout := state.rebindJoinTimeoutOverride
		if timeout <= 0 {
			timeout = providerAuthSignInJoinTimeout
		}
		state.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(timeout):
				state.mu.Lock()
				state.rebinding = false
				state.mu.Unlock()
				return errors.New("changing the Kimi region is unavailable while sign-in cancellation is still concluding — try again in a moment")
			}
		}
	} else {
		state.mu.Unlock()
	}
	defer func() {
		state.mu.Lock()
		state.rebinding = false
		state.mu.Unlock()
	}()

	if m != nil {
		if err := m.SignOut(); err != nil {
			return fmt.Errorf("signing out of Kimi before the region change: %w", err)
		}
	}
	desc := f.kimiAuthDescriptor()
	desc.profile = func() providerauth.Profile { return providerauth.KimiForRegion(region) }
	newManager := state.newManagerFn
	if newManager == nil {
		newManager = func() (chatGPTAuthManager, error) {
			return providerauth.NewTokenManager(providerauth.ManagerConfig{
				Profile: desc.profile(), Store: providerauth.NewKeyringSecretStore(),
				HTTPClient: f.providerAuthOutboundHTTPClient(30 * time.Second), Logger: f.log(), AgentDir: f.agentDir,
			})
		}
	}
	m, err := constructProviderAuthManager(newManager)
	if err != nil {
		return fmt.Errorf("initializing Kimi auth for the selected region: %w", err)
	}
	baseURL := state.modelsBaseURLOverride
	if baseURL == "" {
		baseURL = desc.profile().APIBaseURL
	}
	state.mu.Lock()
	state.manager = m
	state.modelsBaseURL = baseURL
	state.initError = ""
	state.lastError = ""
	state.inFlight = false
	state.cancel = nil
	state.runDone = nil
	state.cancelRequested = false
	f.pushProviderManagerProxyClientLocked(state)
	state.mu.Unlock()
	f.syncProviderBuilderSeam(state, "kimi")
	desc.emit(providerAuthEvent{state: providerAuthEventCancelled})
	return nil
}

// InitKimiAuth restores Kimi credentials without network I/O.
func (l *FrontendAPILifecycle) InitKimiAuth() {
	l.f.initProviderAuth(&l.f.kimiAuth, l.f.kimiAuthDescriptor())
}

// StartKimiSignIn starts one cancellable browser OAuth flow.
func (f *FrontendAPI) StartKimiSignIn() (*KimiSignInResponse, error) {
	authURL, err := f.startProviderSignIn(&f.kimiAuth, f.kimiAuthDescriptor())
	if err != nil {
		return nil, err
	}
	return &KimiSignInResponse{AuthURL: authURL}, nil
}

// CancelKimiSignIn cancels the in-flight sign-in; it is idempotent.
func (f *FrontendAPI) CancelKimiSignIn() error {
	return f.cancelProviderSignIn(&f.kimiAuth)
}

// GetKimiAuthStatus returns secret-free account and flow state.
func (f *FrontendAPI) GetKimiAuthStatus() KimiAuthStatusResponse {
	st := f.providerAuthStatus(&f.kimiAuth, f.kimiAuthDescriptor())
	return KimiAuthStatusResponse{
		SignedIn: st.signedIn, Email: st.email, ExpiresAt: st.expiresAt,
		Mode: st.mode, InFlight: st.inFlight, LastError: st.lastError,
	}
}

// SignOutKimi clears credentials and withdraws only Kimi's token source.
func (f *FrontendAPI) SignOutKimi() error {
	return f.signOutProvider(&f.kimiAuth, f.kimiAuthDescriptor())
}

func (f *FrontendAPI) emitKimiAuthState(data KimiAuthEventData) {
	f.seedAcquire()
	if f.emitEvent != nil {
		f.emitEvent(EventKimiAuthState, data)
	}
}

// kimiModelPreset is the offline fallback when the live coding catalog
// cannot be read. Order matches the public Kimi Code model table.
var kimiModelPreset = []string{
	"k3.1-pro",
	"k3.1-pro-highspeed",
	"k3",
	"k3-256k",
	"kimi-for-coding",
	"kimi-for-coding-highspeed",
}

// GetKimiModelPreset returns the offline Kimi for Coding subscription preset.
// API-key mode lists Moonshot platform models through ListProviderModels.
// The signed-in checklist comes from FetchKimiModels.
func (f *FrontendAPI) GetKimiModelPreset() KimiModelPresetResponse {
	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]KimiModelPresetEntry, 0, len(kimiModelPreset))
	for _, name := range kimiModelPreset {
		metadata := resolveSubscriptionModelMetadata(name, reg)
		models = append(models, KimiModelPresetEntry{
			Name: name, ContextWindow: metadata.contextWindow,
			OutputLimit: metadata.outputLimit, Reasoning: metadata.reasoning,
		})
	}
	return KimiModelPresetResponse{Models: models}
}

type kimiModelsCatalog struct {
	Data []kimiCatalogModel `json:"data"`
}

type kimiCatalogModel struct {
	ID                string `json:"id"`
	ContextLength     int64  `json:"context_length"`
	SupportsReasoning bool   `json:"supports_reasoning"`
}

// FetchKimiModels returns the models this Kimi for Coding subscription
// currently serves. Server order is kept and redirects are not followed.
func (f *FrontendAPI) FetchKimiModels() (KimiModelPresetResponse, error) {
	body, err := f.fetchSubscriptionCatalog(&f.kimiAuth, f.kimiAuthDescriptor(), "", false)
	if err != nil {
		return KimiModelPresetResponse{}, err
	}
	var catalog kimiModelsCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return KimiModelPresetResponse{}, fmt.Errorf("fetching the Kimi model list: decoding the catalog: %w", err)
	}
	var reg *llm.ModelRegistry
	if b := f.builder(); b != nil {
		reg = b.ModelRegistry()
	}
	models := make([]KimiModelPresetEntry, 0, len(catalog.Data))
	seen := make(map[string]struct{}, len(catalog.Data))
	for _, cm := range catalog.Data {
		if cm.ID == "" {
			continue
		}
		if _, ok := seen[cm.ID]; ok {
			continue
		}
		seen[cm.ID] = struct{}{}
		models = append(models, kimiCatalogEntry(cm, reg))
	}
	return KimiModelPresetResponse{Models: models}, nil
}

func kimiCatalogEntry(cm kimiCatalogModel, reg *llm.ModelRegistry) KimiModelPresetEntry {
	metadata := resolveSubscriptionModelMetadata(cm.ID, reg)
	entry := KimiModelPresetEntry{
		Name: cm.ID, ContextWindow: metadata.contextWindow,
		OutputLimit: metadata.outputLimit, Reasoning: metadata.reasoning,
	}
	if entry.ContextWindow > 0 {
		return entry
	}
	if cm.ContextLength > 0 && cm.ContextLength <= config.MaxModelContextWindow {
		entry.ContextWindow = int(cm.ContextLength)
	}
	entry.Reasoning = cm.SupportsReasoning
	return entry
}
