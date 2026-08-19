package backend

import (
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/llm"
)

// ---------------------------------------------------------------------------
// DTO types exposed to the frontend via Wails bindings.
// These types were previously defined in the desktop/ package.
// ---------------------------------------------------------------------------

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded       bool                         `json:"loaded"`
	LogLevel     string                       `json:"log_level"`
	ConfigErrors []string                     `json:"config_errors"`
	LLM          ConfigLLMResponse            `json:"llm"`
	Search       ConfigSearchResp             `json:"search"`
	Proxy        ProxySettingsResponse        `json:"proxy"`
	Experimental ExperimentalSettingsResponse `json:"experimental"`
}

// ExperimentalSettingsResponse exposes the master experimental-features switch
// to the settings UI. It carries no feature-specific state by design — the
// switch is all-or-nothing.
type ExperimentalSettingsResponse struct {
	Enabled bool `json:"enabled"`
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
	Vision    bool           `json:"vision"`              // true if model supports image/PDF attachments
	Reasoning *ReasoningInfo `json:"reasoning,omitempty"` // nil = family doesn't support reasoning
}

// ConfigLLMResponse holds sanitised LLM provider info.
type ConfigLLMResponse struct {
	DefaultModel        string                        `json:"default_model"` // global, cross-provider
	Anthropic           ConfigProviderFull            `json:"anthropic"`
	OpenAICompatible    map[string]ConfigProviderFull `json:"openai_compatible"`
	AnthropicCompatible map[string]ConfigProviderFull `json:"anthropic_compatible"`
	ChatGPT             ConfigProviderFull            `json:"chatgpt"`
	AllModels           []ModelInfo                   `json:"all_models"`   // flat list of all enabled models with family + reasoning metadata
	ModelsReady         bool                          `json:"models_ready"` // false during async LLM init; true once registry is wired
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
	DefaultModel        string                           `json:"default_model"`
	Anthropic           *ProviderConfigRequest           `json:"anthropic,omitempty"`
	OpenAICompatible    map[string]ProviderConfigRequest `json:"openai_compatible,omitempty"`
	AnthropicCompatible map[string]ProviderConfigRequest `json:"anthropic_compatible,omitempty"`
	ChatGPT             *ProviderConfigRequest           `json:"chatgpt,omitempty"`
}

// ProviderConfigRequest holds a single provider's configuration.
type ProviderConfigRequest struct {
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// ModelConfigResponse returns a single model's configurable parameters: the
// currently-effective values (override value when set, otherwise the built-in
// default) plus the built-in factory defaults so the UI can show what would
// change. HasOverride is true when an entry exists in config.LLM.Models.
type ModelConfigResponse struct {
	Model                string                `json:"model"`
	ContextWindow        int                   `json:"context_window"`
	OutputLimit          int                   `json:"output_limit"`
	TokenizerType        string                `json:"tokenizer_type"`
	Family               string                `json:"family"`
	Protocol             string                `json:"protocol"`
	Capabilities         llm.ModelCapabilities `json:"capabilities"`
	DefaultContextWindow int                   `json:"default_context_window"`
	DefaultOutputLimit   int                   `json:"default_output_limit"`
	DefaultTokenizerType string                `json:"default_tokenizer_type"`
	DefaultFamily        string                `json:"default_family"`
	DefaultProtocol      string                `json:"default_protocol"`
	DefaultCapabilities  llm.ModelCapabilities `json:"default_capabilities"`
	HasOverride          bool                  `json:"has_override"`
}

// ModelConfigRequest holds the per-model parameter overrides submitted from the
// Configure dialog. The backend stores only fields that differ from the
// built-in default (and removes the entry entirely when everything matches).
//
// TokenizerType/Family/Protocol use "" as "inherit": SetModelConfig records the
// built-in default (also "") for a matching value, so no override entry is
// persisted. Capabilities uses a nil pointer as "inherit": nil records the
// built-in capability set (no override persisted), while a non-nil value
// overrides all four flags atomically.
type ModelConfigRequest struct {
	ContextWindow int                    `json:"context_window"`
	OutputLimit   int                    `json:"output_limit"`
	TokenizerType string                 `json:"tokenizer_type"`
	Family        string                 `json:"family"`
	Protocol      string                 `json:"protocol"`
	Capabilities  *llm.ModelCapabilities `json:"capabilities,omitempty"`
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

// SecuritySettingsResponse holds security settings for the frontend. The
// tool-security schema is group-based (security.groups): the frontend edits
// exactly the seven configurable groups — per-tool policies no longer exist.
type SecuritySettingsResponse struct {
	// Groups maps the configurable tool-group names (config.ToolGroup*) to
	// their policy (and, for the "execute" group only, a command blacklist).
	// GetSecuritySettings always returns the full set of seven groups;
	// UpdateSecuritySettings replaces the stored set with what it receives.
	Groups                     map[string]GroupPolicyResponse `json:"groups"`
	AutoApproveWorkspaceWrites bool                           `json:"auto_approve_workspace_writes"`
	// SmartApprove enables the strict OWASP ASI judge for effective
	// user_confirm calls. Only a strict ALLOW skips UI; every other outcome
	// still requires the user. Default: false.
	SmartApprove bool `json:"smart_approve"`
	// JudgeAvailable reports whether the strict judge is configured and ready.
	// Read-only: the frontend uses it to disable the Smart Approve toggle when
	// no judge is operational (e.g. no LLM model configured). Always sent; the
	// backend ignores any incoming value during updates.
	JudgeAvailable bool `json:"judge_available"`
	// ExecuteBlacklistDefaults carries the shipped default patterns for the
	// execute group's command blacklist. Read-only: the settings UI offers
	// them as a one-click restore, so a user who emptied the blacklist can
	// return to the default posture without re-typing patterns — saving them
	// hits the store-as-unset rule (config.StoreDefaultBlacklistAsUnset) and
	// lands the config back in the track-defaults state. Always sent; the
	// backend ignores any incoming value during updates.
	ExecuteBlacklistDefaults []string `json:"execute_blacklist_defaults"`
}

// GroupPolicyResponse holds one tool group's policy for the frontend. It
// mirrors config.GroupPolicyConfig: a policy from the group enum
// ("allow"|"user_confirm"|"deny") and — execute group only — a regex
// blacklist of shell commands forced to confirmation.
type GroupPolicyResponse struct {
	Policy string `json:"policy"`
	// Blacklist is serialized WITHOUT omitempty: for the execute group the
	// nil-vs-empty distinction is meaningful (nil = unset, the shipped
	// defaults are in force; empty = explicitly no patterns) and must
	// survive the JSON round trip — the settings UI echoes
	// GetSecuritySettings output straight back into UpdateSecuritySettings
	// on every save. Non-execute groups serialize null, which the update
	// path ignores.
	Blacklist []string `json:"blacklist"`
}

// SmallLLMConfigResponse is the small-LLM profile configuration exposed to the
// UI. It mirrors config.SmallLLMConfig with JSON (snake_case) tags and is used
// for both GetSmallLLMConfig (read) and UpdateSmallLLMConfig (write). There
// are no secrets to mask, so a single type serves both directions.
type SmallLLMConfigResponse struct {
	Enabled        bool                       `json:"enabled"`
	EssentialTools SmallLLMEssentialToolsResp `json:"essential_tools"`
	SystemPrompt   SmallLLMSystemPromptResp   `json:"system_prompt"`
	Sampling       SmallLLMSamplingResp       `json:"sampling"`
	LoopHardening  SmallLLMLoopHardeningResp  `json:"loop_hardening"`
	Context        SmallLLMContextResp        `json:"context"`
}

// SmallLLMEssentialToolsResp is the always-present tool-subset variant.
// ProtectedTools is read-only informational: the backend always includes the
// protected set regardless of any UI selection, so the UI can render those
// tools as locked. It is ignored on write.
type SmallLLMEssentialToolsResp struct {
	Enabled             bool     `json:"enabled"`
	AlwaysPresent       []string `json:"always_present"`
	MaxTools            int      `json:"max_tools"`
	CompactDescriptions bool     `json:"compact_descriptions"`
	ProtectedTools      []string `json:"protected_tools"`
}

// SmallLLMSystemPromptResp is the prompt-simplification variant.
type SmallLLMSystemPromptResp struct {
	Lite              bool `json:"lite"`
	FewShot           bool `json:"few_shot"`
	ReasoningScaffold bool `json:"reasoning_scaffold"`
}

// SmallLLMSamplingResp is the sampling-override variant. Zero numeric values
// mean "inherit the vendor preset" (not "send 0").
type SmallLLMSamplingResp struct {
	Enabled           bool    `json:"enabled"`
	Temperature       float64 `json:"temperature"`
	TopP              float64 `json:"top_p"`
	TopK              int     `json:"top_k"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	ReasoningEffort   string  `json:"reasoning_effort"`
}

// SmallLLMLoopHardeningResp is the tightened circuit-breaker variant.
type SmallLLMLoopHardeningResp struct {
	Enabled                      bool `json:"enabled"`
	RepeatNudgeThreshold         int  `json:"repeat_nudge_threshold"`
	ParseErrorAbortThreshold     int  `json:"parse_error_abort_threshold"`
	FruitlessNudgeThreshold      int  `json:"fruitless_nudge_threshold"`
	FruitlessAbortThreshold      int  `json:"fruitless_abort_threshold"`
	SameToolRepeatNudgeThreshold int  `json:"same_tool_repeat_nudge_threshold"`
}

// SmallLLMContextResp is the aggressive context-management variant.
type SmallLLMContextResp struct {
	Enabled             bool                   `json:"enabled"`
	Compaction          SmallLLMCompactionResp `json:"compaction"`
	ToolOutputKeepLastN int                    `json:"tool_output_keep_last_n"`
	OutputTokenReserve  int                    `json:"output_token_reserve"`
}

// SmallLLMCompactionResp holds the compaction-tightening overrides of the
// context variant.
type SmallLLMCompactionResp struct {
	KeepLast       int `json:"keep_last"`
	BlockSize      int `json:"block_size"`
	TriggerPercent int `json:"trigger_percent"`
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

// BranchBase represents a ref usable as a start-point for CreateBranch.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type BranchBase = workspace.BranchBase

// CommitFile describes a single file changed by a commit.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type CommitFile = workspace.CommitFile

// StashEntry describes a single entry in the stash list.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type StashEntry = workspace.StashEntry

// GitHistoryCommit describes a commit for the unified history+graph view.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type GitHistoryCommit = workspace.GitHistoryCommit

// HunkDiffInfo describes a single diff hunk with staging status and raw
// diff text. Defined in core/workspace; re-exported here as a type alias
// for ViewModel convenience.
type HunkDiffInfo = workspace.HunkDiffInfo

// MergeRebaseState reports whether a merge or rebase is in progress.
// Defined in core/workspace; re-exported here as a type alias for ViewModel convenience.
type MergeRebaseState = workspace.MergeRebaseState

// ReviewHunk describes a single unified-diff hunk for the code-review page.
// Defined in core/workspace; re-exported here as a type alias for ViewModel
// convenience.
type ReviewHunk = workspace.ReviewHunk

// ReviewFileDiff groups the uncommitted hunks of a single file for the
// code-review page. Defined in core/workspace; re-exported here as a type
// alias for ViewModel convenience.
type ReviewFileDiff = workspace.ReviewFileDiff

// ReviewPromptMessage is the descriptor of a persisted review_prompt chat
// message, returned by SaveReviewPrompt. The frontend uses Content for the
// live (pre-reload) card so the displayed text always matches the persisted
// message — the backend is the single source of truth for the prompt wording,
// so the string is not duplicated on the client.
type ReviewPromptMessage struct {
	PromptID string `json:"prompt_id"`
	Content  string `json:"content"`
}

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	Model             string  `json:"model"`
	Family            string  `json:"family"`
	FillPercent       float64 `json:"fill_percent"`
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

// ToolInfo represents a tool with its metadata, source, security group, and
// effective policy for the frontend. Policy is derived from the tool's GROUP
// on the live registry — per-tool configuration does not exist, so the value
// is display-only.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	// Group is the tool's capability group (one of the config.ToolGroup*
	// names except the reserved "system" group — system tools are filtered
	// out of the list entirely).
	Group string `json:"group"`
	// Policy is the effective group policy ("allow"|"user_confirm"|"deny")
	// enforced by the shared tool registry for this tool's group.
	Policy string `json:"policy"`
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
	Attachments     []BlackboardAttachmentResponse    `json:"attachments"`
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

// BlackboardAttachmentResponse is a metadata-only view of a user-attached file
// for the blackboard viewer. The markdown content is intentionally excluded so
// large attachments do not bloat the API response.
type BlackboardAttachmentResponse struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	Format       string `json:"format"`
	SizeBytes    int64  `json:"size_bytes"`
	AttachedAt   string `json:"attached_at"`
}

// SkillDescriptorDTO is a lightweight skill descriptor exposed to the frontend.
type SkillDescriptorDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AgentDescriptorDTO is a lightweight Subagent Profile descriptor exposed to
// the frontend for #-autocomplete. Mirrors SkillDescriptorDTO.
type AgentDescriptorDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
