package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
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
		},
		Proxy: ProxySettingsResponse{
			Enabled:    f.config.Proxy.Enabled,
			URL:        proxy.MaskURL(f.config.Proxy.URL),
			BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
			TLSCertDir: f.config.Proxy.TLSCertDir,
		},
	}

	// Populate AllModels: flat list of all enabled models.
	// Always build from the per-provider config lists so the frontend sees
	// configured models immediately, even before the async ModelRegistry
	// finishes initializing. When the registry is ready, enrich with family
	// and reasoning metadata.
	b := f.builder()
	var reg *llm.ModelRegistry
	if b != nil {
		reg = b.ModelRegistry()
	}
	resp.LLM.AllModels = f.collectAllModels(reg)
	resp.LLM.ModelsReady = reg != nil

	return resp
}

// buildLLMResponse constructs the sanitized ConfigLLMResponse from config.
func (f *FrontendAPI) buildLLMResponse() ConfigLLMResponse {
	resp := ConfigLLMResponse{
		DefaultModel: f.config.LLM.DefaultModel,
		Anthropic: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.Anthropic.APIKey),
			Models: f.config.LLM.Anthropic.Models,
		},
		ChatGPT: ConfigProviderFull{
			APIKey: maskAPIKey(f.config.LLM.ChatGPT.APIKey),
			Models: f.config.LLM.ChatGPT.Models,
		},
		OpenAICompatible:    make(map[string]ConfigProviderFull, len(f.config.LLM.OpenAICompatible)),
		AnthropicCompatible: make(map[string]ConfigProviderFull, len(f.config.LLM.AnthropicCompatible)),
	}
	for name, cfg := range f.config.LLM.OpenAICompatible {
		resp.OpenAICompatible[name] = ConfigProviderFull{
			APIKey:  maskAPIKey(cfg.APIKey),
			BaseURL: cfg.BaseURL,
			Models:  cfg.Models,
		}
	}
	for name, cfg := range f.config.LLM.AnthropicCompatible {
		resp.AnthropicCompatible[name] = ConfigProviderFull{
			APIKey:  maskAPIKey(cfg.APIKey),
			BaseURL: cfg.BaseURL,
			Models:  cfg.Models,
		}
	}
	return resp
}

// collectAllModels iterates all enabled provider models and builds ModelInfo
// entries. When reg is non-nil, family and reasoning metadata are resolved
// from the registry. When reg is nil (registry not yet initialized), models
// are returned without metadata so the frontend still sees configured models.
//
// Entries are keyed by composite (provider, model) so that two providers
// exposing the same bare model name both appear — the frontend uses the
// composite "provider/name" value to select a specific provider while
// displaying the bare model name.
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
				meta, _ := reg.Resolve(f.ctx(), modelName)
				family = meta.Family
				vision = meta.Capabilities.Attachment
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
func (f *FrontendAPI) UpdateLLMConfig(req LLMFullConfigRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Update default model
	if req.DefaultModel != "" {
		f.config.LLM.DefaultModel = req.DefaultModel
	}

	// Update each provider's models list and credentials
	if req.Anthropic != nil {
		if req.Anthropic.Models != nil {
			f.config.LLM.Anthropic.Models = req.Anthropic.Models
		}
		if req.Anthropic.APIKey != "" && req.Anthropic.APIKey != maskedAPIKey {
			f.config.LLM.Anthropic.APIKey = req.Anthropic.APIKey
		}
	}
	if req.OpenAICompatible != nil {
		newMap := make(map[string]config.OpenAICompatibleConfig, len(req.OpenAICompatible))
		for name, ocReq := range req.OpenAICompatible {
			apiKey := ocReq.APIKey
			if apiKey == maskedAPIKey || apiKey == "" {
				if existing, ok := f.config.LLM.OpenAICompatible[name]; ok {
					apiKey = existing.APIKey
				}
			}
			newMap[name] = config.OpenAICompatibleConfig{
				APIKey:  apiKey,
				BaseURL: ocReq.BaseURL,
				Models:  ocReq.Models,
			}
		}
		f.config.LLM.OpenAICompatible = newMap
	}
	if req.AnthropicCompatible != nil {
		newMap := make(map[string]config.AnthropicCompatibleConfig, len(req.AnthropicCompatible))
		for name, acReq := range req.AnthropicCompatible {
			apiKey := acReq.APIKey
			if apiKey == maskedAPIKey || apiKey == "" {
				if existing, ok := f.config.LLM.AnthropicCompatible[name]; ok {
					apiKey = existing.APIKey
				}
			}
			newMap[name] = config.AnthropicCompatibleConfig{
				APIKey:  apiKey,
				BaseURL: acReq.BaseURL,
				Models:  acReq.Models,
			}
		}
		f.config.LLM.AnthropicCompatible = newMap
	}
	if req.ChatGPT != nil {
		if req.ChatGPT.Models != nil {
			f.config.LLM.ChatGPT.Models = req.ChatGPT.Models
		}
		if req.ChatGPT.APIKey != "" && req.ChatGPT.APIKey != maskedAPIKey {
			f.config.LLM.ChatGPT.APIKey = req.ChatGPT.APIKey
		}
	}

	// Invariant: after provider changes, the persisted default model must
	// still resolve to a model that is enabled in some provider. If the
	// provider/model that owned the default was removed or had its model
	// disabled, clear it instead of persisting a dangling selector — a stale
	// default would fail router validation. The settings dialog blocks close
	// until the user picks a new default, so this never leaves the app in a
	// state where LLM calls have no target model.
	//
	// Note: an incoming empty `default_model` is intentionally ignored above
	// (to avoid wiping a valid selection during debounced partial edits), so
	// this re-validation is the only path that clears a now-invalid default.
	if f.config.LLM.DefaultModel != "" {
		if _, _, err := f.config.LLM.ResolveDefaultModelProvider(); err != nil {
			f.config.LLM.DefaultModel = ""
		}
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist LLM config", "error", err)
	}

	// Clear any config load errors since settings are now valid
	f.configLoadErrors = nil

	// Ensure No Project exists now that the app is usable.
	// On a clean first run this is the first time the pseudo-project
	// is created — it was deferred during startup to avoid provisioning
	// infrastructure before configuration validation.
	//
	// Guard on a non-empty default model: during initial LLM setup the
	// frontend may debounce-save partial edits before the user has selected
	// a model. Creating No Project and switching on every keystroke would
	// disrupt the file panel while the settings dialog is still open.
	if f.projectManager != nil && f.config.LLM.DefaultModel != "" {
		created, err := f.projectManager.EnsureNoProject()
		if err != nil {
			f.log().Warn("failed to ensure No Project after config update", "error", err)
		}
		// Only refresh the project list when No Project was just created
		// on first-run setup. The frontend's loadAndActivate auto-selects
		// the first project only when no project is active, so emitting
		// backend:ready here lands the user in CHAT mode on first run
		// without disrupting an already-active project/session on
		// mid-session config edits.
		if err == nil && created {
			if projects, pErr := f.projectManager.ListProjects(); pErr == nil {
				f.emitEvent(EventBackendReady, projects)
			} else {
				f.log().Warn("failed to list projects after config update", "error", pErr)
			}
		}
	}

	// Rebuild judge and LLM router via the backend builder so new sessions
	// use the updated provider immediately.
	if b := f.builder(); b != nil {
		bcfg := ToBuilderConfig(f.config)
		b.RebuildJudge(bcfg)
		if err := b.RebuildRouter(bcfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after config update", "error", err)
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

	f.config.Search.Provider = settings.Provider
	// Only update API key if it's not the masked placeholder
	if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
		f.config.Search.APIKey = settings.APIKey
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist search settings", "error", err)
	}

	// Rebuild web search tool via the backend builder.
	if b := f.builder(); b != nil {
		b.UpdateSearchTool(ToBuilderConfig(f.config))
	}

	return nil
}

// GetProxySettings returns current proxy settings for the UI.
// The proxy URL password is masked.
func (f *FrontendAPI) GetProxySettings() ProxySettingsResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return ProxySettingsResponse{BypassList: []string{}}
	}

	return ProxySettingsResponse{
		Enabled:    f.config.Proxy.Enabled,
		URL:        proxy.MaskURL(f.config.Proxy.URL),
		BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
		TLSCertDir: f.config.Proxy.TLSCertDir,
	}
}

// UpdateProxySettings updates proxy configuration at runtime and propagates
// the change to all subsystems (LLM providers, web tools, MCP, child processes).
func (f *FrontendAPI) UpdateProxySettings(settings ProxySettingsRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	f.config.Proxy.Enabled = settings.Enabled
	if settings.URL != "" {
		f.config.Proxy.URL = settings.URL
	}
	if settings.BypassList != nil {
		f.config.Proxy.BypassList = settings.BypassList
	}
	f.config.Proxy.TLSCertDir = settings.TLSCertDir

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist proxy settings", "error", err)
	}

	// Rebuild proxy transport and propagate to all subsystems.
	if b := f.builder(); b != nil {
		bcfg := ToBuilderConfig(f.config)
		if err := b.RebuildProxy(context.Background(), bcfg); err != nil {
			f.log().Warn("failed to rebuild proxy after settings update", "error", err)
			return fmt.Errorf("proxy rebuild failed: %w", err)
		}
	}

	return nil
}

// GetSecuritySettings returns current security settings for the UI.
// Internal tools are filtered out from the policy map.
func (f *FrontendAPI) GetSecuritySettings() SecuritySettingsResponse {
	f.configMu.RLock()
	defer f.configMu.RUnlock()

	if f.config == nil {
		return SecuritySettingsResponse{DefaultPolicy: "user_confirm"}
	}
	resp := SecuritySettingsResponse{
		DefaultPolicy:              f.config.Security.DefaultPolicy,
		ToolPolicies:               make(map[string]ToolPolicyResponse),
		AutoApproveWorkspaceWrites: f.config.Security.AutoApproveWorkspaceWrites,
	}
	for name, cfg := range f.config.Security.ToolPolicies {
		// Filter out internal tools
		if tools.IsInternalTool(name) {
			continue
		}
		resp.ToolPolicies[name] = ToolPolicyResponse{
			Policy:    cfg.Policy,
			Blacklist: cfg.Blacklist,
		}
	}
	return resp
}

// UpdateSecuritySettings updates security settings at runtime.
// Internal tools are silently stripped from incoming policy updates.
func (f *FrontendAPI) UpdateSecuritySettings(settings SecuritySettingsResponse) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Update config — replace the full policy set so config stays in sync
	// with the registry (the frontend always sends the complete set).
	f.config.Security.DefaultPolicy = settings.DefaultPolicy
	f.config.Security.AutoApproveWorkspaceWrites = settings.AutoApproveWorkspaceWrites
	newPolicies := make(map[string]config.ToolPolicyConfig, len(settings.ToolPolicies))
	for name, policyCfg := range settings.ToolPolicies {
		// Silently skip internal tools
		if tools.IsInternalTool(name) {
			continue
		}
		newPolicies[name] = config.ToolPolicyConfig{
			Policy:    policyCfg.Policy,
			Blacklist: policyCfg.Blacklist,
		}
	}
	f.config.Security.ToolPolicies = newPolicies

	// Apply policies to the shared tool registry via the backend builder.
	if b := f.builder(); b != nil {
		b.UpdateSecurityPolicies(ToBuilderConfig(f.config))
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist security settings", "error", err)
	}

	return nil
}

// GetLogLevel returns the current log level.
func (f *FrontendAPI) GetLogLevel() string {
	f.configMu.RLock()
	defer f.configMu.RUnlock()
	return f.logLevel
}

// SetLogLevel sets the log level dynamically.
func (f *FrontendAPI) SetLogLevel(level string) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	// Validate the level
	level = strings.ToUpper(level)
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		f.logLevel = level
		if f.app != nil {
			f.app.Manager().SetLogLevel(level)
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

// ListProviderModels returns available model names for a given provider.
// For Anthropic: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible: fetches from the provider's API.
func (f *FrontendAPI) ListProviderModels(provider string) ([]string, error) {
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
	cfg := ToBuilderConfig(f.config)
	f.configMu.RUnlock()

	return b.ListProviderModels(context.Background(), provider, cfg)
}

// persistConfig saves the current in-memory config to disk.
func (f *FrontendAPI) persistConfig() error {
	if f.configPath == "" || f.config == nil {
		return errors.New("config path or config not set")
	}
	return config.Save(f.config, f.configPath)
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
