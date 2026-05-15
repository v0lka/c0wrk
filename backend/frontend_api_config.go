package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/user/agent/backend/config"
	"github.com/user/agent/core"
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

	return ConfigResponse{
		Loaded:       true,
		LogLevel:     f.config.LogLevel,
		ConfigErrors: nonNilStringSlice(f.configLoadErrors),
		LLM: ConfigLLMResponse{
			ActiveProvider: f.config.LLM.ActiveProvider,
			Anthropic: ConfigProviderKeyModel{
				APIKey: maskAPIKey(f.config.LLM.Anthropic.APIKey),
				Model:  f.config.LLM.Anthropic.Model,
			},
			Gemini: ConfigProviderKeyModel{
				APIKey: maskAPIKey(f.config.LLM.Gemini.APIKey),
				Model:  f.config.LLM.Gemini.Model,
			},
			LMStudio: ConfigProviderFull{
				BaseURL: f.config.LLM.LMStudio.BaseURL,
				APIKey:  maskAPIKey(f.config.LLM.LMStudio.APIKey),
				Model:   f.config.LLM.LMStudio.Model,
			},
			OpenAICompatible: ConfigProviderFull{
				BaseURL: f.config.LLM.OpenAICompatible.BaseURL,
				APIKey:  maskAPIKey(f.config.LLM.OpenAICompatible.APIKey),
				Model:   f.config.LLM.OpenAICompatible.Model,
			},
			ChatGPT: ConfigProviderKeyModel{
				APIKey: maskAPIKey(f.config.LLM.ChatGPT.APIKey),
				Model:  f.config.LLM.ChatGPT.Model,
			},
		},
		Memory: ConfigMemResponse{
			Database: f.config.Memory.Database,
		},
		Search: ConfigSearchResp{
			Provider: f.config.Search.Provider,
			APIKey:   maskAPIKey(f.config.Search.APIKey),
		},
		Proxy: ProxySettingsResponse{
			Enabled:    f.config.Proxy.Enabled,
			URL:        core.MaskProxyURL(f.config.Proxy.URL),
			BypassList: nonNilStringSlice(f.config.Proxy.BypassList),
			TLSCertDir: f.config.Proxy.TLSCertDir,
		},
	}
}

// UpdateLLMSettings updates LLM active provider and model settings.
func (f *FrontendAPI) UpdateLLMSettings(settings LLMSettingsRequest) error {
	f.configMu.Lock()
	defer f.configMu.Unlock()

	if f.config == nil {
		return errors.New("config not initialized")
	}

	// Update active provider
	if settings.ActiveProvider != "" {
		if !config.ValidProviders[settings.ActiveProvider] {
			return fmt.Errorf("active_provider %q is not a valid provider", settings.ActiveProvider)
		}
		f.config.LLM.ActiveProvider = settings.ActiveProvider
	}

	// Update model on the active provider
	if f.config.LLM.ActiveProvider != "" {
		switch f.config.LLM.ActiveProvider {
		case "anthropic":
			if settings.Model != "" {
				f.config.LLM.Anthropic.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				f.config.LLM.Anthropic.APIKey = settings.APIKey
			}
		case "gemini":
			if settings.Model != "" {
				f.config.LLM.Gemini.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				f.config.LLM.Gemini.APIKey = settings.APIKey
			}
		case "lmstudio":
			if settings.Model != "" {
				f.config.LLM.LMStudio.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				f.config.LLM.LMStudio.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				f.config.LLM.LMStudio.BaseURL = settings.BaseURL
			}
		case "openai_compatible":
			if settings.Model != "" {
				f.config.LLM.OpenAICompatible.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				f.config.LLM.OpenAICompatible.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				f.config.LLM.OpenAICompatible.BaseURL = settings.BaseURL
			}
		case "chatgpt":
			if settings.Model != "" {
				f.config.LLM.ChatGPT.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				f.config.LLM.ChatGPT.APIKey = settings.APIKey
			}
		}
	}

	if err := f.persistConfig(); err != nil {
		f.log().Warn("failed to persist LLM settings", "error", err)
	}

	// Clear any config load errors since settings are now valid
	f.configLoadErrors = nil

	// Rebuild judge and LLM router via the backend builder so new sessions
	// use the updated provider immediately.
	if f.app != nil {
		bcfg := ToBuilderConfig(f.config)
		f.app.Builder().RebuildJudge(bcfg)
		if err := f.app.Builder().RebuildRouter(bcfg); err != nil {
			f.log().Warn("failed to rebuild LLM router after settings update", "error", err)
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
	if f.app != nil {
		f.app.Builder().UpdateSearchTool(ToBuilderConfig(f.config))
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
		URL:        core.MaskProxyURL(f.config.Proxy.URL),
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
	if f.app != nil {
		bcfg := ToBuilderConfig(f.config)
		if err := f.app.Builder().RebuildProxy(context.Background(), bcfg); err != nil {
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
		DefaultPolicy: f.config.Security.DefaultPolicy,
		ToolPolicies:  make(map[string]ToolPolicyResponse),
	}
	for name, cfg := range f.config.Security.ToolPolicies {
		// Filter out internal tools
		if IsInternalTool(name) {
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
	newPolicies := make(map[string]config.ToolPolicyConfig, len(settings.ToolPolicies))
	for name, policyCfg := range settings.ToolPolicies {
		// Silently skip internal tools
		if IsInternalTool(name) {
			continue
		}
		newPolicies[name] = config.ToolPolicyConfig{
			Policy:    policyCfg.Policy,
			Blacklist: policyCfg.Blacklist,
		}
	}
	f.config.Security.ToolPolicies = newPolicies

	// Apply policies to the shared tool registry via the backend builder.
	if f.app != nil {
		f.app.Builder().UpdateSecurityPolicies(ToBuilderConfig(f.config))
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
// For Anthropic/Gemini: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible/LM Studio: fetches from the provider's API.
func (f *FrontendAPI) ListProviderModels(provider string) ([]string, error) {
	f.configMu.RLock()
	if f.config == nil {
		f.configMu.RUnlock()
		return nil, errors.New("config not initialized")
	}
	if f.app == nil {
		f.configMu.RUnlock()
		return nil, errors.New("application not initialized")
	}
	cfg := ToBuilderConfig(f.config)
	builder := f.app.Builder()
	f.configMu.RUnlock()

	return builder.ListProviderModels(context.Background(), provider, cfg)
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
