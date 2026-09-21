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

	"github.com/v0lka/c0wrk/core/vectorindex"
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

	Router     RouterConfig     `yaml:"router"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Security   SecurityConfig   `yaml:"security"`
	Skills     SkillsConfig     `yaml:"skills"`
	Agents     AgentsConfig     `yaml:"agents"`
	Search     SearchConfig     `yaml:"search"`
	ToolLimits ToolLimitsConfig `yaml:"toolLimits"`
	Timeouts   TimeoutsConfig   `yaml:"timeouts"`
	// ShellExec overrides the shell-execution tool's launch shape
	// (bash_exec on Unix, posh_exec on Windows) and declares the shell the
	// command text is written in. Zero value = built-in launch shape; load
	// problems are fail-soft (warning + default). See shell_exec.go.
	ShellExec     ShellExecConfig     `yaml:"shell_exec"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
	GoalLoop      GoalLoopConfig      `yaml:"goal_loop"`
	VectorIndex   VectorIndexConfig   `yaml:"vector_index"`
	Proxy         ProxyConfig         `yaml:"proxy"`
	Terminal      TerminalConfig      `yaml:"terminal"`
	Git           GitConfig           `yaml:"git"`
	Runtime       RuntimeConfig       `yaml:"runtime"`
	Notifications NotificationsConfig `yaml:"notifications"`

	// ModelProfiles configures optimizations applied when running on a "small"
	// (low-capacity / cheaper) LLM. Only the two durable operator choices are
	// persisted here — the manual-only master toggle (no auto-detection) and
	// the active profile id. Model Profiles is independent of the
	// experimental-features switch: its own master toggle is the only switch.
	// The 25 variant knobs are NOT stored in
	// config.yaml: they are resolved at runtime from the active profile
	// (predefined catalog ∪ custom store, see ResolveModelProfilesConfig). A legacy
	// inline `small_llm:` section with the full knob set is ignored at load
	// (decoding is non-strict) and silently dropped by the next save — the
	// sanctioned reset migration; the effective profile falls back to
	// "generic".
	ModelProfiles ModelProfilesPersistConfig `yaml:"model_profiles"`

	// E2S configures the E2S (explicit-state) execution mode: a run style
	// where the model maintains an externalized state Σ that is patched and
	// re-presented every turn (context bounded at O(1)) instead of replaying
	// a growing transcript. The domain types and the validated merge operator
	// live in core/e2s. The section is gated by experimental.enabled: while
	// the gate is off the section is ineffective (treated as disabled).
	E2S E2SConfig `yaml:"e2s"`

	// Experimental gates features that are still under active development
	// (currently the E2S execution mode) behind a single master switch. Model
	// Profiles is NOT gated by this switch — it carries its own manual master
	// toggle (model_profiles.enabled). When disabled, every gated feature is
	// treated as off. Default: off.
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

// GitConfig controls the automatic git background behaviour of the app
// (periodic and event-driven background fetches). Manual fetches triggered
// from the UI are not gated by this section.
// NotificationsConfig configures the OS-level notification banners of the
// desktop system-notification channel (specs/domains/frontend/
// system-notifications.md).
type NotificationsConfig struct {
	// BannerTimeoutSeconds is how long a delivered banner stays on screen:
	//
	//   -1  let the notification daemon apply its own default
	//    0  never expire — the banner stays until clicked or dismissed
	//   >0  explicit lifetime in seconds
	//
	// A pointer so an explicit 0 ("never expire" — the reason the setting
	// exists) is distinguishable from an absent key, which defaults to -1.
	// Same convention as GitConfig.AutoFetch below.
	//
	// Linux only: the value becomes the freedesktop `expire_timeout` argument
	// (in milliseconds) of the org.freedesktop.Notifications Notify call.
	// macOS and Windows notification centers own banner lifetime themselves
	// and ignore it.
	BannerTimeoutSeconds *int `yaml:"banner_timeout_seconds"`
}

type GitConfig struct {
	// AutoFetch is the master gate for ALL automatic fetch triggers: app
	// startup, project switch, window focus, and the periodic ticker. It is
	// a pointer-bool so callers can distinguish "unset" (defaults to true)
	// from "explicitly disabled" (false), matching the UpdatesConfig
	// convention. When false, no automatic fetch ever runs — only manual
	// fetches from the UI still work.
	AutoFetch *bool `yaml:"auto_fetch"`

	// AutoFetchInterval is the period of the periodic background fetch
	// ticker, expressed as a duration string (e.g. "2m"). Defaults to "2m".
	// The special value "0" disables ONLY the ticker — the event-driven
	// triggers (startup, project switch, window focus) remain active while
	// AutoFetch is true. The string is parsed with time.ParseDuration by the
	// consumer; the config layer only stores the raw value and applies the
	// default. An unparseable value is treated by the consumer as the
	// default.
	AutoFetchInterval string `yaml:"auto_fetch_interval"`
}

// maxMemoryLimitMiB bounds the MiB-valued memory knobs
// (runtime.memory_soft_limit_mb and vector_index.park_budget_mb). Consumers
// convert MiB→bytes with `<< 20`, which overflows int64 to a negative above
// this ceiling — a negative SetMemoryLimit is ignored by the runtime (the soft
// limit silently disappears) and a negative park budget reads as "budget
// disabled". 1<<31 MiB = 2 PiB, far above any real machine, so it only catches
// unit mix-ups (e.g. a byte value entered as MiB).
const maxMemoryLimitMiB = 1 << 31

// RuntimeConfig holds process-level runtime tunables that govern the Go
// runtime itself rather than any agent subsystem (desktop/startup.go consumes
// this section right after config load, before background indexing starts).
type RuntimeConfig struct {
	// MemorySoftLimitMB configures the Go runtime soft memory limit
	// (debug.SetMemoryLimit — a GC target, NOT a hard cap: the heap may
	// exceed it under live-set pressure; the GC just runs more often).
	// Without it, GOGC=100 lets transient spikes — a full project
	// re-index (embedding the whole corpus) — double the resident set, and
	// the memory is never handed back to the OS promptly.
	//
	// Values:
	//   0 (or unset, the default) — AUTO: 50% of physical RAM, clamped to
	//       [2 GiB, 8 GiB] (see desktop/memlimit.go);
	//   >0 — explicit limit in MiB, applied verbatim;
	//   -1 — OFF: no soft limit is set at all.
	//
	// An explicit GOMEMLIMIT environment variable takes priority over AUTO
	// mode (the Go runtime already applied it at process start; the app must
	// not silently override an operator-level setting) — but an explicit
	// positive config value still wins over the env var for this app.
	MemorySoftLimitMB int `yaml:"memory_soft_limit_mb"`
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

	// EmbeddingCacheMaxBytes caps the branch-independent content-addressed
	// embedding cache stored under each project's vector-index directory.
	// 0 (or unset) defaults to 512 MiB; a negative value disables it.
	EmbeddingCacheMaxBytes int64 `yaml:"embedding_cache_max_bytes"`

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

	// ContentFilter configures deterministic early rejection of generated,
	// minified, and pathological files before chunking (see
	// VectorIndexContentFilterConfig). Unset fields fall back to the
	// vectorindex package defaults; the resolved policy participates in
	// the chunker fingerprint, so policy changes re-validate affected
	// files automatically.
	ContentFilter VectorIndexContentFilterConfig `yaml:"content_filter"`

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

	// ParkCapacity is the maximum number of recently-closed projects whose
	// vector-index state (chromem DB + bleve lexical index + file-hash
	// sidecar) is kept resident in RAM so that returning to one of them
	// restores it instantly — skipping the expensive chromem gob-decode of
	// every branch document, plus the lexical index reopen. It is a
	// pointer-int so an unset key resolves to the default of 3 while an
	// explicit 0 is preserved and DISABLES parking (reproducing the
	// historical behaviour where every project switch reopened the
	// persistent DB from scratch). Memory/latency tradeoff: each parked
	// project holds its full vector + lexical index in memory, so raise this
	// on a RAM-rich machine to make frequent project hopping instant, and
	// lower it (or set 0) on a constrained one to cap resident memory.
	ParkCapacity *int `yaml:"park_capacity"`

	// ParkBudgetMb is the cumulative byte budget (in MiB) for the parked
	// project states: right after a state is parked, the oldest parked
	// states are evicted — sidecar flushed, freed RAM nudged back to the OS
	// — while the summed ESTIMATED footprints (documents × (embedding
	// dims × 4 B + 1 KiB)) exceed the budget. The effective bound is
	// min(park_capacity, park_budget_mb): capacity still applies, and an
	// explicit park_capacity: 0 still disables parking entirely regardless
	// of the budget. It is a pointer-int64 so an unset key resolves to the
	// default of 1024 while an explicit -1 is preserved as the "budget
	// disabled" sentinel (park_capacity alone bounds the LRU). An explicit
	// 0 is rejected by validate() as ambiguous and any value below -1 is
	// rejected as a typo (only -1 disables), mirroring
	// runtime.memory_soft_limit_mb.
	ParkBudgetMb *int64 `yaml:"park_budget_mb"`

	// ExecutionProvider selects the ONNX Runtime execution provider the
	// embedding model runs on: VectorIndexProviderAuto ("auto"),
	// VectorIndexProviderCPU ("cpu") or VectorIndexProviderCUDA ("cuda").
	// The empty string (a config written before the knob existed) is
	// normalized to "auto" by ApplyDefaults: try the NVIDIA CUDA provider
	// and fall back to the CPU provider when CUDA is not usable. "cpu"
	// always runs on the CPU; "cuda" targets an NVIDIA GPU. Both "auto"
	// and "cuda" additionally require a GPU (CUDA-enabled) ONNX Runtime
	// build sitting next to the executable — see config.example.yaml.
	// Invalid values are rejected at load time by validate().
	ExecutionProvider string `yaml:"execution_provider"`

	// DeviceID is the GPU device index used when ExecutionProvider
	// resolves to CUDA. Defaults to 0 (the first GPU) — the right choice
	// on single-GPU machines. It is ignored by the CPU provider. Negative
	// values are rejected at load time by validate().
	DeviceID int `yaml:"device_id"`
}

// VectorIndexContentFilterConfig is the YAML surface of the pre-chunk content
// policy (see vectorindex.ContentFilterConfig). Every field is optional:
// pointer bools distinguish "unset" (package default) from an explicit false;
// zero numeric thresholds fall back to the package defaults, matching the
// rest of the vector_index knobs.
type VectorIndexContentFilterConfig struct {
	// Enabled gates the whole policy. Nil (unset) resolves to true.
	Enabled *bool `yaml:"enabled"`
	// DetectGenerated / DetectMinified / DetectPathological toggle the
	// individual heuristics. Nil (unset) resolves to true.
	DetectGenerated    *bool `yaml:"detect_generated"`
	DetectMinified     *bool `yaml:"detect_minified"`
	DetectPathological *bool `yaml:"detect_pathological"`

	// Numeric thresholds; 0 (or unset) keeps the package default.
	GeneratedHeaderBytes       int     `yaml:"generated_header_bytes"`
	MinifiedMinBytes           int     `yaml:"minified_min_bytes"`
	MinifiedMaxLineBytes       int     `yaml:"minified_max_line_bytes"`
	MinifiedMaxWhitespaceRatio float64 `yaml:"minified_max_whitespace_ratio"`
	PathologicalMinBytes       int     `yaml:"pathological_min_bytes"`
	PathologicalMaxTokenBytes  int     `yaml:"pathological_max_token_bytes"`
}

// ResolveContentFilter converts the YAML layer struct into the resolved
// vectorindex policy. Absent fields inherit DefaultContentFilterConfig, so a
// missing content_filter block behaves exactly like the package default.
func (c VectorIndexContentFilterConfig) ResolveContentFilter() vectorindex.ContentFilterConfig {
	resolved := vectorindex.DefaultContentFilterConfig()
	if c.Enabled != nil {
		resolved.Enabled = *c.Enabled
	}
	if c.DetectGenerated != nil {
		resolved.DetectGenerated = *c.DetectGenerated
	}
	if c.DetectMinified != nil {
		resolved.DetectMinified = *c.DetectMinified
	}
	if c.DetectPathological != nil {
		resolved.DetectPathological = *c.DetectPathological
	}
	if c.GeneratedHeaderBytes > 0 {
		resolved.GeneratedHeaderBytes = c.GeneratedHeaderBytes
	}
	if c.MinifiedMinBytes > 0 {
		resolved.MinifiedMinBytes = c.MinifiedMinBytes
	}
	if c.MinifiedMaxLineBytes > 0 {
		resolved.MinifiedMaxLineBytes = c.MinifiedMaxLineBytes
	}
	if c.MinifiedMaxWhitespaceRatio > 0 {
		resolved.MinifiedMaxWhitespaceRatio = c.MinifiedMaxWhitespaceRatio
	}
	if c.PathologicalMinBytes > 0 {
		resolved.PathologicalMinBytes = c.PathologicalMinBytes
	}
	if c.PathologicalMaxTokenBytes > 0 {
		resolved.PathologicalMaxTokenBytes = c.PathologicalMaxTokenBytes
	}
	return resolved
}

// Vector index embedding execution provider values
// (VectorIndexConfig.ExecutionProvider).
const (
	// VectorIndexProviderAuto tries the CUDA provider and falls back to
	// the CPU provider when CUDA is not usable. The default.
	VectorIndexProviderAuto = "auto"
	// VectorIndexProviderCPU always runs embedding inference on the CPU.
	VectorIndexProviderCPU = "cpu"
	// VectorIndexProviderCUDA runs embedding inference on an NVIDIA GPU
	// via the CUDA execution provider.
	VectorIndexProviderCUDA = "cuda"
)

// LLMConfig holds LLM provider configuration with fixed provider schema.
type LLMConfig struct {
	DefaultModel        string                               `yaml:"default_model"` // cross-provider default model (must exist in some provider's Models list)
	Anthropic           AnthropicConfig                      `yaml:"anthropic"`
	OpenAICompatible    map[string]OpenAICompatibleConfig    `yaml:"openai_compatible"`
	AnthropicCompatible map[string]AnthropicCompatibleConfig `yaml:"anthropic_compatible"`
	ChatGPT             ChatGPTConfig                        `yaml:"chatgpt"`
	Models              map[string]ModelOverride             `yaml:"models"`
	Retry               LLMRetryConfig                       `yaml:"retry"`
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
	// TLSFingerprint pins the endpoint's SPKI: base64(SHA-256(SPKI DER)).
	// Non-empty = ONLY the pinned key is accepted (self-signed / internal
	// PKI); empty = normal system CA verification. The pin is the only
	// verification override — there is no configured accept-any state, and
	// no separate toggle. Ignored while an effective HTTP proxy is active
	// (proxy.enabled + a proxy.url). See ADR-054.
	TLSFingerprint string `yaml:"tls_fingerprint,omitempty"`
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
	// TLSFingerprint pins the endpoint's SPKI: base64(SHA-256(SPKI DER)).
	// Non-empty = ONLY the pinned key is accepted (self-signed / internal
	// PKI); empty = normal system CA verification. The pin is the only
	// verification override — there is no configured accept-any state, and
	// no separate toggle. Ignored while an effective HTTP proxy is active
	// (proxy.enabled + a proxy.url). See ADR-054.
	TLSFingerprint string `yaml:"tls_fingerprint,omitempty"`
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
	// execute-group deny policy and the command blocklist still apply — but
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
	SlidingWindow       SlidingWindowConfig      `yaml:"sliding_window"`
	Summarization       SummarizationConfig      `yaml:"summarization"`
	Hierarchical        HierarchicalConfig       `yaml:"hierarchical"`
	Thresholds          CompactionThresholds     `yaml:"thresholds"`
	MaxSummarizeTokens  int                      `yaml:"maxSummarizeTokens"`  // max tokens for summarization LLM calls (default: 16000)
	ObservationTruncate int                      `yaml:"observationTruncate"` // chars to truncate observations in summaries (default: 500)
	SafetyMarginPercent int                      `yaml:"safetyMarginPercent"` // % of context window reserved as safety margin (default: 5)
	Forecast            CompactionForecastConfig `yaml:"forecast"`            // compression-ratio forecast seeds for manual-compaction prediction
}

// CompactionForecastConfig holds the compression-ratio forecast seeds used to
// predict the effect of the LLM-backed manual-compaction strategies. They seed
// an EWMA that is refined after each real compaction; the forecast affects only
// the predicted reclaim number in the compact menu, never strategy availability
// (which is an exact structural verdict). Zero values fall back to sp4rk's
// conservative defaults.
type CompactionForecastConfig struct {
	SummarizationRatio       float64 `yaml:"summarization_ratio"`        // summarization + hierarchical middle (default: 0.3)
	HierarchicalDistantRatio float64 `yaml:"hierarchical_distant_ratio"` // hierarchical distant zone (default: 0.15)
	HierarchicalMiddleRatio  float64 `yaml:"hierarchical_middle_ratio"`  // hierarchical middle zone (default: 0.3)
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

// Mode values accepted by security.autonomy_mode — the unified autonomy
// posture replacing the former pair of autonomy booleans (the Smart Approve
// toggle and the silent-mode master switch). The core mirror of this
// vocabulary lives in core (core.AutonomyMode*); backend/
// configadapter_test.go pins the two dictionaries against each other so
// neither side can rename a value alone.
const (
	// AutonomyModeStandard is the default posture: every interactive prompt
	// (tool confirmations, step-limit cards, ask_user, review prompt)
	// reaches a human.
	AutonomyModeStandard = "standard"
	// AutonomyModeAssisted lets the strict OWASP ASI judge resolve escalated
	// calls (the former Smart Approve toggle): only a strict ALLOW skips the
	// UI; every other outcome still asks the user.
	AutonomyModeAssisted = "assisted"
	// AutonomyModeSilent is the unattended posture (the former
	// security.silent_mode.enabled=true): the four silent-mode sub-policies
	// resolve the interactive prompts without a human.
	AutonomyModeSilent = "silent"
)

// Mode values accepted by security.silent_mode.tool_confirm.mode.
//
// In silent mode a tool call that would normally open a confirmation card
// must resolve without a human. This enum selects how:
const (
	// SilentToolConfirmJudge routes the call through the strict judge (the
	// same OWASP ASI evaluation Smart Approve uses): a strict ALLOW executes,
	// every other outcome (CONFIRM, error, timeout, unparseable) denies. A
	// CANONICAL hard reason is deterministically denied even on a strict ALLOW
	// (the isCanonicalHardReason backstop, as on the interactive path).
	// Default.
	SilentToolConfirmJudge = "judge"
	// SilentToolConfirmAllow executes a confirmation-gated call without UI when
	// it carries no hard safety reason; a call carrying a HARD reason (canonical
	// or not) escalates to the strict judge, which decides (a canonical ALLOW is
	// still backstopped to a denial).
	SilentToolConfirmAllow = "allow"
	// SilentToolConfirmDeny blocks every confirmation-gated call.
	SilentToolConfirmDeny = "deny"
)

// Mode values accepted by security.silent_mode.step_limit.mode.
//
// Selects what the execution loops do at a step-limit boundary (step-budget
// exhaustion or a circuit-breaker abort). "auto" lets the strict loop judge
// decide among the four responses; the other values pin one response
// deterministically without consulting the judge.
const (
	// SilentStepLimitAuto lets the strict loop judge decide the boundary from
	// the trajectory (task, plan progress, abort category, recent steps,
	// metrics): it may grant more steps (allow_once/allow_more/allow_always)
	// or stop (deny). Fail-closed to deny when the judge is unavailable or
	// fails. Default.
	SilentStepLimitAuto = "auto"
	// SilentStepLimitAllowOnce grants exactly one more step.
	SilentStepLimitAllowOnce = "allow_once"
	// SilentStepLimitAllowMore grants a fresh batch of steps.
	SilentStepLimitAllowMore = "allow_more"
	// SilentStepLimitAllowAlways suspends the step budget entirely.
	SilentStepLimitAllowAlways = "allow_always"
	// SilentStepLimitDeny automates the "stop" answer: the run halts at the
	// boundary and reports it, with NO card (unlike "stop", which keeps the
	// interactive card and therefore opts this gate out of silent mode).
	SilentStepLimitDeny = "deny"
	// SilentStepLimitStop does NOT resolve the boundary: it keeps the blocking
	// step-limit card, i.e. opts this gate out of silent mode.
	SilentStepLimitStop = "stop"
)

// Mode values accepted by security.silent_mode.ask_user.mode.
//
// Controls the availability of the ask_user tool:
const (
	// SilentAskUserDisable registers the ask_user tool with a nil callback, so
	// a call resolves to the explicit "not available" result and the agent can
	// never block on a question. Default.
	SilentAskUserDisable = "disable"
	// SilentAskUserEnable keeps the ask_user tool registered.
	SilentAskUserEnable = "enable"
)

// Mode values accepted by security.silent_mode.review_prompt.mode.
//
// Controls the post-task code-review prompt (the review_prompt chat card
// injected after a successful task_complete with uncommitted changes):
const (
	// SilentReviewPromptSuppress does not emit the review prompt. Default.
	SilentReviewPromptSuppress = "suppress"
	// SilentReviewPromptAllow emits the review prompt as when silent mode is off.
	SilentReviewPromptAllow = "allow"
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
	// command blocklist). See the ToolGroup* constants for the group names and
	// defaults.go for the default policies.
	Groups map[string]GroupPolicyConfig `yaml:"groups"`

	// AutoApproveWorkspaceWrites, when true, auto-executes file write tools
	// (write_file, edit_file, delete_file, delete_directory, create_directory)
	// without user confirmation when all paths are within the session workspace.
	// Symlink traversals are still intercepted and forced to confirmation
	// regardless of this setting. Default: false (always confirm writes).
	AutoApproveWorkspaceWrites bool `yaml:"auto_approve_workspace_writes"`

	// AutonomyMode is the unified autonomy posture (security.autonomy_mode),
	// replacing the former pair of autonomy booleans (the Smart Approve
	// toggle and the silent-mode master switch):
	//   standard (default) — every interactive prompt reaches a human;
	//   assisted           — the strict judge resolves effective user_confirm
	//                        calls (the former Smart Approve toggle on);
	//   silent             — unattended operation: the silent_mode
	//                        sub-policies resolve the interactive prompts
	//                        without a human (the former silent-mode master
	//                        switch on).
	// Configs written before the enum carry the two legacy keys instead; the
	// loader migrates them onto this enum (migrateLegacyAutonomyMode) with
	// silent taking priority over assisted, and the legacy keys disappear at
	// the next Save. An unknown value fails safe to standard with a load
	// warning (never a hard load error).
	AutonomyMode string `yaml:"autonomy_mode"`

	// LegacySmartApprove is the load-time mirror of the pre-enum
	// `smart_approve` yaml key. It is populated only by yaml decoding of an
	// old config file and is migrated into AutonomyMode (or dropped) by
	// migrateLegacyAutonomyMode immediately after load — the same pattern as
	// GroupPolicyConfig.LegacyBlacklist (blacklist_migration.go). It is never
	// persisted: omitempty plus the unconditional clearing in the migration
	// keep the stale key out of every Save, and json:"-" keeps it out of any
	// JSON view.
	LegacySmartApprove *bool `yaml:"smart_approve,omitempty" json:"-"`

	// SilentMode is the silent-mode sub-policy container
	// (security.silent_mode). The former master switch (enabled) is gone —
	// whether these policies are live is decided solely by AutonomyMode
	// ("silent"). When the mode is not "silent" every sub-policy is inert
	// and the app behaves exactly as before — confirmations, step-limit
	// cards, ask_user questions, and the post-task review prompt all surface
	// normally. When the mode is "silent", the sub-policies decide how the
	// loops resolve those four interactive prompts without a human. See
	// SilentModeConfig.
	SilentMode SilentModeConfig `yaml:"silent_mode"`

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
	// Blocklist holds regex patterns applied to shell commands; only the
	// "execute" group supports it and validation rejects it on any other
	// group. A matching command is forced to confirmation regardless of
	// Policy. Empty by default: the app ships no predefined patterns, the
	// list is purely a user-authored extension.
	Blocklist []string `yaml:"blocklist,omitempty" json:"blocklist,omitempty"`

	// LegacyBlacklist is the load-time mirror of the pre-rename
	// `blacklist` yaml key. It is populated only by yaml decoding of an
	// old config file and is migrated into Blocklist (or dropped) by
	// migrateLegacyGroupBlacklists immediately after load — see
	// blacklist_migration.go. It is never persisted: omitempty plus the
	// unconditional clearing in the migration keep the stale key out of
	// every Save, and json:"-" keeps it out of any JSON view.
	LegacyBlacklist []string `yaml:"blacklist,omitempty" json:"-"`
}

// SilentModeConfig is security.silent_mode: the container for the four
// unattended-operation sub-policies. It is scoped to four interactive
// decisions the execution loops would otherwise punt to the user. The
// policies are live only while the unified autonomy mode is "silent"
// (security.autonomy_mode — the former master switch silent_mode.enabled is
// migrated onto that enum by the loader); in every other mode each
// sub-policy is inert. When live, each sub-policy decides how its prompt is
// resolved without a human — see the SilentToolConfirm*, SilentStepLimit*,
// SilentAskUser*, and SilentReviewPrompt* enum constants for the accepted
// Mode values and their meaning.
//
// Silent mode only replaces the human ANSWER to a prompt; it never weakens a
// gate: `deny` groups, the deterministic pre-funnel floor, and the canonical
// hard-reason backstop (isCanonicalHardReason) are preserved under every mode —
// a canonical reason is never auto-executed, regardless of the tool_confirm
// mode.
type SilentModeConfig struct {
	// LegacyEnabled is the load-time mirror of the pre-enum
	// `silent_mode.enabled` yaml key. It is populated only by yaml decoding
	// of an old config file and is migrated into SecurityConfig.AutonomyMode
	// (or dropped) by migrateLegacyAutonomyMode immediately after load — the
	// same pattern as GroupPolicyConfig.LegacyBlacklist
	// (blacklist_migration.go). It is never persisted: omitempty plus the
	// unconditional clearing in the migration keep the stale key out of every
	// Save, and json:"-" keeps it out of any JSON view.
	LegacyEnabled *bool `yaml:"enabled,omitempty" json:"-"`

	// ToolConfirm resolves a tool call that would open a confirmation card.
	// Default mode: "judge".
	ToolConfirm SilentSubPolicyConfig `yaml:"tool_confirm"`
	// StepLimit resolves the step-budget-exhaustion card.
	// Default mode: "auto".
	StepLimit SilentSubPolicyConfig `yaml:"step_limit"`
	// AskUser controls availability of the ask_user tool.
	// Default mode: "disable".
	AskUser SilentSubPolicyConfig `yaml:"ask_user"`
	// ReviewPrompt resolves the post-task code-review prompt.
	// Default mode: "suppress".
	ReviewPrompt SilentSubPolicyConfig `yaml:"review_prompt"`
}

// SilentSubPolicyConfig is one silent-mode sub-policy: a single Mode drawn
// from that sub-policy's enum.
type SilentSubPolicyConfig struct {
	Mode string `yaml:"mode"`
}

// ValidateSilentMode checks that every silent-mode sub-policy carries a value
// from its enum. It is shared by config-file validation (validate) and the
// runtime security-settings update (UpdateSecuritySettings) so a UI-sourced
// value can never store what the loader would reject on the next start. An
// empty Mode is accepted here because ApplyDefaults has already seeded the
// default before validation runs; callers that bypass ApplyDefaults (the RPC)
// validate the fully-defaulted value.
func ValidateSilentMode(sm SilentModeConfig) error {
	checks := []struct {
		name    string
		mode    string
		allowed []string
	}{
		{"tool_confirm", sm.ToolConfirm.Mode, []string{SilentToolConfirmJudge, SilentToolConfirmAllow, SilentToolConfirmDeny}},
		{"step_limit", sm.StepLimit.Mode, []string{SilentStepLimitAuto, SilentStepLimitAllowOnce, SilentStepLimitAllowMore, SilentStepLimitAllowAlways, SilentStepLimitDeny, SilentStepLimitStop}},
		{"ask_user", sm.AskUser.Mode, []string{SilentAskUserDisable, SilentAskUserEnable}},
		{"review_prompt", sm.ReviewPrompt.Mode, []string{SilentReviewPromptSuppress, SilentReviewPromptAllow}},
	}
	for _, c := range checks {
		if c.mode == "" {
			continue // unset → default seeded by ApplyDefaults
		}
		valid := false
		for _, a := range c.allowed {
			if c.mode == a {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf(
				"security.silent_mode.%s.mode has invalid value %q; must be one of: %s",
				c.name, c.mode, strings.Join(c.allowed, ", "),
			)
		}
	}
	return nil
}

// SilentModeDefaults returns the canonical default silent-mode configuration:
// each sub-policy at its documented default (inert until the autonomy mode
// is "silent"). It is the single source of truth for the sub-policy
// defaults, used by ApplyDefaults and by the runtime security-settings
// update.
func SilentModeDefaults() SilentModeConfig {
	return SilentModeConfig{
		ToolConfirm:  SilentSubPolicyConfig{Mode: SilentToolConfirmJudge},
		StepLimit:    SilentSubPolicyConfig{Mode: SilentStepLimitAuto},
		AskUser:      SilentSubPolicyConfig{Mode: SilentAskUserDisable},
		ReviewPrompt: SilentSubPolicyConfig{Mode: SilentReviewPromptSuppress},
	}
}

// ApplySilentModeDefaults fills each unset (empty) sub-policy mode with its
// default. An explicit mode is preserved.
func ApplySilentModeDefaults(sm *SilentModeConfig) {
	d := SilentModeDefaults()
	if sm.ToolConfirm.Mode == "" {
		sm.ToolConfirm.Mode = d.ToolConfirm.Mode
	}
	if sm.StepLimit.Mode == "" {
		sm.StepLimit.Mode = d.StepLimit.Mode
	}
	if sm.AskUser.Mode == "" {
		sm.AskUser.Mode = d.AskUser.Mode
	}
	if sm.ReviewPrompt.Mode == "" {
		sm.ReviewPrompt.Mode = d.ReviewPrompt.Mode
	}
}

// AskUserDisabled reports whether the autonomy mode is "silent" AND the
// ask_user sub-policy disables the tool — the predicate that suppresses
// ask_user registration. The core mirror of this predicate lives on
// core.BuilderSecurityConfig (pinned against this one by
// backend/configadapter_test.go).
func (s SecurityConfig) AskUserDisabled() bool {
	return s.AutonomyMode == AutonomyModeSilent && s.SilentMode.AskUser.Mode == SilentAskUserDisable
}

// migrateLegacyAutonomyMode migrates the pre-enum autonomy booleans
// (security.smart_approve and security.silent_mode.enabled, mirrored by
// LegacySmartApprove / SilentModeConfig.LegacyEnabled) onto the unified
// SecurityConfig.AutonomyMode enum — the same load-time pattern as
// migrateLegacyGroupBlacklists (blacklist_migration.go). Resolution order:
//
//   - an explicit security.autonomy_mode decides. A value outside the enum
//     fails SAFE to "standard" with a warning (never a hard load error — a
//     value written by a newer app version must not brick startup);
//   - otherwise the legacy keys are honored: silent_mode.enabled=true maps
//     to "silent" (priority — an explicit unattended posture wins), else
//     smart_approve=true maps to "assisted", else "standard";
//
// The legacy mirrors are cleared unconditionally so both stale keys
// disappear from the file at the next Save. Every observed legacy key (and
// every ignored-legacy or unknown-enum case) is reported as a warning
// string for the load-warnings channel the UI displays.
func migrateLegacyAutonomyMode(sec *SecurityConfig) []string {
	legacySmart := sec.LegacySmartApprove
	legacySilent := sec.SilentMode.LegacyEnabled
	sec.LegacySmartApprove = nil
	sec.SilentMode.LegacyEnabled = nil

	if sec.AutonomyMode != "" {
		switch sec.AutonomyMode {
		case AutonomyModeStandard, AutonomyModeAssisted, AutonomyModeSilent:
		default:
			warning := fmt.Sprintf(
				"security.autonomy_mode has unknown value %q; falling back to %q (must be one of: %s, %s, %s)",
				sec.AutonomyMode, AutonomyModeStandard, AutonomyModeStandard, AutonomyModeAssisted, AutonomyModeSilent,
			)
			sec.AutonomyMode = AutonomyModeStandard
			if legacySmart != nil || legacySilent != nil {
				warning += "; legacy security.smart_approve / security.silent_mode.enabled keys were ignored and are dropped at the next save"
			}
			return []string{warning}
		}
		if legacySmart != nil || legacySilent != nil {
			return []string{
				"security.autonomy_mode is set: legacy security.smart_approve / security.silent_mode.enabled keys were ignored and are dropped at the next save",
			}
		}
		return nil
	}

	mode := AutonomyModeStandard
	switch {
	case legacySilent != nil && *legacySilent:
		mode = AutonomyModeSilent // explicit unattended posture wins
	case legacySmart != nil && *legacySmart:
		mode = AutonomyModeAssisted
	}
	sec.AutonomyMode = mode
	if legacySmart != nil || legacySilent != nil {
		return []string{
			fmt.Sprintf(
				"legacy security.smart_approve / security.silent_mode.enabled keys are deprecated and were migrated to security.autonomy_mode: %s; they are dropped at the next save",
				mode,
			),
		}
	}
	return nil
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
	WebFetchProxyTimeout     int `yaml:"webFetchProxyTimeout"`     // seconds, default: 30 — per-attempt web fetch timeout used when the proxy is enabled
	WebFetchRetries          int `yaml:"webFetchRetries"`          // retry count (not seconds) for failed web fetches; each retry doubles the active timeout (webFetchTimeout, or webFetchProxyTimeout when the proxy is on), default: 2
	WebSearchTimeout         int `yaml:"webSearchTimeout"`         // seconds, default: 30
	PersistenceTimeout       int `yaml:"persistenceTimeout"`       // seconds, default: 5
	LLMRequestTimeout        int `yaml:"llmRequestTimeout"`        // seconds, default: 600 (10 min) — main chat loop
	ServiceLLMRequestTimeout int `yaml:"serviceLLMRequestTimeout"` // seconds, default: 120 (2 min) — one-shot service LLM requests (session title, commit message, prompt optimization)
	GitCommitTimeout         int `yaml:"gitCommitTimeout"`         // seconds, default: 300 (5 min) — git commit spawn (rev-parse and other quick git probes keep the fast 30s timeout)
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

	// MaxParallelSubagents caps how many subagents execute concurrently,
	// enforced through the launcher's shared limiter that every dispatch path
	// acquires from — the delegate tool (blocking batches and async launches
	// alike) and plan waves. <= 0 resolves to the default (4) in
	// ApplyDefaults. Bounding concurrency bounds the peak rate of subagent
	// lifecycle events (and the burst of concurrent LLM calls / tool
	// executions) without changing results or their order.
	MaxParallelSubagents int `yaml:"max_parallel_subagents"`
}

// envVarPattern matches ${ENV_VAR} patterns for substitution.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ExperimentalConfig gates features that are still under active development
// behind a single master switch. It is all-or-nothing by design: there is no
// per-feature toggle, so enabling it exposes every gated feature and
// disabling it treats each as off. Currently gated: the E2S execution mode
// (e2s.*). Model Profiles (model_profiles.*) is NOT gated — it carries its own
// manual master toggle.
type ExperimentalConfig struct {
	// Enabled is the master switch for the gated experimental features (the
	// E2S execution mode). When false, every gated feature is treated as off
	// regardless of its own toggles. Default: false.
	Enabled bool `yaml:"enabled"`
}

// ModelProfilesPersistConfig is the persisted `model_profiles:` section of config.yaml. It carries
// exactly the two durable operator choices; the 25 variant knobs live in
// profiles (the predefined catalog and ~/.c0wrk/model-profiles.yaml), so this
// struct deliberately has no knob fields. Legacy inline `small_llm.*` keys in
// an existing config.yaml are ignored by the non-strict YAML decoding and
// disappear on the next save (sanctioned reset migration).
type ModelProfilesPersistConfig struct {
	// Enabled is the master toggle for the model-profile profile. When false,
	// every variant sub-toggle is ignored. There is no auto-detection — this
	// must be set explicitly. Default: false.
	Enabled bool `yaml:"enabled"`

	// ActiveProfile is the id of the profile whose 25 knob values form the
	// effective runtime configuration (see ResolveModelProfilesConfig). ApplyDefaults
	// seeds it with the model-agnostic "generic" profile; an id that no longer
	// resolves (e.g. a custom profile deleted by hand) falls back to "generic"
	// with a warning instead of failing the run.
	ActiveProfile string `yaml:"active_profile"`
}

// FindModelProfile returns the profile with the given id from the catalog, by
// value (the catalog slice is never exposed for mutation).
func FindModelProfile(profiles []ModelProfile, id string) (ModelProfile, bool) {
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return ModelProfile{}, false
}

// ResolveModelProfilesConfig builds the effective runtime ModelProfilesConfig for a persisted
// `model_profiles:` section against a profile catalog (predefined ∪ custom — see
// LoadModelProfilesCatalog). Resolution rules:
//
//   - a known profile id → that profile's values;
//   - an empty or unknown id → soft fallback to the model-agnostic "generic"
//     profile plus one warning each — a stale id must never break the run.
//
// The master Enabled flag is carried over from the persist section verbatim —
// the master toggle is the only switch (Model Profiles is not gated by the
// experimental-features switch). The returned struct owns its slices, so
// callers cannot mutate the catalog through it.
func ResolveModelProfilesConfig(persist ModelProfilesPersistConfig, catalog []ModelProfile) (resolved ModelProfilesConfig, warnings []string) {
	var profile ModelProfile
	if id := persist.ActiveProfile; id != "" {
		if found, ok := FindModelProfile(catalog, id); ok {
			profile = found
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"model_profiles.active_profile %q not found in the profile catalog; falling back to the %q profile",
				id, ModelProfilesGenericProfileID))
			profile, _ = FindPredefinedModelProfile(ModelProfilesGenericProfileID)
		}
	} else {
		warnings = append(warnings, "model_profiles.active_profile is empty; falling back to the \""+ModelProfilesGenericProfileID+"\" profile")
		profile, _ = FindPredefinedModelProfile(ModelProfilesGenericProfileID)
	}
	values := cloneModelProfileConfig(profile.Config)
	resolved = ModelProfilesConfig{
		Enabled:        persist.Enabled,
		EssentialTools: values.EssentialTools,
		SystemPrompt:   values.SystemPrompt,
		Sampling:       values.Sampling,
		LoopHardening:  values.LoopHardening,
		Context:        values.Context,
	}
	return resolved, warnings
}

// LoadModelProfilesCatalog assembles the full profile catalog used for resolution: the
// compiled-in predefined entries plus the operator's custom profiles from
// <agentDir>/model-profiles.yaml. Store-level problems (unreadable/broken file,
// discarded records) are returned as warnings rather than errors — a damaged
// custom store must never take the predefined catalog down with it.
func LoadModelProfilesCatalog(agentDir string) (catalog []ModelProfile, warnings []string) {
	custom, storeWarnings := LoadCustomModelProfiles(ModelProfilesPath(agentDir))
	catalog = PredefinedModelProfiles()
	return append(catalog, custom...), storeWarnings
}

// ModelProfilesConfig is the RESOLVED runtime view of the model-profile profile: the master
// toggle plus the 25 variant knobs of the active profile (see ResolveModelProfilesConfig).
// It is not persisted to config.yaml anymore — only model_profiles.enabled and
// model_profiles.active_profile are (see ModelProfilesPersistConfig). Each variant carries its own
// sub-toggle so individual optimizations can be turned off independently, and
// every threshold/value is exposed so behaviour can be tuned without a rebuild.
type ModelProfilesConfig struct {
	// Enabled is the master toggle for the model-profile profile. When false, every
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
	Sampling ModelProfilesSamplingConfig `yaml:"sampling"`

	// LoopHardening tightens the executor circuit-breaker thresholds so a small
	// model that repeats itself or fails to make progress is nudged/aborted
	// sooner, conserving the token budget.
	LoopHardening LoopHardeningConfig `yaml:"loop_hardening"`

	// Context applies aggressive context management: tighter compaction, stricter
	// tool-output pruning, and a larger output token reserve.
	Context ModelProfilesContextConfig `yaml:"context"`
}

// EssentialToolsConfig narrows the tool set visible to a smaller model to reduce
// per-prompt schema overhead.
type EssentialToolsConfig struct {
	// Enabled gates this variant. When false the full tool set is exposed
	// regardless of the master ModelProfiles.Enabled toggle.
	Enabled bool `yaml:"enabled"`

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. Tools not in this list are hidden from the model.
	// Protected orchestration tools and all MCP tools are always preserved
	// regardless, and the selection is never trimmed.
	AlwaysPresent []string `yaml:"always_present"`

	// CompactDescriptions replaces every known builtin's full rubric
	// description with a one-line compact variant while this variant is
	// active, shrinking prompt overhead on small models. Off by default:
	// with it off, descriptions are byte-identical to the non-ModelProfiles
	// behavior.
	CompactDescriptions bool `yaml:"compact_descriptions"`
}

// SystemPromptConfig applies prompt-simplification variants to shrink the
// system prompt injected for a smaller model. Each flag is independent; FewShot
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

// ModelProfilesSamplingConfig overrides LLM sampling parameters for a small model.
// Every parameter uses zero as the "not set" sentinel: an unset parameter
// inherits the per-family vendor preset (prompt.DefaultSampling) instead of
// clobbering it, so enabling the sampling variant with no explicit values is
// a behavioral no-op. Out-of-range values are rejected by validation
// (config/modelProfiles_profiles.go, ValidateModelProfileConfig) whenever they are set.
type ModelProfilesSamplingConfig struct {
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

	// ReasoningEffort controls reasoning depth: "" (unset → inherit the
	// model default; the shipped "generic" profile pins "medium") | "off" |
	// "low" | "medium". Smaller models generally benefit from reduced
	// reasoning effort; an explicit value is never overwritten.
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

// ModelProfilesContextConfig is the fifth model-profile profile variant: aggressive
// context management. When active it tightens the executor's compaction knobs
// (smaller sliding window, smaller summarization block, earlier trigger),
// prunes tool outputs more aggressively, and reserves more output tokens so a
// small model is less likely to exhaust the context window mid-task. The
// general executor defaults are NOT changed — the overrides only apply while
// both the master toggle (ModelProfiles.Enabled) and this variant's toggle are
// enabled.
type ModelProfilesContextConfig struct {
	// Enabled gates this variant (in addition to the master ModelProfiles.Enabled).
	Enabled bool `yaml:"enabled"`

	// Compaction overrides the executor compaction knobs.
	Compaction ModelProfilesCompactionConfig `yaml:"compaction"`

	// ToolOutputKeepLastN overrides the executor's tool-output pruning depth
	// (stricter than the general executor default).
	ToolOutputKeepLastN int `yaml:"tool_output_keep_last_n"`

	// OutputTokenReserve overrides the token budget reserved for the model's
	// output (e.g. 8192).
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// ModelProfilesCompactionConfig holds the compaction-tightening overrides. Zero
// values mean "do not override" — the corresponding executor baseline is kept.
type ModelProfilesCompactionConfig struct {
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

// E2SConfig configures the E2S (explicit-state) execution mode. The mode is
// experimental and fail-closed gated by experimental.enabled alone: when the
// gate is off the whole section is ineffective. Every knob is seeded with a
// default so tuning never requires a rebuild.
type E2SConfig struct {
	// MaxSteps caps the number of E2S turns (patch+action cycles) per run.
	// Exhaustion is NOT a failure: the run stops at a resumable step-limit
	// checkpoint (Σ preserved; Resume continues with a fresh budget).
	// Default: 50.
	MaxSteps int `yaml:"max_steps"`

	// StateByteLimit caps the JSON-encoded size of the working state Σ, in
	// bytes. A patch whose merged Σ exceeds the limit is rejected as a
	// validation error (bounded retry, then the error becomes the next
	// observation and the run CONTINUES — Σ unchanged); the initial Σ
	// itself is checked up front, so an oversized objective fails fast
	// instead of wedging the run. Default: 16384 (mirrors
	// core/e2s.DefaultStateByteLimit).
	StateByteLimit int `yaml:"state_byte_limit"`

	// PatchRetries is how many times a rejected state patch (validation
	// error or over-limit Σ) may be re-requested from the model before the
	// failure becomes an error observation (the run continues; Σ
	// unchanged). Default: 1.
	PatchRetries int `yaml:"patch_retries"`

	// ObservationTruncate caps the tool observation fed back to the model per
	// turn, in characters; longer observations are truncated. Default: 2000.
	ObservationTruncate int `yaml:"observation_truncate"`

	// RepeatNudgeThreshold is the number of consecutive identical step
	// actions (same tool + target anchor — the semantic fingerprint of
	// ADR-040 §6: precision args like line ranges are ignored, while the
	// content payloads of write_file/edit_file are part of the identity)
	// before a corrective nudge observation is injected instead of
	// dispatching the redundant action again. Default: 3.
	RepeatNudgeThreshold int `yaml:"repeat_nudge_threshold"`

	// RepeatAbortThreshold is the number of consecutive identical step
	// actions before the run is aborted as a spin. Must be >= the nudge
	// threshold so the model always gets at least one nudge first.
	// Default: 5.
	RepeatAbortThreshold int `yaml:"repeat_abort_threshold"`
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
	// TLSFingerprint carries the per-provider SPKI pin (only meaningful for
	// compatible providers, which are the only ones with a BaseURL):
	// non-empty = ONLY the pinned key is accepted; empty = system CA
	// verification. See ADR-054.
	TLSFingerprint string
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
	tlsFingerprint     string
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
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, tlsFingerprint: cfg.TLSFingerprint, outputTokenReserve: cfg.OutputTokenReserve})
	}
	for _, name := range anthropicKeys {
		cfg := c.AnthropicCompatible[name]
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, tlsFingerprint: cfg.TLSFingerprint, outputTokenReserve: cfg.OutputTokenReserve})
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
			TLSFingerprint:     p.tlsFingerprint,
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
						Name:           p.name,
						ProviderType:   c.providerType(p.name),
						APIKey:         p.apiKey,
						BaseURL:        p.baseURL,
						Models:         p.models,
						TLSFingerprint: p.tlsFingerprint,
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
					Name:           p.name,
					ProviderType:   c.providerType(p.name),
					APIKey:         p.apiKey,
					BaseURL:        p.baseURL,
					Models:         p.models,
					TLSFingerprint: p.tlsFingerprint,
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

	// One-time migration from the pre-rename `blacklist` key to `blocklist`
	// (see blacklist_migration.go): a customized legacy list is carried over,
	// a default-equal or absent one is dropped. Must run before ApplyDefaults
	// and validate so the migrated value flows through both untouched.
	migrateLegacyGroupBlacklists(cfg.Security.Groups)

	// One-time migration from the pre-enum autonomy booleans
	// (smart_approve / silent_mode.enabled) onto security.autonomy_mode.
	// Must run BEFORE ApplyDefaults: only an empty AutonomyMode at this
	// point means "the config predates the enum", so the legacy keys are
	// honored; afterwards ApplyDefaults seeds the standard default. Each
	// observed legacy key (and any unknown enum value, which fails safe to
	// standard) is surfaced as a load warning for the UI.
	autonomyWarnings := migrateLegacyAutonomyMode(&cfg.Security)

	// Apply defaults for zero-value fields
	ApplyDefaults(&cfg)

	// Validate the shell_exec override section in place (fail-soft: invalid
	// entries are warned about and reset to the built-in launch shape). Must
	// run after ApplyDefaults (which seeds the platform-default shell kind
	// for an active override) and before validate, so the persisted shape is
	// always valid.
	shellExecWarnings := normalizeShellExec(&cfg)

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return &LoadResult{
			Config:     &cfg,
			LoadErrors: append(append(autonomyWarnings, shellExecWarnings...), "Config validation failed: "+err.Error()),
		}, fmt.Errorf("config validation failed: %w", err)
	}

	return &LoadResult{Config: &cfg, LoadErrors: append(autonomyWarnings, shellExecWarnings...)}, nil
}

// Save writes the configuration to a YAML file atomically.
func Save(cfg *Config, path string) error {
	// Marshal the config as-is: the execute blocklist is empty by default
	// and purely user-authored, so there is no derived view to maintain —
	// what is stored is what is written.
	data, err := yaml.Marshal(cfg)
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
	// config, policies must use the group enum, and a blocklist is an
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
		if name != ToolGroupExecute && len(group.Blocklist) > 0 {
			return fmt.Errorf(
				"security group %q does not support a blocklist; only %q does",
				name, ToolGroupExecute,
			)
		}
		for _, pattern := range group.Blocklist {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf(
					"security group %q blocklist pattern %q does not compile: %w",
					name, pattern, err,
				)
			}
		}
	}

	// Validate security.silent_mode sub-policy enums. ApplyDefaults has
	// already seeded the defaults, so an unset mode is valid here; an
	// explicit value must be drawn from that sub-policy's enum.
	if err := ValidateSilentMode(cfg.Security.SilentMode); err != nil {
		return err
	}

	// Validate vector_index.content_filter thresholds: explicit values must
	// be non-negative and the whitespace ratio within [0, 1]. A mis-typed
	// threshold (negative bytes, ratio > 1) would silently disable a
	// heuristic in the detector, so it fails fast at load time instead.
	cf := cfg.VectorIndex.ContentFilter
	for name, v := range map[string]int{
		"generated_header_bytes":       cf.GeneratedHeaderBytes,
		"minified_min_bytes":           cf.MinifiedMinBytes,
		"minified_max_line_bytes":      cf.MinifiedMaxLineBytes,
		"pathological_min_bytes":       cf.PathologicalMinBytes,
		"pathological_max_token_bytes": cf.PathologicalMaxTokenBytes,
	} {
		if v < 0 {
			return fmt.Errorf("vector_index.content_filter.%s must be >= 0, got %d", name, v)
		}
	}
	if cf.MinifiedMaxWhitespaceRatio < 0 || cf.MinifiedMaxWhitespaceRatio > 1 {
		return fmt.Errorf("vector_index.content_filter.minified_max_whitespace_ratio must be within [0, 1], got %v",
			cf.MinifiedMaxWhitespaceRatio)
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

	// Validate the E2S section: explicit numeric values must be non-negative
	// (the seeded defaults are positive, so only a hand-written YAML can go
	// below zero), and the anti-spin thresholds must keep their ordering —
	// the model always gets at least one corrective nudge before the loop
	// aborts. Fail fast at load rather than misbehaving mid-run.
	for name, v := range map[string]int{
		"max_steps":              cfg.E2S.MaxSteps,
		"state_byte_limit":       cfg.E2S.StateByteLimit,
		"patch_retries":          cfg.E2S.PatchRetries,
		"observation_truncate":   cfg.E2S.ObservationTruncate,
		"repeat_nudge_threshold": cfg.E2S.RepeatNudgeThreshold,
		"repeat_abort_threshold": cfg.E2S.RepeatAbortThreshold,
	} {
		if v < 0 {
			return fmt.Errorf("e2s.%s must be >= 0, got %d", name, v)
		}
	}
	if cfg.E2S.RepeatNudgeThreshold > cfg.E2S.RepeatAbortThreshold {
		return fmt.Errorf(
			"e2s.repeat_nudge_threshold (%d) must be <= e2s.repeat_abort_threshold (%d) so a nudge always precedes the abort",
			cfg.E2S.RepeatNudgeThreshold, cfg.E2S.RepeatAbortThreshold,
		)
	}

	// Validate vector_index.execution_provider enum. ApplyDefaults has
	// already normalized the empty string to "auto", so anything else
	// here is a user-authored value.
	switch cfg.VectorIndex.ExecutionProvider {
	case VectorIndexProviderAuto, VectorIndexProviderCPU, VectorIndexProviderCUDA:
		// valid
	default:
		return fmt.Errorf(
			"vector_index.execution_provider %q is not valid; must be one of: %s, %s, %s",
			cfg.VectorIndex.ExecutionProvider,
			VectorIndexProviderAuto, VectorIndexProviderCPU, VectorIndexProviderCUDA,
		)
	}

	// Validate vector_index.device_id. 0 (the first GPU) is the default;
	// negative indexes have no meaning.
	if cfg.VectorIndex.DeviceID < 0 {
		return fmt.Errorf(
			"vector_index.device_id %d is not valid; must be >= 0",
			cfg.VectorIndex.DeviceID,
		)
	}

	// Validate vector_index.park_budget_mb. ApplyDefaults has already
	// resolved an unset key to the 1024 MiB default, so an explicit 0 here
	// is a hand-authored value. Zero is ambiguous — read literally, a zero
	// budget would evict every non-empty parked state the moment it parks —
	// so it is rejected: use a positive budget, or -1 to disable the byte
	// budget (park_capacity alone). Only -1 is a legal negative, mirroring
	// runtime.memory_soft_limit_mb; anything below it is a typo, not a
	// sentinel, and fails fast rather than silently disabling the budget.
	if budget := cfg.VectorIndex.ParkBudgetMb; budget != nil {
		switch {
		case *budget == 0:
			return errors.New(
				"vector_index.park_budget_mb must be > 0, or -1 to disable the byte budget; 0 is ambiguous",
			)
		case *budget < -1:
			return fmt.Errorf(
				"vector_index.park_budget_mb must be -1 (disabled) or a positive MiB value, got %d",
				*budget,
			)
		case *budget > maxMemoryLimitMiB:
			return fmt.Errorf(
				"vector_index.park_budget_mb %d exceeds the maximum of %d MiB (2 PiB); the MiB→bytes shift would overflow",
				*budget, maxMemoryLimitMiB,
			)
		}
	}

	// Validate runtime.memory_soft_limit_mb. The field is a tri-state int:
	// 0 (or unset) = auto, >0 = explicit MiB, -1 = off. Only -1 is a legal
	// negative — anything below it is a typo (e.g. -10), not a sentinel, and
	// fails fast at load rather than silently selecting auto mode. The upper
	// bound guards the int64 MiB→bytes shift in the consumer
	// (desktop/memlimit.go), which overflows to a negative — and a negative
	// SetMemoryLimit is ignored by the runtime, silently dropping the limit.
	if cfg.Runtime.MemorySoftLimitMB < -1 {
		return fmt.Errorf(
			"runtime.memory_soft_limit_mb must be -1 (off), 0/unset (auto), or a positive MiB value, got %d",
			cfg.Runtime.MemorySoftLimitMB,
		)
	}
	if cfg.Runtime.MemorySoftLimitMB > maxMemoryLimitMiB {
		return fmt.Errorf(
			"runtime.memory_soft_limit_mb %d exceeds the maximum of %d MiB (2 PiB); the MiB→bytes shift would overflow",
			cfg.Runtime.MemorySoftLimitMB, maxMemoryLimitMiB,
		)
	}

	return nil
}

// Notification banner-timeout sentinels, shared by the config layer, the
// FrontendAPI validation and the desktop transport so the three never drift.
const (
	// NotificationBannerTimeoutDaemonDefault leaves the lifetime to the
	// notification daemon (freedesktop expire_timeout = -1).
	NotificationBannerTimeoutDaemonDefault = -1

	// NotificationBannerTimeoutNever keeps the banner on screen until the
	// user clicks or dismisses it (freedesktop expire_timeout = 0).
	NotificationBannerTimeoutNever = 0

	// NotificationBannerTimeoutMaxSeconds caps an explicit lifetime at one
	// day — past that, "never expire" is the honest choice.
	NotificationBannerTimeoutMaxSeconds = 86400
)
