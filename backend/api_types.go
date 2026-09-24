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
	Loaded        bool                          `json:"loaded"`
	LogLevel      string                        `json:"log_level"`
	ConfigErrors  []string                      `json:"config_errors"`
	LLM           ConfigLLMResponse             `json:"llm"`
	Search        ConfigSearchResp              `json:"search"`
	Proxy         ProxySettingsResponse         `json:"proxy"`
	Experimental  ExperimentalSettingsResponse  `json:"experimental"`
	ModelProfiles ModelProfilesSettingsResponse `json:"model_profiles"`
	VectorIndex   VectorIndexSettingsResponse   `json:"vector_index"`
}

// ExperimentalSettingsResponse exposes the master experimental-features switch
// to the settings UI. It carries no feature-specific state by design — the
// switch is all-or-nothing and gates every experimental feature (currently the
// E2S execution mode). Model Profiles is NOT gated by this switch; it carries
// its own manual master toggle (model_profiles.enabled).
type ExperimentalSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

// ModelProfilesSettingsResponse exposes the EFFECTIVE (resolved) Model Profiles profile state
// to the settings UI, not the raw persisted config. Enabled mirrors the
// resolved master toggle (model_profiles.enabled, carried through verbatim — the
// experimental-features switch does not affect it); EssentialToolsEnabled
// mirrors the resolved essential-tools variant sub-toggle. Both are false when
// the config is not loaded.
type ModelProfilesSettingsResponse struct {
	Enabled               bool `json:"enabled"`
	EssentialToolsEnabled bool `json:"essential_tools_enabled"`
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
	// AutoRetryMaxSeconds is the inclusive upper bound for per-provider
	// auto_retry_seconds (ADR-065) — the SAME bound validate() and the
	// UpdateLLMConfig RPC path enforce. Exposed so the Settings form clamps
	// its input against the backend's actual limit instead of a duplicated
	// frontend constant (single source of truth; a backend limit change
	// propagates with the next config load, no frontend release needed).
	AutoRetryMaxSeconds int `json:"auto_retry_max_seconds"`
}

// ConfigProviderFull is a provider with api_key, optional base_url, and enabled models list.
type ConfigProviderFull struct {
	APIKey  string   `json:"api_key"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models"` // enabled models for this provider
	// TLSFingerprint exposes the per-provider SPKI pin (ADR-054): non-empty =
	// only the pinned key is accepted; empty = system CA verification. Only
	// compatible providers carry one.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	// AutoRetrySeconds exposes the per-provider session-layer auto-retry
	// interval: 0 = disabled. Only compatible providers carry one; the fixed
	// anthropic/chatgpt providers always report 0.
	AutoRetrySeconds int `json:"auto_retry_seconds,omitempty"`
}

// ConfigSearchResp holds search config values.
type ConfigSearchResp struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// VectorIndexSettingsResponse holds the vector-index embedding settings for
// the frontend: which ONNX Runtime execution provider the embedder runs on
// ("auto" | "cpu" | "cuda") and the GPU device index used when the provider
// resolves to CUDA. Changing these requires an app restart to take effect —
// the embedder is created once per process — so the update path only
// validates and persists them; it never touches the vector manager.
type VectorIndexSettingsResponse struct {
	ExecutionProvider string `json:"execution_provider"`
	DeviceID          int    `json:"device_id"`
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
	// TLSFingerprint is the base64(SHA-256(SPKI DER)) pin (ADR-054); the pin
	// is the only verification override. nil = keep the persisted pin
	// (debounced partial saves must not drop it); non-nil = apply verbatim,
	// so an explicit empty string CLEARS the pin (back to system CA
	// verification). Only meaningful for compatible providers.
	TLSFingerprint *string `json:"tls_fingerprint,omitempty"`
	// AutoRetrySeconds is the per-provider session-layer auto-retry interval
	// (compatible providers only). nil = keep the persisted interval
	// (debounced partial saves must not drop it); non-nil = apply verbatim,
	// so an explicit 0 DISABLES the retry timer. Not mapped into the router:
	// the timer lives in the c0wrk session layer, so it only round-trips
	// through config. Ignored for the fixed anthropic/chatgpt providers.
	AutoRetrySeconds *int `json:"auto_retry_seconds,omitempty"`
}

// ListProviderModelsRequest is the payload for ListProviderModels.
//
// Provider is required. APIKey / BaseURL / Type are optional draft overrides
// from the settings UI so a compatible provider that has not been persisted
// yet (first-run, or no default_model selected so saves are held back) can
// still fetch its model list. Empty / masked APIKey falls back to the saved
// key when the provider already exists in config.
type ListProviderModelsRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	// Type is the transport: "openai" or "anthropic". Empty means derive from
	// the saved provider, or default to "openai" for an unknown provider.
	Type string `json:"type,omitempty"`
	// TLSFingerprint is the draft pin override (ADR-054) so "Fetch Models"
	// reaches a self-pinned endpoint before the provider is persisted. nil =
	// fall back to the saved value; non-nil applies verbatim.
	TLSFingerprint *string `json:"tls_fingerprint,omitempty"`
}

// GetProviderTLSCertificateRequest asks the backend to connect to a
// provider's endpoint (draft BaseURL, or the persisted one when empty) and
// return the certificate fingerprint the server currently presents — the
// settings UI "Get" button. No verification happens on this connection: the
// fingerprint IS the thing being fetched.
//
// The request deliberately carries NO fingerprint field: the button is
// unconditional with respect to any configured pin (ADR-054). It always
// reports what the endpoint serves right now, whether or not the provider
// already has a pin, and whatever the caller does with the result is the
// user's decision.
type GetProviderTLSCertificateRequest struct {
	Provider string `json:"provider"`
	// BaseURL is the DRAFT base URL from the settings form; empty = use the
	// provider's persisted base_url.
	BaseURL string `json:"base_url,omitempty"`
}

// TLSCertificateResponse carries the fetched leaf-certificate pin.
type TLSCertificateResponse struct {
	// Fingerprint is base64(SHA-256(SPKI DER)) of the server's leaf cert.
	Fingerprint string `json:"fingerprint"`
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
	// their policy (and, for the "execute" group only, a command blocklist).
	// GetSecuritySettings always returns the full set of seven groups;
	// UpdateSecuritySettings replaces the stored set with what it receives.
	Groups                     map[string]GroupPolicyResponse `json:"groups"`
	AutoApproveWorkspaceWrites bool                           `json:"auto_approve_workspace_writes"`
	// AutonomyMode is the unified autonomy posture (security.autonomy_mode):
	// "standard" (default — every prompt reaches a human), "assisted" (the
	// strict OWASP ASI judge resolves escalated calls), or "silent"
	// (unattended operation via the silent_mode sub-policies). It replaces
	// the former Smart Approve / silent-mode master-switch pair of keys.
	AutonomyMode string `json:"autonomy_mode"`
	// SilentMode is the silent-mode sub-policy container
	// (security.silent_mode). Its policies are live only while AutonomyMode
	// is "silent". UpdateSecuritySettings replaces the stored value with
	// what it receives (validated against the sub-policy enums).
	SilentMode SilentModeResponse `json:"silent_mode"`
	// JudgeAvailable reports whether the strict judge is configured and ready.
	// Read-only: the frontend uses it to disable the assisted/silent judge-
	// dependent options when no judge is operational (e.g. no LLM model
	// configured). Always sent; the backend ignores any incoming value
	// during updates.
	JudgeAvailable bool `json:"judge_available"`
}

// GroupPolicyResponse holds one tool group's policy for the frontend. It
// mirrors config.GroupPolicyConfig: a policy from the group enum
// ("allow"|"user_confirm"|"deny") and — execute group only — a regex
// blocklist of shell commands forced to confirmation.
type GroupPolicyResponse struct {
	Policy string `json:"policy"`
	// Blocklist is serialized WITHOUT omitempty so the nil-vs-empty
	// distinction survives the JSON round trip — the settings UI echoes
	// GetSecuritySettings output straight back into UpdateSecuritySettings
	// on every save. There are no predefined patterns: nil (JSON null) and
	// an explicit empty array both mean "no patterns". Non-execute groups
	// serialize null, which the update path ignores.
	Blocklist []string `json:"blocklist"`
}

// SilentModeResponse is the frontend view of security.silent_mode: the
// container for the three unattended-operation sub-policies (live only while
// the autonomy mode is "silent" — see SecuritySettingsResponse.AutonomyMode).
// Each sub-policy Mode uses that sub-policy's config enum (see
// config.SilentToolConfirm*, SilentStepLimit*, SilentAskUser*).
// UpdateSecuritySettings validates the modes against the same enums the
// config loader uses.
type SilentModeResponse struct {
	ToolConfirm SilentSubPolicyResponse `json:"tool_confirm"`
	StepLimit   SilentSubPolicyResponse `json:"step_limit"`
	AskUser     SilentSubPolicyResponse `json:"ask_user"`
}

// SilentSubPolicyResponse is one silent-mode sub-policy: a single Mode drawn
// from that sub-policy's enum.
type SilentSubPolicyResponse struct {
	Mode string `json:"mode"`
}

// ModelProfilesResponse is the model-profile profile catalog view for the settings
// picker: every profile (predefined ∪ custom) with its 25 knob values, the
// persisted active profile id (config.yaml model_profiles.active_profile), the
// suggested profile id (a normalized match of the default model name against
// the predefined slugs; null when nothing matches), and the read-only picker
// universe (builtin_tools / tool_groups) read from the live tool registry.
type ModelProfilesResponse struct {
	// Enabled is the global master toggle (config.yaml model_profiles.enabled) reported
	// verbatim — it is NOT a value of any profile. False when config is not
	// yet initialized.
	Enabled bool `json:"enabled"`
	// Profiles is the full catalog: predefined entries first (catalog order),
	// then custom entries in store order. Always non-nil ([] not null).
	Profiles []ModelProfileDTO `json:"profiles"`
	// ActiveID is the STORED active profile id, reported verbatim — including
	// a dangling id after an external store edit; the resolver then falls
	// back to generic and says so in Warnings.
	ActiveID string `json:"active_id"`
	// SuggestedProfileID is nil (JSON null) when no predefined profile
	// matches the default model name.
	SuggestedProfileID *string                    `json:"suggested_profile_id"`
	BuiltinTools       []ModelProfilesBuiltinTool `json:"builtin_tools"`
	ToolGroups         []ModelProfilesToolGroup   `json:"tool_groups"`
	// ProtectedTools lists the orchestration tools the backend always keeps
	// regardless of any selection, so the UI can render them as locked chips.
	ProtectedTools []string `json:"protected_tools"`
	// Warnings carries store-load warnings, resolver warnings (dangling
	// active id → generic fallback) and one-shot notices (e.g. "the active
	// profile was deleted; switched to generic"). Always non-nil.
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

// ModelProfileValues carries the 25 knob values of one profile. The master
// enabled toggle is NOT here: it is a config.yaml field (model_profiles.enabled), not a
// profile value.
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

// ModelProfileUpdateRequest is the update payload for UpdateModelProfile. Only
// the two request-level fields are optional: nil Name keeps the stored display
// name and nil Config keeps the stored values. A non-nil Config replaces the
// WHOLE 25-knob value set (no per-section merge).
type ModelProfileUpdateRequest struct {
	Name   *string             `json:"name"`
	Config *ModelProfileValues `json:"config"`
}

// ModelProfilesBuiltinTool describes one pin-able built-in tool for the
// always-present picker: its registry name plus the description the UI renders
// in the entry's hover tooltip.
type ModelProfilesBuiltinTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ModelProfilesToolGroup describes a functional cluster of built-in tools offered
// as one atomic picker entry: picking the cluster pins every still-selectable
// member at once. The UI renders Title and Description in the entry's tooltip
// and lists Tools alongside them. Members are already restricted to the picker
// universe reported in BuiltinTools.
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

// GitHistoryPage is a single page of the unified commit history for the
// frontend's incremental (scroll/load-more) loading. Defined in
// core/workspace; re-exported here as a type alias for ViewModel convenience.
type GitHistoryPage = workspace.GitHistoryPage

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

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	Model             string  `json:"model"`
	Family            string  `json:"family"`
	FillPercent       float64 `json:"fill_percent"`
	// UsedTokens/MaxTokens are the conductor's live context-window usage
	// ("N of M"). They come from the in-memory emitter snapshot (present only
	// while the session is in memory, e.g. mid-task) — the persisted session
	// row does not carry them. Zero when only persisted state is available;
	// the frontend keeps its last live values in that case.
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

// VectorEmbedderInfo carries the embedder's execution-provider facts from the
// one-shot desktop background init into every VectorIndexStatus payload. It is
// written once (embedder creation outcome) and never mutated afterwards, so the
// values stay consistent across all subsequent status emissions.
type VectorEmbedderInfo struct {
	// EffectiveProvider is the provider inference actually runs on: "cpu" or
	// "cuda" — "auto" is resolved at embedder creation and never leaks here.
	EffectiveProvider string

	// RequestedProvider is the config value passed to the embedder:
	// "auto" | "cpu" | "cuda". Diverges from EffectiveProvider for every
	// "auto" request (auto is resolved to the winner at creation —
	// auto→cuda is a success, auto→cpu is Auto's expected degradation)
	// and for an explicit "cuda" degrading to CPU after init failure —
	// only the latter is a fallback, and only that path sets
	// FallbackReason.
	RequestedProvider string

	// FallbackReason carries why an explicit "cuda" request degraded to
	// the CPU provider (CUDA init failure text). Empty otherwise — the
	// "auto" degradation cause is WARN-logged at startup and is not part
	// of the status payload. Surfaced in the status for diagnostics.
	FallbackReason string

	// CUDAVerified is the external nvidia-smi verdict — true when the driver
	// lists this process among CUDA compute apps. Nil when the probe did not
	// run (CPU embedder, embedder unavailable, probe skipped).
	CUDAVerified *bool

	// DeviceID is the ONNX device index the embedder was created with
	// (vector_index.device_id). Paired with the provider fields so a future
	// UI can detect restart-pending: the config's device_id diverging from
	// this value means the running embedder predates the config change.
	// 0 is the valid "first GPU" default, not a "missing" marker — the
	// desktop init always writes it alongside RequestedProvider, which is
	// what keeps IsZero sound despite 0 being indistinguishable from the
	// zero value.
	DeviceID int
}

// IsZero reports whether any fact has been recorded. True when the background
// init has not (yet) populated the info — e.g. no embedder exists at all.
// DeviceID participates in the check for non-zero values only: 0 is a valid
// device index (first GPU) and cannot alone distinguish "recorded" from
// "missing"; the desktop init always records RequestedProvider together with
// DeviceID, so the provider fields carry the populated/not-populated signal.
func (i VectorEmbedderInfo) IsZero() bool {
	return i.EffectiveProvider == "" && i.RequestedProvider == "" &&
		i.FallbackReason == "" && i.CUDAVerified == nil && i.DeviceID == 0
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

	// ExecutionProvider is the ONNX Runtime execution provider the embedder
	// effectively runs on: "cpu" or "cuda" — never "auto" ("auto" is resolved
	// once, at embedder creation; the winner is reported here). Empty when no
	// embedder exists (model files missing or creation failed). Comparing it
	// with RequestedExecutionProvider classifies the outcome (ADR-045): an
	// explicit "cuda" landing on "cpu" is a fallback; an "auto" request always
	// diverges (it is resolved to a winner), so auto→cuda is a success and
	// auto→cpu is Auto's expected degradation.
	ExecutionProvider string `json:"execution_provider,omitempty"`

	// RequestedExecutionProvider is the config value (auto|cpu|cuda) the
	// embedder was created with. Diverges from ExecutionProvider for every
	// "auto" request (auto is resolved to the winner) and for an explicit
	// "cuda" degrading to CPU after init failure (WARN + runtime_error
	// toast) — only the latter is a fallback.
	RequestedExecutionProvider string `json:"requested_execution_provider,omitempty"`

	// CUDAVerified is the external nvidia-smi verdict, set only when the
	// effective provider is "cuda" and the startup verification probe ran:
	// true = the driver lists this process among CUDA compute apps; false =
	// absent (possible silent CPU fallback inside the CUDA-capable build).
	// Nil when no probe ran (CPU embedder, embedder unavailable, or the
	// embedder never initialized).
	CUDAVerified *bool `json:"cuda_verified,omitempty"`

	// ProviderFallbackReason explains why an explicit "cuda" request
	// degraded to the CPU provider (CUDA init failure text). Empty when the
	// requested provider was honored — and empty on the auto→cpu
	// degradation path, whose concrete cause is WARN-logged at startup and
	// does not travel in the status payload.
	ProviderFallbackReason string `json:"provider_fallback_reason,omitempty"`

	// DeviceID is the ONNX device index the running embedder was created
	// with (vector_index.device_id at embedder-creation time). Surfaced for
	// restart-pending detection: comparing it with the live config's
	// device_id shows the running embedder predates a config change (the
	// embedder and its ONNX session are created once per process — ADR-045).
	// Omitted when 0 (the default "first GPU") — a UI treating 0 as the
	// default must read absence as 0.
	DeviceID int `json:"device_id,omitempty"`
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

// GPUDeviceResponse describes one GPU visible to the NVIDIA driver, for the
// vector-index settings UI (populating the execution-provider device picker).
// It mirrors embedding.GPUDevice as a plain DTO rather than re-exporting the
// SDK type through the Wails bindings, keeping the frontend contract owned
// by this package.
type GPUDeviceResponse struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
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
// The full output (body) is intentionally excluded: it can be large and would
// bloat the GetBlackboardState payload. The viewer fetches a single step's full
// output on demand via GetStepOutput, and matches step outputs against a search
// query via SearchBlackboardStepOutputs.
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
