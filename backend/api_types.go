package backend

// ---------------------------------------------------------------------------
// DTO types exposed to the frontend via Wails bindings.
// These types were previously defined in the desktop/ package.
// ---------------------------------------------------------------------------

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded             bool              `json:"loaded"`
	LogLevel           string            `json:"log_level"`
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

// FileNode represents a file or directory entry in the workspace tree.
type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// GitStatusEntry describes the git status of a single file.
type GitStatusEntry struct {
	Status string `json:"status"` // "M" or "A"
	Staged bool   `json:"staged"`
}

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	Model             string `json:"model"`
	Family            string `json:"family"`
}

// ToolInfo represents a tool with its metadata, source, and policy for the frontend.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Policy      string `json:"policy"`
}
