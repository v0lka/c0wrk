package backend

import (
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/llm"
)

// ConfigResponse is the typed response for GetConfig, with sanitized (masked) API keys.
type ConfigResponse struct {
	Loaded        bool                          `json:"loaded"`
	LogLevel      string                        `json:"log_level"`
	ConfigErrors  []string                      `json:"config_errors"`
	LLM           ConfigLLMResponse             `json:"llm"`
	Search        ConfigSearchResp              `json:"search"`
	Proxy         ProxySettingsResponse         `json:"proxy"`
	Experimental  ExperimentalSettingsResponse  `json:"experimental"`
	ModelProfiles ModelProfilesSettingsResponse `json:"model_profiles"`
	// PromptSuggestions mirrors prompt_suggestions.enabled — see
	// PromptSuggestionsSettingsResponse.
	PromptSuggestions PromptSuggestionsSettingsResponse `json:"prompt_suggestions"`
	VectorIndex       VectorIndexSettingsResponse       `json:"vector_index"`
}

// PromptSuggestionsSettingsResponse exposes the prompt_suggestions.enabled
// gate via SuggestionsEnabled() — nil config = on, unloaded config = false
// (ADR-088, specs/domains/prompt-suggestions.md).
type PromptSuggestionsSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

// ExperimentalSettingsResponse exposes the all-or-nothing experimental master
// switch (ADR-068); Model Profiles carries its own toggle
// (model_profiles.enabled) and is not gated by it.
type ExperimentalSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

// ModelProfilesSettingsResponse exposes the EFFECTIVE (resolved) Model
// Profiles state for the settings UI (specs/domains/model-profiles.md). The
// identity fields are filled fresh from pure in-memory reads so a background
// consumer can read them WITHOUT calling GetModelProfiles — that RPC is a
// consuming read that drains one-shot profile notices meant for Settings.
type ModelProfilesSettingsResponse struct {
	Enabled               bool `json:"enabled"`
	EssentialToolsEnabled bool `json:"essential_tools_enabled"`
	// ActiveProfileID is the STORED active profile id, verbatim (dangling ids
	// stay visible; the explanation surfaces via GetModelProfiles).
	ActiveProfileID string `json:"active_profile,omitempty"`
	// ResolvedProfileID is what the stored active id actually resolved to
	// (same id, retired-spelling replacement, or "generic" soft fallback) —
	// value-wise decisions must compare against THIS id, never the verbatim
	// one. Served from the cached gate so GetConfig stays in-memory-only.
	ResolvedProfileID string `json:"resolved_profile_id,omitempty"`
	// SuggestedProfileID is the predefined profile the default model suggests
	// (advisory only; never auto-applied).
	SuggestedProfileID *string `json:"suggested_profile_id,omitempty"`
}

// ReasoningInfo holds native reasoning options for a model family.
type ReasoningInfo struct {
	Options []string `json:"options"` // native reasoning values (e.g. ["minimal", "low", "medium", "high"])
	Default string   `json:"default"` // family default (e.g. "high")
}

// ModelInfo pairs a model name with its resolved family and reasoning
// metadata. The composite "Provider/Name" identifier disambiguates models
// sharing a bare Name across providers while the UI still shows the bare Name.
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
	// AutoRetryMaxSeconds is the inclusive per-provider auto_retry_seconds
	// bound the same as validate()/UpdateLLMConfig enforce (ADR-065), so the
	// form clamps against the backend's real limit instead of a constant.
	AutoRetryMaxSeconds int `json:"auto_retry_max_seconds"`
}

// ConfigProviderFull is a provider with api_key, optional base_url, and enabled models list.
type ConfigProviderFull struct {
	APIKey  string   `json:"api_key"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models"` // enabled models for this provider
	// TLSFingerprint is the per-provider SPKI pin (ADR-054): non-empty = only
	// the pinned key is accepted; empty = system CA verification.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	// AutoRetrySeconds is the per-provider auto-retry interval, 0 = disabled
	// (fixed anthropic/chatgpt providers always report 0).
	AutoRetrySeconds int `json:"auto_retry_seconds,omitempty"`
	// TimeoutClass is the adaptive budget's class override (ADR-071 D3):
	// "local" | "remote"; empty = infer.
	TimeoutClass string `json:"timeout_class,omitempty"`
	// AuthMode is the chatgpt provider's auth mode: "api_key" (default) |
	// "oauth" (ADR-074); every other provider leaves it empty.
	AuthMode string `json:"auth_mode,omitempty"`
}

// ConfigSearchResp holds search config values.
type ConfigSearchResp struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"`
}

// VectorIndexSettingsResponse holds the vector-index settings for the
// frontend. The embedding half (execution provider "auto" | "cpu" | "cuda" |
// "openvino" and the GPU device index) requires an app restart to take
// effect — the embedder is created once per process — so its update path
// only validates and persists. The indexing-policy half (enabled /
// exclude_patterns / index_submodules / respect_gitignore / respect_aiignore)
// is served RESOLVED (pointer-bool defaults applied) and is live-applied by
// UpdateIndexingSettings.
type VectorIndexSettingsResponse struct {
	ExecutionProvider string `json:"execution_provider"`
	DeviceID          int    `json:"device_id"`
	// Enabled is the resolved vector_index.enabled gate (nil = true).
	Enabled bool `json:"enabled"`
	// ExcludePatterns mirrors vector_index.exclude_patterns (nil = none).
	ExcludePatterns []string `json:"exclude_patterns"`
	// IndexSubmodules is the resolved vector_index.index_submodules gate
	// (nil = false: submodules stay out of the index unless explicitly
	// opted in).
	IndexSubmodules bool `json:"index_submodules"`
	// RespectGitIgnore / RespectAIIgnore are the resolved
	// vector_index.respect_gitignore / respect_aiignore gates (nil = true).
	RespectGitIgnore bool `json:"respect_gitignore"`
	RespectAIIgnore  bool `json:"respect_aiignore"`
}

// IndexingSettingsRequest is the UpdateIndexingSettings payload: a full
// replacement of the vector-index indexing policy
// (vector_index.enabled / exclude_patterns / index_submodules /
// respect_gitignore / respect_aiignore). The RPC validates the list,
// persists the change, and applies it live (see UpdateIndexingSettings).
type IndexingSettingsRequest struct {
	Enabled          bool     `json:"enabled"`
	ExcludePatterns  []string `json:"exclude_patterns"`
	IndexSubmodules  bool     `json:"index_submodules"`
	RespectGitIgnore bool     `json:"respect_gitignore"`
	RespectAIIgnore  bool     `json:"respect_aiignore"`
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
	// TLSFingerprint is the SPKI pin (ADR-054). nil = keep the persisted pin
	// (debounced partial saves must not drop it); non-nil applies verbatim,
	// so an explicit "" CLEARS it. Compatible providers only.
	TLSFingerprint *string `json:"tls_fingerprint,omitempty"`
	// AutoRetrySeconds: nil = keep the persisted interval; non-nil applies
	// verbatim, so an explicit 0 DISABLES the timer. Config round-trip only
	// (the timer lives in the c0wrk session layer); compatible providers.
	AutoRetrySeconds *int `json:"auto_retry_seconds,omitempty"`
	// TimeoutClass (ADR-071 D3): nil = keep; non-nil applies verbatim after
	// enum validation, so an explicit "" CLEARS the override.
	TimeoutClass *string `json:"timeout_class,omitempty"`
	// AuthMode selects chatgpt auth: "api_key" (static key) or "oauth"
	// (ADR-074). nil = keep the persisted mode; non-nil applies after enum
	// validation. Other providers ignore it.
	AuthMode *string `json:"auth_mode,omitempty"`
}

// ChatGPTSignInResponse is StartChatGPTSignIn's answer: the authorization URL
// the frontend opens; later transitions arrive via chatgpt_auth:state
// (specs/decisions/074-chatgpt-subscription-auth.md).
type ChatGPTSignInResponse struct {
	// AuthURL is the issuer's authorization endpoint (PKCE S256 + state,
	// loopback redirect pre-bound).
	AuthURL string `json:"auth_url"`
}

// ChatGPTAuthStatusResponse is the GetChatGPTAuthStatus snapshot — identity
// and configured mode only, never a token value (ADR-074).
type ChatGPTAuthStatusResponse struct {
	// SignedIn reports stored credentials exist (the keychain's answer; no
	// refresh is attempted).
	SignedIn bool `json:"signed_in"`
	// Email / AccountID / ExpiresAt (RFC3339) describe the signed-in
	// account, when known.
	Email string `json:"email,omitempty"`
	// AccountID is the ChatGPT account id backing the subscription.
	AccountID string `json:"account_id,omitempty"`
	// ExpiresAt is the stored access token's expiry, when known.
	ExpiresAt string `json:"expires_at,omitempty"`
	// Mode is the configured chatgpt auth mode: "api_key" | "oauth".
	Mode string `json:"mode"`
	// InFlight is the authoritative busy flag: a remounted panel restores
	// its posture from THIS field, not a possibly missed `pending` event.
	InFlight bool `json:"in_flight"`
	// LastError names the last failed sign-in, a keychain-unavailable
	// construction failure, or a non-fatal restore problem; "" when none.
	LastError string `json:"last_error,omitempty"`
}

// ChatGPTModelPresetResponse is the curated ChatGPT (Codex) preset — the
// models c0wrk's registry knows, most capable first (ADR-074).
type ChatGPTModelPresetResponse struct {
	Models []ChatGPTModelPresetEntry `json:"models"`
}

// ChatGPTModelPresetEntry is one preset model; metadata is fail-soft zeros
// until the async model registry wires up.
type ChatGPTModelPresetEntry struct {
	Name string `json:"name"`
	// ContextWindow / OutputLimit come from the model registry (tokens).
	ContextWindow int  `json:"context_window,omitempty"`
	OutputLimit   int  `json:"output_limit,omitempty"`
	Reasoning     bool `json:"reasoning,omitempty"`
}

// ChatGPTAuthEventData is the chatgpt_auth:state payload: exactly one flow
// transition per event, state-dependent fields, never a secret
// (specs/contracts/event-catalog.md).
type ChatGPTAuthEventData struct {
	// State is one of pending | success | error | cancelled.
	State string `json:"state"`
	// AuthURL (pending only) is the authorization URL the frontend opens.
	AuthURL string `json:"auth_url,omitempty"`
	// Error (error only) is the actionable failure cause.
	Error string `json:"error,omitempty"`
	// Email / AccountID / ExpiresAt (success only); ExpiresAt is RFC3339.
	Email     string `json:"email,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// ListProviderModelsRequest carries optional draft overrides so an
// unpersisted compatible provider can still fetch its model list; an empty
// or masked APIKey falls back to the saved key.
type ListProviderModelsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	// Type is the transport: "openai" | "anthropic"; empty derives from the
	// saved provider or defaults to "openai".
	Type string `json:"type,omitempty"`
	// TLSFingerprint is the draft pin override (ADR-054) so "Fetch Models"
	// reaches a self-pinned endpoint before persistence; nil = saved value.
	TLSFingerprint *string `json:"tls_fingerprint,omitempty"`
}

// GetProviderTLSCertificateRequest asks the backend to connect (draft
// BaseURL, else the persisted one) and report the certificate the server
// presents right now — deliberately unverified and pin-independent
// (ADR-054, specs/decisions/054-per-provider-tls-pinning.md).
type GetProviderTLSCertificateRequest struct {
	Provider string `json:"provider"`
	// BaseURL is the DRAFT base URL from the settings form; empty = persisted.
	BaseURL string `json:"base_url,omitempty"`
}

// TLSCertificateResponse carries the fetched leaf-certificate pin.
type TLSCertificateResponse struct {
	// Fingerprint is base64(SHA-256(SPKI DER)) of the server's leaf cert.
	Fingerprint string `json:"fingerprint"`
}

// ModelConfigResponse returns one model's effective values (override or
// built-in default) plus the factory defaults; HasOverride mirrors an entry
// in config.LLM.Models.
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

// ModelConfigRequest holds per-model overrides; only fields differing from
// the built-in default are stored (entry removed when everything matches).
// ""/nil string and capability fields mean "inherit" — a matching value
// records no override entry.
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
	BaseURL  string `json:"base_url"`
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

// SecuritySettingsResponse holds the group-based security settings
// (security.groups; specs/architecture/security-model.md).
type SecuritySettingsResponse struct {
	// Groups maps the seven configurable groups to their policies;
	// UpdateSecuritySettings replaces the stored set wholesale.
	Groups                     map[string]GroupPolicyResponse `json:"groups"`
	AutoApproveWorkspaceWrites bool                           `json:"auto_approve_workspace_writes"`
	// AutonomyMode is the unified autonomy posture: "standard" | "assisted" |
	// "silent" (security.autonomy_mode).
	AutonomyMode string `json:"autonomy_mode"`
	// SilentMode is the silent_mode container, live only in "silent" mode.
	SilentMode SilentModeResponse `json:"silent_mode"`
	// JudgeAvailable is read-only; the backend ignores any incoming value.
	JudgeAvailable bool `json:"judge_available"`
}

// GroupPolicyResponse mirrors config.GroupPolicyConfig: a group-enum policy
// ("allow"|"user_confirm"|"deny") plus — execute group only — a command
// blocklist forced to confirmation.
type GroupPolicyResponse struct {
	Policy string `json:"policy"`
	// Blocklist has no omitempty so the nil-vs-empty distinction survives
	// the settings UI's echo of GetSecuritySettings output back into
	// UpdateSecuritySettings; both spellings mean "no patterns".
	Blocklist []string `json:"blocklist"`
}

// SilentModeResponse is the frontend view of security.silent_mode: the four
// unattended-operation sub-policies, each Mode validated against the same
// config enum the loader uses (ADR-053).
type SilentModeResponse struct {
	ToolConfirm SilentSubPolicyResponse `json:"tool_confirm"`
	UserConfirm SilentSubPolicyResponse `json:"user_confirm"`
	StepLimit   SilentSubPolicyResponse `json:"step_limit"`
	AskUser     SilentSubPolicyResponse `json:"ask_user"`
}

// SilentSubPolicyResponse is one silent-mode sub-policy: a single Mode drawn
// from that sub-policy's enum.
type SilentSubPolicyResponse struct {
	Mode string `json:"mode"`
}

// ModelProfilesResponse is the settings-picker catalog view: profiles with
// their knob values, stored/suggested ids, the live-registry picker universe,
// and the notice channel (specs/domains/model-profiles.md).
type ModelProfilesResponse struct {
	// Enabled is the global master toggle verbatim — not a profile value.
	Enabled bool `json:"enabled"`
	// Profiles: predefined first (catalog order), then custom. Always non-nil.
	Profiles []ModelProfileDTO `json:"profiles"`
	// ActiveID is the STORED id, verbatim — dangling ids stay visible and
	// the generic fallback is explained in Warnings.
	ActiveID string `json:"active_id"`
	// SuggestedProfileID is nil when no predefined profile matches.
	SuggestedProfileID *string                    `json:"suggested_profile_id"`
	BuiltinTools       []ModelProfilesBuiltinTool `json:"builtin_tools"`
	ToolGroups         []ModelProfilesToolGroup   `json:"tool_groups"`
	// ProtectedTools are the orchestration tools always kept (locked chips).
	ProtectedTools []string `json:"protected_tools"`
	// Warnings: store-load warnings, resolver warnings, one-shot notices.
	Warnings []string `json:"warnings"`
}

// ModelProfileDTO is one catalog entry: stable id, display name, kind
// ("predefined"|"custom") and the profile's 25 knob values.
type ModelProfileDTO struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Kind   string             `json:"kind"`
	Values ModelProfileValues `json:"values"`
}

// ModelProfileValues carries one profile's knob values; the master toggle is
// a config.yaml field, not a profile value.
type ModelProfileValues struct {
	EssentialTools ModelProfilesEssentialToolsValues `json:"essential_tools"`
	SystemPrompt   ModelProfilesSystemPromptResp     `json:"system_prompt"`
	Sampling       ModelProfilesSamplingResp         `json:"sampling"`
	LoopHardening  ModelProfilesLoopHardeningResp    `json:"loop_hardening"`
	Context        ModelProfilesContextResp          `json:"context"`
}

// ModelProfilesEssentialToolsValues is the value part of the always-present
// tool-subset variant (the picker universe lives on ModelProfilesResponse).
type ModelProfilesEssentialToolsValues struct {
	Enabled             bool     `json:"enabled"`
	AlwaysPresent       []string `json:"always_present"`
	CompactDescriptions bool     `json:"compact_descriptions"`
}

// ModelProfileUpdateRequest: nil Name/Config keep the stored values; a
// non-nil Config replaces the WHOLE value set (no per-section merge).
type ModelProfileUpdateRequest struct {
	Name   *string             `json:"name"`
	Config *ModelProfileValues `json:"config"`
}

// ModelProfilesBuiltinTool is one pin-able built-in tool for the
// always-present picker (registry name + tooltip description).
type ModelProfilesBuiltinTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModelProfilesToolGroup is a cluster of built-in tools pinned atomically as
// one picker entry; members are already restricted to the picker universe.
type ModelProfilesToolGroup struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tools       []string `json:"tools"`
}

// ModelProfilesSystemPromptResp is the prompt-simplification variant.
type ModelProfilesSystemPromptResp struct {
	Lite              bool `json:"lite"`
	FewShot           bool `json:"few_shot"`
	ReasoningScaffold bool `json:"reasoning_scaffold"`
}

// ModelProfilesSamplingResp is the sampling-override variant. Zero numeric values
// mean "inherit the vendor preset" (not "send 0").
type ModelProfilesSamplingResp struct {
	Enabled           bool    `json:"enabled"`
	Temperature       float64 `json:"temperature"`
	TopP              float64 `json:"top_p"`
	TopK              int     `json:"top_k"`
	RepetitionPenalty float64 `json:"repetition_penalty"`
	PresencePenalty   float64 `json:"presence_penalty"`
	ReasoningEffort   string  `json:"reasoning_effort"`
}

// ModelProfilesLoopHardeningResp is the tightened circuit-breaker variant.
type ModelProfilesLoopHardeningResp struct {
	Enabled                      bool `json:"enabled"`
	RepeatNudgeThreshold         int  `json:"repeat_nudge_threshold"`
	ParseErrorAbortThreshold     int  `json:"parse_error_abort_threshold"`
	FruitlessNudgeThreshold      int  `json:"fruitless_nudge_threshold"`
	FruitlessAbortThreshold      int  `json:"fruitless_abort_threshold"`
	SameToolRepeatNudgeThreshold int  `json:"same_tool_repeat_nudge_threshold"`
}

// ModelProfilesContextResp is the aggressive context-management variant.
type ModelProfilesContextResp struct {
	Enabled             bool                        `json:"enabled"`
	Compaction          ModelProfilesCompactionResp `json:"compaction"`
	ToolOutputKeepLastN int                         `json:"tool_output_keep_last_n"`
	OutputTokenReserve  int                         `json:"output_token_reserve"`
}

// ModelProfilesCompactionResp holds the compaction-tightening overrides of the
// context variant.
type ModelProfilesCompactionResp struct {
	KeepLast       int `json:"keep_last"`
	BlockSize      int `json:"block_size"`
	TriggerPercent int `json:"trigger_percent"`
}

type FileNode = workspace.FileNode

// FileIconResponse holds the icon and color for a single file or directory.
type FileIconResponse struct {
	Icon      string `json:"icon"`
	IconColor string `json:"icon_color"`
}

type GitStatusEntry = workspace.GitStatusEntry

type DiffStat = workspace.DiffStat

type Branch = workspace.Branch

type BranchInfo = workspace.BranchInfo

type BranchBase = workspace.BranchBase

type CommitFile = workspace.CommitFile

type StashEntry = workspace.StashEntry

type GitHistoryCommit = workspace.GitHistoryCommit

type GitHistoryPage = workspace.GitHistoryPage

type HunkDiffInfo = workspace.HunkDiffInfo

type MergeRebaseState = workspace.MergeRebaseState

type ReviewHunk = workspace.ReviewHunk

type ReviewFileDiff = workspace.ReviewFileDiff

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	Model             string  `json:"model"`
	Family            string  `json:"family"`
	FillPercent       float64 `json:"fill_percent"`
	// UsedTokens/MaxTokens are the live "N of M" context usage from the
	// in-memory emitter snapshot; zero from persisted-only state, where the
	// frontend keeps its last live values.
	UsedTokens int `json:"used_tokens,omitempty"`
	MaxTokens  int `json:"max_tokens,omitempty"`
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

// ToolInfo carries a tool's metadata, source, capability group, and effective
// policy — policy derives from the GROUP (ADR-024) and is display-only.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	// Group is a config.ToolGroup* name; "system" tools are filtered out.
	Group string `json:"group"`
	// Policy is the effective group policy ("allow"|"user_confirm"|"deny").
	Policy string `json:"policy"`
}

// VectorEmbedderInfo carries the embedder's execution-provider facts from the
// one-shot background init; written once, never mutated (ADR-045/089/090).
type VectorEmbedderInfo struct {
	// EffectiveProvider is what inference runs on ("cpu" | "cuda" |
	// "openvino") — "auto" is resolved at creation, never leaks here.
	EffectiveProvider string

	// RequestedProvider is the config value; it diverges from
	// EffectiveProvider for every "auto" request (always resolved) and for
	// an explicit provider degrading to CPU — only that sets FallbackReason.
	RequestedProvider string

	// FallbackReason is the init-failure text of an explicit
	// cuda/openvino→cpu degradation; the auto degradation is WARN-logged.
	FallbackReason string

	// CUDAVerified is the nvidia-smi verdict; nil when the probe did not run.
	CUDAVerified *bool

	// OVDevice is the xpu-smi verdict ("gpu" | "cpu"); empty when unverified
	// — a missing verdict is never "cpu".
	OVDevice string

	// DeviceID is the ONNX device index; 0 is a valid "first GPU" default,
	// so populated-ness rides the provider fields (keeping IsZero sound).
	DeviceID int
}

// IsZero reports the background init has not populated the info (e.g. no
// embedder exists); 0 is a valid DeviceID, so populated-ness rides the
// provider fields.
func (i VectorEmbedderInfo) IsZero() bool {
	return i.EffectiveProvider == "" && i.RequestedProvider == "" &&
		i.FallbackReason == "" && i.CUDAVerified == nil && i.OVDevice == "" && i.DeviceID == 0
}

// VectorIndexStatus describes the current state of the vector index for the frontend.
type VectorIndexStatus struct {
	State        string   `json:"state"`
	Progress     float64  `json:"progress"`
	FilesIndexed int      `json:"files_indexed"`
	TotalFiles   int      `json:"total_files"`
	CurrentFile  string   `json:"current_file"`
	Branch       string   `json:"branch"`
	Phase        string   `json:"phase"`   // "both" | "embedding" | "lexical" | "open" (the ADR-064 pre-open `loading` state)
	Indices      []string `json:"indices"` // e.g. ["vector", "lexical"

	// ExecutionProvider / RequestedExecutionProvider / ProviderFallbackReason /
	// CUDAVerified / OVDevice / DeviceID mirror VectorEmbedderInfo field for
	// field (same semantics; ADR-045). DeviceID is omitted when 0 — read
	// absence as 0.
	ExecutionProvider string `json:"execution_provider,omitempty"`

	RequestedExecutionProvider string `json:"requested_execution_provider,omitempty"`

	CUDAVerified *bool `json:"cuda_verified,omitempty"`

	OVDevice string `json:"ov_device,omitempty"`

	ProviderFallbackReason string `json:"provider_fallback_reason,omitempty"`

	DeviceID int `json:"device_id,omitempty"`
}

// VectorStoreEntry is one chunk from the vector store; the Score/Rank fields
// are per-side attribution — vector-only populates Vector*, lexical Lexical*,
// hybrid any subset.
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

// SearchRequest is the SearchVectorStore payload. Mode "hybrid" | "vector" |
// "lexical"; empty/unknown defaults to hybrid (auto-falls back to vector-only
// when the lexical index is empty or unavailable). FilePattern is a
// doublestar glob on file_path; MustMatch lists literal substrings every
// chunk must carry.
type SearchRequest struct {
	Query       string   `json:"query"`
	TopK        int      `json:"top_k"`
	FilePattern string   `json:"file_pattern"`
	MustMatch   []string `json:"must_match"`
	Mode        string   `json:"mode"`
}

// GPUDeviceResponse is one GPU for the execution-provider device picker,
// mirrored as a plain DTO so the contract stays in this package.
type GPUDeviceResponse struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	// Provider scopes the index: "cuda" and "openvino" enumerate
	// independently — the same index names different hardware.
	Provider string `json:"provider"`
}

// OptimizePromptResponse holds the result of prompt optimization for the frontend.
type OptimizePromptResponse struct {
	OptimizedPrompt string   `json:"optimized_prompt"`
	Keywords        []string `json:"keywords"`
	UsedContext     bool     `json:"used_context"`
}

// Blackboard Viewer DTOs

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

// BlackboardStepResponse omits the step body on purpose — it can be large;
// the viewer fetches it on demand via GetStepOutput / SearchBlackboardStepOutputs.
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

// BlackboardAttachmentResponse is a metadata-only attachment view (the
// markdown body is excluded to bound the payload).
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

// AgentDescriptorDTO is a lightweight Subagent Profile descriptor for the
// /-autocomplete (ADR-076: `#` is no longer a trigger). Mirrors
// SkillDescriptorDTO.
type AgentDescriptorDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CommandDescriptorDTO is a custom-command descriptor for /-autocomplete and
// send-time template expansion (ADR-096): the body ships in the descriptor
// because expansion happens in the send path.
type CommandDescriptorDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	Template    string `json:"template"`
}
