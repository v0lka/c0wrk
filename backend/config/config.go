// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/v0lka/sp4rk/llm"

	"gopkg.in/yaml.v3"
)

// DefaultAgentDir is the default directory for agent files (data, tools, config).
const DefaultAgentDir = ".c0wrk"

// Config is the top-level configuration structure.
type Config struct {
	LogLevel string    `yaml:"log_level"`
	LLM      LLMConfig `yaml:"llm"`
	MCP      MCPConfig `yaml:"mcp"`

	Router        RouterConfig        `yaml:"router"`
	Executor      ExecutorConfig      `yaml:"executor"`
	Security      SecurityConfig      `yaml:"security"`
	Skills        SkillsConfig        `yaml:"skills"`
	Agents        AgentsConfig        `yaml:"agents"`
	Search        SearchConfig        `yaml:"search"`
	ToolLimits    ToolLimitsConfig    `yaml:"toolLimits"`
	Timeouts      TimeoutsConfig      `yaml:"timeouts"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
	GoalLoop      GoalLoopConfig      `yaml:"goal_loop"`
	VectorIndex   VectorIndexConfig   `yaml:"vector_index"`
	Proxy         ProxyConfig         `yaml:"proxy"`
	Terminal      TerminalConfig      `yaml:"terminal"`

	// SmallLLM configures optimizations applied when running on a "small"
	// (low-capacity / cheaper) LLM. The master toggle is manual only — there
	// is no auto-detection; the operator decides when to enable it. Each
	// variant carries its own sub-toggle so individual optimizations can be
	// turned off without disabling the whole profile, and every threshold/value
	// is exposed so behaviour can be tuned without a rebuild.
	SmallLLM SmallLLMConfig `yaml:"small_llm"`

	// Experimental gates features that are still under active development as a
	// single master switch (all-or-nothing). When disabled, every gated feature
	// (RESEARCH mode, the Small-LLM profile, and ChatGPT subscription sign-in)
	// is treated as off and its UI affordances are hidden. Default: off.
	Experimental ExperimentalConfig `yaml:"experimental"`

	// Updates configures the automatic "check for updates" subsystem that runs
	// in the background after the backend is ready. The state it produces (the
	// timestamp of the last check) is persisted to update_state.json, not to
	// this file; see core/updater/state.go and config.UpdateStatePath.
	Updates UpdatesConfig `yaml:"updates"`
}

// UpdatesConfig controls the self-update subsystem. Both toggles are
// operator-level settings in config.yaml; there is no separate user-preference
// file.
type UpdatesConfig struct {
	// Enabled is the master switch for the update subsystem. It is a
	// pointer-bool so callers can distinguish "unset" (defaults to true) from
	// "explicitly disabled" (false), matching the ProxyConfig.SetGlobalEnv
	// convention. When false, CheckForUpdates reports no update, the
	// background auto-check never runs, and the UI disables all update
	// affordances.
	Enabled *bool `yaml:"enabled"`

	// AutoCheck controls whether the app polls for updates automatically on
	// startup. It is a pointer-bool defaulting to true (an absent key keeps
	// auto-checks on, while an explicit false disables them). When false only
	// the automatic background check is suppressed — manual checks from the UI
	// still work (provided Enabled is true).
	AutoCheck *bool `yaml:"auto_check"`

	// CheckInterval is the minimum time between automatic checks, expressed as
	// a duration string (e.g. "6h"). Defaults to "6h". It is parsed with
	// time.ParseDuration; an unparseable value is treated as the default.
	CheckInterval string `yaml:"check_interval"`
}

// ProxyConfig holds HTTP/HTTPS proxy settings for all outbound connections.
type ProxyConfig struct {
	Enabled    bool     `yaml:"enabled"`
	URL        string   `yaml:"url"`          // scheme://user:password@host:port
	BypassList []string `yaml:"bypass_list"`  // hostnames/IPs to skip proxy
	TLSCertDir string   `yaml:"tls_cert_dir"` // directory with .pem/.crt CA certs

	// SetGlobalEnv, when true, exports HTTP_PROXY/HTTPS_PROXY/NO_PROXY/SSL_CERT_DIR
	// into the process environment so subprocesses (bash_exec children, MCP
	// stdio servers) inherit the proxy. Default: true when proxy is enabled
	// (backward compat). Set to false to prevent proxy state from leaking into
	// third-party Go libraries that read env vars at init time.
	// Pointer-bool so callers can distinguish "unset" from "explicitly false".
	SetGlobalEnv *bool `yaml:"set_global_env"`
}

// TerminalConfig configures the embedded PTY terminal (xterm.js panel).
type TerminalConfig struct {
	// Env holds extra environment variables set on every terminal shell
	// process, on top of the app's inherited environment and the built-in
	// terminal defaults (TERM, COLORTERM, TERM_PROGRAM=c0wrk). Values win
	// over both, so a user can override the defaults. `${VAR}` references
	// are expanded at startup.
	//
	// Typical use: marker variables that shell rc files check to skip
	// behaviors that assume a real standalone terminal — most commonly
	// tmux auto-attach (the embedded terminal is NOT the user's own tmux
	// instance; attaching would land the panel in an unrelated session
	// and directory). Example rc guard:
	//
	//	[[ -z "$TMUX" && "$TERM_PROGRAM" != "c0wrk" ]] && tmux attach
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// VectorIndexConfig holds vector / hybrid search runtime settings.
//
// Hybrid is a pointer-bool so callers can distinguish "unset" (defaults
// to true) from "explicitly disabled" (false). When Hybrid is false the
// service only writes and reads the chromem vector index; bleve is
// still opened but not consulted at query time.
//
// The HybridRRFK / HybridFanoutMultiplier / HybridFanoutMin fields tune
// Reciprocal Rank Fusion. A zero value falls back to the built-in
// default (60 / 4 / 100).
//
// The HybridVectorScoreFloor / HybridVectorScoreRatio /
// HybridLexicalScoreRatio fields are pointer-float64 so callers can
// distinguish "unset" (defaults applied) from "explicitly zero"
// (threshold disabled). They suppress noise-tail hits before fusion:
//   - VectorScoreFloor: absolute cosine-similarity floor.
//   - VectorScoreRatio: relative cutoff (sim < ratio × top sim rejected).
//   - LexicalScoreRatio: relative BM25 cutoff (score < ratio × top rejected).
type VectorIndexConfig struct {
	Hybrid *bool `yaml:"hybrid"`

	HybridRRFK              int      `yaml:"hybrid_rrf_k"`
	HybridFanoutMultiplier  int      `yaml:"hybrid_fanout_multiplier"`
	HybridFanoutMin         int      `yaml:"hybrid_fanout_min"`
	HybridVectorScoreFloor  *float64 `yaml:"hybrid_vector_score_floor"`
	HybridVectorScoreRatio  *float64 `yaml:"hybrid_vector_score_ratio"`
	HybridLexicalScoreRatio *float64 `yaml:"hybrid_lexical_score_ratio"`

	// EmbeddingThreads controls the ONNX intra-op thread pool used by the
	// embedding model during indexing. 0 (or unset) lets ONNX use all cores —
	// the legacy behaviour and default. Set 1..N to cap intra-op threads and
	// lower the CPU spike during (re)indexing at the cost of throughput.
	EmbeddingThreads int `yaml:"embedding_threads"`

	// MaxFileSize is the upper bound (in bytes) on a file's size for it to be
	// read fully into memory for chunking and embedding. Files above this are
	// skipped at walk, validation, and read time. 0 (or unset) defaults to
	// 4 MiB. Raise it if legitimate large source files are excluded from
	// vector search; lower it to bound memory on constrained machines.
	MaxFileSize int64 `yaml:"max_file_size"`

	// MaxChunkSize is the maximum chunk size in characters passed to the
	// chunker. 0 (or unset) defaults to 1500. It is sized to fit the embedding
	// model's context window; raising it significantly above 1500 risks
	// producing chunks the model cannot embed in a single pass.
	MaxChunkSize int `yaml:"max_chunk_size"`

	// MaxChunksPerFile is the upper bound on the number of chunks a single
	// file may contribute to one index pass. A file that exceeds it is
	// skipped wholesale (logged at WARN) rather than handed to the embedder
	// in a runaway batch. This guards against data-format files — BPE
	// vocab/merges tokenizer.json, minified assets, lockfiles — that the
	// structure-aware splitter fragments into tens of thousands of tiny
	// chunks, each requiring a separate ONNX inference pass that would
	// otherwise hang/OOM the embedder. Embedding is sub-batched, so this cap
	// is a backstop against pathological inputs, not a tight per-call limit.
	// 0 (or unset) defaults to 4000, which sits above any legitimate source
	// file (a 4 MiB file yields ~3230 chunks).
	MaxChunksPerFile int `yaml:"max_chunks_per_file"`

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session (sp4rk embedding.EmbedderConfig.BatchSize). Larger
	// capacities amortize ONNX inference across more chunks per call
	// (higher indexing throughput) but linearly grow the per-inference
	// output tensor (B × 512 × 512 × 4 bytes ≈ B MiB) and single-call
	// latency. 32 sits at the measured throughput knee. 0 (or unset)
	// defaults to 32 — identical to the previous behaviour, where sp4rk
	// applied its own default.
	EmbeddingBatchSize int `yaml:"embedding_batch_size"`

	// PrepWorkers is the number of parallel file-preparation workers
	// (read/hash/chunk) overlapping ONNX inference in the indexing
	// pipeline. 1 reproduces the historical serial behaviour; higher
	// values overlap file I/O with embedding (embedding itself stays
	// single-threaded under the service write lock). 0 (or unset)
	// defaults to 2.
	PrepWorkers int `yaml:"prep_workers"`

	// DebounceMs is how long file-change notifications wait, in
	// milliseconds, before a single incremental index pass runs — it
	// coalesces bursts of watcher events into one pass. 0 (or unset)
	// defaults to 1000 (the historical hardcoded value).
	DebounceMs int `yaml:"debounce_ms"`

	// ChunkOverlap is the character overlap between adjacent chunks handed
	// to the chunker. 0 (or unset) defaults to 200 (the historical
	// hardcoded value). Changing this (or MaxChunkSize) is picked up
	// automatically: the file-hash sidecar records the chunker
	// configuration each file was chunked under, and the next incremental
	// validation pass reports affected files as stale so they are
	// re-chunked — no manual Reindex required.
	ChunkOverlap int `yaml:"chunk_overlap"`

	// SearchWaitTimeoutMs bounds, in milliseconds, how long a consumer may
	// WAIT for the vector index to become ready (e.g. right after a project
	// switch while the initial index pass is still running). It is a
	// pointer-int so an unset key resolves to the 3000 ms default while an
	// explicit 0 is preserved as the "fail fast" sentinel — never wait,
	// return immediately with whatever the index can serve now. The bound
	// covers readiness waiting exactly once per consumer: the
	// semantic_search tool waits in its dedicated wait step (the search
	// call itself never waits — it fails fast with an actionable not-ready
	// error if readiness flipped in between), RAG hint generation calls
	// wait+search under one shared deadline, and the SearchVectorStore RPC
	// bounds its single call. On expiry the caller gets an actionable
	// not-ready error (tool/RPC) or proceeds without hints (RAG). Query
	// execution is separately bounded by the same value as
	// defense-in-depth.
	SearchWaitTimeoutMs *int `yaml:"search_wait_timeout_ms"`
}

// LLMConfig holds LLM provider configuration with fixed provider schema.
type LLMConfig struct {
	DefaultModel        string                               `yaml:"default_model"` // cross-provider default model (must exist in some provider's Models list)
	Anthropic           AnthropicConfig                      `yaml:"anthropic"`
	OpenAICompatible    map[string]OpenAICompatibleConfig    `yaml:"openai_compatible"`
	AnthropicCompatible map[string]AnthropicCompatibleConfig `yaml:"anthropic_compatible"`
	ChatGPT             ChatGPTConfig                        `yaml:"chatgpt"`
	Subscriptions       SubscriptionProvidersConfig          `yaml:"subscriptions"`
	Models              map[string]ModelOverride             `yaml:"models"`
	Retry               LLMRetryConfig                       `yaml:"retry"`
}

// SubscriptionProvidersConfig contains public connection metadata only. Access
// and refresh tokens are held exclusively in the encrypted CredentialStore.
type SubscriptionProvidersConfig struct {
	ChatGPT SubscriptionProviderConfig `yaml:"chatgpt"`
	Kimi    SubscriptionProviderConfig `yaml:"kimi"`
}

// SubscriptionProviderConfig configures an operator-approved managed service.
// No field is a credential. ChatGPT is additionally gated by experimental.enabled.
type SubscriptionProviderConfig struct {
	Enabled          bool     `yaml:"enabled"`
	BaseURL          string   `yaml:"base_url"`
	AuthorizationURL string   `yaml:"authorization_url"`
	TokenURL         string   `yaml:"token_url"`
	ClientID         string   `yaml:"client_id"`
	Scopes           []string `yaml:"scopes"`
	Models           []string `yaml:"models"`
	// EnabledModels is the user-selected subset of the subscription catalogue.
	// nil preserves the full catalogue for existing configurations; an explicit
	// empty list intentionally hides every model without touching credentials.
	EnabledModels []string `yaml:"enabled_models"`
}

// AnthropicConfig holds Anthropic provider configuration.
type AnthropicConfig struct {
	APIKey string   `yaml:"api_key"`
	Models []string `yaml:"models"` // enabled models for this provider
	// OutputTokenReserve overrides the output-token budget for every model
	// served by this provider: it is subtracted from the context window in
	// overflow validation and caps executor MaxTokens. 0 = inherit the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// OpenAICompatibleConfig holds OpenAI-compatible provider configuration.
type OpenAICompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
	// OutputTokenReserve overrides the output-token budget for every model
	// served by this provider: it is subtracted from the context window in
	// overflow validation and caps executor MaxTokens. 0 = inherit the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// AnthropicCompatibleConfig holds Anthropic-compatible provider configuration
// (custom endpoints speaking Anthropic's Messages API, e.g. a proxy or gateway).
type AnthropicCompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
	// OutputTokenReserve overrides the output-token budget for every model
	// served by this provider: it is subtracted from the context window in
	// overflow validation and caps executor MaxTokens. 0 = inherit the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// ChatGPTConfig holds ChatGPT (OpenAI) provider configuration.
type ChatGPTConfig struct {
	APIKey string   `yaml:"api_key"`
	Models []string `yaml:"models"` // enabled models for this provider
	// OutputTokenReserve overrides the output-token budget for every model
	// served by this provider: it is subtracted from the context window in
	// overflow validation and caps executor MaxTokens. 0 = inherit the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// ModelOverride allows overriding built-in model metadata.
// Fields use omitempty so a 0/empty/nil value (meaning "inherit the built-in
// default") is not serialized — only fields that actually differ from the
// built-in metadata are persisted to config.yaml.
//
// TokenizerType/Family/Protocol use the empty string as the "inherit" sentinel
// (the built-in resolver derives them via DetectFamily/DetectProtocol when
// unset). Capabilities uses a nil pointer: nil = inherit default, a non-nil
// value overrides all four capability flags atomically. The string and pointer
// sentinels ensure a deliberate override to "default"/""/all-false is still
// distinguishable from "no override", so a user can force e.g. a false
// Attachment capability that differs from the built-in true.
type ModelOverride struct {
	ContextWindow int                    `yaml:"context_window,omitempty"`
	OutputLimit   int                    `yaml:"output_limit,omitempty"`
	TokenizerType string                 `yaml:"tokenizer_type,omitempty"`
	Family        string                 `yaml:"family,omitempty"`
	Protocol      string                 `yaml:"protocol,omitempty"`
	Capabilities  *llm.ModelCapabilities `yaml:"capabilities,omitempty"`
}

// LLMRetryConfig configures retry behavior for LLM API calls.
type LLMRetryConfig struct {
	MaxRetries     int    `yaml:"max_retries"`     // max retry attempts (0 = no retries)
	InitialBackoff string `yaml:"initial_backoff"` // initial backoff duration (e.g. "1s")
	MaxBackoff     string `yaml:"max_backoff"`     // maximum backoff duration (e.g. "30s")
}

// MCPConfig holds MCP server configurations.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig defines how to launch an MCP server.
type MCPServerConfig struct {
	Transport string `yaml:"transport,omitempty" json:"transport,omitempty"` // "stdio" | "http"; default "stdio"

	// stdio fields (existing)
	Command string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// http fields (new)
	URL     string            `yaml:"url,omitempty" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// RouterConfig holds router settings.
type RouterConfig struct {
	HistoryWindow int `yaml:"history_window"`
}

// ToolResultBudgetConfig configures tool result size limits to prevent single tool outputs
// from consuming too much of the context window.
type ToolResultBudgetConfig struct {
	HardCapTokens   int     `yaml:"hard_cap_tokens"`   // absolute max tokens per tool result (default: 4096)
	MaxFillFraction float64 `yaml:"max_fill_fraction"` // max fraction of available context space (default: 0.3)
	CacheTTLSeconds int     `yaml:"cacheTTLSeconds"`   // TTL in seconds for MCP tool cache entries (default: 300)
}

// ToolOutputPruningConfig configures selective pruning of old tool outputs to save context.
type ToolOutputPruningConfig struct {
	KeepLastN        int      `yaml:"keepLastN"`
	ProtectedTools   []string `yaml:"protectedTools"`
	ThresholdPercent float64  `yaml:"thresholdPercent"` // Context fill % below which pruning is skipped (default: 50)
}

// HistoryMutationConfig configures regular (non-emergency) history mutation
// to reduce O(n²) replay cost. Unlike emergency compaction, mutation runs on
// every BuildPrompt call and replaces old tool results with cache references.
type HistoryMutationConfig struct {
	ToolResultEvictionStep int  `yaml:"toolResultEvictionStep"` // evict tool results to cache refs after N steps (default: 10)
	EvictStepStatus        bool `yaml:"evictStepStatus"`        // evict update_checklist results immediately
	DedupRepeatedReads     bool `yaml:"dedupRepeatedReads"`     // replace duplicate file reads with reference
}

// CircuitBreakerConfig holds circuit breaker thresholds for executor protection.
type CircuitBreakerConfig struct {
	RepeatNudgeThreshold         int `yaml:"repeatNudgeThreshold"`         // consecutive identical tool calls before nudge
	RepeatAbortThreshold         int `yaml:"repeatAbortThreshold"`         // consecutive identical tool calls before abort
	TruncationAbortThreshold     int `yaml:"truncationAbortThreshold"`     // consecutive truncated responses before abort
	ParseErrorAbortThreshold     int `yaml:"parseErrorAbortThreshold"`     // consecutive parse errors before abort
	FruitlessNudgeThreshold      int `yaml:"fruitlessNudgeThreshold"`      // consecutive minimal-result calls before nudge (default: 4)
	FruitlessAbortThreshold      int `yaml:"fruitlessAbortThreshold"`      // consecutive minimal-result calls before abort (default: 6)
	FruitlessMaxResultLen        int `yaml:"fruitlessMaxResultLen"`        // result length at or below which a call is "fruitless" (default: 32)
	SameToolRepeatNudgeThreshold int `yaml:"sameToolRepeatNudgeThreshold"` // same tool with varied args, similar results (default: 6)
	SameToolRepeatAbortThreshold int `yaml:"sameToolRepeatAbortThreshold"` // abort threshold (default: 10)
	SameToolResultSizeDelta      int `yaml:"sameToolResultSizeDelta"`      // max result length difference to consider "similar" (default: 64)
}

// ExecutorConfig holds executor settings.
type ExecutorConfig struct {
	MaxRetries         int                     `yaml:"max_retries"`
	OutputTokenReserve int                     `yaml:"output_token_reserve"`
	Compaction         CompactionConfig        `yaml:"compaction"`
	ToolResultBudget   ToolResultBudgetConfig  `yaml:"tool_result_budget"`
	ToolOutputPruning  ToolOutputPruningConfig `yaml:"toolOutputPruning"`
	HistoryMutation    HistoryMutationConfig   `yaml:"historyMutation"`
	CircuitBreaker     CircuitBreakerConfig    `yaml:"circuitBreaker"`
	// VerifyOnEdit runs a user-configured verification command (tests/linter)
	// after every successful file edit in CODE tasks and injects its output
	// into the conversation as a system observation. Default off; the command
	// always comes from this config, never from the model. See
	// specs/domains/verify-on-edit.md.
	VerifyOnEdit VerifyOnEditConfig `yaml:"verify_on_edit"`
}

// VerifyOnEditConfig holds the verify-on-edit settings. The zero value is a
// fully disabled no-op.
type VerifyOnEditConfig struct {
	Enabled bool `yaml:"enabled"`
	// Command is the shell command executed after a successful write_file/
	// edit_file call. It runs through the bash tool machinery, so the
	// execute-group deny policy and the command blacklist still apply — but
	// because the command is user-configured (not model-authored) it is not
	// routed through interactive confirmation.
	Command string `yaml:"command"`
	// Timeout is a Go duration string ("120s", "2m"). Empty or invalid values
	// fall back to 2 minutes. The effective timeout is capped by
	// timeouts.bashMaxTimeout — the bash tool enforces its own maximum on
	// every command, so a larger value here is clamped (with a warning).
	Timeout string `yaml:"timeout"`
	// MaxOutputChars caps the verification output injected into the model
	// context (truncated with a marker when exceeded). 0 falls back to the
	// SDK default (agent.DefaultVerifyOnEditCap, currently 4000).
	MaxOutputChars int `yaml:"max_output_chars"`
}

// CompactionConfig holds context compaction settings.
type CompactionConfig struct {
	SlidingWindow       SlidingWindowConfig  `yaml:"sliding_window"`
	Summarization       SummarizationConfig  `yaml:"summarization"`
	Hierarchical        HierarchicalConfig   `yaml:"hierarchical"`
	Thresholds          CompactionThresholds `yaml:"thresholds"`
	MaxSummarizeTokens  int                  `yaml:"maxSummarizeTokens"`  // max tokens for summarization LLM calls (default: 16000)
	ObservationTruncate int                  `yaml:"observationTruncate"` // chars to truncate observations in summaries (default: 500)
	SafetyMarginPercent int                  `yaml:"safetyMarginPercent"` // % of context window reserved as safety margin (default: 5)
	ManualTargetPercent int                  `yaml:"manualTargetPercent"` // target context fill % user-triggered manual compaction aims to reach (default: 30)
}

// CompactionThresholds defines context window usage thresholds for compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int `yaml:"predictive_percent"`
	WarningPercent    int `yaml:"warning_percent"`
	EmergencyPercent  int `yaml:"emergency_percent"`
	PreWarningPercent int `yaml:"pre_warning_percent"`
}

// SlidingWindowConfig configures sliding window compaction.
type SlidingWindowConfig struct {
	KeepFirst int `yaml:"keep_first"`
	KeepLast  int `yaml:"keep_last"`
}

// SummarizationConfig configures summarization compaction.
type SummarizationConfig struct {
	BlockSize int `yaml:"block_size"`
	KeepLast  int `yaml:"keepLast"` // number of recent steps to preserve verbatim (default: 5)
}

// HierarchicalConfig configures hierarchical compaction.
type HierarchicalConfig struct {
	EnabledAboveSteps int     `yaml:"enabled_above_steps"`
	DistantRatio      float64 `yaml:"distantRatio"` // ratio for distant zone (default: 0.4)
	MiddleRatio       float64 `yaml:"middleRatio"`  // ratio for middle zone (default: 0.3)
	RecentRatio       float64 `yaml:"recentRatio"`  // ratio for recent zone (default: 0.3)
}

// InjectionDefenseConfig holds prompt injection defense settings.
type InjectionDefenseConfig struct {
	Enabled *bool `yaml:"enabled"` // pointer to distinguish "not set" from "false"; nil defaults to true
}

// Tool group names for the security.groups schema. The set of configurable
// groups is fixed; every non-internal tool maps into exactly one group (the
// mapping is declared per tool via ToolGroup on each tool's constructor —
// see the ADR-024 group table and tools/group.go in sp4rk).
const (
	ToolGroupLocalRead   = "local_read"   // read-only access to local workspace files
	ToolGroupRemoteRead  = "remote_read"  // read-only access to remote resources (web_fetch, web_search)
	ToolGroupExecute     = "execute"      // shell execution (bash_exec, posh_exec)
	ToolGroupLocalWrite  = "local_write"  // writes to local workspace files
	ToolGroupLocalMCP    = "local_mcp"    // MCP servers launched locally (stdio)
	ToolGroupRemoteMCP   = "remote_mcp"   // MCP servers reached over the network (http)
	ToolGroupRemoteWrite = "remote_write" // writes to remote resources (e.g. remote MCP mutations)

	// ToolGroupSystem is a reserved group covering internal/system tools (the
	// sdktools.GroupSystem each such tool declares on its BaseTool). Its
	// policy is fixed (always allowed, never surfaced for
	// confirmation) and it MUST NOT appear in config.yaml — validation rejects
	// it so users cannot widen or narrow the internal-tools bypass from the
	// config file.
	ToolGroupSystem = "system"
)

// Policy values accepted by security.groups.<group>.policy.
const (
	GroupPolicyAllow       = "allow"        // execute without confirmation
	GroupPolicyUserConfirm = "user_confirm" // ask the user before each call
	GroupPolicyDeny        = "deny"         // refuse to execute
)

// TrustedGitRepo is one entry in security.trusted_git_repos: a repository
// whose untrusted-git-config intake warning the user has explicitly dismissed.
// Path is the absolute, filepath.Clean-ed repository work-tree root (the same
// form TrustGitRepo stores and notifyGitConfigRisk attributes warnings to).
// Fingerprint identifies the git-config snapshot captured at trust time
// (stored separately under ~/.c0wrk/git-config-snapshots/, see
// GitConfigSnapshotsDir) so a later scan can diff against it and reinstate the
// warning when the config changed after the trust decision. Fingerprint may be
// empty for entries migrated from the pre-fingerprint string format (a bare
// path) — those keep suppressing the warning unconditionally until re-trusted.
type TrustedGitRepo struct {
	Path        string `yaml:"path"`
	Fingerprint string `yaml:"fingerprint,omitempty"`
}

// UnmarshalYAML accepts both the current mapping form ({path, fingerprint})
// and the legacy string form (a bare absolute path, pre-fingerprint). A legacy
// string migrates to a Path with an empty Fingerprint, preserving warning
// suppression for already-trusted repositories without inventing a snapshot
// that was never captured.
func (r *TrustedGitRepo) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		r.Path = value.Value
		r.Fingerprint = ""
		return nil
	}
	type plain TrustedGitRepo
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*r = TrustedGitRepo(p)
	return nil
}

// SecurityConfig holds security settings.
type SecurityConfig struct {
	Judge            JudgeConfig            `yaml:"judge"`
	InjectionDefense InjectionDefenseConfig `yaml:"injection_defense"`

	// Groups is the tool-security schema: a fixed set of tool groups, each
	// with its own policy (and, for the "execute" group only, an optional
	// command blacklist). See the ToolGroup* constants for the group names and
	// defaults.go for the default policies.
	Groups map[string]GroupPolicyConfig `yaml:"groups"`

	// AutoApproveWorkspaceWrites, when true, auto-executes file write tools
	// (write_file, edit_file, delete_file, delete_directory, create_directory)
	// without user confirmation when all paths are within the session workspace.
	// Symlink traversals are still intercepted and forced to confirmation
	// regardless of this setting. Default: false (always confirm writes).
	AutoApproveWorkspaceWrites bool `yaml:"auto_approve_workspace_writes"`

	// SmartApprove, when true, asks the strict judge to resolve effective
	// user_confirm calls after deterministic and workspace gates. Only strict
	// ALLOW skips UI; every other outcome still requires the user. Default false.
	SmartApprove bool `yaml:"smart_approve"`

	// AgentsMDMaxBytes caps the AGENTS.md content size injected into prompts.
	// AGENTS.md is workspace-controlled untrusted input; without a cap a large or
	// malicious file could flood the context window.
	//   0  = use default (65536)
	//  -1  = no cap (unlimited — USE WITH CAUTION)
	AgentsMDMaxBytes int `yaml:"agents_md_max_bytes"`

	// TrustedGitRepos lists repository roots for which the untrusted-git-config
	// intake warning (the project:git_config_risk event fired on project switch
	// or work-directory add, see ADR-033 layer 3) has been explicitly
	// dismissed by the user via the "Trust this repo" action. Each entry is a
	// {path, fingerprint} pair: the absolute, filepath.Clean-ed repository
	// work-tree root and the fingerprint of the git-config snapshot captured
	// when the user trusted it (empty for entries migrated from the legacy
	// string format). Matching is exact on the path. Trusting a repo both
	// suppresses the UI warning AND opts the repository back into its own git
	// configuration: backend mirrors this list into the process-wide
	// core/gittrust registry, which the spawn layer consults to run raw git
	// for the root (sysproc.GitCmdRaw) — so its hooks, filters and signing
	// apply as they would outside c0wrk. A root that is NOT trusted (or is
	// hardened, below) keeps the full spawn-layer neutralization.
	// Default: empty (every suspicious config warns; every repo is hardened).
	TrustedGitRepos []TrustedGitRepo `yaml:"trusted_git_repos,omitempty"`

	// HardenGitRepos lists repository roots that are always treated as
	// hardened: their untrusted-git-config intake warning is never suppressed
	// and they are excluded from the trust list — a root cannot be both
	// trusted and hardened (validation enforces the mutual exclusion). Entries
	// are absolute, filepath.Clean-ed paths, matching is exact. Hardening is
	// the inverse of trust at the spawn layer: a hardened root never becomes
	// raw-git eligible, so the spawn-layer neutralization stays in force for
	// it regardless of anything else.
	// Default: empty.
	HardenGitRepos []string `yaml:"harden_git_repos,omitempty"`
}

// GroupPolicyConfig holds per-group security policy configuration
// (security.groups.<group>).
type GroupPolicyConfig struct {
	Policy string `yaml:"policy"` // "allow"|"user_confirm"|"deny"
	// Blacklist holds regex patterns applied to shell commands; only the
	// "execute" group supports it and validation rejects it on any other
	// group. A matching command is forced to confirmation regardless of
	// Policy.
	Blacklist []string `yaml:"blacklist,omitempty"`
}

// JudgeConfig holds LLM-based tool safety judge settings.
type JudgeConfig struct {
	Model string `yaml:"model"` // LLM model override for judge calls (empty = use default)
}

// SearchConfig holds web search configuration.
type SearchConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
}

// ToolLimitsConfig holds configurable limits for builtin tools.
// These limits prevent tool outputs from consuming excessive context.
type ToolLimitsConfig struct {
	// File read limits
	ReadDefaultLines int `yaml:"readDefaultLines"` // max lines per read call (default: 2000)

	// Search limits
	WebSearchMaxResults int `yaml:"webSearchMaxResults"` // max web search results (default: 5)

	// Per-tool Stage 1 truncation defaults (line/byte-based, applied before token budget).
	// If omitted for a tool, no Stage 1 truncation is applied.
	PerToolTruncation map[string]ToolTruncationConfig `yaml:"perToolTruncation"`
}

// ToolTruncationConfig — per-tool truncation settings for the universal caching layer.
type ToolTruncationConfig struct {
	MaxLines int `yaml:"maxLines"` // 0 = no line-based truncation
	MaxBytes int `yaml:"maxBytes"` // 0 = no byte-based truncation
}

// TimeoutsConfig holds configurable timeout values for various operations.
type TimeoutsConfig struct {
	BashMaxTimeout           int `yaml:"bashMaxTimeout"`           // seconds, default: 120
	BashWaitDelay            int `yaml:"bashWaitDelay"`            // seconds, default: 5
	RipgrepTimeout           int `yaml:"ripgrepTimeout"`           // seconds, default: 60
	WebFetchTimeout          int `yaml:"webFetchTimeout"`          // seconds, default: 30
	WebSearchTimeout         int `yaml:"webSearchTimeout"`         // seconds, default: 30
	PersistenceTimeout       int `yaml:"persistenceTimeout"`       // seconds, default: 5
	LLMRequestTimeout        int `yaml:"llmRequestTimeout"`        // seconds, default: 600 (10 min) — main chat loop
	ServiceLLMRequestTimeout int `yaml:"serviceLLMRequestTimeout"` // seconds, default: 120 (2 min) — one-shot service LLM requests (session title, commit message, prompt optimization)
}

// OrchestrationConfig holds orchestration-specific limits and settings.
type OrchestrationConfig struct {
	MaxDependencyContextChars int `yaml:"maxDependencyContextChars"` // default: 8000
	MaxSummaryLength          int `yaml:"maxSummaryLength"`          // default: 500
	MaxJudgeCacheSize         int `yaml:"maxJudgeCacheSize"`         // default: 1000
	// MaxRedelegationDepth caps recursive delegation when allow_redelegate is
	// true (ASI07-R6). 0 means "use the orchestrator default" (currently 2).
	MaxRedelegationDepth int `yaml:"maxRedelegationDepth"` // default: 2
}

// GoalLoopConfig holds settings for the goal-derivation / verification loop.
//
// Verification controls whether the loop runs the independent verifier.
//   - "independent" (default): the goal loop uses an independent verifier turn
//     to confirm task completion before declaring success.
//   - "off": the verifier is disabled; the loop relies solely on the agent's
//     own declare_goal_status verdict.
type GoalLoopConfig struct {
	Verification string `yaml:"verification"` // "independent" | "off"; default "independent"
}

// SkillsConfig holds Agent Skills discovery configuration.
type SkillsConfig struct {
	// Dirs lists skill discovery directories in priority order (highest first).
	// Paths may be absolute or relative to the agent directory (~/.c0wrk).
	Dirs []string `yaml:"dirs"`
}

// AgentsConfig holds Subagent Profile (AGENT.md) discovery configuration.
// Mirrors SkillsConfig: the project-local `.agents/agents` directory is always
// scanned automatically (see core/builder.go) and does NOT need to be listed.
type AgentsConfig struct {
	// Dirs lists subagent profile discovery directories in priority order
	// (highest first). Paths may be absolute or relative to the agent dir.
	Dirs []string `yaml:"dirs"`
}

// envVarPattern matches ${ENV_VAR} patterns for substitution.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ExperimentalConfig gates experimental features behind a single master switch.
// It is all-or-nothing by design: there is no per-feature toggle, so enabling
// it exposes every gated feature and disabling it hides every gated feature.
type ExperimentalConfig struct {
	// Enabled is the master switch for experimental features. When false, every
	// gated feature (RESEARCH mode, the Small-LLM profile, and ChatGPT
	// subscription sign-in) is treated as off. Default: false.
	Enabled bool `yaml:"enabled"`
}

// SmallLLMConfig configures optimizations applied when running on a "small"
// (low-capacity / cheaper) LLM. The master toggle is manual only — there is no
// auto-detection; the operator decides when to enable the profile. Each variant
// carries its own sub-toggle so individual optimizations can be turned off
// independently, and every threshold/value is exposed so behaviour can be tuned
// without a rebuild.
type SmallLLMConfig struct {
	// Enabled is the master toggle for the small-LLM profile. When false, every
	// variant sub-toggle is ignored. There is no auto-detection — this must be
	// set explicitly. Default: false.
	Enabled bool `yaml:"enabled"`

	// EssentialTools narrows the visible tool set to a curated subset to reduce
	// the schema/token overhead injected into every prompt.
	EssentialTools EssentialToolsConfig `yaml:"essential_tools"`

	// SystemPrompt applies prompt-simplification variants (lite, few-shot,
	// reasoning scaffold) to shrink the system prompt size.
	SystemPrompt SystemPromptConfig `yaml:"system_prompt"`

	// Sampling overrides LLM sampling parameters for more deterministic,
	// lower-effort generation suitable for smaller models.
	Sampling SmallLLMSamplingConfig `yaml:"sampling"`

	// LoopHardening tightens the executor circuit-breaker thresholds so a small
	// model that repeats itself or fails to make progress is nudged/aborted
	// sooner, conserving the token budget.
	LoopHardening LoopHardeningConfig `yaml:"loop_hardening"`

	// Context applies aggressive context management: tighter compaction, stricter
	// tool-output pruning, and a larger output token reserve.
	Context SmallLLMContextConfig `yaml:"context"`
}

// EssentialToolsConfig narrows the tool set visible to a small LLM to reduce
// per-prompt schema overhead.
type EssentialToolsConfig struct {
	// Enabled gates this variant. When false the full tool set is exposed
	// regardless of the master SmallLLM.Enabled toggle.
	Enabled bool `yaml:"enabled"`

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. Tools not in this list are hidden from the model
	// unless the router matches them into a free slot.
	AlwaysPresent []string `yaml:"always_present"`

	// MaxTools caps the router-matched slots: at most
	// maxTools − len(guaranteed) matched tools are kept, where guaranteed =
	// always-present ∪ protected ∪ MCP. The guaranteed set itself is never
	// trimmed (validateSmallLLMConfig rejects configs where it alone exceeds
	// MaxTools).
	MaxTools int `yaml:"max_tools"`

	// CompactDescriptions replaces every known builtin's full rubric
	// description with a one-line compact variant while this variant is
	// active, shrinking prompt overhead on small models. Off by default:
	// with it off, descriptions are byte-identical to the non-SmallLLM
	// behavior.
	CompactDescriptions bool `yaml:"compact_descriptions"`
}

// SystemPromptConfig applies prompt-simplification variants to shrink the
// system prompt injected for a small LLM. Each flag is independent; FewShot
// and ReasoningScaffold are only honored when Lite is active (both are
// tailored to the compact lite directive).
type SystemPromptConfig struct {
	// Lite trims verbose guidance from the base system prompt, swapping the
	// verbose OrchestratorSystem directive for the compact OrchestratorSystemLite.
	Lite bool `yaml:"lite"`
	// FewShot appends curated worked-example ReAct cycles demonstrating correct
	// tool-call format, tool choice, error recovery, and finish. Only applied
	// when Lite is active.
	FewShot bool `yaml:"few_shot"`
	// ReasoningScaffold appends a lightweight three-step reasoning scaffold
	// (goal → tool+why → args) tailored to small-model instruction following.
	// Only applied when Lite is active.
	ReasoningScaffold bool `yaml:"reasoning_scaffold"`
}

// SmallLLMSamplingConfig overrides LLM sampling parameters for a small model.
// Every parameter uses zero as the "not set" sentinel: an unset parameter
// inherits the per-family vendor preset (prompt.DefaultSampling) instead of
// clobbering it, so enabling the sampling variant with no explicit values is
// a behavioral no-op. Out-of-range values are rejected by validation
// (frontend_api_config.go) whenever they are set.
type SmallLLMSamplingConfig struct {
	// Enabled gates this variant.
	Enabled bool `yaml:"enabled"`

	// Temperature sets generation temperature (lower = more deterministic).
	// 0 (unset) inherits the vendor preset; when set it must be > 0.
	Temperature float64 `yaml:"temperature"`

	// TopP sets nucleus-sampling probability mass. 0 (unset) inherits the
	// vendor preset; when set it must be in (0, 1].
	TopP float64 `yaml:"top_p"`

	// TopK sets top-k sampling. 0 (unset) inherits the vendor preset; when
	// set it must be >= 1.
	TopK int `yaml:"top_k"`

	// RepetitionPenalty penalizes repeated tokens. 0 (unset) inherits the
	// vendor preset; when set it must be in [1, 2].
	RepetitionPenalty float64 `yaml:"repetition_penalty"`

	// PresencePenalty penalizes tokens already present in the context — the
	// OpenAI-schema anti-repetition lever. The Qwen card sanctions 0–2 (the
	// instruct default is 1.5); values above 2 increase language mixing, so
	// they are rejected. 0 (unset) inherits the vendor preset (no family
	// preset sets it, so the field is simply not sent); when set it must be
	// in [0, 2].
	PresencePenalty float64 `yaml:"presence_penalty"`

	// ReasoningEffort controls reasoning depth: "" (unset → default
	// "medium", see defaults.go) | "off" | "low" | "medium". Smaller models
	// generally benefit from reduced reasoning effort; explicit values are
	// never overwritten by the seeded default.
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// LoopHardeningConfig tightens executor circuit-breaker thresholds for a small
// LLM so loops are caught earlier, conserving the token budget.
type LoopHardeningConfig struct {
	// Enabled gates this variant.
	Enabled bool `yaml:"enabled"`

	// RepeatNudgeThreshold is the number of consecutive identical tool calls
	// before a corrective nudge is issued.
	RepeatNudgeThreshold int `yaml:"repeat_nudge_threshold"`

	// ParseErrorAbortThreshold is the number of consecutive response parse
	// errors before the executor aborts.
	ParseErrorAbortThreshold int `yaml:"parse_error_abort_threshold"`

	// FruitlessNudgeThreshold is the number of consecutive minimal-result tool
	// calls before a corrective nudge is issued.
	FruitlessNudgeThreshold int `yaml:"fruitless_nudge_threshold"`

	// FruitlessAbortThreshold is the number of consecutive minimal-result tool
	// calls before the executor aborts.
	FruitlessAbortThreshold int `yaml:"fruitless_abort_threshold"`

	// SameToolRepeatNudgeThreshold is the number of same-tool (varied args, similar
	// results) calls before a corrective nudge is issued.
	SameToolRepeatNudgeThreshold int `yaml:"same_tool_repeat_nudge_threshold"`
}

// SmallLLMContextConfig is the fifth small-LLM profile variant: aggressive
// context management. When active it tightens the executor's compaction knobs
// (smaller sliding window, smaller summarization block, earlier trigger),
// prunes tool outputs more aggressively, and reserves more output tokens so a
// small model is less likely to exhaust the context window mid-task. The
// general executor defaults are NOT changed — the overrides only apply while
// both the master toggle (SmallLLM.Enabled) and this variant's toggle are
// enabled.
type SmallLLMContextConfig struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool `yaml:"enabled"`

	// Compaction overrides the executor compaction knobs.
	Compaction SmallLLMCompactionConfig `yaml:"compaction"`

	// ToolOutputKeepLastN overrides the executor's tool-output pruning depth
	// (stricter than the general executor default).
	ToolOutputKeepLastN int `yaml:"tool_output_keep_last_n"`

	// OutputTokenReserve overrides the token budget reserved for the model's
	// output (e.g. 8192).
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// SmallLLMCompactionConfig holds the compaction-tightening overrides. Zero
// values mean "do not override" — the corresponding executor baseline is kept.
type SmallLLMCompactionConfig struct {
	// KeepLast overrides the sliding-window keep-last count (variant default 6
	// vs the general executor default of 10).
	KeepLast int `yaml:"keep_last"`

	// BlockSize overrides the summarization block size (variant default 5 vs
	// the general 7).
	BlockSize int `yaml:"block_size"`

	// TriggerPercent overrides the predictive compaction trigger percentage
	// (variant default 80 vs the general 85).
	TriggerPercent int `yaml:"trigger_percent"`
}

// ExpandEnvVars expands ${ENV_VAR} patterns in a string with their environment variable values.
// This is a public function that can be used at runtime for values that bypass config file loading.
func ExpandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract the variable name from ${VAR_NAME}
		varName := match[2 : len(match)-1]
		return os.Getenv(varName)
	})
}

// LoadResult contains the result of loading a configuration file.
type LoadResult struct {
	Config     *Config
	LoadErrors []string // non-fatal errors/warnings encountered during load
}

// ProviderWithModels pairs a provider config key with its enabled models.
type ProviderWithModels struct {
	Name         string // config key: "anthropic", "chatgpt", or a named openai_compatible provider
	ProviderType string // Go type constant
	APIKey       string
	BaseURL      string
	Models       []string // enabled models for this one provider
	// OutputTokenReserve is the per-provider output-token budget override
	// (0 = inherit the global executor.output_token_reserve).
	OutputTokenReserve int
}

// providerEntry is the canonical, single-source-of-truth provider list.
type providerEntry struct {
	name               string
	apiKey             string
	baseURL            string
	models             []string
	outputTokenReserve int
}

// allProviderEntries returns the flat list of all known providers.
func (c *LLMConfig) allProviderEntries() []providerEntry {
	// Sort openai_compatible keys for deterministic order
	openaiKeys := make([]string, 0, len(c.OpenAICompatible))
	for k := range c.OpenAICompatible {
		openaiKeys = append(openaiKeys, k)
	}
	sort.Strings(openaiKeys)
	// Sort anthropic_compatible keys for deterministic order
	anthropicKeys := make([]string, 0, len(c.AnthropicCompatible))
	for k := range c.AnthropicCompatible {
		anthropicKeys = append(anthropicKeys, k)
	}
	sort.Strings(anthropicKeys)
	entries := make([]providerEntry, 0, 2+len(openaiKeys)+len(anthropicKeys))
	entries = append(entries,
		providerEntry{name: "anthropic", apiKey: c.Anthropic.APIKey, models: c.Anthropic.Models, outputTokenReserve: c.Anthropic.OutputTokenReserve},
		providerEntry{name: "chatgpt", apiKey: c.ChatGPT.APIKey, models: c.ChatGPT.Models, outputTokenReserve: c.ChatGPT.OutputTokenReserve},
	)
	for _, name := range openaiKeys {
		cfg := c.OpenAICompatible[name]
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, outputTokenReserve: cfg.OutputTokenReserve})
	}
	for _, name := range anthropicKeys {
		cfg := c.AnthropicCompatible[name]
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, outputTokenReserve: cfg.OutputTokenReserve})
	}
	return entries
}

// providerType maps a config-level provider name to the Go provider type.
func (c *LLMConfig) providerType(name string) string {
	switch name {
	case "anthropic":
		return "anthropic"
	case "chatgpt":
		return "openai"
	default:
		if _, ok := c.OpenAICompatible[name]; ok {
			return "openai"
		}
		if _, ok := c.AnthropicCompatible[name]; ok {
			return "anthropic"
		}
		return ""
	}
}

// GetAllProviderConfigs returns all known providers, including those with no
// models enabled yet. Callers that require enabled models (e.g. the LLM router)
// filter by len(Models) > 0 at the usage site.
func (c *LLMConfig) GetAllProviderConfigs() []ProviderWithModels {
	result := make([]ProviderWithModels, 0, len(c.allProviderEntries()))
	for _, p := range c.allProviderEntries() {
		result = append(result, ProviderWithModels{
			Name:               p.name,
			ProviderType:       c.providerType(p.name),
			APIKey:             p.apiKey,
			BaseURL:            p.baseURL,
			Models:             p.models,
			OutputTokenReserve: p.outputTokenReserve,
		})
	}
	return result
}

// ResolveDefaultModelProvider looks up the provider that owns DefaultModel.
// DefaultModel may be a bare model name (resolved to the first matching
// provider) or a composite identifier "provider/model" (resolved to the named
// provider). Returns the provider config and the bare model name, or an error
// if DefaultModel is empty or not found in any provider's Models list.
func (c *LLMConfig) ResolveDefaultModelProvider() (ProviderWithModels, string, error) {
	if c.DefaultModel == "" {
		return ProviderWithModels{}, "", errors.New("default_model is not set")
	}

	// Composite default_model: resolve to the named provider + bare model.
	if provider, model, ok := llm.ParseCompositeModelID(c.DefaultModel); ok {
		for _, p := range c.allProviderEntries() {
			if p.name != provider {
				continue
			}
			for _, m := range p.models {
				if m == model {
					return ProviderWithModels{
						Name:         p.name,
						ProviderType: c.providerType(p.name),
						APIKey:       p.apiKey,
						BaseURL:      p.baseURL,
						Models:       p.models,
					}, m, nil
				}
			}
		}
		return ProviderWithModels{}, "", fmt.Errorf("default_model %q not found in provider %q enabled models", model, provider)
	}

	// Bare default_model: first matching provider wins.
	for _, p := range c.allProviderEntries() {
		for _, m := range p.models {
			if m == c.DefaultModel {
				return ProviderWithModels{
					Name:         p.name,
					ProviderType: c.providerType(p.name),
					APIKey:       p.apiKey,
					BaseURL:      p.baseURL,
					Models:       p.models,
				}, m, nil
			}
		}
	}

	return ProviderWithModels{}, "", fmt.Errorf("default_model %q not found in any provider's enabled models", c.DefaultModel)
}

// Load reads a configuration file, applies defaults, validates the configuration, and returns it.
// Environment variable references like ${VAR} are preserved as-is in the config struct;
// use ExpandEnvVars() at runtime to resolve them when needed.
// For better error handling and migration support, use LoadWithResult.
func Load(path string) (*Config, error) {
	result, err := LoadWithResult(path)
	if err != nil {
		return nil, err
	}
	return result.Config, nil
}

// LoadWithResult reads a configuration file with full error reporting.
// Environment variable references like ${VAR} are preserved as-is;
// they are resolved at runtime via ExpandEnvVars() when actually needed.
func LoadWithResult(path string) (*LoadResult, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Unmarshal as current format (env var references like ${VAR} are preserved as-is;
	// they are resolved at runtime via ExpandEnvVars when actually needed).
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Apply defaults for zero-value fields
	ApplyDefaults(&cfg)

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return &LoadResult{
			Config:     &cfg,
			LoadErrors: []string{"Config validation failed: " + err.Error()},
		}, fmt.Errorf("config validation failed: %w", err)
	}

	return &LoadResult{Config: &cfg}, nil
}

// Save writes the configuration to a YAML file atomically.
func Save(cfg *Config, path string) error {
	// Marshal a store-as-unset view of the security groups: an execute
	// blacklist exactly equal to the shipped defaults is written as omitted,
	// so every persist path — not just the security settings tab — preserves
	// the file-format contract that omitting `blacklist:` tracks the app's
	// shipped defaults (config.example.yaml). The in-memory config is left
	// untouched: ApplyDefaults re-derives the effective list at load, and the
	// runtime consumers (ToBuilderConfig, groupPoliciesToResponse) treat nil
	// and the materialized defaults identically.
	view := *cfg
	view.Security.Groups = StoreDefaultBlacklistAsUnset(cfg.Security.Groups)
	data, err := yaml.Marshal(&view)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically: write to temp file, then rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Clean up temp file on rename failure
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename config file: %w", err)
	}
	return nil
}

// validate checks that the configuration is valid.
func validate(cfg *Config) error {
	// Validate default_model
	if cfg.LLM.DefaultModel == "" {
		return errors.New("llm.default_model is not set")
	}

	// Validate at least one provider has models
	hasModels := false
	for _, p := range cfg.LLM.allProviderEntries() {
		if len(p.models) > 0 {
			hasModels = true
			break
		}
	}
	if !hasModels {
		return errors.New("at least one provider must have enabled models")
	}

	// Validate default_model exists in some provider's models list
	_, _, err := cfg.LLM.ResolveDefaultModelProvider()
	if err != nil {
		return err
	}

	// Validate the security.groups schema: only the fixed set of configurable
	// groups is accepted, the reserved "system" group must never appear in
	// config, policies must use the group enum, and a blacklist is an
	// execute-only feature.
	for name, group := range cfg.Security.Groups {
		if name == ToolGroupSystem {
			return fmt.Errorf(
				"security group %q is reserved for system tools and cannot be configured",
				ToolGroupSystem,
			)
		}
		if !IsConfigurableToolGroup(name) {
			return fmt.Errorf(
				"unknown security group %q; must be one of: %s",
				name, strings.Join(sortedToolGroupNames, ", "),
			)
		}
		switch group.Policy {
		case GroupPolicyAllow, GroupPolicyUserConfirm, GroupPolicyDeny:
			// valid
		default:
			return fmt.Errorf(
				"security group %q has invalid policy %q; must be one of: %s, %s, %s",
				name, group.Policy, GroupPolicyAllow, GroupPolicyUserConfirm, GroupPolicyDeny,
			)
		}
		if name != ToolGroupExecute && len(group.Blacklist) > 0 {
			return fmt.Errorf(
				"security group %q does not support a blacklist; only %q does",
				name, ToolGroupExecute,
			)
		}
		for _, pattern := range group.Blacklist {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf(
					"security group %q blacklist pattern %q does not compile: %w",
					name, pattern, err,
				)
			}
		}
	}

	// Validate and normalize security.trusted_git_repos and
	// security.harden_git_repos. Both hold absolute repository roots — compared
	// literally (after Clean) against scanned work-tree roots — so a relative
	// entry could never match and is rejected at load time rather than leaving
	// dead config. Entries are cleaned in place, duplicate roots are rejected,
	// and the two lists are mutually exclusive: a root cannot be both trusted
	// (warning suppressed) and hardened (warning forced).
	seenTrusted := make(map[string]struct{}, len(cfg.Security.TrustedGitRepos))
	cleanTrusted := make([]TrustedGitRepo, 0, len(cfg.Security.TrustedGitRepos))
	for _, repo := range cfg.Security.TrustedGitRepos {
		if repo.Path == "" {
			return errors.New("security.trusted_git_repos must not contain empty paths")
		}
		cleaned := filepath.Clean(repo.Path)
		if !filepath.IsAbs(cleaned) {
			return fmt.Errorf(
				"security.trusted_git_repos entry %q must be an absolute path",
				repo.Path,
			)
		}
		if _, dup := seenTrusted[cleaned]; dup {
			return fmt.Errorf("security.trusted_git_repos contains duplicate path %q", cleaned)
		}
		seenTrusted[cleaned] = struct{}{}
		cleanTrusted = append(cleanTrusted, TrustedGitRepo{Path: cleaned, Fingerprint: repo.Fingerprint})
	}
	cfg.Security.TrustedGitRepos = cleanTrusted

	seenHarden := make(map[string]struct{}, len(cfg.Security.HardenGitRepos))
	cleanHarden := make([]string, 0, len(cfg.Security.HardenGitRepos))
	for _, repo := range cfg.Security.HardenGitRepos {
		if repo == "" {
			return errors.New("security.harden_git_repos must not contain empty paths")
		}
		cleaned := filepath.Clean(repo)
		if !filepath.IsAbs(cleaned) {
			return fmt.Errorf(
				"security.harden_git_repos entry %q must be an absolute path",
				repo,
			)
		}
		if _, dup := seenHarden[cleaned]; dup {
			return fmt.Errorf("security.harden_git_repos contains duplicate path %q", cleaned)
		}
		if _, conflict := seenTrusted[cleaned]; conflict {
			return fmt.Errorf("repository %q cannot be both trusted and hardened", cleaned)
		}
		seenHarden[cleaned] = struct{}{}
		cleanHarden = append(cleanHarden, cleaned)
	}
	cfg.Security.HardenGitRepos = cleanHarden

	// Validate goal_loop.verification enum.
	switch cfg.GoalLoop.Verification {
	case "independent", "off":
		// valid
	default:
		return fmt.Errorf(
			"goal_loop.verification %q is not valid; must be one of: independent, off",
			cfg.GoalLoop.Verification,
		)
	}

	return nil
}
