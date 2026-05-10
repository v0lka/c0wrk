package backend

// ---------------------------------------------------------------------------
// DTO types exposed to the frontend via Wails bindings.
// These types were previously defined in the desktop/ package.
// ---------------------------------------------------------------------------

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded       bool              `json:"loaded"`
	LogLevel     string            `json:"log_level"`
	ConfigErrors []string          `json:"config_errors"`
	LLM          ConfigLLMResponse `json:"llm"`
	Memory       ConfigMemResponse `json:"memory"`
	Search       ConfigSearchResp  `json:"search"`
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
	Name       string `json:"name"`
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir"`
	Icon       string `json:"icon"`
	IconColor  string `json:"icon_color"`
	Hidden     bool   `json:"hidden"`
	GitIgnored bool   `json:"gitignored"`
}

// FileIconResponse holds the icon and color for a single file or directory.
type FileIconResponse struct {
	Icon      string `json:"icon"`
	IconColor string `json:"icon_color"`
}

// GitStatusEntry describes the git status of a single file.
type GitStatusEntry struct {
	Status string `json:"status"` // "M", "A", "R", "C", or "U"
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

// VectorStoreEntry represents a single chunk from the vector store for the frontend.
type VectorStoreEntry struct {
	FilePath  string  `json:"file_path"`
	FileName  string  `json:"file_name"`
	Content   string  `json:"content"`
	Score     float32 `json:"score"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Language  string  `json:"language"`
}

// OptimizePromptResponse holds the result of prompt optimization for the frontend.
type OptimizePromptResponse struct {
	OptimizedPrompt string   `json:"optimized_prompt"`
	Keywords        []string `json:"keywords"`
	UsedContext     bool     `json:"used_context"`
}

// ---------------------------------------------------------------------------
// Blackboard Viewer DTOs
// ---------------------------------------------------------------------------

// BlackboardStateResponse holds the current blackboard state for the frontend viewer.
type BlackboardStateResponse struct {
	TaskID          string                            `json:"task_id"`
	SessionID       string                            `json:"session_id"`
	Status          string                            `json:"status"` // "in_progress", "completed", "failed"
	OriginalRequest string                            `json:"original_request"`
	Plan            *BlackboardPlanResponse           `json:"plan,omitempty"`
	StepResults     map[string]BlackboardStepResponse `json:"step_results"`
	Reflections     []BlackboardReflectionResponse    `json:"reflections"`
	Facts           []BlackboardFactResponse          `json:"facts"`
	FinalOutput     string                            `json:"final_output,omitempty"`
}

// BlackboardPlanResponse is a simplified plan for the blackboard viewer.
type BlackboardPlanResponse struct {
	Steps []BlackboardPlanStepResponse `json:"steps"`
}

// BlackboardPlanStepResponse is a simplified plan step for the blackboard viewer.
type BlackboardPlanStepResponse struct {
	ID          string   `json:"id"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	DependsOn   []string `json:"depends_on"`
}

// BlackboardStepResponse is a simplified step result for the blackboard viewer.
type BlackboardStepResponse struct {
	StepID  string `json:"step_id"`
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

// BlackboardReflectionResponse is a reflection entry for the blackboard viewer.
type BlackboardReflectionResponse struct {
	Summary         string   `json:"summary"`
	Hypotheses      []string `json:"hypotheses,omitempty"`
	SuggestedAction string   `json:"suggested_action,omitempty"`
	Reasoning       string   `json:"reasoning,omitempty"`
	FailureAnalysis string   `json:"failure_analysis,omitempty"`
	RootCause       string   `json:"root_cause,omitempty"`
	ActionPlan      string   `json:"action_plan,omitempty"`
	Timestamp       string   `json:"timestamp"`
}

// BlackboardFactResponse is a fact entry for the blackboard viewer.
type BlackboardFactResponse struct {
	Keywords []string `json:"keywords"`
	Content  string   `json:"content"`
	Author   string   `json:"author"`
}
