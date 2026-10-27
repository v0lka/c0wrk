package backend

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/llmtls"
	"github.com/v0lka/c0wrk/core/modelprofiles"
	"github.com/v0lka/c0wrk/core/proxy"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

// maskedAPIKey is the placeholder returned for configured API keys in the UI.
const maskedAPIKey = "***configured***"

// GetConfig returns the current configuration (sanitized, no raw API keys).
func (f *FrontendAPI) GetConfig() ConfigResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return ConfigResponse{Loaded: false}
	}

	resp := ConfigResponse{
		Loaded:       true,
		LogLevel:     f.config.LogLevel,
		ConfigErrors: nonNilStringSlice(f.configLoadErrors),
		LLM:          f.buildLLMResponse(),
		Search: ConfigSearchResp{
			Provider: f.config.Search.Provider,
			APIKey:   maskAPIKey(f.config.Search.APIKey),
			BaseURL:  f.config.Search.BaseURL,
		},
		VectorIndex: VectorIndexSettingsResponse{
			ExecutionProvider: f.config.VectorIndex.ExecutionProvider,
			DeviceID:          f.config.VectorIndex.DeviceID,
		},
		Proxy: ProxySettingsResponse{
			Enabled:    f.config.Proxy.Enabled,
			URL:        proxy.MaskURL(f.config.Proxy.URL),
			BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
			TLSCertDir: f.config.Proxy.TLSCertDir,
		},
		Experimental: ExperimentalSettingsResponse{
			Enabled: f.config.Experimental.Enabled,
		},
		// The EFFECTIVE Model Profiles gate, precomputed by
		// refreshModelProfilesGateLocked — see the GUARANTEE on collectAllModels
		// and specs/contracts/desktop-frontend.md.
		ModelProfiles: f.modelProfilesGateResp,
		// The EFFECTIVE prompt-suggestions gate (ADR-088): nil config = on —
		// see specs/contracts/desktop-frontend.md.
		PromptSuggestions: PromptSuggestionsSettingsResponse{
			Enabled: f.config.PromptSuggestions.SuggestionsEnabled(),
		},
	}

	// The identity half (active/suggested) is filled FRESH — pure in-memory
	// reads, so a default-model change is visible without a Model Profiles
	// mutation; ResolvedProfileID stays cached (resolving it needs the
	// catalog on disk). See specs/contracts/desktop-frontend.md
	// (ModelProfilesSettingsResponse).
	resp.ModelProfiles.ActiveProfileID = f.config.ModelProfiles.ActiveProfile
	if id := suggestModelProfileID(f.config.LLM.DefaultModel); id != "" {
		resp.ModelProfiles.SuggestedProfileID = &id
	}

	// Populate AllModels from the per-provider config lists so configured
	// models appear before the async ModelRegistry initializes; network-free
	// metadata enrichment per specs/contracts/desktop-frontend.md.
	b := f.builder()
	var reg *llm.ModelRegistry
	if b != nil {
		reg = b.ModelRegistry()
	}
	resp.LLM.AllModels = f.collectAllModels(reg)
	resp.LLM.ModelsReady = reg != nil

	return resp
}

// HasDefaultModel reports whether a default LLM model is configured — the
// cheap single-fact probe for UI flows that must not pay for a full GetConfig
// response. See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) HasDefaultModel() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return false
	}

	return f.config.LLM.DefaultModel != ""
}

// experimentalFeaturesEnabled reports whether experimental features are on;
// fail-closed (false when the config is not yet initialized).
func (f *FrontendAPI) experimentalFeaturesEnabled() bool {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.config != nil && f.config.Experimental.Enabled
}

// buildLLMResponse constructs the sanitized ConfigLLMResponse from config.
func (f *FrontendAPI) buildLLMResponse() ConfigLLMResponse {
	resp := ConfigLLMResponse{
		DefaultModel: f.config.LLM.DefaultModel,
		// The clamping bound for the Settings form's auto-retry interval input,
		// published as the single source of truth (ADR-065,
		// specs/decisions/065-auto-resend-retryable-errors.md).
		AutoRetryMaxSeconds: config.MaxAutoRetrySeconds(),
		Anthropic: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.Anthropic.APIKey),
			Models: f.config.LLM.Anthropic.Models,
		},
		ChatGPT: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.ChatGPT.APIKey),
			Models: f.config.LLM.ChatGPT.Models,
			// The auth mode round-trips so the UI renders the active mode
			// (empty stored value reads as api_key); no secret travels —
			// OAuth tokens live in the OS keychain.
			// See specs/decisions/074-chatgpt-subscription-auth.md.
			AuthMode: chatGPTAuthModeOrAPIKey(f.config.LLM.ChatGPT.Auth.Mode),
		},
		OpenAICompatible:    make(map[string]ConfigProviderFull, len(f.config.LLM.OpenAICompatible)),
		AnthropicCompatible: make(map[string]ConfigProviderFull, len(f.config.LLM.AnthropicCompatible)),
	}
	for name, cfg := range f.config.LLM.OpenAICompatible {
		resp.OpenAICompatible[name] = ConfigProviderFull{
			APIKey:           maskAPIKey(cfg.APIKey),
			BaseURL:          cfg.BaseURL,
			Models:           cfg.Models,
			TLSFingerprint:   cfg.TLSFingerprint,
			AutoRetrySeconds: cfg.AutoRetrySeconds,
			TimeoutClass:     cfg.TimeoutClass,
		}
	}
	for name, cfg := range f.config.LLM.AnthropicCompatible {
		resp.AnthropicCompatible[name] = ConfigProviderFull{
			APIKey:           maskAPIKey(cfg.APIKey),
			BaseURL:          cfg.BaseURL,
			Models:           cfg.Models,
			TLSFingerprint:   cfg.TLSFingerprint,
			AutoRetrySeconds: cfg.AutoRetrySeconds,
			TimeoutClass:     cfg.TimeoutClass,
		}
	}
	return resp
}

// collectAllModels iterates all enabled provider models and builds ModelInfo
// entries (family/reasoning metadata from reg when non-nil), keyed by
// composite (provider, model) so two providers exposing the same bare name
// both appear.
//
// GUARANTEE: network-free — everything resolves from memory via
// ModelRegistry.ResolveLocal (specs/domains/llm-providers.md,
// specs/contracts/desktop-frontend.md); the Model Profiles gate is
// precomputed by refreshModelProfilesGateLocked.
func (f *FrontendAPI) collectAllModels(reg *llm.ModelRegistry) []ModelInfo {
	// GetAllProviderConfigs returns providers in deterministic order
	// (anthropic, chatgpt, then sorted openai_compatible, then sorted anthropic_compatible).
	providers := f.config.LLM.GetAllProviderConfigs()

	seen := make(map[string]bool) // dedupe by composite "provider/model"
	var result []ModelInfo
	for _, p := range providers {
		for _, modelName := range p.Models {
			compositeID := llm.CompositeModelID(p.Name, modelName)
			if seen[compositeID] {
				continue
			}
			seen[compositeID] = true

			var family string
			var vision bool
			if reg != nil {
				meta, _ := reg.ResolveLocal(modelName)
				family = meta.Family
				// ResolveLocal output is enriched (non-nil capabilities), but
				// guard anyway: a raw/partial record must not panic the listing.
				vision = meta.Capabilities != nil && meta.Capabilities.Attachment
			}

			info := ModelInfo{
				Name:     modelName,
				Provider: p.Name,
				Family:   family,
				Vision:   vision,
			}

			if family != "" {
				if opts, def, ok := llm.ModelReasoningOptions(family, modelName); ok {
					info.Reasoning = &ReasoningInfo{
						Options: opts,
						Default: def,
					}
				}
			}

			result = append(result, info)
		}
	}
	return result
}

// UpdateLLMConfig updates the full LLM configuration atomically:
// default model, each provider's models list, and API keys.
//
// Locking layout: saveMu serializes whole save sequences (mutation → persist
// → rebuild never interleave); configMu covers validation, the atomic
// commit/rollback and the default capture; provisioning and the judge/router
// rebuilds run outside it (rebuilds under a shared RLock on a fresh snapshot —
// readers never convoyed, concurrent writers never rolled back).
// See specs/domains/llm-providers.md (Config Save Discipline).
func (f *FrontendAPI) UpdateLLMConfig(req LLMFullConfigRequest) error {
	f.seedAcquire()
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}

	// Build the proposed LLM state separately. A rejected request must not
	// leak partial provider mutations to GetConfig, YAML, or the router.
	previous := f.config.LLM
	candidate := previous

	// An empty default_model means "leave it unchanged": debounced partial
	// updates use that sentinel while the initial setup has no default yet.
	if req.DefaultModel != "" {
		candidate.DefaultModel = req.DefaultModel
	}

	if req.Anthropic != nil {
		if req.Anthropic.Models != nil {
			candidate.Anthropic.Models = req.Anthropic.Models
		}
		if req.Anthropic.APIKey != "" && req.Anthropic.APIKey != maskedAPIKey {
			candidate.Anthropic.APIKey = req.Anthropic.APIKey
		}
	}
	if req.OpenAICompatible != nil {
		newMap := make(map[string]config.OpenAICompatibleConfig, len(req.OpenAICompatible))
		for name, ocReq := range req.OpenAICompatible {
			apiKey := ocReq.APIKey
			outputReserve := 0
			existing, exists := candidate.OpenAICompatible[name]
			if exists {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.OpenAICompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            ocReq.BaseURL,
				Models:             ocReq.Models,
				TLSFingerprint:     resolveTLSFingerprint(ocReq.TLSFingerprint, existing.TLSFingerprint, exists),
				OutputTokenReserve: outputReserve,
				AutoRetrySeconds:   resolveAutoRetrySeconds(ocReq.AutoRetrySeconds, existing.AutoRetrySeconds, exists),
				TimeoutClass:       resolveTimeoutClass(ocReq.TimeoutClass, existing.TimeoutClass, exists),
			}
		}
		candidate.OpenAICompatible = newMap
	}
	if req.AnthropicCompatible != nil {
		newMap := make(map[string]config.AnthropicCompatibleConfig, len(req.AnthropicCompatible))
		for name, acReq := range req.AnthropicCompatible {
			apiKey := acReq.APIKey
			outputReserve := 0
			existing, exists := candidate.AnthropicCompatible[name]
			if exists {
				if apiKey == maskedAPIKey || apiKey == "" {
					apiKey = existing.APIKey
				}
				outputReserve = existing.OutputTokenReserve
			}
			newMap[name] = config.AnthropicCompatibleConfig{
				APIKey:             apiKey,
				BaseURL:            acReq.BaseURL,
				Models:             acReq.Models,
				TLSFingerprint:     resolveTLSFingerprint(acReq.TLSFingerprint, existing.TLSFingerprint, exists),
				OutputTokenReserve: outputReserve,
				AutoRetrySeconds:   resolveAutoRetrySeconds(acReq.AutoRetrySeconds, existing.AutoRetrySeconds, exists),
				TimeoutClass:       resolveTimeoutClass(acReq.TimeoutClass, existing.TimeoutClass, exists),
			}
		}
		candidate.AnthropicCompatible = newMap
	}
	if req.ChatGPT != nil {
		if req.ChatGPT.Models != nil {
			candidate.ChatGPT.Models = req.ChatGPT.Models
		}
		if req.ChatGPT.APIKey != "" && req.ChatGPT.APIKey != maskedAPIKey {
			candidate.ChatGPT.APIKey = req.ChatGPT.APIKey
		}
		// An explicit two-state switch, not a nullable override: nil keeps the
		// persisted mode (debounced partial saves must not flip it); a
		// non-nil value must name a real mode — fail-closed, the same stance
		// the loader's validate() takes.
		// See specs/decisions/074-chatgpt-subscription-auth.md.
		if req.ChatGPT.AuthMode != nil {
			switch mode := *req.ChatGPT.AuthMode; mode {
			case config.ChatGPTAuthModeAPIKey, config.ChatGPTAuthModeOAuth:
				candidate.ChatGPT.Auth.Mode = mode
			default:
				f.configMu.Unlock()
				return fmt.Errorf("invalid LLM configuration: chatgpt auth_mode must be %q or %q, got %q",
					config.ChatGPTAuthModeAPIKey, config.ChatGPTAuthModeOAuth, mode)
			}
		}
	}

	// The `embedded` provider record is BACKEND-OWNED: a request carrying
	// openai_compatible REPLACES the whole map, so a draft that never saw the
	// generated entry would silently delete the local model — re-inject it
	// (or drop it after uninstall) after the candidate is built, before
	// validation (specs/domains/embedded-llm.md). contextWindow is 0: no
	// probe/network here, so any llm.models override stays as install wrote it.
	candidate.SyncEmbeddedProvider(f.config.EmbeddedLLM, 0)

	// A first-run config has no default until setup finishes; once one
	// exists, every candidate must still resolve. Validate before committing
	// so a dangling replacement cannot change any state.
	if candidate.DefaultModel != "" {
		if _, _, err := candidate.ResolveDefaultModelProvider(); err != nil {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: default_model would be unresolved: %w", err)
		}
	}

	// Reject out-of-range auto_retry_seconds (ADR-065) and invalid
	// timeout_class (ADR-071) before anything is committed — the same bounds
	// Load enforces; this RPC is a trust boundary for arbitrary callers, and
	// an invalid value would only fail at the next Load (whole-config rollback).
	for name, oc := range candidate.OpenAICompatible {
		if oc.AutoRetrySeconds < 0 || oc.AutoRetrySeconds > config.MaxAutoRetrySeconds() {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: openai_compatible %q auto_retry_seconds must be within [0, %d], got %d",
				name, config.MaxAutoRetrySeconds(), oc.AutoRetrySeconds)
		}
		// The shared validator applies the full timeout_class rule (enum plus
		// the reserved-name guard on `embedded`) — the one gate Load and this
		// boundary share, since Save does not run validate
		// (specs/decisions/071-adaptive-llm-request-budgets.md D3).
		if err := config.ValidateProviderTimeoutClass(name, oc.TimeoutClass); err != nil {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: %w", err)
		}
	}
	for name, ac := range candidate.AnthropicCompatible {
		if ac.AutoRetrySeconds < 0 || ac.AutoRetrySeconds > config.MaxAutoRetrySeconds() {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: anthropic_compatible %q auto_retry_seconds must be within [0, %d], got %d",
				name, config.MaxAutoRetrySeconds(), ac.AutoRetrySeconds)
		}
		if err := config.ValidateProviderTimeoutClass(name, ac.TimeoutClass); err != nil {
			f.configMu.Unlock()
			return fmt.Errorf("invalid LLM configuration: %w", err)
		}
	}

	f.config.LLM = candidate

	// Republish the auto-retry snapshot BEFORE the persist, under configMu:
	// the session-layer resolver reads it lock-free, and a failed persist
	// republishes the restored state on the same path.
	if f.app != nil {
		f.app.publishAutoRetryIntervals(f.config)
	}

	// Persist under configMu so a failed write restores the prior state before
	// any reader observes the candidate; the config-updated event waits for
	// success — a failed update must be indistinguishable from a rejection.
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.LLM = previous
		if f.app != nil {
			f.app.publishAutoRetryIntervals(f.config)
		}
		// Release configMu before returning: a failed persist must leave the
		// stack as usable as a rejected request — holding the lock would
		// deadlock every GetConfig/Settings RPC on the first persist failure.
		f.configMu.Unlock()
		return fmt.Errorf("failed to persist LLM config: %w", err)
	}

	// Capture the provisioning guard here — after the unlock, f.config must
	// only be touched under configMu again.
	defaultModel := f.config.LLM.DefaultModel
	f.configMu.Unlock()
	f.emitConfigUpdated()

	// --- Heavy work below runs OUTSIDE configMu (readers stay responsive) ---
	// saveMu is still held, so concurrent UpdateLLMConfig calls are serialized.

	// Clear any config load errors since settings are now valid
	f.configMu.Lock()
	f.configLoadErrors = nil
	f.configMu.Unlock()

	// Ensure No Project exists now that the app is usable (deferred during
	// startup to avoid provisioning before config validation). Guarded on a
	// non-empty default model so the debounced partial saves of initial setup
	// do not create and switch projects on every keystroke while the settings
	// dialog is open.
	if f.projectManager != nil && defaultModel != "" {
		created, err := f.projectManager.EnsureNoProject()
		if err != nil {
			f.log().Warn("failed to ensure No Project after config update", "error", err)
		}
		// Emit backend:ready only when No Project was just created: the
		// frontend auto-selects the first project then, landing first-run
		// users in CHAT mode without disrupting an active project.
		if err == nil && created {
			if projects, pErr := f.projectManager.ListProjects(); pErr == nil {
				f.emitEvent(EventBackendReady, projects)
			} else {
				f.log().Warn("failed to list projects after config update", "error", pErr)
			}
		}
	}

	// Rebuild judge and router on a FRESH snapshot; configMu.RLock spans
	// snapshot + rebuild so a concurrent config writer's changes are never
	// rolled back, while readers stay shared (no convoy) and the core calls
	// never re-enter FrontendAPI (no deadlock).
	if b := f.builder(); b != nil {
		// An auth-mode switch to oauth must sync the builder's
		// subscription-auth seam to the LIVE manager before the rebuild —
		// toBuilderConfigLocked carries no OAuth token source, so without
		// this the router would serve the signed-out stand-in.
		// See specs/domains/llm-providers.md (ChatGPT Subscription Auth).
		f.syncChatGPTBuilderSeam()
		f.configMu.RLock()
		fresh := f.toBuilderConfigLocked()
		b.RebuildJudge(fresh)
		rebuildErr := b.RebuildRouter(fresh)
		f.configMu.RUnlock()
		if rebuildErr != nil {
			f.log().Warn("failed to rebuild LLM router after config update", "error", rebuildErr)
		}
	}

	return nil
}

// UpdateSearchSettings updates search configuration.
func (f *FrontendAPI) UpdateSearchSettings(settings SearchSettingsRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Validate BEFORE any mutation: a rejected request leaves the in-memory
	// config and YAML untouched; the error wording mirrors load-time
	// validate() so the settings UI shows the same guidance as a bad YAML.
	provider := strings.ToLower(settings.Provider)
	baseURL := strings.TrimSpace(settings.BaseURL)
	if provider == "searxng" && baseURL == "" {
		// Sensible default: the local SearXNG instance (the upstream
		// docker-compose default), mirroring config.ApplyDefaults.
		baseURL = websearch.DefaultSearxngBaseURL
	}
	if err := config.ValidateSearchBaseURL(provider, baseURL); err != nil {
		return err
	}

	f.config.Search.Provider = settings.Provider
	f.config.Search.BaseURL = baseURL
	if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
		f.config.Search.APIKey = settings.APIKey
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist search settings", "error", err)
	}

	// Rebuild web search tool via the backend builder.
	if b := f.builder(); b != nil {
		if err := b.UpdateSearchTool(f.toBuilderConfigLocked()); err != nil {
			return err
		}
	}

	return nil
}

// UpdateVectorIndexSettings updates the vector-index embedding settings
// (execution provider + device id); deliberately NO hot application — the
// embedder is created once per process, so a change needs a restart.
// See specs/domains/workspace.md.
func (f *FrontendAPI) UpdateVectorIndexSettings(settings VectorIndexSettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Validate BEFORE any mutation: a rejected request leaves the in-memory
	// config and YAML untouched; error wording mirrors load-time validate().
	switch settings.ExecutionProvider {
	case config.VectorIndexProviderAuto, config.VectorIndexProviderCPU, config.VectorIndexProviderCUDA, config.VectorIndexProviderOpenVINO:
		// valid
	default:
		return fmt.Errorf(
			"vector_index.execution_provider %q is not valid; must be one of: %s, %s, %s, %s",
			settings.ExecutionProvider,
			config.VectorIndexProviderAuto, config.VectorIndexProviderCPU, config.VectorIndexProviderCUDA, config.VectorIndexProviderOpenVINO,
		)
	}
	if settings.DeviceID < 0 {
		return fmt.Errorf(
			"vector_index.device_id %d is not valid; must be >= 0",
			settings.DeviceID,
		)
	}

	f.config.VectorIndex.ExecutionProvider = settings.ExecutionProvider
	f.config.VectorIndex.DeviceID = settings.DeviceID

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist vector index settings", "error", err)
		return fmt.Errorf("failed to persist vector index settings: %w", err)
	}

	return nil
}

// proxyRebuildTimeout bounds the propagation phase of a proxy settings change
// (builder readiness + MCP gateway reconfigure) — the runMCPInit budget.
const proxyRebuildTimeout = 30 * time.Second

// UpdateProxySettings updates proxy configuration at runtime and propagates
// it to all subsystems (LLM providers, web tools, MCP, child processes).
//
// Locking contract (mirrors UpdateLLMConfig): saveMu serializes save
// sequences; configMu covers only the mutation and disk write, never the
// propagation — a queued writer already stops Go's RWMutex readers, so
// holding it across the gateway restart and rebuilds would freeze every
// GetConfig. The bounded local write stays inside.
// See specs/domains/llm-providers.md (Config Save Discipline).
func (f *FrontendAPI) UpdateProxySettings(settings ProxySettingsRequest) error {
	f.saveMu.Lock()
	defer f.saveMu.Unlock()

	f.configMu.Lock()
	if f.config == nil {
		f.configMu.Unlock()
		return errors.New("config not initialized")
	}

	f.config.Proxy.Enabled = settings.Enabled
	// Preserve the existing URL ONLY when the incoming value is the masked
	// form GetConfig returned (the frontend round-trips it verbatim when only
	// another field is edited — otherwise "***" would become the stored
	// password). The comparison is against the mask ALONE: MaskURL("") is "",
	// so an empty incoming value still clears the URL — swallowing a clear
	// would keep old credentials dialing while the UI reports no proxy.
	if settings.URL != proxy.MaskURL(f.config.Proxy.URL) {
		f.config.Proxy.URL = settings.URL
	}
	if settings.BypassList != nil {
		f.config.Proxy.BypassList = settings.BypassList
	}
	f.config.Proxy.TLSCertDir = settings.TLSCertDir

	// Persist (and announce) under configMu — a bounded local write; a
	// failure is warn-only and the change stays live in memory.
	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist proxy settings", "error", err)
	}

	// Snapshot what the rebuild needs while the lock is still held; after the
	// unlock f.config must only be touched under configMu again.
	b := f.builder()
	bcfg := f.toBuilderConfigLocked()
	f.configMu.Unlock()

	// --- Heavy work below runs OUTSIDE configMu (readers stay responsive);
	// saveMu still held, so a later save never propagates before an earlier
	// one. Bounded: RebuildProxy reconfigures the MCP gateway, which retries
	// previously-failed servers with no deadline of their own (the
	// runMCPInit budget); expiry aborts only the gateway step.
	var rebuildErr error
	if b != nil {
		rebuildCtx, cancel := context.WithTimeout(context.Background(), proxyRebuildTimeout)
		rebuildErr = b.RebuildProxy(rebuildCtx, bcfg)
		cancel()
		if rebuildErr != nil {
			f.log().Warn("failed to rebuild proxy after settings update", "error", rebuildErr)
		}
	}

	// The auth manager's token-endpoint client rides the same effective
	// proxy: re-point it even when the rebuild above failed — the change is
	// already live everywhere else, and leaving the manager on the old
	// transport would desynchronize it until the next successful update.
	// A nil manager (auth unavailable, restore in flight) is skipped.
	f.chatgptAuth.mu.Lock()
	f.pushChatGPTManagerProxyClientLocked()
	f.chatgptAuth.mu.Unlock()

	if rebuildErr != nil {
		return fmt.Errorf("proxy rebuild failed: %w", rebuildErr)
	}
	return nil
}

// runExperimentalChangedHook invokes the installed desktop hook (the
// FrontendAPIConfig seam, specs/decisions/101-browser-web-access.md),
// installed once inside Init's seed critical section so callers that passed
// seedAcquire read it unlocked. It runs SYNCHRONOUSLY on the caller's
// goroutine; lock order configMu → webAccessMu, never reversed.
func (f *FrontendAPI) runExperimentalChangedHook(enabled bool) {
	if hook := f.onExperimentalChanged; hook != nil {
		hook(enabled)
	}
}

// UpdateExperimentalFeatures toggles the master experimental-features switch:
// persist, rebuild the router (the E2S gate) and push the gate onto live
// orchestrators. The desktop hook fires only on a successful persist and
// stays INSIDE the configMu critical section, so no reader sees the gate
// flipped while the sidecar still serves the old state. Lock order configMu
// → webAccessMu, one-way — specs/decisions/101-browser-web-access.md.
func (f *FrontendAPI) UpdateExperimentalFeatures(enabled bool) error {
	f.seedAcquire()
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	prev := f.config.Experimental.Enabled
	f.config.Experimental.Enabled = enabled
	if err := f.persistConfig(); err != nil {
		// Roll back: an error return must not leave the in-memory gate
		// diverged from the persisted one (and the hook unrun).
		f.config.Experimental.Enabled = prev
		f.log().Warn("failed to persist experimental features toggle", "error", err)
		return fmt.Errorf("persisting experimental features toggle: %w", err)
	}

	builderCfg := f.toBuilderConfigLocked()
	if b := f.builder(); b != nil {
		if err := b.RebuildRouter(builderCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after experimental-features toggle", "error", err)
		}
	}

	// Push the refreshed E2S gate onto already-built session orchestrators —
	// otherwise the mode stays stale there until an app restart.
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			mgr.SetE2SSettings(builderCfg.E2S)
		}
	}

	f.runExperimentalChangedHook(enabled)

	// The gate flip moves the section's exposure-warning state; recompute the
	// config_errors contribution here, INLINE (this method holds configMu and
	// RefreshWebAccessLoadWarnings re-takes it — self-deadlock), mirroring
	// that method exactly: prefix-filter, then append the fresh slice.
	// See specs/decisions/102-web-access-tls.md (decision 8).
	fresh := config.WebAccessLoadWarnings(f.config.WebAccess, f.config.Experimental.Enabled)
	kept := make([]string, 0, len(f.configLoadErrors)+len(fresh))
	for _, w := range f.configLoadErrors {
		if strings.HasPrefix(w, webAccessLoadWarningPrefix) {
			continue
		}
		kept = append(kept, w)
	}
	kept = append(kept, fresh...)
	f.configLoadErrors = kept

	return nil
}

// GetSecuritySettings returns the group-based security settings for the UI:
// every configurable tool group (seven; the reserved "system" group is never
// included) with its policy and the execute command blocklist.
func (f *FrontendAPI) GetSecuritySettings() SecuritySettingsResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		// No config yet: render the canonical defaults (ApplyDefaults is
		// idempotent and pure) so the UI has a complete group set.
		var defaults config.Config
		config.ApplyDefaults(&defaults)
		return SecuritySettingsResponse{
			Groups:       groupPoliciesToResponse(defaults.Security.Groups),
			AutonomyMode: defaults.Security.AutonomyMode,
			SilentMode:   silentModeToResponse(defaults.Security.SilentMode),
		}
	}
	resp := SecuritySettingsResponse{
		Groups:                     groupPoliciesToResponse(f.config.Security.Groups),
		AutoApproveWorkspaceWrites: f.config.Security.AutoApproveWorkspaceWrites,
		AutonomyMode:               f.config.Security.AutonomyMode,
		SilentMode:                 silentModeToResponse(f.config.Security.SilentMode),
	}
	if b := f.builder(); b != nil {
		resp.JudgeAvailable = b.JudgeAvailable()
	}
	return resp
}

// groupPoliciesToResponse converts config group policies into the frontend
// shape, deep-copying blocklist slices so the caller cannot mutate the live
// config. The nil-vs-empty blocklist distinction round-trips (null vs []) so
// a UI save cannot change the user's editor state; both mean "no patterns".
func groupPoliciesToResponse(groups map[string]config.GroupPolicyConfig) map[string]GroupPolicyResponse {
	out := make(map[string]GroupPolicyResponse, len(groups))
	for name, g := range groups {
		entry := GroupPolicyResponse{Policy: g.Policy}
		if len(g.Blocklist) > 0 {
			entry.Blocklist = make([]string, len(g.Blocklist))
			copy(entry.Blocklist, g.Blocklist)
		} else if g.Blocklist != nil {
			entry.Blocklist = []string{}
		}
		out[name] = entry
	}
	return out
}

// silentModeToResponse converts the config silent-mode sub-policies into the
// frontend response shape. The mode strings are passed through verbatim.
func silentModeToResponse(sm config.SilentModeConfig) SilentModeResponse {
	return SilentModeResponse{
		ToolConfirm: SilentSubPolicyResponse{Mode: sm.ToolConfirm.Mode},
		UserConfirm: SilentSubPolicyResponse{Mode: sm.UserConfirm.Mode},
		StepLimit:   SilentSubPolicyResponse{Mode: sm.StepLimit.Mode},
		AskUser:     SilentSubPolicyResponse{Mode: sm.AskUser.Mode},
	}
}

// responseToSilentMode validates a frontend silent-mode payload: unset modes
// take their defaults, explicit values must be valid enums — mirroring
// config.validate so a UI-sourced value can never store what the loader would
// reject. See specs/decisions/072-silent-mode-user-confirm-policy.md.
func responseToSilentMode(r SilentModeResponse) (config.SilentModeConfig, error) {
	sm := config.SilentModeConfig{
		ToolConfirm: config.SilentSubPolicyConfig{Mode: r.ToolConfirm.Mode},
		UserConfirm: config.SilentSubPolicyConfig{Mode: r.UserConfirm.Mode},
		StepLimit:   config.SilentSubPolicyConfig{Mode: r.StepLimit.Mode},
		AskUser:     config.SilentSubPolicyConfig{Mode: r.AskUser.Mode},
	}
	config.ApplySilentModeDefaults(&sm)
	if err := config.ValidateSilentMode(sm); err != nil {
		return config.SilentModeConfig{}, err
	}
	return sm, nil
}

// responseToAutonomyMode validates against the security.autonomy_mode enum,
// mirroring config validation so a UI-sourced value can never store what the
// loader would reject. Empty means "keep the stored value" — transitional
// tolerance for frontends predating the enum.
func responseToAutonomyMode(mode string) (string, error) {
	switch mode {
	case "":
		return "", nil // keep stored — transitional tolerance for pre-enum payloads
	case config.AutonomyModeStandard, config.AutonomyModeAssisted, config.AutonomyModeSilent:
		return mode, nil
	default:
		return "", fmt.Errorf(
			"security.autonomy_mode has invalid value %q; must be one of: %s, %s, %s",
			mode, config.AutonomyModeStandard, config.AutonomyModeAssisted, config.AutonomyModeSilent,
		)
	}
}

// UpdateSecuritySettings updates security settings at runtime. The groups map
// REPLACES the stored one and must be the COMPLETE set of the seven
// configurable groups — a partial payload would silently weaken security
// (omitted groups fail safe to user_confirm; the reserved "system" group is
// rejected — specs/decisions/024-group-policies.md). Delivery is split — see
// specs/architecture/security-model.md: policies and blocklist reach live
// sessions immediately, the autonomy posture only the shared registry. A
// changed execute blocklist re-registers the shell tool first, atomically.
// See specs/decisions/024-group-policies.md (§8, wholesale groups replacement).
func (f *FrontendAPI) UpdateSecuritySettings(settings SecuritySettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	newGroups, err := responseToGroupPolicies(settings.Groups)
	if err != nil {
		return err
	}

	newSilent, err := responseToSilentMode(settings.SilentMode)
	if err != nil {
		return err
	}

	newAutonomyMode, err := responseToAutonomyMode(settings.AutonomyMode)
	if err != nil {
		return err
	}

	// Replace the full group set; prevSecurity snapshots the block so a
	// failed shell-tool re-registration below rolls the whole change back.
	prevSecurity := f.config.Security
	f.config.Security.Groups = newGroups
	f.config.Security.AutoApproveWorkspaceWrites = settings.AutoApproveWorkspaceWrites
	// An empty payload mode keeps the stored posture (transitional tolerance
	// for frontends predating the enum — see responseToAutonomyMode).
	if newAutonomyMode != "" {
		f.config.Security.AutonomyMode = newAutonomyMode
	}
	f.config.Security.SilentMode = newSilent

	// Apply policies to the shared tool registry via the backend builder.
	if b := f.builder(); b != nil {
		builderCfg := f.toBuilderConfigLocked()
		// Re-register the shell tool FIRST: the blocklist is compiled into the
		// tool instance, so runtime edits need re-registration. Atomic — on
		// error the config is restored with nothing half-applied; nil and an
		// emptied list compare equal, so toggling re-registers nothing.
		if !slices.Equal(
			prevSecurity.Groups[config.ToolGroupExecute].Blocklist,
			newGroups[config.ToolGroupExecute].Blocklist,
		) {
			if err := b.UpdateShellBlocklist(builderCfg); err != nil {
				f.config.Security = prevSecurity
				return fmt.Errorf("failed to apply execute blocklist: %w", err)
			}
		}
		b.UpdateSecurityPolicies(builderCfg)
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist security settings", "error", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Shell execution settings (the shell_exec launch-shape override)
// ---------------------------------------------------------------------------

// ShellExecToolSettings is one shell-exec tool's launch-shape override: the
// argv template (exactly one "{command}" element) and the declared shell.
type ShellExecToolSettings struct {
	Command []string `json:"command"`
	Shell   string   `json:"shell"`
}

// ShellExecSettingsResponse is the shell_exec section surface for the Settings
// UI: exactly one of BashExec/PoshExec is live per platform, both editable so
// the UI renders the full section.
type ShellExecSettingsResponse struct {
	BashExec ShellExecToolSettings `json:"bash_exec"`
	PoshExec ShellExecToolSettings `json:"posh_exec"`
}

// GetShellExecSettings returns the stored shell_exec launch-shape override;
// an uninitialized config renders the built-in defaults. Command slices are
// deep-copied. See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) GetShellExecSettings() ShellExecSettingsResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	section := config.ShellExecConfig{}
	if f.config != nil {
		section = f.config.ShellExec
	}
	return ShellExecSettingsResponse{
		BashExec: shellExecToolToResponse(section.BashExec),
		PoshExec: shellExecToolToResponse(section.PoshExec),
	}
}

func shellExecToolToResponse(tool config.ShellExecToolConfig) ShellExecToolSettings {
	command := make([]string, len(tool.Command))
	copy(command, tool.Command)
	return ShellExecToolSettings{Command: command, Shell: tool.Shell}
}

// responseToShellExecTool validates one tool's override (argv template shape
// + closed shell enum); an empty command means "no override" and is always
// valid. See specs/contracts/desktop-frontend.md.
func responseToShellExecTool(settings ShellExecToolSettings) (config.ShellExecToolConfig, error) {
	if len(settings.Command) == 0 {
		return config.ShellExecToolConfig{}, nil
	}
	command := make([]string, len(settings.Command))
	copy(command, settings.Command)
	if err := config.ValidateShellExecCommand(command); err != nil {
		return config.ShellExecToolConfig{}, err
	}
	if err := config.ValidateShellKind(settings.Shell); err != nil {
		return config.ShellExecToolConfig{}, err
	}
	return config.ShellExecToolConfig{Command: command, Shell: settings.Shell}, nil
}

// UpdateShellExecSettings replaces the shell_exec launch-shape override and
// applies it without a restart by re-registering the shell tool (the same
// atomic path as the blocklist update — failure rolls the section back). An
// invalid payload mutates nothing; a no-change payload re-registers nothing.
// See specs/contracts/desktop-frontend.md, specs/decisions/058-shell-execution-override.md.
func (f *FrontendAPI) UpdateShellExecSettings(settings ShellExecSettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	newBash, err := responseToShellExecTool(settings.BashExec)
	if err != nil {
		return fmt.Errorf("bash_exec: %w", err)
	}
	newPosh, err := responseToShellExecTool(settings.PoshExec)
	if err != nil {
		return fmt.Errorf("posh_exec: %w", err)
	}

	prev := f.config.ShellExec
	changed := !slices.Equal(prev.BashExec.Command, newBash.Command) ||
		prev.BashExec.Shell != newBash.Shell ||
		!slices.Equal(prev.PoshExec.Command, newPosh.Command) ||
		prev.PoshExec.Shell != newPosh.Shell
	if !changed {
		return nil
	}

	f.config.ShellExec = config.ShellExecConfig{BashExec: newBash, PoshExec: newPosh}

	if b := f.builder(); b != nil {
		builderCfg := f.toBuilderConfigLocked()
		if err := b.UpdateShellBlocklist(builderCfg); err != nil {
			f.config.ShellExec = prev
			return fmt.Errorf("failed to apply shell command override: %w", err)
		}
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist shell exec settings", "error", err)
	}

	return nil
}

// responseToGroupPolicies validates a frontend groups payload into config
// group policies, deep-copying blocklists; the rules mirror config.validate
// (fixed group set, policy enum, execute-only blocklists, pattern
// compilation) so a UI-sourced update can never store what the loader would
// reject. The payload must carry the COMPLETE set: it replaces the stored
// map wholesale, and a partial one would silently weaken security.
func responseToGroupPolicies(groups map[string]GroupPolicyResponse) (map[string]config.GroupPolicyConfig, error) {
	out := make(map[string]config.GroupPolicyConfig, len(groups))
	for name, g := range groups {
		if name == config.ToolGroupSystem {
			return nil, fmt.Errorf(
				"security group %q is reserved for system tools and cannot be configured",
				config.ToolGroupSystem,
			)
		}
		if !config.IsConfigurableToolGroup(name) {
			return nil, fmt.Errorf(
				"unknown security group %q; must be one of: %s",
				name, strings.Join(config.SortedToolGroupNames(), ", "),
			)
		}
		switch g.Policy {
		case config.GroupPolicyAllow, config.GroupPolicyUserConfirm, config.GroupPolicyDeny:
		default:
			return nil, fmt.Errorf(
				"security group %q has invalid policy %q; must be one of: %s, %s, %s",
				name, g.Policy, config.GroupPolicyAllow, config.GroupPolicyUserConfirm, config.GroupPolicyDeny,
			)
		}
		if name != config.ToolGroupExecute && len(g.Blocklist) > 0 {
			return nil, fmt.Errorf(
				"security group %q does not support a blocklist; only %q does",
				name, config.ToolGroupExecute,
			)
		}
		for _, pattern := range g.Blocklist {
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf(
					"security group %q blocklist pattern %q does not compile: %w",
					name, pattern, err,
				)
			}
		}
		entry := config.GroupPolicyConfig{Policy: g.Policy}
		// Preserve nil vs empty: the stored shape round-trips the user's
		// editor state (see groupPoliciesToResponse).
		if g.Blocklist != nil {
			entry.Blocklist = make([]string, len(g.Blocklist))
			copy(entry.Blocklist, g.Blocklist)
		}
		out[name] = entry
	}
	// Completeness: a partial payload is a fail-closed error, never a silent
	// weakening of omitted groups.
	if names := config.SortedToolGroupNames(); len(out) != len(names) {
		missing := make([]string, 0, len(names))
		for _, name := range names {
			if _, ok := out[name]; !ok {
				missing = append(missing, name)
			}
		}
		return nil, fmt.Errorf(
			"security groups payload must include all %d configurable groups; missing: %s",
			len(names), strings.Join(missing, ", "),
		)
	}
	return out, nil
}

// GetModelProfiles returns the model-profile catalog (predefined ∪ custom)
// for the settings picker: every profile's 25 knob values, the stored active
// id, the suggested id, the read-only picker universe from the live tool
// registry, and warnings (store, resolver, one-shot notices). Returned even
// before config is initialized. See specs/domains/model-profiles.md.
func (f *FrontendAPI) GetModelProfiles() ModelProfilesResponse {
	f.seedAcquire()
	// The picker universe comes from the live tool registry and the catalog
	// load below does I/O — both run BEFORE the config lock (like
	// modelProfilesGoalBlocked), which then guards only the field copies.
	builtinTools, toolGroups := f.modelProfilesPickerData()

	resp := ModelProfilesResponse{
		Profiles:       []ModelProfileDTO{},
		BuiltinTools:   builtinTools,
		ToolGroups:     toolGroups,
		ProtectedTools: nonNilStringSlice(modelprofiles.ProtectedToolNames()),
		Warnings:       []string{},
	}

	// Fresh store read so runtime-created/deleted custom profiles show up
	// without a restart; the catalog is independent of config.yaml.
	catalog, storeWarnings := config.LoadModelProfilesCatalog(f.agentDir)
	for _, w := range storeWarnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	resp.Warnings = append(resp.Warnings, storeWarnings...)
	for _, p := range catalog {
		resp.Profiles = append(resp.Profiles, modelProfileToDTO(p))
	}

	// Lock (not RLock): the one-shot notices are drained on read; all I/O
	// already ran above.
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return resp
	}

	resp.Enabled = f.config.ModelProfiles.Enabled

	// Stored view, verbatim: a dangling id stays visible while the resolver
	// warning explains the generic fallback.
	resp.ActiveID = f.config.ModelProfiles.ActiveProfile
	_, resolveWarnings := config.ResolveModelProfilesConfig(f.config.ModelProfiles, catalog)
	for _, w := range resolveWarnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	resp.Warnings = append(resp.Warnings, resolveWarnings...)

	// One-shot notices (e.g. delete of the active profile) appear exactly in
	// the next Get.
	resp.Warnings = append(resp.Warnings, f.modelProfilesNotices...)
	f.modelProfilesNotices = nil

	if id := suggestModelProfileID(f.config.LLM.DefaultModel); id != "" {
		resp.SuggestedProfileID = &id
	}
	return resp
}

// Trailing suffixes stripped (with "-") from BOTH the model name and the
// predefined slug before comparison — see specs/domains/model-profiles.md.
var modelProfilesSuggestSuffixTokens = []string{"instruct", "it", "latest", "free"}

// normalizeModelProfilesModelToken canonicalizes a model name or profile slug
// for the suggestion match: lowercase, last "/" segment, drop ":"-decoration,
// strip suffix tokens (see modelProfilesSuggestSuffixTokens), drop
// non-alphanumerics. Grammar specified in specs/domains/model-profiles.md.
func normalizeModelProfilesModelToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	for changed := true; changed; {
		changed = false
		for _, suffix := range modelProfilesSuggestSuffixTokens {
			if strings.HasSuffix(s, "-"+suffix) {
				s = strings.TrimSuffix(s, "-"+suffix)
				changed = true
			}
		}
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// modelProfilesSuggestAliasIDs maps a model whose shipping name shares no
// token with its architecture to the predefined profile slug that fits it:
// the embedded "Bonsai 2 27B" (a Qwen3.8-27B rebrand) → "bonsai.2-27b",
// keyed off config.EmbeddedLLMModelName so a rename moves the key; identity
// outranks containment. A HINT only — nothing is auto-applied.
// See specs/domains/model-profiles.md.
var modelProfilesSuggestAliasIDs = map[string]string{
	normalizeModelProfilesModelToken(config.EmbeddedLLMModelName): "bonsai.2-27b",
}

// suggestModelProfileID returns the predefined profile whose slug best
// matches the default model name (containment on normalized tokens, longest
// slug wins; identity aliases outrank; "generic" never suggested), or "" when
// nothing matches. Specified in specs/domains/model-profiles.md.
func suggestModelProfileID(defaultModel string) string {
	norm := normalizeModelProfilesModelToken(defaultModel)
	if norm == "" {
		return ""
	}
	if alias, ok := modelProfilesSuggestAliasIDs[norm]; ok {
		p, found := config.FindPredefinedModelProfile(alias)
		if !found || p.ID == config.ModelProfilesGenericProfileID {
			return ""
		}
		return p.ID
	}
	best := ""
	bestLen := 0
	for _, p := range config.PredefinedModelProfiles() {
		if p.ID == config.ModelProfilesGenericProfileID {
			continue
		}
		slug := normalizeModelProfilesModelToken(p.ID)
		if slug == "" {
			continue
		}
		if (norm == slug || strings.Contains(norm, slug)) && len(slug) > bestLen {
			best = p.ID
			bestLen = len(slug)
		}
	}
	return best
}

// modelProfilesCatalog loads the full profile catalog (predefined ∪ custom
// store under f.agentDir), logging store-level warnings. Reads are fresh so a
// runtime profile change applies without a restart; conversions are not hot.
func (f *FrontendAPI) modelProfilesCatalog() []config.ModelProfile {
	f.seedAcquire()
	return loadModelProfilesCatalog(f.agentDir, f.log())
}

// refreshModelProfilesGateLocked recomputes the cached effective gate served
// as ConfigResponse.model_profiles. Callers must hold configMu for WRITING;
// the catalog (a disk read) is resolved only here, never on the GetConfig
// read path (see the GUARANTEE on collectAllModels).
// See specs/domains/model-profiles.md.
func (f *FrontendAPI) refreshModelProfilesGateLocked() {
	if f.config == nil {
		f.modelProfilesGateResp = ModelProfilesSettingsResponse{}
		return
	}
	catalog := f.modelProfilesCatalog()
	profile, _ := effectiveModelProfilesConfig(f.config, catalog)
	f.modelProfilesGateResp = ModelProfilesSettingsResponse{
		Enabled:               profile.Enabled,
		EssentialToolsEnabled: profile.EssentialTools.Enabled,
		// The resolved id shares the cache's inputs exactly (the stored
		// active id + the catalog), so it cannot go stale without one of the
		// mutations that already trigger this refresh; it applies the same
		// soft fallback and retired-predefined-id aliasing as the value and
		// metrics-identity paths (activeModelProfile).
		ResolvedProfileID: activeModelProfile(f.config.ModelProfiles, catalog).ID,
	}
}

// modelProfilesGoalBlocked reports whether goal mode is refused because the
// essential-tools narrowing is active (resolved master toggle AND variant —
// specs/domains/model-profiles.md). Fail-open on an unloaded config.
func (f *FrontendAPI) modelProfilesGoalBlocked() bool {
	// Cheap shunt under the read lock: no config or master-off can never
	// block — skip the catalog read too. The config pointer is never
	// swapped, so the short hold is safe.
	f.configMu.RLock()
	shunt := f.config == nil || !f.config.ModelProfiles.Enabled
	f.configMu.RUnlock()
	if shunt {
		return false
	}
	// The catalog does file I/O — load it OUTSIDE the lock, then resolve the
	// effective profile UNDER the read lock: the setters mutate
	// cfg.ModelProfiles in place under the write lock, so reading a detached
	// pointer would be a data race.
	catalog := f.modelProfilesCatalog()
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil {
		return false
	}
	profile, _ := effectiveModelProfilesConfig(f.config, catalog)
	return profile.Enabled && profile.EssentialTools.Enabled
}

// modelProfilesPickerData returns the read-only picker universe (every
// registered built-in that is neither MCP-sourced nor goal-mode-only, plus
// the workflow clusters restricted to it); non-nil so JSON serializes [].
// See specs/domains/model-profiles.md.
func (f *FrontendAPI) modelProfilesPickerData() ([]ModelProfilesBuiltinTool, []ModelProfilesToolGroup) {
	if f.appCell() == nil {
		return []ModelProfilesBuiltinTool{}, []ModelProfilesToolGroup{}
	}
	tools := builtinToolInfos(f.app.ListTools())
	return tools, modelProfilesToolGroups(tools)
}

// builtinToolInfos extracts the picker universe from a descriptor list, sorted
// by name. MCP tools and goal-mode-only tools are excluded (pinning them is
// inert — specs/domains/model-profiles.md); everything else is returned,
// including the reserved system group and the protected tools (the selection's
// un-narrowable members). Freshly allocated; descriptions are the registry
// text verbatim (the UI reformats them into markdown tooltips).
func builtinToolInfos(descriptors []sdktools.ToolDescriptor) []ModelProfilesBuiltinTool {
	out := make([]ModelProfilesBuiltinTool, 0, len(descriptors))
	for _, d := range descriptors {
		if d.SourceCategory == sdktools.SourceCategoryMCP || coretools.IsGoalModeTool(d.Name) {
			continue
		}
		out = append(out, ModelProfilesBuiltinTool{Name: d.Name, Description: d.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// modelProfilesToolGroups projects the workflow-cluster catalog onto the
// picker universe: a cluster survives when at least one member does.
// See specs/domains/model-profiles.md.
func modelProfilesToolGroups(tools []ModelProfilesBuiltinTool) []ModelProfilesToolGroup {
	registered := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		registered[t.Name] = struct{}{}
	}
	catalog := modelprofiles.ToolGroupCatalog()
	out := make([]ModelProfilesToolGroup, 0, len(catalog))
	for _, g := range catalog {
		members := make([]string, 0, len(g.Tools))
		for _, name := range g.Tools {
			if _, ok := registered[name]; ok {
				members = append(members, name)
			}
		}
		if len(members) == 0 {
			continue
		}
		out = append(out, ModelProfilesToolGroup{
			ID:          g.ID,
			Title:       g.Title,
			Description: g.Description,
			Tools:       members,
		})
	}
	return out
}

// modelProfilesStore reads the custom profile store (fail-soft), logging
// store-level warnings; mutations operate on the fresh snapshot.
func (f *FrontendAPI) modelProfilesStore() (custom []config.ModelProfile, storePath string) {
	f.seedAcquire()
	storePath = config.ModelProfilesPath(f.agentDir)
	custom, warnings := config.LoadCustomModelProfiles(storePath)
	for _, w := range warnings {
		f.log().Warn("model-profile profile warning", "warning", w)
	}
	return custom, storePath
}

// applyModelProfilesChange is the post-mutation tail shared by the profile
// CRUD/select methods (callers hold configMu, after a successful write):
// clear stale load warnings, emit config:updated, rebuild the router, refresh
// the session manager's snapshot. See specs/domains/model-profiles.md.
func (f *FrontendAPI) applyModelProfilesChange() {
	f.seedAcquire()
	f.configLoadErrors = nil
	f.emitConfigUpdated()
	// Recompute the cached effective gate GetConfig serves (runs under
	// configMu.Lock) so the UI's goal-mode block tracks the change.
	f.refreshModelProfilesGateLocked()
	builderCfg := f.toBuilderConfigLocked()
	if b := f.builder(); b != nil {
		if err := b.RebuildRouter(builderCfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after model-profile profile change", "error", err)
		}
	}
	if app := f.app; app != nil {
		if mgr := app.Manager(); mgr != nil {
			modelProfilesCatalog := f.modelProfilesCatalog()
			modelProfile, _ := effectiveModelProfilesConfig(f.config, modelProfilesCatalog)
			mgr.SetModelProfile(modelProfile, activeModelProfile(f.config.ModelProfiles, modelProfilesCatalog))
			// Push the refreshed settings onto live orchestrators so the
			// goal-mode guard and essential-tools filter track the change —
			// mirrors the E2S push.
			mgr.SetModelProfilesSettings(core.ModelProfilesSettingsFromBuilderConfig(builderCfg.ModelProfiles))
		}
	}
}

// CreateModelProfile duplicates catalog profile baseID under a new display
// name as a custom profile (empty baseID = generic) and persists it. The name
// must be unique across predefined ∪ custom; the active profile is not
// changed; validation runs before the atomic write. Returns the new id.
// See specs/domains/model-profiles.md.
func (f *FrontendAPI) CreateModelProfile(baseID, name string) (string, error) {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return "", errors.New("config not initialized")
	}
	if baseID == "" {
		baseID = config.ModelProfilesGenericProfileID
	}
	base, ok := config.FindModelProfile(f.modelProfilesCatalog(), baseID)
	if !ok {
		return "", fmt.Errorf("base profile %q does not exist in the model-profile profile catalog", baseID)
	}

	custom, storePath := f.modelProfilesStore()
	created, err := config.CreateCustomModelProfile(name, base.Config, custom)
	if err != nil {
		return "", fmt.Errorf("invalid model-profile profile: %w", err)
	}
	if err := config.SaveCustomModelProfiles(storePath, slices.Concat(custom, []config.ModelProfile{created})); err != nil {
		return "", fmt.Errorf("failed to persist the model-profile profile store: %w", err)
	}
	f.applyModelProfilesChange()
	return created.ID, nil
}

// UpdateModelProfile updates the CUSTOM profile id: nil Name/Config keep the
// stored values; a supplied Config is a whole-value replacement of all 25
// knobs. Predefined profiles are read-only; validation runs before the atomic
// write. See specs/domains/model-profiles.md.
func (f *FrontendAPI) UpdateModelProfile(id string, req ModelProfileUpdateRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if req.Name == nil && req.Config == nil {
		return nil // nothing requested — nothing to validate or persist
	}
	if _, isPredefined := config.FindPredefinedModelProfile(id); isPredefined {
		return fmt.Errorf("the model-profile profile %q is predefined and read-only; duplicate it as a custom profile to edit it", id)
	}

	custom, storePath := f.modelProfilesStore()
	idx := slices.IndexFunc(custom, func(p config.ModelProfile) bool { return p.ID == id })
	if idx < 0 {
		return fmt.Errorf("the model-profile profile %q does not exist in the profile catalog", id)
	}

	// Next name: trimmed, non-empty, unique against every OTHER profile
	// (predefined ∪ custom, self excluded).
	nextName := custom[idx].Name
	if req.Name != nil {
		nextName = strings.TrimSpace(*req.Name)
		if nextName == "" {
			return errors.New("model-profile profile name must not be empty")
		}
		for _, p := range config.PredefinedModelProfiles() {
			if p.Name == nextName {
				return fmt.Errorf("model-profile profile name %q collides with the predefined profile %q", nextName, p.ID)
			}
		}
		for i, p := range custom {
			if i != idx && p.Name == nextName {
				return fmt.Errorf("model-profile profile name %q is already used by custom profile %q", nextName, p.ID)
			}
		}
	}

	// Next values: the profile constructor re-validates everything
	// (config.ValidateModelProfileConfig semantics).
	nextCfg := custom[idx].Config
	if req.Config != nil {
		nextCfg = modelProfilesValuesToProfileConfig(*req.Config)
	}
	updated, err := config.NewModelProfile(custom[idx].ID, nextName, config.ModelProfileKindCustom, nextCfg)
	if err != nil {
		return fmt.Errorf("invalid model-profile profile payload: %w", err)
	}
	next := slices.Clone(custom)
	next[idx] = updated
	if err := config.SaveCustomModelProfiles(storePath, next); err != nil {
		return fmt.Errorf("failed to persist the model-profile profile: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// DeleteModelProfile removes the CUSTOM profile id; predefined and unknown
// ids are rejected. Deleting the active profile falls back to generic in
// config.yaml and records a one-shot notice. See specs/domains/model-profiles.md.
func (f *FrontendAPI) DeleteModelProfile(id string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if _, isPredefined := config.FindPredefinedModelProfile(id); isPredefined {
		return fmt.Errorf("the model-profile profile %q is predefined and cannot be deleted", id)
	}

	custom, storePath := f.modelProfilesStore()
	if !slices.ContainsFunc(custom, func(p config.ModelProfile) bool { return p.ID == id }) {
		return fmt.Errorf("the model-profile profile %q does not exist in the profile catalog", id)
	}

	// Switch config.yaml to generic FIRST: a failed persist restores the id
	// and never touches the store; a failed store delete after that leaves
	// config pointing at generic while the profile exists — benign, retry-safe.
	wasActive := f.config.ModelProfiles.ActiveProfile == id
	if wasActive {
		if f.configPath == "" {
			return errors.New("config path not set")
		}
		prev := f.config.ModelProfiles.ActiveProfile
		f.config.ModelProfiles.ActiveProfile = config.ModelProfilesGenericProfileID
		if err := config.Save(f.config, f.configPath); err != nil {
			f.config.ModelProfiles.ActiveProfile = prev
			return fmt.Errorf("failed to persist model-profile config: %w", err)
		}
	}

	if _, err := config.DeleteCustomModelProfile(storePath, id); err != nil {
		return fmt.Errorf("failed to delete the model-profile profile: %w", err)
	}
	if wasActive {
		f.modelProfilesNotices = append(f.modelProfilesNotices,
			fmt.Sprintf("the active model-profile profile %q was deleted; switched to the generic profile", id))
	}
	f.applyModelProfilesChange()
	return nil
}

// SelectModelProfile makes id the active profile and persists it to
// config.yaml; the id must exist in the catalog; a failed write rolls the
// in-memory state back. See specs/domains/model-profiles.md.
func (f *FrontendAPI) SelectModelProfile(id string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if id == "" {
		return errors.New("model-profile profile id must not be empty")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if _, ok := config.FindModelProfile(f.modelProfilesCatalog(), id); !ok {
		return fmt.Errorf("model-profile profile %q does not exist in the profile catalog", id)
	}
	if f.config.ModelProfiles.ActiveProfile == id {
		return nil // already active — no state change
	}

	prev := f.config.ModelProfiles.ActiveProfile
	f.config.ModelProfiles.ActiveProfile = id
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.ModelProfiles.ActiveProfile = prev
		return fmt.Errorf("failed to persist model-profile config: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// SetModelProfilesEnabled toggles the global master switch
// (model_profiles.enabled) and persists it — the feature's only switch, not
// gated by experimental features. A matching request is a no-op; a failed
// write rolls back. See specs/domains/model-profiles.md.
func (f *FrontendAPI) SetModelProfilesEnabled(enabled bool) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}
	if f.configPath == "" {
		return errors.New("config path not set")
	}
	if f.config.ModelProfiles.Enabled == enabled {
		return nil // already in the requested state — no state change
	}

	prev := f.config.ModelProfiles.Enabled
	f.config.ModelProfiles.Enabled = enabled
	if err := config.Save(f.config, f.configPath); err != nil {
		f.config.ModelProfiles.Enabled = prev
		return fmt.Errorf("failed to persist model-profile config: %w", err)
	}
	f.applyModelProfilesChange()
	return nil
}

// modelProfileToDTO converts one catalog profile into the picker DTO.
func modelProfileToDTO(p config.ModelProfile) ModelProfileDTO {
	return ModelProfileDTO{
		ID:     p.ID,
		Name:   p.Name,
		Kind:   string(p.Kind),
		Values: modelProfileConfigToValues(p.Config),
	}
}

// modelProfileConfigToValues converts the 25-knob struct into the values DTO;
// AlwaysPresent is normalized to a non-nil slice so JSON yields [].
func modelProfileConfigToValues(c config.ModelProfileConfig) ModelProfileValues {
	return ModelProfileValues{
		EssentialTools: ModelProfilesEssentialToolsValues{
			Enabled:             c.EssentialTools.Enabled,
			AlwaysPresent:       nonNilStringSlice(c.EssentialTools.AlwaysPresent),
			CompactDescriptions: c.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: ModelProfilesSystemPromptResp{
			Lite:              c.SystemPrompt.Lite,
			FewShot:           c.SystemPrompt.FewShot,
			ReasoningScaffold: c.SystemPrompt.ReasoningScaffold,
		},
		Sampling: ModelProfilesSamplingResp{
			Enabled:           c.Sampling.Enabled,
			Temperature:       c.Sampling.Temperature,
			TopP:              c.Sampling.TopP,
			TopK:              c.Sampling.TopK,
			RepetitionPenalty: c.Sampling.RepetitionPenalty,
			PresencePenalty:   c.Sampling.PresencePenalty,
			ReasoningEffort:   c.Sampling.ReasoningEffort,
		},
		LoopHardening: ModelProfilesLoopHardeningResp{
			Enabled:                      c.LoopHardening.Enabled,
			RepeatNudgeThreshold:         c.LoopHardening.RepeatNudgeThreshold,
			ParseErrorAbortThreshold:     c.LoopHardening.ParseErrorAbortThreshold,
			FruitlessNudgeThreshold:      c.LoopHardening.FruitlessNudgeThreshold,
			FruitlessAbortThreshold:      c.LoopHardening.FruitlessAbortThreshold,
			SameToolRepeatNudgeThreshold: c.LoopHardening.SameToolRepeatNudgeThreshold,
		},
		Context: ModelProfilesContextResp{
			Enabled: c.Context.Enabled,
			Compaction: ModelProfilesCompactionResp{
				KeepLast:       c.Context.Compaction.KeepLast,
				BlockSize:      c.Context.Compaction.BlockSize,
				TriggerPercent: c.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: c.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  c.Context.OutputTokenReserve,
		},
	}
}

// modelProfilesValuesToProfileConfig converts the values DTO into the 25-knob
// struct for validation/persistence via config.NewModelProfile.
func modelProfilesValuesToProfileConfig(r ModelProfileValues) config.ModelProfileConfig {
	return config.ModelProfileConfig{
		EssentialTools: config.EssentialToolsConfig{
			Enabled:             r.EssentialTools.Enabled,
			AlwaysPresent:       r.EssentialTools.AlwaysPresent,
			CompactDescriptions: r.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: config.SystemPromptConfig{
			Lite:              r.SystemPrompt.Lite,
			FewShot:           r.SystemPrompt.FewShot,
			ReasoningScaffold: r.SystemPrompt.ReasoningScaffold,
		},
		Sampling: config.ModelProfilesSamplingConfig{
			Enabled:           r.Sampling.Enabled,
			Temperature:       r.Sampling.Temperature,
			TopP:              r.Sampling.TopP,
			TopK:              r.Sampling.TopK,
			RepetitionPenalty: r.Sampling.RepetitionPenalty,
			PresencePenalty:   r.Sampling.PresencePenalty,
			ReasoningEffort:   r.Sampling.ReasoningEffort,
		},
		LoopHardening: config.LoopHardeningConfig{
			Enabled:                      r.LoopHardening.Enabled,
			RepeatNudgeThreshold:         r.LoopHardening.RepeatNudgeThreshold,
			ParseErrorAbortThreshold:     r.LoopHardening.ParseErrorAbortThreshold,
			FruitlessNudgeThreshold:      r.LoopHardening.FruitlessNudgeThreshold,
			FruitlessAbortThreshold:      r.LoopHardening.FruitlessAbortThreshold,
			SameToolRepeatNudgeThreshold: r.LoopHardening.SameToolRepeatNudgeThreshold,
		},
		Context: config.ModelProfilesContextConfig{
			Enabled: r.Context.Enabled,
			Compaction: config.ModelProfilesCompactionConfig{
				KeepLast:       r.Context.Compaction.KeepLast,
				BlockSize:      r.Context.Compaction.BlockSize,
				TriggerPercent: r.Context.Compaction.TriggerPercent,
			},
			ToolOutputKeepLastN: r.Context.ToolOutputKeepLastN,
			OutputTokenReserve:  r.Context.OutputTokenReserve,
		},
	}
}

// GetLogLevel returns the current log level.
func (f *FrontendAPI) GetLogLevel() string {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.logLevel
}

// SetLogLevel sets the log level dynamically.
func (f *FrontendAPI) SetLogLevel(level string) error {
	f.seedAcquire()
	f.configMu.Lock()
	defer f.configMu.Unlock()

	level = strings.ToUpper(level)
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		f.logLevel = level
		// Nil-checked like the sibling call sites: a shell app can have f.app
		// non-nil with no manager yet; a bare dereference would panic the RPC.
		if f.app != nil {
			if m := f.app.Manager(); m != nil {
				m.SetLogLevel(level)
			}
		}
		if f.config != nil {
			f.config.LogLevel = level
		}
		if err := f.persistConfig(); err != nil {
			f.log().Warn("failed to persist log level change", "error", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
}

// GetNotificationBannerTimeout returns how long an OS notification banner
// stays on screen, in seconds: -1 (daemon default), 0 (never expire) or an
// explicit lifetime. Fail-safe: an unloaded config answers -1, never the Go
// zero value (which would silently mean "banners never go away"). See
// specs/domains/frontend/system-notifications.md.
func (f *FrontendAPI) GetNotificationBannerTimeout() int {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	if f.config == nil || f.config.Notifications.BannerTimeoutSeconds == nil {
		return config.NotificationBannerTimeoutDaemonDefault
	}
	return *f.config.Notifications.BannerTimeoutSeconds
}

// SetNotificationBannerTimeout persists the banner lifetime (see
// GetNotificationBannerTimeout for the accepted values).
func (f *FrontendAPI) SetNotificationBannerTimeout(seconds int) error {
	if seconds < config.NotificationBannerTimeoutDaemonDefault ||
		seconds > config.NotificationBannerTimeoutMaxSeconds {
		return fmt.Errorf("invalid notification banner timeout: %d seconds "+
			"(want -1 for the daemon default, 0 for never, or 1..%d)",
			seconds, config.NotificationBannerTimeoutMaxSeconds)
	}

	f.configMu.Lock()
	defer f.configMu.Unlock()
	if f.config == nil {
		return errors.New("configuration is not loaded")
	}
	f.config.Notifications.BannerTimeoutSeconds = &seconds
	if err := f.persistConfig(); err != nil {
		return fmt.Errorf("persist notification banner timeout: %w", err)
	}
	return nil
}

// ListProviderModels returns available model names for a provider (Anthropic:
// registry list; ChatGPT/OpenAI-compatible: provider API). Draft credentials
// on req are merged into a throwaway BuilderConfig copy so the settings UI
// can list models for a not-yet-persisted provider. See
// specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) ListProviderModels(req ListProviderModelsRequest) ([]string, error) {
	if req.Provider == "" {
		return nil, errors.New("provider is required")
	}

	f.configMu.RLock()
	if f.config == nil {
		f.configMu.RUnlock()
		return nil, errors.New("config not initialized")
	}
	b := f.builder()
	if b == nil {
		f.configMu.RUnlock()
		return nil, errors.New("application not initialized")
	}
	cfg := f.toBuilderConfigLocked()
	f.configMu.RUnlock()

	if err := applyListProviderModelsOverrides(cfg, req); err != nil {
		return nil, err
	}
	return b.ListProviderModels(context.Background(), req.Provider, cfg)
}

// GetProviderTLSCertificate dials the provider's endpoint and returns the
// SPKI fingerprint it currently presents — handshake only, no HTTP, no API
// key, verification deliberately skipped (the fingerprint IS the payload) —
// unconditional with respect to any configured pin. The draft base_url wins
// over the persisted one (${VAR} expanded like every dial path). The probe
// dials DIRECTLY, so an effective, non-bypassed proxy is refused up front
// (the pin would be inert); snapshotted under configMu.RLock, released
// before the handshake. See specs/decisions/054-per-provider-tls-pinning.md.
func (f *FrontendAPI) GetProviderTLSCertificate(req GetProviderTLSCertificateRequest) (TLSCertificateResponse, error) {
	if req.Provider == "" {
		return TLSCertificateResponse{}, errors.New("provider is required")
	}

	f.configMu.RLock()
	// Effective proxy state mirrors proxy.BuildTransport: enabled AND a URL
	// must both be set; an enabled-but-empty proxy dials directly, so the pin
	// stays meaningful there.
	proxyEnabled := f.config != nil && f.config.Proxy.Enabled && f.config.Proxy.URL != ""
	var bypass proxy.BypassMatcher
	var persisted string
	if f.config != nil {
		bypass = proxy.NewBypassMatcher(f.config.Proxy.BypassList)
		// The canonical provider list carries the raw (env-var) base URL; the
		// expander resolves it below.
		for _, p := range f.config.LLM.GetAllProviderConfigs() {
			if p.Name == req.Provider {
				persisted = p.BaseURL
				break
			}
		}
	}
	f.configMu.RUnlock()
	// No lock is held from here on — the handshake below is a network call.

	raw := req.BaseURL
	if raw == "" {
		raw = persisted
	}
	if raw == "" {
		return TLSCertificateResponse{}, fmt.Errorf("provider %q has no base URL configured", req.Provider)
	}
	// ${VAR} expansion — every dial path's treatment; a persisted
	// "${LLM_BASE_URL}" would otherwise fail url.Parse inside FetchFingerprint.
	raw = config.ExpandEnvVars(raw)

	if proxyEnabled {
		// The bypass decision must use the same URL the probe will dial —
		// the draft wins over the persisted base_url, like every dial path.
		bypassed := bypass.Matches(llmtls.TargetHost(raw))
		if !bypassed {
			return TLSCertificateResponse{}, errors.New("TLS fingerprint fetching is unavailable while an HTTP proxy is enabled (Settings → General → HTTP Proxy): the pin does not apply to proxied connections. Disable the proxy, or add this host to the proxy bypass list, to pin this server's certificate")
		}
	}

	fp, err := llmtls.FetchFingerprint(context.Background(), raw, f.log())
	if err != nil {
		return TLSCertificateResponse{}, fmt.Errorf("fetching certificate fingerprint from %q: %w", raw, err)
	}
	return TLSCertificateResponse{Fingerprint: fp}, nil
}

// resolveTLSFingerprint applies the pointer sentinel for the per-provider
// TLS pin (ADR-054): nil keeps the persisted pin (a debounced partial save
// must not silently clear it); non-nil applies verbatim (explicit "" clears).
// See specs/contracts/desktop-frontend.md.
func resolveTLSFingerprint(requested *string, persisted string, providerExists bool) string {
	if requested != nil {
		return *requested
	}
	if providerExists {
		return persisted
	}
	return ""
}

// resolveAutoRetrySeconds applies the pointer sentinel for the per-provider
// auto-retry interval (ADR-065): nil keeps the persisted value; non-nil
// applies verbatim (explicit 0 disables). See specs/domains/llm-providers.md.
func resolveAutoRetrySeconds(requested *int, persisted int, providerExists bool) int {
	if requested != nil {
		return *requested
	}
	if providerExists {
		return persisted
	}
	return 0
}

// resolveTimeoutClass applies the pointer sentinel for the per-provider
// timeout_class override (ADR-071 D3): nil keeps the persisted value; non-nil
// applies verbatim (explicit "" = infer). See
// specs/decisions/071-adaptive-llm-request-budgets.md.
func resolveTimeoutClass(requested *string, persisted string, providerExists bool) string {
	if requested != nil {
		return *requested
	}
	if providerExists {
		return persisted
	}
	return ""
}

// applyListProviderModelsOverrides merges draft credentials from the settings
// UI into cfg so ListProviderModels can resolve providers that exist only in
// the frontend draft (not yet written to config.yaml). cfg must be a
// throwaway ToBuilderConfig result — this mutates its ProviderConfigs map.
// See specs/contracts/desktop-frontend.md, specs/domains/llm-providers.md.
func applyListProviderModelsOverrides(cfg *core.BuilderConfig, req ListProviderModelsRequest) error {
	existing, exists := cfg.LLM.ProviderConfigs[req.Provider]

	apiKey := req.APIKey
	if (apiKey == "" || apiKey == maskedAPIKey) && exists {
		apiKey = existing.APIKey
	}

	baseURL := req.BaseURL
	if baseURL == "" && exists {
		baseURL = existing.BaseURL
	}

	providerType := req.Type
	switch providerType {
	case "openai", "anthropic":
		// explicit draft transport
	case "":
		if exists {
			providerType = existing.ProviderType
		} else {
			// Unsaved compatible providers default to OpenAI Chat Completions.
			providerType = "openai"
		}
	default:
		return fmt.Errorf("unsupported provider type %q", providerType)
	}

	// Draft TLS pin (ADR-054): nil falls back to the saved value so a partial
	// draft (only credentials edited) does not silently drop the pin; a
	// non-nil value applies verbatim, so an explicit draft "" wins.
	tlsFingerprint := resolveTLSFingerprint(req.TLSFingerprint, existing.TLSFingerprint, exists)

	if !exists && baseURL == "" {
		// Fixed providers are always present in ToBuilderConfig; reaching here
		// means a named compatible provider that has not been saved yet.
		return fmt.Errorf("unknown provider: %s (set a base URL to fetch models before saving)", req.Provider)
	}

	cfg.LLM.ProviderConfigs[req.Provider] = core.BuilderProviderConfig{
		ProviderType:       providerType,
		APIKey:             apiKey,
		BaseURL:            baseURL,
		Models:             existing.Models,
		TLSFingerprint:     tlsFingerprint,
		OutputTokenReserve: existing.OutputTokenReserve,
	}
	return nil
}

// validTokenizerTypes enumerates the TokenizerType values the Configure dialog
// may select (mirrors llm.NewTokenCounter; enumerated explicitly because the
// dialog offers a fixed, curated list). Keep in sync with NewTokenCounter.
var validTokenizerTypes = map[string]struct{}{
	"approximate":          {},
	"tiktoken/o200k_base":  {},
	"tiktoken/cl100k_base": {},
	"anthropic-api":        {},
}

// validFamilies enumerates the ModelFamily values the dialog may select,
// built from the llm.Family* constants (stays in sync with DetectFamily).
var validFamilies = map[string]struct{}{
	string(llm.FamilyAnthropic):      {},
	string(llm.FamilyOpenAIFlagship): {},
	string(llm.FamilyOpenAIStandard): {},
	string(llm.FamilyGoogle):         {},
	string(llm.FamilyMistral):        {},
	string(llm.FamilyDeepSeek):       {},
	string(llm.FamilyOpenAICodex):    {},
	string(llm.FamilyQwen):           {},
	string(llm.FamilyGLM):            {},
	string(llm.FamilyKimi):           {},
	string(llm.FamilyDefault):        {},
}

// validProtocols enumerates the APIProtocol values the dialog may select,
// built from the llm.Protocol* constants (stays in sync with DetectProtocol).
var validProtocols = map[string]struct{}{
	string(llm.ProtocolChatCompletions): {},
	string(llm.ProtocolResponses):       {},
	string(llm.ProtocolAnthropic):       {},
	string(llm.ProtocolGoogle):          {},
}

// GetModelConfig returns a single model's configurable parameters — the
// currently-effective values (override when set, otherwise the built-in
// default) and the factory defaults — for the Configure dialog's pre-fill.
// An override field of 0/""/nil means "inherit"; llm.ResolveBuiltInModel
// resolves network-free and always fills Family/Protocol.
func (f *FrontendAPI) GetModelConfig(model string) (ModelConfigResponse, error) {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return ModelConfigResponse{}, errors.New("config not initialized")
	}

	defMeta, _ := llm.ResolveBuiltInModel(model)

	// ResolveBuiltInModel always resolves non-nil capabilities today; guard
	// so a future nil-returning path degrades to all-false instead of panicking.
	defCaps := llm.ModelCapabilities{}
	if defMeta.Capabilities != nil {
		defCaps = *defMeta.Capabilities
	}

	resp := ModelConfigResponse{
		Model:                model,
		ContextWindow:        defMeta.ContextWindow,
		OutputLimit:          defMeta.OutputLimit,
		TokenizerType:        defMeta.TokenizerType,
		Family:               defMeta.Family,
		Protocol:             string(defMeta.Protocol),
		Capabilities:         defCaps,
		DefaultContextWindow: defMeta.ContextWindow,
		DefaultOutputLimit:   defMeta.OutputLimit,
		DefaultTokenizerType: defMeta.TokenizerType,
		DefaultFamily:        defMeta.Family,
		DefaultProtocol:      string(defMeta.Protocol),
		DefaultCapabilities:  defCaps,
	}

	if override, ok := f.config.LLM.Models[model]; ok {
		resp.HasOverride = true
		if override.ContextWindow > 0 {
			resp.ContextWindow = override.ContextWindow
		}
		if override.OutputLimit > 0 {
			resp.OutputLimit = override.OutputLimit
		}
		if override.TokenizerType != "" {
			resp.TokenizerType = override.TokenizerType
		}
		if override.Family != "" {
			resp.Family = override.Family
		}
		if override.Protocol != "" {
			resp.Protocol = override.Protocol
		}
		if override.Capabilities != nil {
			resp.Capabilities = *override.Capabilities
		}
	}

	return resp, nil
}

// SetModelConfig persists per-model overrides from the Configure dialog.
// Only fields differing from the built-in default are stored (others take
// their "inherit" sentinel; when everything matches, the entry is dropped —
// specs/domains/embedded-llm.md). request_timeout (ADR-071), which the
// dialog does not own, is carried through untouched. The change is persisted
// and the router rebuilt; enum fields are validated against the curated sets
// (the backend is the enforcement boundary).
func (f *FrontendAPI) SetModelConfig(model string, req ModelConfigRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// ContextWindow/OutputLimit must be positive: the Input's min={1} is
	// advisory only, and a negative would persist as nonsensical state.
	if req.ContextWindow < 1 || req.OutputLimit < 1 {
		return fmt.Errorf("context window and output limit must be positive integers, got %d/%d",
			req.ContextWindow, req.OutputLimit)
	}

	// The same ceiling config.validate enforces at load; Save does not run
	// validate, so an out-of-range window would surface only at the next
	// launch — whose failed validation discards the whole config.
	if req.ContextWindow > config.MaxModelContextWindow {
		return fmt.Errorf("context window %d is not valid; must be within 1-%d tokens",
			req.ContextWindow, config.MaxModelContextWindow)
	}

	// TokenizerType/Family/Protocol are selected from fixed dropdown lists; the
	// backend is the enforcement boundary. Empty is allowed (means "inherit").
	if req.TokenizerType != "" {
		if _, ok := validTokenizerTypes[req.TokenizerType]; !ok {
			return fmt.Errorf("invalid tokenizer type %q", req.TokenizerType)
		}
	}
	if req.Family != "" {
		if _, ok := validFamilies[req.Family]; !ok {
			return fmt.Errorf("invalid family %q", req.Family)
		}
	}
	if req.Protocol != "" {
		if _, ok := validProtocols[req.Protocol]; !ok {
			return fmt.Errorf("invalid protocol %q", req.Protocol)
		}
	}

	defMeta, _ := llm.ResolveBuiltInModel(model)
	defCaps := llm.ModelCapabilities{}
	if defMeta.Capabilities != nil {
		defCaps = *defMeta.Capabilities
	}

	// Record the "inherit" sentinel for any field matching the built-in
	// default: the override stays partial and unset fields inherit from the
	// tiers below (specs/domains/embedded-llm.md).
	var newCW, newOL int
	var newTok, newFam, newProto string
	var newCaps *llm.ModelCapabilities

	if req.ContextWindow != defMeta.ContextWindow {
		newCW = req.ContextWindow
	}
	if req.OutputLimit != defMeta.OutputLimit {
		newOL = req.OutputLimit
	}
	if req.TokenizerType != defMeta.TokenizerType {
		newTok = req.TokenizerType
	}
	if req.Family != defMeta.Family {
		newFam = req.Family
	}
	if req.Protocol != string(defMeta.Protocol) {
		newProto = req.Protocol
	}
	if req.Capabilities != nil && *req.Capabilities != defCaps {
		newCaps = req.Capabilities
	}

	if f.config.LLM.Models == nil {
		f.config.LLM.Models = make(map[string]config.ModelOverride)
	}

	// The dialog has no editor for request_timeout (ADR-071) — a hand-edited
	// key the rebuilt entry must carry through untouched, or an all-defaults
	// save would delete the whole entry.
	existing := f.config.LLM.Models[model]

	if newCW == 0 && newOL == 0 && newTok == "" && newFam == "" && newProto == "" && newCaps == nil && existing.RequestTimeout == 0 {
		delete(f.config.LLM.Models, model)
	} else {
		f.config.LLM.Models[model] = config.ModelOverride{
			ContextWindow:  newCW,
			OutputLimit:    newOL,
			TokenizerType:  newTok,
			Family:         newFam,
			Protocol:       newProto,
			Capabilities:   newCaps,
			RequestTimeout: existing.RequestTimeout,
		}
	}

	if err := f.persistConfig(); err != nil {
		return fmt.Errorf("failed to persist model config: %w", err)
	}

	// Clear any config load errors since settings are now valid.
	f.configLoadErrors = nil

	// Rebuild the LLM router so the new override takes effect for new sessions.
	if b := f.builder(); b != nil {
		bcfg := f.toBuilderConfigLocked()
		if err := b.RebuildRouter(bcfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after model config update", "error", err)
		}
	}

	return nil
}

// persistConfig saves the in-memory config to disk — the funnel every
// successful config mutation goes through, and the single point announcing
// config changes (see emitConfigUpdated, specs/contracts/event-catalog.md).
func (f *FrontendAPI) persistConfig() error {
	if f.configPath == "" || f.config == nil {
		return errors.New("config path or config not set")
	}
	err := config.Save(f.config, f.configPath)
	// Announce even when the disk write failed: the in-memory config GetConfig
	// serves has already changed (specs/contracts/event-catalog.md).
	f.emitConfigUpdated()
	return err
}

// emitConfigUpdated notifies the frontend of a config mutation. Dispatched on
// a fresh goroutine: persistConfig runs under its caller's configMu and
// emitEvent is a synchronous Wails dispatch that must never run under it.
// Nil-guarded: most tests exercise persistConfig without wiring emitEvent.
// See specs/contracts/event-catalog.md.
func (f *FrontendAPI) emitConfigUpdated() {
	f.seedAcquire()
	if f.emitEvent == nil {
		return
	}
	go f.emitEvent(EventConfigUpdated)
}

// maskAPIKey returns a masked representation of an API key for display.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		return key
	}
	return maskedAPIKey
}

// nonNilStringSlice returns an empty slice if the input is nil,
// ensuring JSON serialization produces [] instead of null.
func nonNilStringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
