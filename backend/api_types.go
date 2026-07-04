package backend

import "github.com/v0lka/c0wrk/core/workspace"

// ---------------------------------------------------------------------------
// DTO types exposed to the frontend via Wails bindings.
// These types were previously defined in the desktop/ package.
// ---------------------------------------------------------------------------

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded       bool                  `json:"loaded"`
	LogLevel     string                `json:"log_level"`
	ConfigErrors []string              `json:"config_errors"`
	LLM          ConfigLLMResponse     `json:"llm"`
	Search       ConfigSearchResp      `json:"search"`
	Proxy        ProxySettingsResponse `json:"proxy"`
}

// ReasoningInfo holds native reasoning options for a model family.
type ReasoningInfo struct {
	Options []string `json:"options"` // native reasoning values (e.g. ["minimal", "low", "medium", "high"])
	Default string   `json:"default"` // family default (e.g. "high")
}

// ModelInfo pairs a model name with its resolved family and reasoning metadata.
// Provider is the config key of the provider that exposes this model
// ("anthropic", "chatgpt", or a named openai_compatible provider). The composite
// model identifier "Provider/Name" uniquely identifies the (provider, model)
// pair so the frontend can disambiguate models that share the same bare Name
// across providers, while still displaying the bare Name to the user.
type ModelInfo struct {
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Family    string         `json:"family"`
	Reasoning *ReasoningInfo `json:"reasoning,omitempty"` // nil = family doesn't support reasoning
}

// ConfigLLMResponse holds sanitised LLM provider info.
type ConfigLLMResponse struct {
	DefaultModel     string                        `json:"default_model"` // global, cross-provider
	Anthropic        ConfigProviderFull            `json:"anthropic"`
	OpenAICompatible map[string]ConfigProviderFull `json:"openai_compatible"`
	ChatGPT          ConfigProviderFull            `json:"chatgpt"`
	AllModels        []ModelInfo                   `json:"all_models"`   // flat list of all enabled models with family + reasoning metadata
	ModelsReady      bool                          `json:"models_ready"` // false during async LLM init; true once registry is wired
}

// ConfigProviderFull is a provider with api_key, optional base_url, and enabled models list.
type ConfigProviderFull struct {
	APIKey  string   `json:"api_key"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models"` // enabled models for this provider
}

// ConfigSearchResp holds search config values.
type ConfigSearchResp struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// LLMSettingsRequest holds LLM settings from the frontend.
type LLMSettingsRequest struct {
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"` // enabled models for the current provider being edited
}

// LLMFullConfigRequest is the full LLM configuration payload for UpdateLLMConfig.
type LLMFullConfigRequest struct {
	DefaultModel     string                           `json:"default_model"`
	Anthropic        *ProviderConfigRequest           `json:"anthropic,omitempty"`
	OpenAICompatible map[string]ProviderConfigRequest `json:"openai_compatible,omitempty"`
	ChatGPT          *ProviderConfigRequest           `json:"chatgpt,omitempty"`
}

// ProviderConfigRequest holds a single provider's configuration.
type ProviderConfigRequest struct {
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// SearchSettingsRequest holds search settings from the frontend.
type SearchSettingsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// ProxySettingsResponse holds proxy settings for the frontend.
type ProxySettingsResponse struct {
	Enabled    bool     `json:"enabled"`
	URL        string   `json:"url"` // password masked in response
	BypassList []string `json:"bypass_list"`
	TLSCertDir string   `json:"tls_cert_dir"`
}

// ProxySettingsRequest holds proxy settings from the frontend.
type ProxySettingsRequest struct {
	Enabled    bool     `json:"enabled"`
	URL        string   `json:"url"`
	BypassList []string `json:"bypass_list"`
	TLSCertDir string   `json:"tls_cert_dir"`
}

// SecuritySettingsResponse holds security settings for the frontend.
type SecuritySettingsResponse struct {
	DefaultPolicy              string                        `json:"default_policy"`
	ToolPolicies               map[string]ToolPolicyResponse `json:"tool_policies"`
	AutoApproveWorkspaceWrites bool                          `json:"auto_approve_workspace_writes"`
}

// ToolPolicyResponse holds per-tool policy for the frontend.
type ToolPolicyResponse struct {
	Policy    string   `json:"policy"`
	Blacklist []string `json:"blacklist,omitempty"`
}

// FileNode represents a file or directory entry in the workspace tree.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type FileNode = workspace.FileNode

// FileIconResponse holds the icon and color for a single file or directory.
type FileIconResponse struct {
	Icon      string `json:"icon"`
	IconColor string `json:"icon_color"`
}

// GitStatusEntry describes the git status of a single file.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type GitStatusEntry = workspace.GitStatusEntry

// DiffStat reports the number of added and deleted lines in a diff.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type DiffStat = workspace.DiffStat

// Branch represents a local git branch.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type Branch = workspace.Branch

// BranchInfo describes the current branch and its upstream tracking state.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type BranchInfo = workspace.BranchInfo

// CommitInfo describes a single commit in the repository history.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type CommitInfo = workspace.CommitInfo

// CommitFile describes a single file changed by a commit.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type CommitFile = workspace.CommitFile

// StashEntry describes a single entry in the stash list.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type StashEntry = workspace.StashEntry

// GraphCommit describes a commit for graph visualization.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type GraphCommit = workspace.GraphCommit

// HunkRange identifies a contiguous slice of a file in old-file line coordinates.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type HunkRange = workspace.HunkRange

// MergeRebaseState reports whether a merge or rebase is in progress.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type MergeRebaseState = workspace.MergeRebaseState

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	Model             string `json:"model"`
	Family            string `json:"family"`
}

// ProjectUIStateRequest is the payload used to persist project switch UI state.
type ProjectUIStateRequest struct {
	ProjectID      string   `json:"project_id"`
	SavedSessionID string   `json:"saved_session_id"`
	OpenTabs       []string `json:"open_tabs"`
	ActiveFile     string   `json:"active_file"`
}

// ProjectUIStateResponse is the persisted project switch UI state returned to the frontend.
type ProjectUIStateResponse struct {
	ProjectID      string   `json:"project_id"`
	SavedSessionID string   `json:"saved_session_id"`
	OpenTabs       []string `json:"open_tabs"`
	ActiveFile     string   `json:"active_file"`
	UpdatedAt      string   `json:"updated_at"`
}

// ToolInfo represents a tool with its metadata, source, and policy for the frontend.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Policy      string `json:"policy"`
}

// VectorIndexStatus describes the current state of the vector index for the frontend.
type VectorIndexStatus struct {
	State        string   `json:"state"`
	Progress     float64  `json:"progress"`
	FilesIndexed int      `json:"files_indexed"`
	TotalFiles   int      `json:"total_files"`
	CurrentFile  string   `json:"current_file"`
	Branch       string   `json:"branch"`
	Phase        string   `json:"phase"`   // "both" | "embedding" | "lexical"
	Indices      []string `json:"indices"` // e.g. ["vector", "lexical"]
}

// VectorStoreEntry represents a single chunk from the vector store for the frontend.
//
// VectorScore/LexicalScore/VectorRank/LexicalRank are optional per-side
// attribution fields populated by hybrid (RRF) and per-side searches.
// Pure vector results populate VectorScore/VectorRank; pure lexical
// results populate LexicalScore/LexicalRank; hybrid results may populate
// any subset depending on which retriever returned the document.
type VectorStoreEntry struct {
	FilePath     string  `json:"file_path"`
	FileName     string  `json:"file_name"`
	Content      string  `json:"content"`
	Score        float32 `json:"score"`
	StartLine    int     `json:"start_line"`
	EndLine      int     `json:"end_line"`
	Language     string  `json:"language"`
	VectorScore  float32 `json:"vector_score,omitempty"`
	LexicalScore float32 `json:"lexical_score,omitempty"`
	VectorRank   int     `json:"vector_rank,omitempty"`
	LexicalRank  int     `json:"lexical_rank,omitempty"`
}

// SearchRequest is the request payload for SearchVectorStore.
//
// Mode accepts "hybrid" | "vector" | "lexical"; empty/unknown defaults
// to "hybrid" (with auto-fallback to vector-only when the lexical index
// is empty or unavailable).
//
// FilePattern is a doublestar glob against the chunk's file_path (e.g.
// "**/*.go", "src/**"). MustMatch is a list of literal substrings that
// must all appear in a chunk's content for it to be returned.
type SearchRequest struct {
	Query       string   `json:"query"`
	TopK        int      `json:"top_k"`
	FilePattern string   `json:"file_pattern"`
	MustMatch   []string `json:"must_match"`
	Mode        string   `json:"mode"`
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

// SkillDescriptorDTO is a lightweight skill descriptor exposed to the frontend.
type SkillDescriptorDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
