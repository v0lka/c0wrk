package desktop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/user/agent/backend"
	"github.com/user/agent/backend/config"
)

// maskedAPIKey is the placeholder returned for configured API keys in the UI.
const maskedAPIKey = "***configured***"

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded             bool              `json:"loaded"`
	LogLevel           string            `json:"log_level"`
	Theme              string            `json:"theme"`
	ConfigMigrated     bool              `json:"config_migrated"`
	ConfigMigrationMsg string            `json:"config_migration_msg"`
	ConfigErrors       []string          `json:"config_errors"`
	LLM                ConfigLLMResponse `json:"llm"`
	Memory             ConfigMemResponse `json:"memory"`
	Search             ConfigSearchResp  `json:"search"`
}

// ConfigLLMResponse holds sanitised LLM provider info.
type ConfigLLMResponse struct {
	ActiveProvider   string                 `json:"active_provider"`
	Anthropic        ConfigProviderKeyModel `json:"anthropic"`
	Gemini           ConfigProviderKeyModel `json:"gemini"`
	LMStudio         ConfigProviderFull     `json:"lmstudio"`
	OpenAICompatible ConfigProviderFull     `json:"openai_compatible"`
	ChatGPT          ConfigProviderKeyModel `json:"chatgpt"`
}

// ConfigProviderKeyModel is a provider with api_key + model.
type ConfigProviderKeyModel struct {
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

// ConfigProviderFull is a provider with base_url + api_key + model.
type ConfigProviderFull struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// ConfigMemResponse holds memory section of config response.
type ConfigMemResponse struct {
	Database string `json:"database"`
}

// ConfigSearchResp holds search config values.
type ConfigSearchResp struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// LLMSettingsRequest holds LLM settings from the frontend.
type LLMSettingsRequest struct {
	ActiveProvider string `json:"active_provider"`
	APIKey         string `json:"api_key"`
	BaseURL        string `json:"base_url"`
	Model          string `json:"model"`
}

// SearchSettingsRequest holds search settings from the frontend.
type SearchSettingsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// SecuritySettingsResponse holds security settings for the frontend.
type SecuritySettingsResponse struct {
	DefaultPolicy string                        `json:"default_policy"`
	ToolPolicies  map[string]ToolPolicyResponse `json:"tool_policies"`
}

// ToolPolicyResponse holds per-tool policy for the frontend.
type ToolPolicyResponse struct {
	Policy    string   `json:"policy"`
	Blacklist []string `json:"blacklist,omitempty"`
}

// GetConfig returns the current configuration (sanitized, no raw API keys).
func (a *App) GetConfig() ConfigResponse {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return ConfigResponse{Loaded: false}
	}

	return ConfigResponse{
		Loaded:             true,
		LogLevel:           a.config.LogLevel,
		Theme:              a.config.Theme,
		ConfigMigrated:     a.configMigrated,
		ConfigMigrationMsg: a.configMigrationMsg,
		ConfigErrors:       nonNilStringSlice(a.configLoadErrors),
		LLM: ConfigLLMResponse{
			ActiveProvider: a.config.LLM.ActiveProvider,
			Anthropic: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.Anthropic.APIKey),
				Model:  a.config.LLM.Anthropic.Model,
			},
			Gemini: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.Gemini.APIKey),
				Model:  a.config.LLM.Gemini.Model,
			},
			LMStudio: ConfigProviderFull{
				BaseURL: a.config.LLM.LMStudio.BaseURL,
				APIKey:  maskAPIKey(a.config.LLM.LMStudio.APIKey),
				Model:   a.config.LLM.LMStudio.Model,
			},
			OpenAICompatible: ConfigProviderFull{
				BaseURL: a.config.LLM.OpenAICompatible.BaseURL,
				APIKey:  maskAPIKey(a.config.LLM.OpenAICompatible.APIKey),
				Model:   a.config.LLM.OpenAICompatible.Model,
			},
			ChatGPT: ConfigProviderKeyModel{
				APIKey: maskAPIKey(a.config.LLM.ChatGPT.APIKey),
				Model:  a.config.LLM.ChatGPT.Model,
			},
		},
		Memory: ConfigMemResponse{
			Database: a.config.Memory.Database,
		},
		Search: ConfigSearchResp{
			Provider: a.config.Search.Provider,
			APIKey:   maskAPIKey(a.config.Search.APIKey),
		},
	}
}

// UpdateLLMSettings updates LLM active provider and model settings.
func (a *App) UpdateLLMSettings(settings LLMSettingsRequest) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	// Update active provider
	if settings.ActiveProvider != "" {
		if !config.ValidProviders[settings.ActiveProvider] {
			return fmt.Errorf("active_provider %q is not a valid provider", settings.ActiveProvider)
		}
		a.config.LLM.ActiveProvider = settings.ActiveProvider
	}

	// Update model on the active provider
	if a.config.LLM.ActiveProvider != "" {
		switch a.config.LLM.ActiveProvider {
		case "anthropic":
			if settings.Model != "" {
				a.config.LLM.Anthropic.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				a.config.LLM.Anthropic.APIKey = settings.APIKey
			}
		case "gemini":
			if settings.Model != "" {
				a.config.LLM.Gemini.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				a.config.LLM.Gemini.APIKey = settings.APIKey
			}
		case "lmstudio":
			if settings.Model != "" {
				a.config.LLM.LMStudio.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				a.config.LLM.LMStudio.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				a.config.LLM.LMStudio.BaseURL = settings.BaseURL
			}
		case "openai_compatible":
			if settings.Model != "" {
				a.config.LLM.OpenAICompatible.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				a.config.LLM.OpenAICompatible.APIKey = settings.APIKey
			}
			if settings.BaseURL != "" {
				a.config.LLM.OpenAICompatible.BaseURL = settings.BaseURL
			}
		case "chatgpt":
			if settings.Model != "" {
				a.config.LLM.ChatGPT.Model = settings.Model
			}
			if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
				a.config.LLM.ChatGPT.APIKey = settings.APIKey
			}
		}
	}

	if err := a.persistConfig(); err != nil {
		a.log().Warn("failed to persist LLM settings", "error", err)
	}

	// Clear any config load errors since settings are now valid
	a.configLoadErrors = nil

	// Rebuild judge and LLM router via the backend builder so new sessions
	// use the updated provider immediately.
	if a.app != nil {
		bcfg := backend.ToBuilderConfig(a.config)
		a.app.Builder().RebuildJudge(bcfg)
		if err := a.app.Builder().RebuildRouter(bcfg); err != nil {
			a.log().Warn("failed to rebuild LLM router after settings update", "error", err)
		}
	}

	return nil
}

// UpdateSearchSettings updates search configuration.
func (a *App) UpdateSearchSettings(settings SearchSettingsRequest) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	a.config.Search.Provider = settings.Provider
	// Only update API key if it's not the masked placeholder
	if settings.APIKey != "" && settings.APIKey != maskedAPIKey {
		a.config.Search.APIKey = settings.APIKey
	}

	if err := a.persistConfig(); err != nil {
		a.log().Warn("failed to persist search settings", "error", err)
	}

	// Rebuild web search tool via the backend builder.
	if a.app != nil {
		a.app.Builder().UpdateSearchTool(backend.ToBuilderConfig(a.config))
	}

	return nil
}

// GetSecuritySettings returns current security settings for the UI.
// Internal tools are filtered out from the policy map.
func (a *App) GetSecuritySettings() SecuritySettingsResponse {
	a.configMu.RLock()
	defer a.configMu.RUnlock()

	if a.config == nil {
		return SecuritySettingsResponse{DefaultPolicy: "user_confirm"}
	}
	resp := SecuritySettingsResponse{
		DefaultPolicy: a.config.Security.DefaultPolicy,
		ToolPolicies:  make(map[string]ToolPolicyResponse),
	}
	for name, cfg := range a.config.Security.ToolPolicies {
		// Filter out internal tools
		if backend.IsInternalTool(name) {
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
func (a *App) UpdateSecuritySettings(settings SecuritySettingsResponse) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	if a.config == nil {
		return errors.New("config not initialized")
	}

	// Update config — replace the full policy set so config stays in sync
	// with the registry (the frontend always sends the complete set).
	a.config.Security.DefaultPolicy = settings.DefaultPolicy
	newPolicies := make(map[string]config.ToolPolicyConfig, len(settings.ToolPolicies))
	for name, policyCfg := range settings.ToolPolicies {
		// Silently skip internal tools
		if backend.IsInternalTool(name) {
			continue
		}
		newPolicies[name] = config.ToolPolicyConfig{
			Policy:    policyCfg.Policy,
			Blacklist: policyCfg.Blacklist,
		}
	}
	a.config.Security.ToolPolicies = newPolicies

	// Apply policies to the shared tool registry via the backend builder.
	if a.app != nil {
		a.app.Builder().UpdateSecurityPolicies(backend.ToBuilderConfig(a.config))
	}

	if err := a.persistConfig(); err != nil {
		a.log().Warn("failed to persist security settings", "error", err)
	}

	return nil
}

// GetLogLevel returns the current log level.
func (a *App) GetLogLevel() string {
	a.configMu.RLock()
	defer a.configMu.RUnlock()
	return a.logLevel
}

// SetLogLevel sets the log level dynamically.
func (a *App) SetLogLevel(level string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	// Validate the level
	level = strings.ToUpper(level)
	switch level {
	case "DEBUG", "INFO", "WARN", "ERROR":
		a.logLevel = level
		if a.manager != nil {
			a.manager.SetLogLevel(level)
		}
		a.config.LogLevel = level
		if err := a.persistConfig(); err != nil {
			a.log().Warn("failed to persist log level change", "error", err)
		}
		return nil
	default:
		return fmt.Errorf("invalid log level: %s", level)
	}
}

// SetTheme sets the UI theme and persists to config.
func (a *App) SetTheme(theme string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()

	switch theme {
	case "light", "dark", "system":
		a.config.Theme = theme
		return a.persistConfig()
	default:
		return fmt.Errorf("invalid theme: %s (must be light, dark, or system)", theme)
	}
}

// ListProviderModels returns available model names for a given provider.
// For Anthropic/Gemini: returns hardcoded list from model registry.
// For ChatGPT/OpenAI Compatible/LM Studio: fetches from the provider's API.
func (a *App) ListProviderModels(provider string) ([]string, error) {
	a.configMu.RLock()
	if a.config == nil {
		a.configMu.RUnlock()
		return nil, errors.New("config not initialized")
	}
	if a.app == nil {
		a.configMu.RUnlock()
		return nil, errors.New("application not initialized")
	}
	cfg := backend.ToBuilderConfig(a.config)
	builder := a.app.Builder()
	a.configMu.RUnlock()

	return builder.ListProviderModels(context.Background(), provider, cfg)
}

// persistConfig saves the current in-memory config to disk.
func (a *App) persistConfig() error {
	if a.configPath == "" || a.config == nil {
		return errors.New("config path or config not set")
	}
	return config.Save(a.config, a.configPath)
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
