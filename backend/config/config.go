// Package config provides configuration loading and validation for the agent.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/llm"

	"gopkg.in/yaml.v3"

	"github.com/v0lka/sp4rk/safeio"
)

// DefaultAgentDir is the default directory for agent files (data, tools, config).
const DefaultAgentDir = ".c0wrk"

// Config is the top-level configuration structure.
type Config struct {
	LogLevel string    `yaml:"log_level"`
	LLM      LLMConfig `yaml:"llm"`

	// EmbeddedLLM is the optional in-app local model (Bonsai 2 27B): mostly
	// app-written install state; auto_unload/tuning are the operator settings
	// both flows preserve, and llm.openai_compatible.embedded is GENERATED from
	// it (SyncEmbeddedLLMProvider). See specs/domains/embedded-llm.md, ADR-066.
	EmbeddedLLM EmbeddedLLMConfig `yaml:"embedded_llm"`

	MCP MCPConfig `yaml:"mcp"`

	// Capabilities carries per-name, per-context (chat vs code) activation
	// modes for skills, subagent profiles and MCP servers; additive — an
	// omitted name/section behaves as "auto" in both contexts. Shared enum,
	// load normalization and legacy-mode migration live in mcp_mode.go; see
	// specs/domains/capabilities.md.
	Capabilities CapabilitiesConfig `yaml:"capabilities,omitempty"`

	Router     RouterConfig     `yaml:"router"`
	Executor   ExecutorConfig   `yaml:"executor"`
	Security   SecurityConfig   `yaml:"security"`
	Skills     SkillsConfig     `yaml:"skills"`
	Agents     AgentsConfig     `yaml:"agents"`
	Commands   CommandsConfig   `yaml:"commands"`
	Search     SearchConfig     `yaml:"search"`
	ToolLimits ToolLimitsConfig `yaml:"toolLimits"`
	Timeouts   TimeoutsConfig   `yaml:"timeouts"`
	// Shutdown bounds the application teardown so that a stuck goroutine can
	// never leave the process alive on quit. See ShutdownConfig.
	Shutdown ShutdownConfig `yaml:"shutdown"`
	// ShellExec overrides the shell-execution tool's launch shape and declares
	// the shell the command text is written in. Zero value = built-in launch
	// shape; load problems fail soft (warning + default). See shell_exec.go,
	// ADR-058.
	ShellExec     ShellExecConfig     `yaml:"shell_exec"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
	GoalLoop      GoalLoopConfig      `yaml:"goal_loop"`
	// PromptSuggestions gates the follow-up prompts the chat input offers
	// after a task finishes (full or partial). See
	// specs/domains/prompt-suggestions.md.
	PromptSuggestions PromptSuggestionsConfig `yaml:"prompt_suggestions"`
	VectorIndex       VectorIndexConfig       `yaml:"vector_index"`
	Proxy             ProxyConfig             `yaml:"proxy"`
	Terminal          TerminalConfig          `yaml:"terminal"`
	Git               GitConfig               `yaml:"git"`
	Runtime           RuntimeConfig           `yaml:"runtime"`
	Notifications     NotificationsConfig     `yaml:"notifications"`

	// ModelProfiles persists only the two durable operator choices — the
	// manual-only master toggle and the active profile id; the 25 variant
	// knobs are resolved at runtime from the profile catalog
	// (ResolveModelProfilesConfig). A legacy inline small_llm: section is
	// ignored at load and dropped by the next save. See
	// specs/domains/model-profiles.md.
	ModelProfiles ModelProfilesPersistConfig `yaml:"model_profiles"`

	// E2S configures the E2S (explicit-state Σ) execution mode, gated by
	// experimental.enabled. See specs/domains/e2s.md.
	E2S E2SConfig `yaml:"e2s"`

	// Experimental is the all-or-nothing gate for the E2S mode, the embedded
	// model's frontend surfaces and the web-access sidecar (the last a
	// compound gate with web_access.enabled). Model Profiles is NOT gated by
	// it. See ADR-068, specs/domains/web-access.md. Default: off.
	Experimental ExperimentalConfig `yaml:"experimental"`

	// Updates configures the background update checks; their state persists
	// to update_state.json, not here. See ADR-023.
	Updates UpdatesConfig `yaml:"updates"`

	// WebAccess configures the browser web-access sidecar (ADR-101): an
	// HTTP/WS server mirroring the desktop UI, default off. Load-time
	// problems fail soft (see web_access.go); Save refuses to persist an
	// invalid enabled state. See specs/domains/web-access.md.
	WebAccess WebAccessConfig `yaml:"web_access"`
}

// UpdatesConfig controls the self-update subsystem; both toggles are
// operator-level settings in config.yaml.
type UpdatesConfig struct {
	// Enabled is the update subsystem master switch. Pointer-bool: unset
	// defaults to true (the ProxyConfig.SetGlobalEnv convention); false
	// disables every update affordance.
	Enabled *bool `yaml:"enabled"`

	// AutoCheck controls the automatic startup/background check. Pointer-bool
	// defaulting to true; false suppresses only the automatic check — manual
	// checks still work.
	AutoCheck *bool `yaml:"auto_check"`

	// CheckInterval is the minimum time between automatic checks (duration
	// string, default "6h"; unparseable → default).
	CheckInterval string `yaml:"check_interval"`
}

// ProxyConfig holds HTTP/HTTPS proxy settings for all outbound connections.
type ProxyConfig struct {
	Enabled    bool     `yaml:"enabled"`
	URL        string   `yaml:"url"`          // scheme://user:password@host:port
	BypassList []string `yaml:"bypass_list"`  // hostnames/IPs to skip proxy
	TLSCertDir string   `yaml:"tls_cert_dir"` // directory with .pem/.crt CA certs

	// SetGlobalEnv, when true, exports HTTP_PROXY/HTTPS_PROXY/NO_PROXY/SSL_CERT_DIR
	// into the process environment for subprocesses (bash_exec children, MCP
	// stdio servers). Default: true when proxy is enabled. Pointer-bool so
	// "unset" is distinguishable from "explicitly false"; false keeps proxy
	// state out of third-party libraries that read env at init time.
	SetGlobalEnv *bool `yaml:"set_global_env"`
}

// TerminalConfig configures the embedded PTY terminal (xterm.js panel).
type TerminalConfig struct {
	// Env holds extra environment variables set on every terminal shell
	// process, on top of the inherited environment and the built-in terminal
	// defaults (TERM, COLORTERM, TERM_PROGRAM=c0wrk); values win, and ${VAR}
	// references expand at startup. Typical use: marker variables rc files
	// check to skip behaviors that assume a standalone terminal — most
	// commonly tmux auto-attach (this is NOT the user's own tmux):
	//
	//	[[ -z "$TMUX" && "$TERM_PROGRAM" != "c0wrk" ]] && tmux attach
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

// NotificationsConfig configures the OS-level notification banners of the
// desktop system-notification channel
// (specs/domains/frontend/system-notifications.md).
type NotificationsConfig struct {
	// BannerTimeoutSeconds is how long a delivered banner stays on screen:
	// -1 daemon default, 0 never expire, >0 explicit seconds. Pointer so an
	// explicit 0 is distinguishable from unset (default -1; same convention
	// as GitConfig.AutoFetch). Linux only — it becomes the freedesktop
	// expire_timeout; macOS/Windows own banner lifetime themselves.
	BannerTimeoutSeconds *int `yaml:"banner_timeout_seconds"`
}

type GitConfig struct {
	// WorktreesRoot is a config-only base directory for NEW managed trees.
	// Empty selects <agentDir>/projects; each project uses <base>/<id>/worktrees.
	WorktreesRoot string `yaml:"worktrees_root,omitempty"`
	// AutoFetch is the master gate for ALL automatic fetch triggers (startup,
	// project switch, window focus, ticker). Pointer-bool: unset defaults to
	// true (the UpdatesConfig convention); manual UI fetches are never gated.
	// See specs/domains/git-auto-fetch.md.
	AutoFetch *bool `yaml:"auto_fetch"`

	// AutoFetchInterval is the periodic-ticker period (duration string,
	// default "2m"); "0" disables ONLY the ticker. Parsed by the consumer —
	// the config layer stores the raw value; unparseable → default.
	// See specs/domains/git-auto-fetch.md.
	AutoFetchInterval string `yaml:"auto_fetch_interval"`
}

// maxMemoryLimitMiB bounds the MiB-valued memory knobs
// (runtime.memory_soft_limit_mb, vector_index.park_budget_mb): past it the
// consumers' MiB→bytes `<< 20` overflows to a negative, which silently drops
// the soft limit / reads as "budget disabled". 1<<31 MiB = 2 PiB — only a
// unit mix-up (bytes entered as MiB) lands there.
const maxMemoryLimitMiB = 1 << 31

// RuntimeConfig holds process-level tunables for the Go runtime itself
// (consumed by desktop/startup.go right after config load, before background
// indexing starts).
type RuntimeConfig struct {
	// MemorySoftLimitMB is the Go runtime soft memory limit
	// (debug.SetMemoryLimit — a GC target, NOT a hard cap). Values:
	// 0/unset = auto (50% of RAM clamped to [2 GiB, 8 GiB],
	// desktop/memlimit.go), >0 = explicit MiB, -1 = off.
	// Precedence: an explicit positive config value wins over the GOMEMLIMIT
	// env var, which itself outranks AUTO mode (the app must not silently
	// override an operator-level env setting).
	MemorySoftLimitMB int `yaml:"memory_soft_limit_mb"`
}

// VectorIndexConfig holds vector / hybrid search runtime settings.
//
// Hybrid is a pointer-bool (unset = true); false means only the chromem index
// is consulted, bleve is opened but not queried. The HybridRRFK /
// HybridFanout* fields tune Reciprocal Rank Fusion and the pointer-float64
// Hybrid*Score* fields suppress noise-tail hits before fusion (absolute
// cosine floor, relative vector and BM25 cutoffs); zero/nil falls back to the
// built-in defaults (60 / 4 / 100).
type VectorIndexConfig struct {
	Hybrid *bool `yaml:"hybrid"`

	HybridRRFK              int      `yaml:"hybrid_rrf_k"`
	HybridFanoutMultiplier  int      `yaml:"hybrid_fanout_multiplier"`
	HybridFanoutMin         int      `yaml:"hybrid_fanout_min"`
	HybridVectorScoreFloor  *float64 `yaml:"hybrid_vector_score_floor"`
	HybridVectorScoreRatio  *float64 `yaml:"hybrid_vector_score_ratio"`
	HybridLexicalScoreRatio *float64 `yaml:"hybrid_lexical_score_ratio"`

	// EmbeddingThreads caps the ONNX intra-op thread pool during indexing
	// (0/unset = all cores, the legacy default); bounds the CPU spike at the
	// cost of throughput.
	EmbeddingThreads int `yaml:"embedding_threads"`

	// MaxFileSize is the upper bound (bytes) for a file to be read fully for
	// chunking/embedding; larger files are skipped at walk/validation/read
	// time. 0/unset defaults to 4 MiB.
	MaxFileSize int64 `yaml:"max_file_size"`

	// MaxChunkSize is the maximum chunk size in characters handed to the
	// chunker (0/unset → 1500); significantly larger risks chunks the
	// embedding model cannot embed in one pass.
	MaxChunkSize int `yaml:"max_chunk_size"`

	// MaxChunksPerFile caps one file's chunks per index pass; an exceeding
	// file is skipped wholesale (WARN). Backstop against pathological inputs
	// (tokenizer.json, minified assets, lockfiles) the splitter fragments
	// into tens of thousands of inference-pass chunks — not a tight per-call
	// limit. 0/unset defaults to 4000.
	MaxChunksPerFile int `yaml:"max_chunks_per_file"`

	// EmbeddingBatchSize is the fixed row capacity of the embedder's batch
	// ONNX session: larger amortizes inference but linearly grows the output
	// tensor (≈B MiB) and single-call latency; 32 is the measured knee.
	// 0/unset defaults to 32.
	EmbeddingBatchSize int `yaml:"embedding_batch_size"`

	// EmbeddingCacheMaxBytes caps the branch-independent content-addressed
	// embedding cache per project's vector-index directory. 0/unset →
	// 512 MiB; negative disables it.
	EmbeddingCacheMaxBytes int64 `yaml:"embedding_cache_max_bytes"`

	// PrepWorkers is the parallel file-prep worker count (read/hash/chunk)
	// overlapping single-threaded ONNX inference; 1 = historical serial
	// behaviour, 0/unset defaults to 2.
	PrepWorkers int `yaml:"prep_workers"`

	// DebounceMs coalesces file-change notification bursts into one
	// incremental index pass. 0/unset defaults to 1000.
	DebounceMs int `yaml:"debounce_ms"`

	// ChunkOverlap is the character overlap between adjacent chunks
	// (0/unset → 200). Changes (and MaxChunkSize) are picked up
	// automatically: the file-hash sidecar records the chunker config, so
	// the next validation pass re-chunks affected files — no manual Reindex.
	ChunkOverlap int `yaml:"chunk_overlap"`

	// ContentFilter configures deterministic early rejection of generated,
	// minified and pathological files before chunking
	// (VectorIndexContentFilterConfig); the resolved policy participates in
	// the chunker fingerprint, so policy changes re-validate automatically.
	ContentFilter VectorIndexContentFilterConfig `yaml:"content_filter"`

	// SearchWaitTimeoutMs bounds, in ms, how long a consumer may WAIT for
	// index readiness — each consumer waits exactly once, in its dedicated
	// wait step (queries are separately bounded by the same value as
	// defense-in-depth). Pointer-int: unset → 3000; explicit 0 = fail fast,
	// never wait.
	SearchWaitTimeoutMs *int `yaml:"search_wait_timeout_ms"`

	// ParkCapacity is how many recently-closed projects keep their vector
	// index state (chromem DB + bleve + sidecar) resident in RAM for instant
	// restore — each parked project holds its full index in memory, so tune
	// for the machine. Pointer-int: unset → 3; explicit 0 disables parking.
	ParkCapacity *int `yaml:"park_capacity"`

	// ParkBudgetMb is the cumulative MiB budget for parked project states:
	// the oldest states are evicted while their estimated footprints exceed
	// it; the effective bound is min(park_capacity, park_budget_mb).
	// Pointer-int64: unset → 1024; -1 = budget disabled (capacity alone);
	// explicit 0 and values below -1 are rejected by validate() (only -1
	// disables), mirroring runtime.memory_soft_limit_mb.
	ParkBudgetMb *int64 `yaml:"park_budget_mb"`

	// ExecutionProvider selects the ONNX Runtime execution provider for the
	// embedding model — see the VectorIndexProvider* constants. The empty
	// string (pre-knob config) is normalized to "auto" by ApplyDefaults;
	// invalid values are rejected at load by validate().
	ExecutionProvider string `yaml:"execution_provider"`

	// DeviceID is the GPU device index for the CUDA provider (default 0;
	// CPU ignores it, OpenVINO accepts only 0 — the plugin picks the Intel
	// device itself). Negative values are rejected by validate().
	DeviceID int `yaml:"device_id"`
}

// VectorIndexContentFilterConfig is the YAML surface of the pre-chunk content
// policy (see vectorindex.ContentFilterConfig): pointer bools distinguish
// "unset" (package default) from explicit false, zero numeric thresholds fall
// back to the package defaults.
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
// (VectorIndexConfig.ExecutionProvider); "auto"/"cuda" require a GPU ONNX
// Runtime build next to the executable, "openvino" the provider plugin.
const (
	VectorIndexProviderAuto     = "auto"     // CUDA → OpenVINO → CPU fallback; the default
	VectorIndexProviderCPU      = "cpu"      // always CPU inference
	VectorIndexProviderCUDA     = "cuda"     // NVIDIA GPU via the CUDA execution provider
	VectorIndexProviderOpenVINO = "openvino" // Intel hardware; only device_id 0 (the plugin picks the device)
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
	// OutputTokenReserve overrides the per-provider output-token budget:
	// subtracted from the context window in overflow validation and capping
	// executor MaxTokens; 0 inherits the global executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// OpenAICompatibleConfig holds OpenAI-compatible provider configuration.
type OpenAICompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
	// TLSFingerprint pins the endpoint SPKI (base64(SHA-256(SPKI DER))):
	// non-empty = ONLY the pinned key is accepted (self-signed/internal
	// PKI), empty = system CA verification — there is no accept-any state.
	// Ignored while an effective HTTP proxy is active. See ADR-054.
	TLSFingerprint string `yaml:"tls_fingerprint,omitempty"`
	// OutputTokenReserve overrides the per-provider output-token budget
	// (overflow validation + executor MaxTokens cap); 0 inherits the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
	// AutoRetrySeconds enables the c0wrk-layer auto-resend timer for this
	// provider (seconds between re-sends; 0 = disabled, the model is asked
	// how to proceed). Compatible providers only; not mapped into
	// ToBuilderConfig — the timer lives in the c0wrk session layer. See
	// ADR-065.
	AutoRetrySeconds int `yaml:"auto_retry_seconds,omitempty"`
	// TimeoutClass is the adaptive request budget's class override
	// (ADR-071 D3): "local" or "remote"; empty = infer (loopback base_url is
	// local). The class selects the envelope bounding the provider's trained
	// budgets; validate() rejects anything but the enum.
	TimeoutClass string `yaml:"timeout_class,omitempty"`
}

// maxAutoRetrySeconds is the inclusive upper bound for per-provider
// auto_retry_seconds (ADR-065); 0 disables the timer. The Settings UI clamps
// to the same range; validate() enforces it for edits that bypass the UI.
const maxAutoRetrySeconds = 3600

// MaxAutoRetrySeconds exposes the inclusive upper bound of per-provider
// auto_retry_seconds, so the Settings RPC validates against the same bound
// Load enforces.
func MaxAutoRetrySeconds() int {
	return maxAutoRetrySeconds
}

// ValidTimeoutClass reports whether s is a legal per-provider timeout_class
// override (ADR-071 D3): unset (""), "local", or "remote". The reserved
// `embedded` class is deliberately not accepted — fixed by identity, not
// selected.
func ValidTimeoutClass(s string) bool {
	switch s {
	case "", string(llmbudget.ClassLocal), string(llmbudget.ClassRemote):
		return true
	default:
		return false
	}
}

// ValidateProviderTimeoutClass enforces the FULL per-provider timeout_class
// rules (ADR-071 D3): the enum is "local"|"remote"|unset, and the backend-owned
// `embedded` provider must carry no override — an override would let a
// hand-edited entry downgrade the local model even though Classify honors it.
// This is the single gate shared by validate() and the UpdateLLMConfig RPC
// trust boundary: Save persists without validate, so both frontiers must go
// through it or an invalid value fails the whole config at the next Load.
func ValidateProviderTimeoutClass(name, class string) error {
	if !ValidTimeoutClass(class) {
		return fmt.Errorf(
			"llm provider %q timeout_class %q is not valid; must be %q, %q, or unset",
			name, class, llmbudget.ClassLocal, llmbudget.ClassRemote,
		)
	}
	if name == EmbeddedLLMProviderName && class != "" {
		return fmt.Errorf(
			"llm provider %q is backend-owned and its timeout class is fixed; timeout_class must be unset",
			name,
		)
	}
	return nil
}

// AnthropicCompatibleConfig holds Anthropic-compatible provider configuration
// (custom endpoints speaking Anthropic's Messages API, e.g. a proxy or gateway).
type AnthropicCompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
	// TLSFingerprint pins the endpoint SPKI (base64(SHA-256(SPKI DER))):
	// non-empty = ONLY the pinned key is accepted, empty = system CA
	// verification — there is no accept-any state. Ignored while an
	// effective HTTP proxy is active. See ADR-054.
	TLSFingerprint string `yaml:"tls_fingerprint,omitempty"`
	// OutputTokenReserve overrides the per-provider output-token budget
	// (overflow validation + executor MaxTokens cap); 0 inherits the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
	// AutoRetrySeconds enables the c0wrk-layer auto-resend timer for this
	// provider (seconds between re-sends; 0 = disabled, the model is asked
	// how to proceed). Compatible providers only; not mapped into
	// ToBuilderConfig — the timer lives in the c0wrk session layer. See
	// ADR-065.
	AutoRetrySeconds int `yaml:"auto_retry_seconds,omitempty"`
	// TimeoutClass is the adaptive request budget's class override
	// (ADR-071 D3): "local" or "remote"; empty = infer (loopback base_url is
	// local). The class selects the envelope bounding the provider's trained
	// budgets; validate() rejects anything but the enum.
	TimeoutClass string `yaml:"timeout_class,omitempty"`
}

// ChatGPT auth-mode values (llm.chatgpt.auth.mode).
const (
	ChatGPTAuthModeAPIKey = "api_key" // default: the static llm.chatgpt.api_key, exactly as always
	ChatGPTAuthModeOAuth  = "oauth"   // subscription auth: browser sign-in (backend/providerauth), tokens kept fresh, pinned Codex endpoint (ADR-074)
)

// ChatGPTAuthConfig selects how the chatgpt provider authenticates.
type ChatGPTAuthConfig struct {
	// Mode is "api_key" (default; every consumer reads empty as api_key, so
	// a config that bypassed ApplyDefaults keeps the historical behavior) or
	// "oauth". validate() rejects any other value — a typo must never leave
	// the operator believing the opposite of what runs. See ADR-074.
	Mode string `yaml:"mode,omitempty"`
}

// ChatGPTConfig holds ChatGPT (OpenAI) provider configuration.
type ChatGPTConfig struct {
	APIKey string   `yaml:"api_key"`
	Models []string `yaml:"models"` // enabled models for this provider
	// Auth selects the authentication mode: "api_key" (default, the static
	// key above) or "oauth" (ADR-074; api_key is never a silent fallback —
	// signed-out requests fail with an actionable sign-in error).
	Auth ChatGPTAuthConfig `yaml:"auth,omitempty"`
	// OutputTokenReserve overrides the per-provider output-token budget
	// (overflow validation + executor MaxTokens cap); 0 inherits the global
	// executor.output_token_reserve.
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// MaxModelContextWindow is the sanity ceiling for a llm.models.<name>
// .context_window override — a TIER-1 value that shadows the lazy probe and is
// never corrected by re-asking the model, so an absurd value silently disables
// compaction (every prompt "fits"). Both writers (a hand-edited config.yaml
// and the embedded LLM's /props readback) bound against this one constant.
const MaxModelContextWindow = 1 << 24

// ModelOverride allows overriding built-in model metadata. omitempty keeps
// "inherit" values out of config.yaml; TokenizerType/Family/Protocol use ""
// as the inherit sentinel and Capabilities a nil pointer, so a deliberate
// override to the default / all-false stays distinguishable from "no
// override". ContextWindow is 0 (inherit) or within 1..MaxModelContextWindow.
type ModelOverride struct {
	ContextWindow int                    `yaml:"context_window,omitempty"`
	OutputLimit   int                    `yaml:"output_limit,omitempty"`
	TokenizerType string                 `yaml:"tokenizer_type,omitempty"`
	Family        string                 `yaml:"family,omitempty"`
	Protocol      string                 `yaml:"protocol,omitempty"`
	Capabilities  *llm.ModelCapabilities `yaml:"capabilities,omitempty"`
	// RequestTimeout is the per-model FIXED request deadline in seconds
	// (ADR-071 D5): positive = armed exactly, never escalated; 0 = "no
	// opinion" (the adaptive budget governs). validate() bounds it to
	// [0, 3600].
	RequestTimeout int `yaml:"request_timeout,omitempty"`
}

// LLMRetryConfig configures retry behavior for LLM API calls.
type LLMRetryConfig struct {
	MaxRetries     int    `yaml:"max_retries"`     // max retry attempts (0 = no retries)
	InitialBackoff string `yaml:"initial_backoff"` // initial backoff duration (e.g. "1s")
	MaxBackoff     string `yaml:"max_backoff"`     // maximum backoff duration (e.g. "30s")
}

// Embedded LLM identity and limits. The provider record is backend-owned:
// `embedded_llm:` is the authoritative state and
// `llm.openai_compatible.embedded` is GENERATED from it
// (SyncEmbeddedProvider), so the UI's whole-map provider replacement can
// never delete the local model. See specs/domains/embedded-llm.md, ADR-066.
const (
	EmbeddedLLMProviderName = "embedded"     // openai_compatible key of the generated provider
	EmbeddedLLMModelName    = "Bonsai 2 27B" // the single exposed model; also the llm.models override key (bare name)
	EmbeddedLLMHost         = "127.0.0.1"    // loopback only by design — never network-reachable

	EmbeddedLLMDefaultAutoUnloadMinutes = 60 // default idle budget before the server is stopped

	// EmbeddedLLMMinPort/MaxPort bound the persisted loopback port; 0 is the
	// "not allocated yet" sentinel, legal only while Installed is false.
	EmbeddedLLMMinPort = 1024
	EmbeddedLLMMaxPort = 65535

	// EmbeddedLLMOutputLimit is the seeded generation ceiling: the registry's
	// 32768 fallback assumes datacenter decode rates (here: 17-36 minutes of
	// worst-case generation); a user-authored output_limit still wins.
	EmbeddedLLMOutputLimit = 8192
)

// EmbeddedLLMConfig is the persisted state of the embedded local model: mostly
// app-written records of what is on THIS machine so a restart can supervise
// the server without re-probing. Engine, AutoUnload and Tuning are the three
// operator-owned settings every rewrite carries through unchanged.
type EmbeddedLLMConfig struct {
	// Engine is the runtime-family setting: "auto" (empty/absent), "mtplx"
	// (MLX on Apple Silicon, macOS 14+), "llama" (the pinned PrismML-Eng
	// llama.cpp fork). Auto resolves per platform at install time; an
	// explicit value is honored where the platform supports it, refused
	// where it does not. Preserved by install and removal.
	Engine string `yaml:"engine,omitempty"`
	// Installed reports the runtime and weights are on disk and
	// SHA256-verified; gates the generated provider entry.
	Installed bool `yaml:"installed"`
	// Packing is the quantization resolved for this machine
	// ("PQ2_0" | "PTQ1_0"). Informational.
	Packing string `yaml:"packing"`
	// Backend is the probed accelerator ("metal", "cuda-12.4", "cuda-12.8",
	// "cuda-13.3", "rocm", "vulkan", "cpu"). Informational, but per ADR-073
	// the pinned "cuda-13.3" is substituted by the cuda-13.3-crash guard
	// only where safe (cuda-12.8 on linux/amd64 with a CUDA 12.x userland
	// verdict, cuda-12.4 on windows/amd64) — recording and user-selectability
	// are different predicates; this field records.
	Backend string `yaml:"backend"`
	// Port is the persisted loopback port; 0 = allocate at install time.
	// The provider base URL is always derived from it.
	Port int `yaml:"port"`
	// ModelFile is the absolute path of the installed GGUF weights.
	ModelFile string `yaml:"model_file"`
	// RuntimeVersion is the pinned runtime release the install came from;
	// a pin change forces a runtime re-download, weights are kept.
	RuntimeVersion string `yaml:"runtime_version"`
	// PythonVersion is the managed CPython of the mtplx provisioning
	// (informational, empty on the llama family).
	PythonVersion string `yaml:"python_version,omitempty"`
	// PackRevision is the pinned HF revision of the mtplx weight pack
	// (informational; the llama weights pin travels via the checksums).
	PackRevision string `yaml:"pack_revision,omitempty"`
	// InstalledAt is the RFC 3339 timestamp of the install.
	InstalledAt string `yaml:"installed_at"`
	// AutoUnload is the idle budget after which the server is stopped.
	AutoUnload AutoUnloadConfig `yaml:"auto_unload"`
	// Tuning is the operator override surface for the memory plan — the one
	// setting in this section; Install and Remove preserve it verbatim
	// (see TuningConfig).
	Tuning TuningConfig `yaml:"tuning,omitempty"`
}

// AutoUnloadConfig is the embedded server's idle-unload budget; pointer
// fields keep an explicit YAML value distinguishable from "unset" (which
// resolves to the documented default).
type AutoUnloadConfig struct {
	Enabled *bool `yaml:"enabled"` // idle-timer master switch; default true
	// Minutes is the idle budget before unload (default 60; within
	// 1..embeddedllm.MaxAutoUnloadMinutes).
	Minutes *int `yaml:"minutes"`
}

// IsEnabled resolves the idle-timer master switch (nil → true).
func (a AutoUnloadConfig) IsEnabled() bool {
	return a.Enabled == nil || *a.Enabled
}

// IdleMinutes resolves the idle budget (nil → default). validate() bounds
// explicit values to 1..embeddedllm.MaxAutoUnloadMinutes, so a loaded config
// always resolves to a budget the minutes→duration conversion can represent.
func (a AutoUnloadConfig) IdleMinutes() int {
	if a.Minutes == nil {
		return EmbeddedLLMDefaultAutoUnloadMinutes
	}
	return *a.Minutes
}

// Embedded LLM memory-plan tuning (the operator override surface).
//
// YAML spellings of the memory-plan override modes: `embeddedllm.Tuning` is
// the authority on what each choice means and ToTuning the single
// translation; declared here because it is a serialization concern (core's
// ContextMode/OffloadMode are int enums with no spelling).
const (
	// EmbeddedLLMTuningAuto ("auto"; empty string accepted as a synonym so
	// `kv_cache_type: ""` cannot be a silent typo) resolves like an absent
	// key but is a DIFFERENT value: absent means the operator chose nothing,
	// which Install/Remove preservation and the round-trip contract key off.
	EmbeddedLLMTuningAuto = "auto"

	EmbeddedLLMContextExact = "exact" // pins -c to context.tokens

	EmbeddedLLMOffloadAll    = "all"    // -ngl 99: every offloadable layer on the device
	EmbeddedLLMOffloadCPU    = "cpu"    // -ngl 0: nothing offloaded
	EmbeddedLLMOffloadLayers = "layers" // explicit -ngl <offload.layers>
)

// EmbeddedLLMMinContextTokens is the smallest legal explicit context: a plan
// that cannot reach a positive `-c` is a refusal, not a configuration.
const EmbeddedLLMMinContextTokens = 1

// embeddedLLMContextModes/embeddedLLMOffloadModes are the closed sets behind
// the two composite knobs, in config.example.yaml order; `auto` is not listed
// (the EmbeddedLLMTuningAuto sentinel, handled by tuningChoice first).
var (
	embeddedLLMContextModes = []string{EmbeddedLLMContextExact}
	embeddedLLMOffloadModes = []string{
		EmbeddedLLMOffloadAll, EmbeddedLLMOffloadCPU, EmbeddedLLMOffloadLayers,
	}
)

// embeddedLLMMaxContext resolves the explicit-context ceiling from the pinned
// model's OWN training context (core's memory profile — never transcribed, so
// a pin bump cannot leave a stale ceiling). It fails closed: an unreadable
// profile is an error, never a permissive bound.
var embeddedLLMMaxContext = sync.OnceValues(func() (int, error) {
	profile, err := embeddedllm.PinnedMemoryProfile()
	if err != nil {
		return 0, fmt.Errorf("the pinned memory profile is unavailable: %w", err)
	}
	if profile.MaxContext <= 0 {
		return 0, errors.New("the pinned memory profile reports no training context")
	}
	return profile.MaxContext, nil
})

// EmbeddedLLMMaxContextTokens exposes the resolved context ceiling for
// diagnostics and tests (fail-closed).
func EmbeddedLLMMaxContextTokens() (int, error) { return embeddedLLMMaxContext() }

// TuningConfig is the persisted operator override surface for the embedded
// model's memory plan, mapped one-to-one onto `embeddedllm.Tuning` (ToTuning
// is the only translation). Every field is a pointer, so an explicit value —
// including "auto" or 0 — is distinguishable from "never written"; the
// distinction is load-bearing (unset fit lets the exclusivity rule decide,
// explicit false forces `-fit off`; unset cache_ram_mib defers to the
// planner, explicit 0 disables the cache). Nothing is seeded by
// ApplyDefaults — the zero value IS the documented all-Auto default — and
// Install/Remove carry the section through verbatim. See
// specs/domains/embedded-llm.md, ADR-067. `Devices`/`SplitMode` are
// deliberately not exposed: they pin the offload and have no safe default on
// an unmeasured machine.
type TuningConfig struct {
	// Context overrides `-c`; absent (or mode auto) lets the planner size it
	// (fit floor under fit, otherwise the RAM ladder).
	Context EmbeddedLLMContextConfig `yaml:"context,omitempty"`
	// KVCacheType overrides BOTH `-ctk` and `-ctv` (a mixed pair silently
	// drops to CPU flash attention). Absent/"auto" = adaptive (f16 → q8_0 →
	// q4_0 until the context fits); any other spelling must be a measured
	// precision — q5_0 is excluded (measured ~8x long-context decode
	// slowdown) and unlisted ones are refused, never coerced to f16.
	KVCacheType *string `yaml:"kv_cache_type,omitempty"`
	// Offload overrides `-ngl`; absent (or mode auto) leaves the layer count
	// to the runtime's fit pass when nothing else pins it.
	Offload EmbeddedLLMOffloadConfig `yaml:"offload,omitempty"`
	// Fit overrides `-fit`: absent lets the exclusivity rule decide, false
	// forces `-fit off` (planner sizes context), and true next to an explicit
	// offload loses to the exclusivity rule (recorded in the plan notes).
	Fit *bool `yaml:"fit,omitempty"`
	// FitTargetMiB overrides `-fitt` (fit's per-device free margin; explicit
	// 0 also omits the flag). 0..embeddedllm.MaxTuningMiB; emitted under fit.
	FitTargetMiB *int `yaml:"fit_target_mib,omitempty"`
	// FitMinContext overrides `-fitc`; absent means c0wrk's 65536 floor —
	// deliberately NOT the runtime's own 4096, small enough to truncate
	// answers on this model. 1..max context; emitted under fit.
	FitMinContext *int `yaml:"fit_min_context,omitempty"`
	// KVOffload is `-kvo`/`-nkvo`: false moves the KV cache from the device
	// to system RAM (device memory traded for host memory and attention
	// bandwidth); absent/true keep it with the layers.
	KVOffload *bool `yaml:"kv_offload,omitempty"`
	// MMProjOffload is `--mmproj-offload`/`--no-mmproj-offload`: false moves
	// the vision projector's reserve to system RAM.
	MMProjOffload *bool `yaml:"mmproj_offload,omitempty"`
	// Packing overrides the weights quantization; absent/"auto" keeps the
	// probe-resolved packing (recorded in embedded_llm.packing). Any other
	// spelling must be registry-pinned AND measured in the memory model, or
	// the plan is refused instead of projected from a file size.
	Packing *string `yaml:"packing,omitempty"`
	// Parallel overrides `-np` (slot count); absent means 1: one agent loop,
	// one request at a time — `-np 4` was measured to inflate fit's
	// projection (24450 → 77297 MiB) while SPLITTING the context across
	// slots. 1..embeddedllm.MaxTuningParallel.
	Parallel *int `yaml:"parallel,omitempty"`
	// CacheRAMMiB overrides `-cram`, the prompt-cache ceiling; absent leaves
	// it to the planner (runtime default raised to measured spare memory
	// only when per-slot context checkpoints are disabled — with snapshots
	// the spare is held for their storage outside the cache limit), and an
	// explicit 0 DISABLES the cache verbatim (a legitimate choice).
	// 0..embeddedllm.MaxTuningMiB.
	CacheRAMMiB *int `yaml:"cache_ram_mib,omitempty"`
	// CtxCheckpoints overrides `--ctx-checkpoints` (per-slot KV snapshots);
	// absent means embeddedllm.DefaultCtxCheckpoints, rendered explicitly so
	// an inherited LLAMA_ARG_CTX_CHECKPOINTS env value cannot decide it.
	// Explicit 0 disables them verbatim (legitimate). 0..MaxTuningCtxCheckpoints.
	CtxCheckpoints *int `yaml:"ctx_checkpoints,omitempty"`
	// CacheIdleSlots is `--cache-idle-slots`/`--no-cache-idle-slots`: false
	// stops saving idle slots to the prompt cache on a new task.
	CacheIdleSlots *bool `yaml:"cache_idle_slots,omitempty"`
	// HostReserveGiB REPLACES (never stacks on) the derived system-RAM
	// reserve (planner-side budget — the pinned fork has no --host-reserve
	// flag); absent keeps the topology derivation (max(4 GiB, RAM/8)). Must
	// be a FINITE number within 0..MaxTuningHostReserveGiB: the planner's
	// float→int conversion must stay representable (see ToTuning).
	HostReserveGiB *float64 `yaml:"host_reserve_gib,omitempty"`
}

// EmbeddedLLMContextConfig is the `tuning.context` knob: a mode plus, for
// `mode: exact` only, the token count. Mirrors `embeddedllm.ContextTuning`.
type EmbeddedLLMContextConfig struct {
	Mode *string `yaml:"mode,omitempty"` // "auto" (absent is a synonym) or "exact"
	// Tokens is the pinned `-c`, validated even while Mode is auto — switching
	// to exact can never activate an out-of-range context.
	Tokens *int `yaml:"tokens,omitempty"`
}

// EmbeddedLLMOffloadConfig is the `tuning.offload` knob: a mode plus, for
// `mode: layers` only, the layer count. Mirrors `embeddedllm.Offload`.
type EmbeddedLLMOffloadConfig struct {
	Mode *string `yaml:"mode,omitempty"` // "auto" (absent is a synonym), "all", "cpu" or "layers"
	// Layers is the explicit `-ngl` count, validated even while Mode is not
	// "layers" (same rule as Tokens). 0..embeddedllm.MaxTuningLayers.
	Layers *int `yaml:"layers,omitempty"`
}

// tuningChoice resolves one string knob against a closed set, reporting the
// CANONICAL spelling and whether the operator picked something other than
// Auto (matching is case-insensitive/trimmed; the error names the key and
// lists every legal spelling).
func tuningChoice(key string, value *string, allowed []string) (canonical string, explicit bool, err error) {
	if value == nil {
		return "", false, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" || strings.EqualFold(trimmed, EmbeddedLLMTuningAuto) {
		return "", false, nil
	}
	for _, candidate := range allowed {
		if strings.EqualFold(trimmed, candidate) {
			return candidate, true, nil
		}
	}
	list := make([]string, 0, len(allowed)+1)
	list = append(list, EmbeddedLLMTuningAuto)
	list = append(list, allowed...)
	return "", false, fmt.Errorf(
		"%s %q is not valid; must be one of %s",
		key, *value, strings.Join(list, ", "),
	)
}

// tuningKVChoices and tuningPackingChoices read the two closed sets from core
// instead of transcribing them, so a precision/packing core adds or retires
// changes what config.yaml may say with no edit here.
func tuningKVChoices() []string {
	kv := embeddedllm.KVTypes()
	out := make([]string, 0, len(kv))
	for _, t := range kv {
		out = append(out, string(t))
	}
	return out
}

func tuningPackingChoices() []string {
	packings := embeddedllm.SupportedPackings()
	out := make([]string, 0, len(packings))
	for _, p := range packings {
		out = append(out, string(p))
	}
	return out
}

// tuningRange checks one integer knob against a closed range (the message
// names the key and states the fix). The CEILING is as load-bearing as the
// floor: core's MaxTuning* figures are overflow/absurdity guards — each value
// feeds a per-unit footprint downstream (MiB→bytes, layers→weights,
// slots→KV caches), so an unbounded knob wraps that arithmetic instead of
// being refused, and a bound at only one layer is walked around by
// hand-editing config.yaml.
func tuningRange(key string, value *int, floor, ceiling int, fix string) error {
	if value == nil {
		return nil
	}
	if *value < floor || *value > ceiling {
		return fmt.Errorf("%s %d is not valid; must be within %d-%d%s", key, *value, floor, ceiling, fix)
	}
	return nil
}

// ToTuning translates the persisted override surface into the planner's
// `embeddedllm.Tuning` vocabulary — the ONLY translation site
// (validateEmbeddedLLMTuning calls it too, so "validate() accepts" and "the
// planner can honour it" are one statement by construction). It fails closed:
// out-of-set spellings and out-of-range numbers are errors naming key and fix,
// never coerced to Auto; pointer fields are cloned so the result never aliases
// the config.
func (t TuningConfig) ToTuning() (embeddedllm.Tuning, error) {
	maxContext, err := embeddedLLMMaxContext()
	if err != nil {
		return embeddedllm.Tuning{}, fmt.Errorf("embedded_llm.tuning: %w", err)
	}

	out := embeddedllm.Tuning{
		FitEnabled:     cloneBoolPtr(t.Fit),
		FitTargetMiB:   cloneIntPtr(t.FitTargetMiB),
		FitMinContext:  cloneIntPtr(t.FitMinContext),
		KVOffload:      cloneBoolPtr(t.KVOffload),
		MMProjOffload:  cloneBoolPtr(t.MMProjOffload),
		Parallel:       cloneIntPtr(t.Parallel),
		CacheRAMMiB:    cloneIntPtr(t.CacheRAMMiB),
		CtxCheckpoints: cloneIntPtr(t.CtxCheckpoints),
		CacheIdleSlots: cloneBoolPtr(t.CacheIdleSlots),
		HostReserveGiB: cloneFloat64Ptr(t.HostReserveGiB),
	}

	// context: a mode plus, for `exact` only, the token count. Tokens is
	// range-checked even while the mode is auto, so switching to exact can
	// never activate a context the model cannot serve.
	ctxMode, ctxExplicit, err := tuningChoice(
		"embedded_llm.tuning.context.mode", t.Context.Mode, embeddedLLMContextModes)
	if err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningContextRange(t.Context.Tokens, maxContext); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if ctxExplicit && ctxMode == EmbeddedLLMContextExact {
		if t.Context.Tokens == nil {
			return embeddedllm.Tuning{}, errors.New(
				"embedded_llm.tuning.context.mode \"exact\" is not valid; must be paired with embedded_llm.tuning.context.tokens, or set context.mode: auto to let the planner size it",
			)
		}
		out.Context = embeddedllm.ContextTuning{
			Mode:   embeddedllm.ContextExact,
			Tokens: *t.Context.Tokens,
		}
	}

	// kv_cache_type: core owns the closed set, so ParseKVType stays the single
	// authority on which precisions have a measurement.
	kv, kvExplicit, err := tuningChoice(
		"embedded_llm.tuning.kv_cache_type", t.KVCacheType, tuningKVChoices())
	if err != nil {
		return embeddedllm.Tuning{}, err
	}
	if kvExplicit {
		parsed, parseErr := embeddedllm.ParseKVType(kv)
		if parseErr != nil {
			return embeddedllm.Tuning{}, fmt.Errorf(
				"embedded_llm.tuning.kv_cache_type: %w", parseErr)
		}
		out.KVType = parsed
	}

	// offload: a mode plus, for `layers` only, the count.
	offMode, offExplicit, err := tuningChoice(
		"embedded_llm.tuning.offload.mode", t.Offload.Mode, embeddedLLMOffloadModes)
	if err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningRange("embedded_llm.tuning.offload.layers", t.Offload.Layers, 0,
		embeddedllm.MaxTuningLayers,
		" (a layer count is never negative; use offload.mode: cpu for nothing offloaded)"); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if offExplicit {
		switch offMode {
		case EmbeddedLLMOffloadAll:
			out.Offload = embeddedllm.Offload{Mode: embeddedllm.OffloadAll}
		case EmbeddedLLMOffloadCPU:
			out.Offload = embeddedllm.Offload{Mode: embeddedllm.OffloadCPU}
		case EmbeddedLLMOffloadLayers:
			if t.Offload.Layers == nil {
				return embeddedllm.Tuning{}, errors.New(
					"embedded_llm.tuning.offload.mode \"layers\" is not valid; must be paired with embedded_llm.tuning.offload.layers, or set offload.mode: auto to let the runtime size it",
				)
			}
			out.Offload = embeddedllm.Offload{
				Mode:   embeddedllm.OffloadLayers,
				Layers: *t.Offload.Layers,
			}
		}
	}

	// packing: core owns the pinned set AND the measured-residency requirement,
	// so the spelling is checked here and the residency at plan time.
	packing, packingExplicit, err := tuningChoice(
		"embedded_llm.tuning.packing", t.Packing, tuningPackingChoices())
	if err != nil {
		return embeddedllm.Tuning{}, err
	}
	if packingExplicit {
		out.Packing = embeddedllm.Packing(packing)
	}

	// The remaining numeric knobs. Every bound is checked whether or not the
	// knob is currently inert — an out-of-range fit_min_context beside
	// `fit: false` is still a dead budget waiting for the flag to flip.
	if err := tuningRange("embedded_llm.tuning.fit_target_mib", t.FitTargetMiB, 0,
		embeddedllm.MaxTuningMiB,
		" (0 keeps the runtime's own 1024 MiB target)"); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningContextRangeNamed("embedded_llm.tuning.fit_min_context",
		t.FitMinContext, maxContext); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningRange("embedded_llm.tuning.parallel", t.Parallel, 1,
		embeddedllm.MaxTuningParallel,
		fmt.Sprintf(" (default %d: one slot, because -np also SPLITS the context across slots)",
			embeddedllm.DefaultParallel)); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningRange("embedded_llm.tuning.cache_ram_mib", t.CacheRAMMiB, 0,
		embeddedllm.MaxTuningMiB,
		" (0 disables the prompt cache, which is a legitimate choice)"); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if err := tuningRange("embedded_llm.tuning.ctx_checkpoints", t.CtxCheckpoints, 0,
		embeddedllm.MaxTuningCtxCheckpoints,
		fmt.Sprintf(" (default %d: the runtime's own figure; 0 disables the per-slot snapshots, which is a legitimate choice)",
			embeddedllm.DefaultCtxCheckpoints)); err != nil {
		return embeddedllm.Tuning{}, err
	}
	if t.HostReserveGiB != nil {
		// A FINITE-and-bounded check, not just `< 0`: NaN compares false
		// against every bound (the old floor-only check let a typo through,
		// silently ignored), and a float→int conversion the result type cannot
		// represent is implementation-defined — saturates on arm64, negative
		// "indefinite" on amd64, which would make host = ram − reserve enormous
		// and fail the memory gate OPEN on the one knob meant to shrink it.
		// Rejecting ±Inf keeps the conversion total on every architecture.
		reserve := *t.HostReserveGiB
		if math.IsNaN(reserve) || math.IsInf(reserve, 0) ||
			reserve < 0 || reserve > embeddedllm.MaxTuningHostReserveGiB {
			return embeddedllm.Tuning{}, fmt.Errorf(
				"embedded_llm.tuning.host_reserve_gib %g is not valid; must be a finite number within 0-%d (it REPLACES the derived reserve, so a negative one would inflate the host budget)",
				reserve, embeddedllm.MaxTuningHostReserveGiB,
			)
		}
	}

	return out, nil
}

// tuningContextRange checks the `context.tokens` knob, whose key is fixed.
func tuningContextRange(tokens *int, maxContext int) error {
	return tuningContextRangeNamed("embedded_llm.tuning.context.tokens", tokens, maxContext)
}

// tuningContextRangeNamed checks one context-shaped knob against the pinned
// profile's training context (never a transcribed figure).
func tuningContextRangeNamed(key string, value *int, maxContext int) error {
	if value == nil {
		return nil
	}
	if *value < EmbeddedLLMMinContextTokens || *value > maxContext {
		return fmt.Errorf(
			"%s %d is not valid; must be within %d-%d (the pinned model's own training context), or leave it unset to let the planner size it",
			key, *value, EmbeddedLLMMinContextTokens, maxContext,
		)
	}
	return nil
}

// cloneBoolPtr / cloneIntPtr / cloneFloat64Ptr copy a pointer field so a
// translated Tuning never aliases the config it came from.
func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func cloneIntPtr(v *int) *int {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func cloneFloat64Ptr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

// BaseURL derives the loopback endpoint from the persisted port — deriving
// (instead of storing) is what keeps a port reallocation from leaving a stale
// endpoint behind.
func (c EmbeddedLLMConfig) BaseURL() string {
	return fmt.Sprintf("http://%s:%d/v1", EmbeddedLLMHost, c.Port)
}

// ProviderConfig returns the generated openai_compatible record for the
// embedded server (meaningful only while Installed): empty API key (loopback
// takes none) and no TLS fingerprint (plain HTTP; ADR-054 pinning does not
// apply).
func (c EmbeddedLLMConfig) ProviderConfig() OpenAICompatibleConfig {
	return OpenAICompatibleConfig{
		BaseURL: c.BaseURL(),
		Models:  []string{EmbeddedLLMModelName},
	}
}

// SyncEmbeddedLLMProvider reconciles the derived LLM state with the
// authoritative embedded_llm section. See LLMConfig.SyncEmbeddedProvider.
func (c *Config) SyncEmbeddedLLMProvider(contextWindow int) bool {
	return c.LLM.SyncEmbeddedProvider(c.EmbeddedLLM, contextWindow)
}

// SyncEmbeddedProvider makes the generated `embedded` provider record match
// the authoritative embedded_llm state: installed → (re)generated from the
// persisted port (a reallocated port or hand-deleted entry self-heals), not
// installed → removed (no dangling provider survives a Remove).
// contextWindow > 0 additionally records the resolved RAM-tier context window
// as an llm.models override (other override fields preserved); <= 0 leaves it
// untouched. The tier is passed IN — no probe, no network, no disk, safe on
// the config-load path. This is the single defense against UpdateLLMConfig's
// whole-map replacement of openai_compatible; maps are copied before mutation
// so a struct copy sharing maps cannot leak a partial change.
func (c *LLMConfig) SyncEmbeddedProvider(state EmbeddedLLMConfig, contextWindow int) bool {
	changed := false

	existing, present := c.OpenAICompatible[EmbeddedLLMProviderName]
	if !state.Installed {
		if present {
			c.copyOpenAICompatibleMap()
			delete(c.OpenAICompatible, EmbeddedLLMProviderName)
			changed = true
		}
	} else {
		want := state.ProviderConfig()
		// output_token_reserve is the one operator knob with no representation
		// in embedded_llm, so it survives regeneration instead of being reset.
		want.OutputTokenReserve = existing.OutputTokenReserve
		if !present || !embeddedProviderEqual(existing, want) {
			c.copyOpenAICompatibleMap()
			c.OpenAICompatible[EmbeddedLLMProviderName] = want
			changed = true
		}
	}

	if contextWindow > 0 && c.setEmbeddedContextWindow(contextWindow) {
		changed = true
	}
	if limit := embeddedOutputLimitCeiling(contextWindow); c.setEmbeddedOutputLimit(limit) {
		changed = true
	}
	return changed
}

// embeddedOutputLimitCeiling is the local model's generation ceiling:
// EmbeddedLLMOutputLimit clamped under contextWindow/4 (an output reserve at
// a quarter of the window is the shape that disables compaction); <= 0 (the
// pre-readback seed path) keeps the plain default.
func embeddedOutputLimitCeiling(contextWindow int) int {
	if contextWindow <= 0 {
		return EmbeddedLLMOutputLimit
	}
	return min(EmbeddedLLMOutputLimit, contextWindow/4)
}

// setEmbeddedOutputLimit records the local model's generation ceiling as its
// llm.models override. A user-authored value is left alone — this seeds a
// default, it does not override (any non-zero present ⇒ no change).
func (c *LLMConfig) setEmbeddedOutputLimit(limit int) bool {
	if limit <= 0 {
		return false
	}
	override := c.Models[EmbeddedLLMModelName]
	if override.OutputLimit != 0 {
		return false
	}
	override.OutputLimit = limit
	c.copyModelOverridesMap()
	c.Models[EmbeddedLLMModelName] = override
	return true
}

// setEmbeddedContextWindow records the resolved context window as the embedded
// model's llm.models override, preserving the remaining override fields (a
// user-authored tokenizer/family/protocol tweak stays). Reports whether the
// stored value changed.
func (c *LLMConfig) setEmbeddedContextWindow(contextWindow int) bool {
	override := c.Models[EmbeddedLLMModelName]
	if override.ContextWindow == contextWindow {
		return false
	}
	override.ContextWindow = contextWindow
	c.copyModelOverridesMap()
	c.Models[EmbeddedLLMModelName] = override
	return true
}

// embeddedProviderEqual reports whether two generated embedded records carry
// the same backend-owned values.
func embeddedProviderEqual(a, b OpenAICompatibleConfig) bool {
	return a.BaseURL == b.BaseURL &&
		a.APIKey == b.APIKey &&
		a.TLSFingerprint == b.TLSFingerprint &&
		a.OutputTokenReserve == b.OutputTokenReserve &&
		slices.Equal(a.Models, b.Models)
}

// copyOpenAICompatibleMap replaces the provider map with a private copy so a
// sync can never mutate a map another LLMConfig value still references.
func (c *LLMConfig) copyOpenAICompatibleMap() {
	clone := make(map[string]OpenAICompatibleConfig, len(c.OpenAICompatible)+1)
	for name, cfg := range c.OpenAICompatible {
		clone[name] = cfg
	}
	c.OpenAICompatible = clone
}

// copyModelOverridesMap replaces the model-override map with a private copy,
// for the same aliasing reason as copyOpenAICompatibleMap.
func (c *LLMConfig) copyModelOverridesMap() {
	clone := make(map[string]ModelOverride, len(c.Models)+1)
	for name, override := range c.Models {
		clone[name] = override
	}
	c.Models = clone
}

// validateEmbeddedLLM checks the embedded_llm section: most of it is
// app-written state, so an invalid value is a hand edit or a bug — either way
// it must fail fast with an actionable message, and the operator-authored
// tuning sub-section is held to the same standard (refuse at load, not at
// launch).
func validateEmbeddedLLM(c *EmbeddedLLMConfig) error {
	// Closed vocabulary; an explicit mtplx cannot carry llama-only tuning —
	// both halves of one contradiction are named together. Auto is not
	// cross-checked here (a platform fact the install owns; PlanMLX refuses
	// the same list there).
	switch embeddedllm.NormalizeEngine(embeddedllm.Engine(c.Engine)) {
	case embeddedllm.EngineAuto, embeddedllm.EngineLlama, embeddedllm.EngineMTPLX:
	default:
		return fmt.Errorf(
			"embedded_llm.engine %q is not valid; must be one of auto, mtplx, llama (or removed for auto)",
			c.Engine,
		)
	}
	if embeddedllm.NormalizeEngine(embeddedllm.Engine(c.Engine)) == embeddedllm.EngineMTPLX {
		tuning, tuningErr := c.Tuning.ToTuning()
		if tuningErr != nil {
			return tuningErr
		}
		if forbidden := embeddedllm.ForbiddenLlamaTuning(tuning); len(forbidden) > 0 {
			return fmt.Errorf(
				"embedded_llm.tuning.%s does not apply to embedded_llm.engine: mtplx (no offload, no fit pass, no KV or packing choice); remove the key or set engine: llama",
				forbidden[0],
			)
		}
	}

	switch {
	case c.Port == 0:
		// 0 means "allocate at install time", which contradicts a completed
		// install: the provider base URL could not be derived.
		if c.Installed {
			return errors.New(
				"embedded_llm.installed is true but embedded_llm.port is 0; the loopback port is allocated during install — set installed: false or reinstall the embedded model so the provider base URL can be derived",
			)
		}
	case c.Port < EmbeddedLLMMinPort || c.Port > EmbeddedLLMMaxPort:
		return fmt.Errorf(
			"embedded_llm.port %d is not valid; must be within %d-%d, or 0 to allocate it at install time",
			c.Port, EmbeddedLLMMinPort, EmbeddedLLMMaxPort,
		)
	}

	// The idle budget is bounded on BOTH sides: the floor keeps a non-positive
	// timer, the ceiling keeps the minutes→nanoseconds multiply total — past
	// it `Duration(minutes) * Minute` wraps and can yield a short POSITIVE
	// budget, silently inverting "effectively never" into "unload
	// immediately" (same overflow class maxMemoryLimitMiB guards).
	if minutes := c.AutoUnload.IdleMinutes(); minutes < 1 || minutes > embeddedllm.MaxAutoUnloadMinutes {
		return fmt.Errorf(
			"embedded_llm.auto_unload.minutes %d is not valid; must be within 1-%d (default %d), or set embedded_llm.auto_unload.enabled: false to disable the idle timer",
			minutes, embeddedllm.MaxAutoUnloadMinutes, EmbeddedLLMDefaultAutoUnloadMinutes,
		)
	}

	// The tuning knobs are validated unconditionally (model not installed, fit
	// off, knob inert) — a dormant value must not become a dead budget when
	// the switch flips. Routing through ToTuning keeps this list and the
	// planner's closed sets from ever disagreeing.
	return validateEmbeddedLLMTuning(&c.Tuning)
}

// validateEmbeddedLLMTuning checks the embedded_llm.tuning section by
// translating it through ToTuning — "validate() accepts" and "the planner can
// honour it" are the same statement by construction, so the two cannot drift.
func validateEmbeddedLLMTuning(t *TuningConfig) error {
	_, err := t.ToTuning()
	return err
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

	// Timeout bounds the initialization handshake (initialize + tools/list) as
	// a duration string; empty selects the mcp package default (60s). Invalid
	// values are rejected on the UI save path and fail soft to the default on
	// the load path.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// CallTimeout bounds a single tools/call invocation (duration string);
	// empty inherits Timeout (itself defaulted when unset). Same
	// save-rejects / load-fails-soft handling as Timeout.
	CallTimeout string `yaml:"call_timeout,omitempty" json:"call_timeout,omitempty"`

	// Mode is the LEGACY per-server activation key (mcp.servers.<name>.mode):
	// "auto" | "manual" | "disabled". The load pipeline folds it into
	// capabilities.mcp.<name> (both contexts, unless explicit values already
	// exist) and always erases the field — the key disappears from YAML at the
	// next save. Unrecognized values fail soft to "auto" with a load warning;
	// the enum is shared with the capabilities section (ValidMCPServerMode).
	// json:"-" (the wire stopped carrying it); the YAML tag stays so legacy
	// configs still load and migrate. See specs/domains/capabilities.md.
	Mode string `yaml:"mode,omitempty" json:"-"`
}

// CapabilityModes is one capability entry's activation modes, one per
// execution context (ChatMode = interactive chat tasks, CodeMode = project
// CODE tasks). Both share the MCP server mode enum ("auto"/"manual"/"disabled",
// ValidMCPServerMode as the single source of truth); empty IS "auto", hence
// the omitempty keys.
type CapabilityModes struct {
	ChatMode string `yaml:"chat_mode,omitempty" json:"chat_mode,omitempty"`
	CodeMode string `yaml:"code_mode,omitempty" json:"code_mode,omitempty"`
}

// CapabilitiesConfig is the top-level capabilities: section (skills, agents,
// mcp), each map keyed by capability name. Optional: a missing name resolves
// to "auto" in both contexts. Normalized at load — both-empty entries dropped
// as meaningless, capabilities.mcp names outside mcp.servers dropped as
// stale — so the section never carries unresolvable entries.
type CapabilitiesConfig struct {
	Skills map[string]CapabilityModes `yaml:"skills,omitempty" json:"skills,omitempty"`
	Agents map[string]CapabilityModes `yaml:"agents,omitempty" json:"agents,omitempty"`
	MCP    map[string]CapabilityModes `yaml:"mcp,omitempty" json:"mcp,omitempty"`
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

// HistoryMutationConfig configures regular (non-emergency) history mutation,
// reducing O(n²) replay cost: unlike emergency compaction it runs on every
// BuildPrompt call, replacing old tool results with cache references.
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
	// VerifyOnEdit runs a config-authored verification command after every
	// successful file edit in CODE tasks and injects bounded output as a
	// system observation; default off, never model-authored. See
	// specs/domains/verify-on-edit.md.
	VerifyOnEdit VerifyOnEditConfig `yaml:"verify_on_edit"`
}

// VerifyOnEditConfig holds the verify-on-edit settings. The zero value is a
// fully disabled no-op.
type VerifyOnEditConfig struct {
	Enabled bool `yaml:"enabled"`
	// Command is the shell command executed after a successful write_file/
	// edit_file call, through the bash tool machinery: the execute-group deny
	// policy and command blocklist still apply, but being user-configured
	// (not model-authored) it skips interactive confirmation.
	Command string `yaml:"command"`
	// Timeout is a Go duration string; empty/invalid falls back to 2 minutes,
	// and the effective value is clamped (with a warning) by
	// timeouts.bashMaxTimeout.
	Timeout string `yaml:"timeout"`
	// MaxOutputChars caps the verification output injected into the model
	// context (truncated with a marker); 0 falls back to the SDK default
	// (agent.DefaultVerifyOnEditCap, currently 4000).
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

// CompactionForecastConfig holds the compression-ratio forecast seeds for the
// LLM-backed manual-compaction strategies: an EWMA refined after each real
// compaction, affecting only the predicted reclaim number in the compact
// menu — never strategy availability (an exact structural verdict). Zeros
// fall back to sp4rk's conservative defaults.
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

	// ToolGroupSystem is the reserved internal/system-tools group
	// (sdktools.GroupSystem on each such tool's BaseTool): policy fixed
	// (always allowed, never surfaced for confirmation), and it MUST NOT
	// appear in config.yaml — validation rejects it so the internal-tools
	// bypass cannot be widened from the config file.
	ToolGroupSystem = "system"
)

// Policy values accepted by security.groups.<group>.policy.
const (
	GroupPolicyAllow       = "allow"        // execute without confirmation
	GroupPolicyUserConfirm = "user_confirm" // ask the user before each call
	GroupPolicyDeny        = "deny"         // refuse to execute
)

// Mode values accepted by security.autonomy_mode — the unified autonomy
// posture replacing the former autonomy booleans (Smart Approve toggle +
// silent-mode master switch). The core mirror (core.AutonomyMode*) is pinned
// against this dictionary by backend/configadapter_test.go.
const (
	AutonomyModeStandard = "standard" // default: every interactive prompt (confirmations, step limits, ask_user, review) reaches a human
	AutonomyModeAssisted = "assisted" // the strict OWASP ASI judge resolves escalated calls; only a strict ALLOW skips the UI
	AutonomyModeSilent   = "silent"   // unattended: the silent-mode sub-policies resolve interactive prompts without a human
)

// Mode values accepted by security.silent_mode.tool_confirm.mode: how a
// silent-mode tool call that would normally open a confirmation card
// resolves without a human.
const (
	// SilentToolConfirmJudge routes through the strict judge (the Smart
	// Approve evaluation): a strict ALLOW executes — canonical hard reasons
	// included; the silent terminal deliberately drops the interactive
	// canonical backstop, and the executed decision is audited — a deliberate
	// DENY denies, and a fail-closed CONFIRM outcome is resolved by
	// security.silent_mode.user_confirm (deny | escalate | confirm). Default.
	SilentToolConfirmJudge = "judge"
	// SilentToolConfirmAllow executes calls carrying no hard safety reason;
	// a HARD reason (canonical or not) escalates to the strict judge, whose
	// ALLOW executes (a fail-closed CONFIRM follows user_confirm).
	SilentToolConfirmAllow = "allow"
	SilentToolConfirmDeny  = "deny" // blocks every confirmation-gated call
)

// Mode values accepted by security.silent_mode.user_confirm.mode: refines the
// tool_confirm terminal for a fail-closed CONFIRM outcome (judge CONFIRM, or
// unavailable/error/timeout/unparseable verdict). Never governs a deliberate
// judge DENY or ALLOW — those tool_confirm resolves alone.
const (
	SilentUserConfirmConfirm  = "confirm"  // executes unattended, audited as an allow — most permissive
	SilentUserConfirmDeny     = "deny"     // fail-closed default: auto-denies with the reasoning
	SilentUserConfirmEscalate = "escalate" // falls back to the blocking card — least permissive
)

// Mode values accepted by security.silent_mode.step_limit.mode: what the
// execution loops do at a step-limit boundary (budget exhaustion or
// circuit-breaker abort). "auto" lets the strict loop judge decide; the rest
// pin one response deterministically without consulting the judge.
const (
	SilentStepLimitAuto        = "auto"         // judge decides from the trajectory; fail-closed to deny. Default.
	SilentStepLimitAllowOnce   = "allow_once"   // exactly one more step
	SilentStepLimitAllowMore   = "allow_more"   // a fresh batch of steps
	SilentStepLimitAllowAlways = "allow_always" // suspends the step budget entirely
	SilentStepLimitDeny        = "deny"         // halts at the boundary and reports it, with NO card
	SilentStepLimitStop        = "stop"         // keeps the blocking card — opts this gate out of silent mode
)

// Mode values accepted by security.silent_mode.ask_user.mode (ask_user
// availability under silent mode).
const (
	SilentAskUserDisable = "disable" // registered with a nil callback — calls resolve "not available". Default.
	SilentAskUserEnable  = "enable"  // keeps the ask_user tool registered
)

// TrustedGitRepo is one security.trusted_git_repos entry: a repository whose
// untrusted-git-config intake warning the user explicitly dismissed. Path is
// the absolute, Clean-ed work-tree root; Fingerprint identifies the captured
// git-config snapshot (a later scan diffs against it and reinstates the
// warning on change) and SemanticFingerprint its dangerous semantic content —
// the trust lifecycle is bound to the semantic identity, so benign
// branch/alias churn no longer evicts trust while any semantic change still
// does. Empty fingerprints mark pre-fingerprinting records the backend
// migrates on next open (fail-closed eviction when the semantic identity
// cannot be recovered).
type TrustedGitRepo struct {
	Path                string `yaml:"path"`
	Fingerprint         string `yaml:"fingerprint,omitempty"`
	SemanticFingerprint string `yaml:"semantic_fingerprint,omitempty"`
}

// UnmarshalYAML accepts both the current mapping form
// ({path, fingerprint, semantic_fingerprint}) and the legacy bare-path string
// form; a legacy string migrates to a Path with empty fingerprints, preserving
// warning suppression without inventing a snapshot that was never captured.
func (r *TrustedGitRepo) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		r.Path = value.Value
		r.Fingerprint = ""
		r.SemanticFingerprint = ""
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

// validateGitSemanticFingerprint checks a trusted_git_repos entry's
// semantic_fingerprint: empty (not yet migrated) or the 64-character hex
// SHA-256 the scanner writes — anything else can never match a computed
// value and would silently turn every later open into a trust eviction.
func validateGitSemanticFingerprint(v string) error {
	if v == "" {
		return nil
	}
	if len(v) != 64 {
		return errors.New("semantic_fingerprint must be a 64-character hex SHA-256 or empty")
	}
	if _, err := hex.DecodeString(v); err != nil {
		return fmt.Errorf("semantic_fingerprint must be hex: %w", err)
	}
	return nil
}

// SecurityConfig holds security settings.
type SecurityConfig struct {
	InjectionDefense InjectionDefenseConfig `yaml:"injection_defense"`

	// Groups is the tool-security schema: a fixed set of tool groups, each
	// with its own policy (plus an execute-only command blocklist). See the
	// ToolGroup* constants and defaults.go for names and default policies.
	Groups map[string]GroupPolicyConfig `yaml:"groups"`

	// AutoApproveWorkspaceWrites auto-executes the file write tools when all
	// paths stay within the session workspace (symlink traversals still force
	// confirmation). Default: false.
	AutoApproveWorkspaceWrites bool `yaml:"auto_approve_workspace_writes"`

	// AutonomyMode is the unified autonomy posture (see the AutonomyMode*
	// constants). Configs written before the enum carry the two legacy keys,
	// migrated by migrateLegacyAutonomyMode (silent wins over assisted); an
	// unknown value fails safe to standard with a load warning.
	AutonomyMode string `yaml:"autonomy_mode"`

	// LegacySmartApprove is the load-time mirror of the pre-enum
	// smart_approve key: populated only by yaml decoding, migrated (or
	// dropped) by migrateLegacyAutonomyMode immediately after load — the
	// blacklist_migration.go pattern. Never persisted (omitempty +
	// unconditional clearing; json:"-").
	LegacySmartApprove *bool `yaml:"smart_approve,omitempty" json:"-"`

	// SilentMode is the silent-mode sub-policy container
	// (security.silent_mode); live only while AutonomyMode is "silent" — the
	// former master switch migrated onto that enum. See SilentModeConfig.
	SilentMode SilentModeConfig `yaml:"silent_mode"`

	// AgentsMDMaxBytes caps the AGENTS.md content injected into prompts —
	// workspace-controlled untrusted input. 0 = default (65536), -1 = no cap
	// (use with caution).
	AgentsMDMaxBytes int `yaml:"agents_md_max_bytes"`

	// TrustedGitRepos lists roots whose untrusted-git-config warning was
	// dismissed via "Trust this repo": suppression plus opt-back-in to raw
	// git (the list is mirrored into the core/gittrust registry the spawn
	// layer consults; an untrusted root keeps the full neutralization).
	// Exact path matching; default empty.
	TrustedGitRepos []TrustedGitRepo `yaml:"trusted_git_repos,omitempty"`

	// HardenGitRepos lists roots that are always treated as hardened: the
	// warning is never suppressed and the root is excluded from the trust
	// list (mutual exclusion enforced by validation, across the same
	// trust-key forms the runtime matcher probes). Default: empty.
	HardenGitRepos []string `yaml:"harden_git_repos,omitempty"`
}

// GroupPolicyConfig holds per-group security policy configuration
// (security.groups.<group>).
type GroupPolicyConfig struct {
	Policy string `yaml:"policy"` // "allow"|"user_confirm"|"deny"
	// Blocklist holds regex patterns applied to shell commands (execute group
	// only — validation rejects it elsewhere): a match forces confirmation
	// regardless of Policy. Empty by default — the app ships no predefined
	// patterns; the list is purely a user-authored extension.
	Blocklist []string `yaml:"blocklist,omitempty" json:"blocklist,omitempty"`

	// LegacyBlacklist is the load-time mirror of the pre-rename blacklist key:
	// populated only by yaml decoding, migrated into Blocklist (or dropped) by
	// migrateLegacyGroupBlacklists — the blacklist_migration.go pattern.
	// Never persisted (omitempty + unconditional clearing; json:"-").
	LegacyBlacklist []string `yaml:"blacklist,omitempty" json:"-"`
}

// SilentModeConfig is security.silent_mode: the container for the four
// unattended-operation sub-policies over the three interactive decisions the
// execution loops would otherwise punt to the user (tool confirmation is
// refined by user_confirm). Live only while the unified autonomy mode is
// "silent"; the accepted Mode values are documented on the SilentToolConfirm*,
// SilentUserConfirm*, SilentStepLimit* and SilentAskUser* enums.
//
// Silent mode replaces only the human ANSWER, never the gate: deny groups and
// the deterministic pre-funnel floor hold under every mode, and the canonical
// hard-reason backstop (isCanonicalHardReason) is scoped to the interactive
// paths — the silent judge terminal deliberately drops it, so a strict ALLOW
// (and, under user_confirm=confirm, a fail-closed CONFIRM) executes a
// canonical reason unattended, audited.
type SilentModeConfig struct {
	// LegacyEnabled is the load-time mirror of the pre-enum
	// `silent_mode.enabled` yaml key: populated only by yaml decoding,
	// migrated into SecurityConfig.AutonomyMode (or dropped) by
	// migrateLegacyAutonomyMode immediately after load — the
	// blacklist_migration.go pattern. Never persisted (omitempty +
	// unconditional clearing; json:"-").
	LegacyEnabled *bool `yaml:"enabled,omitempty" json:"-"`

	ToolConfirm SilentSubPolicyConfig `yaml:"tool_confirm"` // resolves a call that would open a confirmation card; default "judge"
	// UserConfirm refines the ToolConfirm terminal for a fail-closed CONFIRM
	// outcome ("confirm" executes unattended, "escalate" opens the card,
	// "deny" auto-denies); default "deny".
	UserConfirm SilentSubPolicyConfig `yaml:"user_confirm"`
	StepLimit   SilentSubPolicyConfig `yaml:"step_limit"` // resolves the step-budget-exhaustion card; default "auto"
	AskUser     SilentSubPolicyConfig `yaml:"ask_user"`   // ask_user tool availability; default "disable"
}

// SilentSubPolicyConfig is one silent-mode sub-policy: a single Mode drawn
// from that sub-policy's enum.
type SilentSubPolicyConfig struct {
	Mode string `yaml:"mode"`
}

// ValidateSilentMode checks that every silent-mode sub-policy carries a value
// from its enum; shared by config validation and the runtime
// security-settings update, so the UI can never store what the loader would
// reject. An empty Mode passes here (ApplyDefaults has already seeded the
// default); callers bypassing ApplyDefaults validate the defaulted value.
func ValidateSilentMode(sm SilentModeConfig) error {
	checks := []struct {
		name    string
		mode    string
		allowed []string
	}{
		{"tool_confirm", sm.ToolConfirm.Mode, []string{SilentToolConfirmJudge, SilentToolConfirmAllow, SilentToolConfirmDeny}},
		{"user_confirm", sm.UserConfirm.Mode, []string{SilentUserConfirmConfirm, SilentUserConfirmDeny, SilentUserConfirmEscalate}},
		{"step_limit", sm.StepLimit.Mode, []string{SilentStepLimitAuto, SilentStepLimitAllowOnce, SilentStepLimitAllowMore, SilentStepLimitAllowAlways, SilentStepLimitDeny, SilentStepLimitStop}},
		{"ask_user", sm.AskUser.Mode, []string{SilentAskUserDisable, SilentAskUserEnable}},
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

// SilentModeDefaults returns the canonical default silent-mode configuration
// (each sub-policy at its documented default) — the single source of truth
// for ApplyDefaults and the runtime security-settings update.
func SilentModeDefaults() SilentModeConfig {
	return SilentModeConfig{
		ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmJudge},
		UserConfirm: SilentSubPolicyConfig{Mode: SilentUserConfirmDeny},
		StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitAuto},
		AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserDisable},
	}
}

// ApplySilentModeDefaults fills each unset (empty) sub-policy mode with its
// default. An explicit mode is preserved.
func ApplySilentModeDefaults(sm *SilentModeConfig) {
	d := SilentModeDefaults()
	if sm.ToolConfirm.Mode == "" {
		sm.ToolConfirm.Mode = d.ToolConfirm.Mode
	}
	if sm.UserConfirm.Mode == "" {
		sm.UserConfirm.Mode = d.UserConfirm.Mode
	}
	if sm.StepLimit.Mode == "" {
		sm.StepLimit.Mode = d.StepLimit.Mode
	}
	if sm.AskUser.Mode == "" {
		sm.AskUser.Mode = d.AskUser.Mode
	}
}

// AskUserDisabled reports whether the autonomy mode is "silent" AND the
// ask_user sub-policy disables the tool — the predicate suppressing ask_user
// registration. The core mirror on core.BuilderSecurityConfig is pinned to
// this one by backend/configadapter_test.go.
func (s SecurityConfig) AskUserDisabled() bool {
	return s.AutonomyMode == AutonomyModeSilent && s.SilentMode.AskUser.Mode == SilentAskUserDisable
}

// migrateLegacyAutonomyMode migrates the pre-enum autonomy booleans
// (security.smart_approve / security.silent_mode.enabled) onto the unified
// SecurityConfig.AutonomyMode enum — the blacklist_migration.go load-time
// pattern. Resolution: an explicit security.autonomy_mode decides (unknown
// value fails SAFE to "standard" with a warning — a newer app's value must
// not brick startup); otherwise the legacy keys are honored, silent_mode
// winning priority over smart_approve. The mirrors are cleared
// unconditionally so the stale keys disappear at the next Save; every
// observed/ignored/unknown case is reported as a load warning.
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

// SearchConfig holds web search configuration.
type SearchConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
	// BaseURL is the self-hosted SearXNG instance URL; only used when
	// Provider is "searxng" (validated by ValidateSearchBaseURL, seeded to
	// websearch.DefaultSearxngBaseURL by ApplyDefaults).
	BaseURL string `yaml:"base_url"`
}

// ToolLimitsConfig holds configurable limits for builtin tools.
// These limits prevent tool outputs from consuming excessive context.
type ToolLimitsConfig struct {
	// File read limits
	ReadDefaultLines int `yaml:"readDefaultLines"` // max lines per read call (default: 2000)

	// Search limits
	WebSearchMaxResults int `yaml:"webSearchMaxResults"` // max web search results (default: 5)

	// Glob limits (runaway-walk protection). Together they make a single glob
	// walk bounded so a symlink loop or an enormous directory tree can neither
	// hang the tool nor exhaust memory. *int: an absent key (nil) takes the
	// default; an explicit 0 disables the respective budget.
	GlobMaxEntries *int `yaml:"globMaxEntries"` // max filesystem entries visited per glob walk before abort (nil = default 500000; 0 = no entry budget)
	GlobMaxResults *int `yaml:"globMaxResults"` // max matching paths collected per glob walk before abort (nil = default 10000; 0 = unlimited)

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
// YAMLNilAwareList is a []string whose nil state survives Save→Load:
// yaml.v3 marshals a plain nil slice as `[]`, which would reload as an
// explicit empty list — MarshalYAML renders nil as YAML null and passes
// every non-nil slice (explicit empty included) through verbatim.
type YAMLNilAwareList []string

func (l YAMLNilAwareList) MarshalYAML() (interface{}, error) {
	if l == nil {
		return nil, nil
	}
	return []string(l), nil
}

type TimeoutsConfig struct {
	BashMaxTimeout  int  `yaml:"bashMaxTimeout"`  // seconds, default: 120
	BashWaitDelay   int  `yaml:"bashWaitDelay"`   // seconds, default: 5
	RipgrepTimeout  int  `yaml:"ripgrepTimeout"`  // seconds, default: 60
	GlobTimeout     *int `yaml:"globTimeout"`     // seconds; wall-clock budget for a single glob walk (nil = default 30; 0 = no timeout)
	ToolCallTimeout *int `yaml:"toolCallTimeout"` // seconds; ceiling for a SINGLE tool call in the ReAct loop, applied to the main and every subagent executor (nil = default 300 (5 min); 0 = disabled)
	// ToolCallTimeoutExemptTools replaces the ceiling's exempt tool-NAME set
	// (Conductor/executor loops only — not a capability group; the E2S
	// watchdog keeps its own narrowed built-in set). Absent (nil) follows
	// sp4rk's built-in default (agent.DefaultToolCallTimeoutExemptTools),
	// resolved at runtime and deliberately never persisted; an explicit list
	// replaces it wholesale ("no exemptions" = explicit empty). A stored list
	// equal to the built-in default may be an older build's leftover —
	// deleting the key re-follows the built-in set. The YAMLNilAwareList type
	// preserves absent-vs-empty through Save.
	ToolCallTimeoutExemptTools YAMLNilAwareList `yaml:"toolCallTimeoutExemptTools"`
	WebFetchTimeout            int              `yaml:"webFetchTimeout"`          // seconds, default: 30
	WebFetchProxyTimeout       int              `yaml:"webFetchProxyTimeout"`     // seconds, default: 30 — per-attempt web fetch timeout used when the proxy is enabled
	WebFetchRetries            int              `yaml:"webFetchRetries"`          // retry count (not seconds) for failed web fetches; each retry doubles the active timeout (webFetchTimeout, or webFetchProxyTimeout when the proxy is on), default: 2
	WebSearchTimeout           int              `yaml:"webSearchTimeout"`         // seconds, default: 30
	PersistenceTimeout         int              `yaml:"persistenceTimeout"`       // seconds, default: 5
	LLMRequestTimeout          int              `yaml:"llmRequestTimeout"`        // seconds; the main chat loop. Under the adaptive budget (ADR-071): >0 is a FIXED, never-escalated override, 0 (the default) means "no opinion" and the trained per-model budgets govern; with the adaptive budget disabled it keeps the legacy fixed semantics (0 behaves as 600).
	ServiceLLMRequestTimeout   int              `yaml:"serviceLLMRequestTimeout"` // seconds, default: 600 (10 min) — one-shot service LLM requests (session title, commit message, prompt optimization); one budget for the whole client exchange incl. auto-retry re-sends
	GitCommitTimeout           int              `yaml:"gitCommitTimeout"`         // seconds, default: 300 (5 min) — git commit spawn (rev-parse and other quick git probes keep the fast 30s timeout)
	// AdaptiveBudget configures the adaptive per-model LLM request budget
	// (ADR-071). See AdaptiveBudgetConfig.
	AdaptiveBudget AdaptiveBudgetConfig `yaml:"adaptive_budget"`
}

// ShutdownConfig bounds the application teardown. Every teardown step is
// individually bounded, but a broken cancellation path can still park the
// main thread — the desktop layer arms one hard watchdog over the whole
// teardown (Error log + forced exit), so a silent hang on quit is impossible.
type ShutdownConfig struct {
	// HardDeadline is the total seconds budget for the whole Shutdown
	// teardown; on expiry the desktop layer logs at Error and exits
	// immediately. 0/omitted = 60. The default deliberately sits above the
	// embedded model's stop ceiling (~46 s when a quit races an in-flight
	// cold load holding the supervisor gate): a shorter deadline truncates
	// that stop — the process exits but the detached llama-server may
	// outlive it, the one quit outcome the embedded teardown cannot
	// otherwise produce. Lower only if a short forced exit matters more.
	HardDeadline int `yaml:"hardDeadline"`
}

// AdaptiveBudgetConfig holds the adaptive per-model LLM request budget
// kill-switch (ADR-071 D11). Enabled (the default) arms every main-agent LLM
// request with a per-model deadline learned from the session's own traffic
// (core/llmbudget transport); disabled installs no transport anywhere and
// restores the pre-ADR-071 fixed regime (timeouts.llmRequestTimeout, 0
// behaving as the legacy 600 s). See specs/domains/llm-providers.md.
type AdaptiveBudgetConfig struct {
	// Enabled is the kill-switch (*bool so an explicit false survives
	// defaults; nil = unset → true).
	Enabled *bool `yaml:"enabled"`
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

// GoalLoopConfig holds goal-mode settings. Verification selects the verifier
// ("independent" default — a separate verifier turn confirms completion; "off"
// relies on declare_goal_status alone); Suggest gates the goal-mode suggestion
// (after routing a FRESH non-goal task, a one-shot classifier may offer to
// activate goal mode before it starts). See specs/domains/goal-mode.md, ADR-086.
type GoalLoopConfig struct {
	Verification string            `yaml:"verification"` // "independent" | "off"; default "independent"
	Suggest      GoalSuggestConfig `yaml:"suggest"`
}

// GoalSuggestConfig holds the goal-mode suggestion gate settings.
type GoalSuggestConfig struct {
	Enabled *bool `yaml:"enabled"` // nil defaults to true; explicit false opts out entirely
	// MinComplexity is the routing-complexity floor below which the
	// suitability classifier is never consulted (default 3).
	MinComplexity int `yaml:"min_complexity"`
}

// SuggestEnabled resolves the effective feature gate: unset (nil) means on.
func (c GoalSuggestConfig) SuggestEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// PromptSuggestionsConfig gates the follow-up prompts (ADR-088): after a
// full/partial task outcome, a fail-open one-shot service call generates a
// small set of suggested next prompts for the chat input (↑/↓/→).
type PromptSuggestionsConfig struct {
	// Enabled gates the whole feature; nil defaults to true. Explicit false
	// means the manager's outcome gate never fires — zero LLM calls, zero
	// store writes, zero events.
	Enabled *bool `yaml:"enabled"`
}

// SuggestionsEnabled resolves the effective feature gate: unset (nil) means
// on (ADR-088 D9 — fail-open and bounded; the section is the documented
// opt-out extension point).
func (c PromptSuggestionsConfig) SuggestionsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// SkillsConfig holds Agent Skills discovery configuration.
type SkillsConfig struct {
	// Dirs lists skill discovery directories in priority order (highest
	// first); absolute or relative to the agent directory (~/.c0wrk).
	Dirs []string `yaml:"dirs"`
}

// AgentsConfig holds Subagent Profile (AGENT.md) discovery configuration.
// Mirrors SkillsConfig; the project-local .agents/agents directory is always
// scanned automatically (core/builder.go) and needs no entry here.
type AgentsConfig struct {
	// Dirs lists profile discovery directories in priority order (highest
	// first); absolute or relative to the agent dir.
	Dirs []string `yaml:"dirs"`

	// MaxParallelSubagents caps concurrent subagent execution through the
	// launcher's shared limiter every dispatch path acquires (delegate tool,
	// blocking and async, plus plan waves); <= 0 resolves to the default (4)
	// in ApplyDefaults. Bounding concurrency bounds the event/call burst
	// without changing results or their order.
	MaxParallelSubagents int `yaml:"max_parallel_subagents"`
}

// CommandsConfig holds custom command discovery configuration (ADR-096):
// markdown prompt templates invoked with a leading /name token.
type CommandsConfig struct {
	// Dirs lists command discovery directories in priority order (highest
	// first); absolute or relative to ~/.c0wrk. Mirrors SkillsConfig: the
	// project-local .agents/commands directory is always scanned
	// (backend/frontend_api_commands.go) and needs no entry here.
	Dirs []string `yaml:"dirs"`
}

// envVarPattern matches ${ENV_VAR} patterns for substitution.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// ExperimentalConfig gates features under active development behind one
// all-or-nothing master switch (no per-feature toggle): the E2S execution
// mode, the embedded model's FRONTEND surfaces (the backend keeps serving the
// subsystem's RPCs; ADR-068), and the browser web-access sidecar — a compound
// gate with web_access.enabled, a mid-session flip of either followed live.
// Model Profiles is NOT gated — it carries its own master toggle.
type ExperimentalConfig struct {
	// Enabled is the master switch; false treats every gated feature as off
	// regardless of its own toggles. Default: false.
	Enabled bool `yaml:"enabled"`
}

// ModelProfilesPersistConfig is the persisted `model_profiles:` section: only
// the two durable operator choices — the 25 variant knobs live in profiles
// (predefined catalog + ~/.c0wrk/model-profiles.yaml). Legacy inline
// small_llm.* keys are ignored by non-strict decoding and disappear at the
// next save (sanctioned reset migration). See specs/domains/model-profiles.md.
type ModelProfilesPersistConfig struct {
	// Enabled is the master toggle (no auto-detection); false ignores every
	// variant sub-toggle. Default: false.
	Enabled bool `yaml:"enabled"`

	// ActiveProfile is the id whose knob values form the effective runtime
	// configuration (ResolveModelProfilesConfig). ApplyDefaults seeds
	// "generic"; a retired predefined id resolves to its replacement, an
	// unresolvable id falls back to "generic" with a warning — never failing
	// the run.
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

// ResolveModelProfilesConfig builds the effective runtime ModelProfilesConfig
// for a persisted `model_profiles:` section against a catalog (predefined ∪
// custom — LoadModelProfilesCatalog). A known id → that profile; a retired
// predefined id → its replacement plus one warning; an empty/unknown id →
// soft fallback to "generic" plus one warning (a stale id never breaks the
// run). Enabled carries over verbatim — the master toggle is the only switch.
// The returned struct owns its slices (callers cannot mutate the catalog).
func ResolveModelProfilesConfig(persist ModelProfilesPersistConfig, catalog []ModelProfile) (resolved ModelProfilesConfig, warnings []string) {
	var profile ModelProfile
	if id := persist.ActiveProfile; id != "" {
		if found, resolvedID, ok := FindModelProfileResolvingLegacy(catalog, id); ok {
			if resolvedID != id {
				warnings = append(warnings, fmt.Sprintf(
					"model_profiles.active_profile %q is a retired predefined id; resolving to the %q profile",
					id, resolvedID))
			}
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

// LoadModelProfilesCatalog assembles the full resolution catalog: the
// compiled-in predefined entries plus custom profiles from
// <agentDir>/model-profiles.yaml. Store problems come back as warnings — a
// damaged custom store must never take the predefined catalog down with it.
func LoadModelProfilesCatalog(agentDir string) (catalog []ModelProfile, warnings []string) {
	custom, storeWarnings := LoadCustomModelProfiles(ModelProfilesPath(agentDir))
	catalog = PredefinedModelProfiles()
	return append(catalog, custom...), storeWarnings
}

// ModelProfilesConfig is the RESOLVED runtime view: the master toggle plus the
// active profile's 25 variant knobs (ResolveModelProfilesConfig). Not
// persisted — only enabled/active_profile are (ModelProfilesPersistConfig).
// Each variant carries its own sub-toggle; every threshold is exposed so
// tuning needs no rebuild.
type ModelProfilesConfig struct {
	// Enabled is the master toggle (no auto-detection); false ignores every
	// variant sub-toggle. Default: false.
	Enabled bool `yaml:"enabled"`

	EssentialTools EssentialToolsConfig        `yaml:"essential_tools"` // narrows the visible tool set to cut schema/token overhead
	SystemPrompt   SystemPromptConfig          `yaml:"system_prompt"`   // lite/few-shot/scaffold variants shrink the system prompt
	Sampling       ModelProfilesSamplingConfig `yaml:"sampling"`        // deterministic, lower-effort sampling for smaller models
	LoopHardening  LoopHardeningConfig         `yaml:"loop_hardening"`  // tighter circuit-breakers catch small-model loops sooner
	Context        ModelProfilesContextConfig  `yaml:"context"`         // tighter compaction/pruning, larger output reserve
}

// EssentialToolsConfig narrows the tool set visible to a smaller model,
// reducing per-prompt schema overhead.
type EssentialToolsConfig struct {
	Enabled bool `yaml:"enabled"` // gates the variant; false exposes the full tool set
	// AlwaysPresent is the allow-list always exposed when the variant is
	// active; protected orchestration tools and all MCP tools are preserved
	// regardless, and the selection is never trimmed.
	AlwaysPresent []string `yaml:"always_present"`
	// CompactDescriptions swaps every known builtin's full rubric description
	// for a one-line compact variant; off by default (byte-identical
	// descriptions when off).
	CompactDescriptions bool `yaml:"compact_descriptions"`
}

// SystemPromptConfig applies prompt-simplification variants to shrink the
// system prompt. Each flag is independent; FewShot and ReasoningScaffold are
// honored only under Lite (both tailored to the compact lite directive).
type SystemPromptConfig struct {
	Lite bool `yaml:"lite"` // swaps OrchestratorSystem for the compact OrchestratorSystemLite
	// FewShot appends curated worked-example ReAct cycles (format, tool
	// choice, error recovery, finish); only under Lite.
	FewShot bool `yaml:"few_shot"`
	// ReasoningScaffold appends a three-step scaffold (goal → tool+why →
	// args); only under Lite.
	ReasoningScaffold bool `yaml:"reasoning_scaffold"`
}

// ModelProfilesSamplingConfig overrides LLM sampling parameters for a small
// model. Zero is the "not set" sentinel: unset parameters inherit the
// per-family vendor preset (prompt.DefaultSampling), so enabling the variant
// with no explicit values is a behavioral no-op; out-of-range values are
// rejected by ValidateModelProfileConfig.
type ModelProfilesSamplingConfig struct {
	Enabled bool `yaml:"enabled"` // gates this variant

	Temperature float64 `yaml:"temperature"` // lower = more deterministic; set ⇒ > 0
	TopP        float64 `yaml:"top_p"`       // nucleus mass; set ⇒ (0, 1]
	TopK        int     `yaml:"top_k"`       // set ⇒ >= 1
	// RepetitionPenalty penalizes repeated tokens; set ⇒ [1, 2].
	RepetitionPenalty float64 `yaml:"repetition_penalty"`
	// PresencePenalty is the OpenAI-schema anti-repetition lever: the Qwen
	// card sanctions 0–2 (instruct default 1.5), above 2 increases language
	// mixing and is rejected; set ⇒ [0, 2] (0/unset = not sent).
	PresencePenalty float64 `yaml:"presence_penalty"`
	// ReasoningEffort: "" (inherit; the shipped "generic" profile pins
	// "medium") | "off" | "low" | "medium"; an explicit value is never
	// overwritten.
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// LoopHardeningConfig tightens executor circuit-breaker thresholds for a
// small LLM so loops are caught earlier, conserving the token budget.
type LoopHardeningConfig struct {
	Enabled bool `yaml:"enabled"` // gates this variant

	RepeatNudgeThreshold     int `yaml:"repeat_nudge_threshold"`      // identical tool calls before a corrective nudge
	ParseErrorAbortThreshold int `yaml:"parse_error_abort_threshold"` // parse errors before abort
	FruitlessNudgeThreshold  int `yaml:"fruitless_nudge_threshold"`   // minimal-result calls before a nudge
	FruitlessAbortThreshold  int `yaml:"fruitless_abort_threshold"`   // minimal-result calls before abort
	// SameToolRepeatNudgeThreshold: same-tool (varied args, similar results)
	// calls before a corrective nudge.
	SameToolRepeatNudgeThreshold int `yaml:"same_tool_repeat_nudge_threshold"`
}

// ModelProfilesContextConfig is the fifth variant: aggressive context
// management — tighter compaction, stricter tool-output pruning, a larger
// output reserve. The general executor defaults are NOT changed; overrides
// apply only while both the master toggle and this variant's toggle are on.
type ModelProfilesContextConfig struct {
	Enabled bool `yaml:"enabled"` // gates this variant (besides the master toggle)

	// Compaction overrides the executor compaction knobs.
	Compaction ModelProfilesCompactionConfig `yaml:"compaction"`
	// ToolOutputKeepLastN overrides the tool-output pruning depth.
	ToolOutputKeepLastN int `yaml:"tool_output_keep_last_n"`
	// OutputTokenReserve overrides the reserved output budget (e.g. 8192).
	OutputTokenReserve int `yaml:"output_token_reserve"`
}

// ModelProfilesCompactionConfig holds the compaction-tightening overrides;
// zero values keep the corresponding executor baseline.
type ModelProfilesCompactionConfig struct {
	KeepLast       int `yaml:"keep_last"`       // sliding-window keep-last (variant 6 vs general 10)
	BlockSize      int `yaml:"block_size"`      // summarization block (variant 5 vs general 7)
	TriggerPercent int `yaml:"trigger_percent"` // predictive trigger (variant 80 vs general 85)
}

// E2SConfig configures the E2S (explicit-state) execution mode, fail-closed
// gated by experimental.enabled alone. Every knob is seeded so tuning never
// needs a rebuild. See specs/domains/e2s.md, ADR-040 (§6 anti-spin).
type E2SConfig struct {
	// MaxSteps caps the turns (patch+action cycles) per run; exhaustion is a
	// resumable step-limit checkpoint (Σ preserved, fresh budget on Resume),
	// not a failure. Default: 50.
	MaxSteps int `yaml:"max_steps"`

	// StateByteLimit caps the JSON-encoded Σ size: an over-limit patch is a
	// validation error (bounded retry, then the error becomes the next
	// observation — the run continues, Σ unchanged); the initial Σ is checked
	// up front so an oversized objective fails fast. Default: 16384.
	StateByteLimit int `yaml:"state_byte_limit"`

	// PatchRetries is how often a rejected patch may be re-requested before
	// the failure becomes an error observation. Default: 1.
	PatchRetries int `yaml:"patch_retries"`

	// ObservationTruncate caps the per-turn observation in characters — the
	// FALLBACK when no model context window is resolved; with a window the
	// token-proportional budget below takes over. Default: 8000.
	ObservationTruncate int `yaml:"observation_truncate"`

	// ObservationBudgetTokens is the per-turn observation budget in TOKENS
	// (ReAct-scale); with a resolved window the effective cap is
	// min(budget, fill_fraction × window) × 4 chars. Default: 8192.
	ObservationBudgetTokens int `yaml:"observation_budget_tokens"`

	// ObservationFillFraction caps the budget as a window fraction so a
	// small-window model gets proportionally smaller observations.
	// Default: 0.4.
	ObservationFillFraction float64 `yaml:"observation_fill_fraction"`

	// RepeatNudgeThreshold: consecutive identical step actions (same tool +
	// target anchor, the ADR-040 §6 semantic fingerprint — line ranges
	// ignored, write/edit payloads included) before a corrective nudge is
	// injected instead of the redundant dispatch. Default: 3.
	RepeatNudgeThreshold int `yaml:"repeat_nudge_threshold"`

	// RepeatAbortThreshold: consecutive identical actions before the run is
	// aborted as a spin; must be >= the nudge threshold. Default: 5.
	RepeatAbortThreshold int `yaml:"repeat_abort_threshold"`

	// Tools narrows the E2S tool catalog — the "Available Tools" prompt
	// section AND the dispatch surface (the catalog is the dispatch contract;
	// filtered names are rejected fail-closed). Filtering runs before the
	// plan/delegation/goal stripping. See core/orchestrator_e2s.go.
	Tools E2SToolsConfig `yaml:"tools"`
}

// E2S tool-catalog preset values (must match core.E2SToolsPresetCore /
// core.E2SToolsPresetAll — backend/config does not import the core package).
const (
	E2SToolsPresetCore = "core" // default: local_read/local_write/execute/remote_read groups + E2S system plumbing
	E2SToolsPresetAll  = "all"  // the full former surface (goal/plan/delegation stripping always applies)
)

// E2SToolsConfig configures the E2S tool-catalog narrowing (e2s.tools).
type E2SToolsConfig struct {
	// Preset selects the base catalog: "core" (default) or "all"; anything
	// else is rejected at config load.
	Preset string `yaml:"preset"`
	// Allow re-includes preset-excluded tools; ignored under preset=all.
	Allow []string `yaml:"allow"`
	// Deny removes tools regardless of preset/allow — absent from the catalog
	// AND rejected fail-closed at dispatch. Deny wins over allow.
	Deny []string `yaml:"deny"`
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
	// compatible providers — the only ones with a BaseURL): non-empty = ONLY
	// the pinned key accepted; empty = system CA verification. See ADR-054.
	TLSFingerprint string
	// OutputTokenReserve is the per-provider output-token budget override
	// (0 = inherit the global executor.output_token_reserve).
	OutputTokenReserve int
	// AutoRetrySeconds is the per-provider auto-retry timer knob for
	// compatible providers (0 = disabled); fixed providers always report 0.
	AutoRetrySeconds int
	// TimeoutClass is the adaptive budget's class override for compatible
	// providers (ADR-071 D3); empty = infer; fixed providers always report "".
	TimeoutClass string
}

// providerEntry is the canonical, single-source-of-truth provider list.
type providerEntry struct {
	name               string
	apiKey             string
	baseURL            string
	models             []string
	tlsFingerprint     string
	outputTokenReserve int
	autoRetrySeconds   int
	timeoutClass       string
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
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, tlsFingerprint: cfg.TLSFingerprint, outputTokenReserve: cfg.OutputTokenReserve, autoRetrySeconds: cfg.AutoRetrySeconds, timeoutClass: cfg.TimeoutClass})
	}
	for _, name := range anthropicKeys {
		cfg := c.AnthropicCompatible[name]
		entries = append(entries, providerEntry{name: name, apiKey: cfg.APIKey, baseURL: cfg.BaseURL, models: cfg.Models, tlsFingerprint: cfg.TLSFingerprint, outputTokenReserve: cfg.OutputTokenReserve, autoRetrySeconds: cfg.AutoRetrySeconds, timeoutClass: cfg.TimeoutClass})
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
// models enabled yet; callers needing enabled models filter by
// len(Models) > 0 at the usage site (e.g. the LLM router).
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
			AutoRetrySeconds:   p.autoRetrySeconds,
			TimeoutClass:       p.timeoutClass,
		})
	}
	return result
}

// ResolveDefaultModelProvider looks up the provider owning DefaultModel —
// a bare model name (first matching provider) or composite "provider/model"
// (the named provider). Returns the provider config and bare model name, or
// an error when DefaultModel is empty or not found in any Models list.
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

// Load reads, defaults, validates and returns the configuration; ${VAR}
// references are preserved as-is (resolve at runtime via ExpandEnvVars). For
// full error reporting and migration support use LoadWithResult.
func Load(path string) (*Config, error) {
	result, err := LoadWithResult(path)
	if err != nil {
		return nil, err
	}
	return result.Config, nil
}

// LoadWithResult reads a configuration file with full error reporting;
// ${VAR} references are preserved as-is and resolved at runtime via
// ExpandEnvVars() when actually needed.
func LoadWithResult(path string) (*LoadResult, error) {
	// Read file
	data, err := safeio.ReadFile(path)
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
	// (blacklist_migration.go): a customized legacy list is carried over, a
	// default-equal or absent one dropped. Before ApplyDefaults and validate.
	migrateLegacyGroupBlacklists(cfg.Security.Groups)

	// One-time migration from the pre-enum autonomy booleans onto
	// security.autonomy_mode. Before ApplyDefaults: only an empty
	// AutonomyMode here means "pre-enum config", so the legacy keys are
	// honored; unknown enum values fail safe to standard — all surfaced as
	// load warnings.
	autonomyWarnings := migrateLegacyAutonomyMode(&cfg.Security)

	// Apply defaults for zero-value fields
	ApplyDefaults(&cfg)

	// Reconcile the backend-owned `embedded` provider record with the
	// authoritative embedded_llm state (generated while installed, removed
	// otherwise). Pure in-memory; after ApplyDefaults and before validate so
	// the generated provider joins default_model resolution. The
	// context_window override is deliberately NOT recomputed here — the
	// resolved RAM tier is written by the install/supervision paths.
	cfg.SyncEmbeddedLLMProvider(0)

	// Validate the shell_exec override in place (fail-soft: warn + reset to
	// the built-in launch shape). After ApplyDefaults (seeds the platform
	// shell kind), before validate — the persisted shape is always valid.
	shellExecWarnings := normalizeShellExec(&cfg)

	// Validate the web_access section in place (fail-soft: invalid enabled
	// state warned + reset to the safe default; a valid non-loopback bind
	// stays enabled with an exposure warning). See web_access.go / ADR-101.
	webAccessWarnings := normalizeWebAccess(&cfg)

	// Validate the MCP per-server timeout durations (fail-soft: warned and
	// left untouched — the config→builder adapter resolves them at build
	// time). Surfacing here is what makes the ADR-063 "logged warning" reach
	// the user on the production load path (the UI's configLoadErrors
	// channel; frontend rebuild paths run the adapter without a logger).
	mcpWarnings := normalizeMCPTimeouts(&cfg)

	// Fold the legacy per-server MCP activation key into capabilities.mcp and
	// erase it (dead state after load; the next save drops the key). Fail-soft
	// to "auto"; same pipeline position/surfacing as normalizeMCPTimeouts.
	mcpModeWarnings := migrateLegacyMCPServerModes(&cfg)

	// Canonicalize the capabilities sections (fail-soft: unrecognized
	// chat_mode/code_mode warned + reset to "auto"), dropping stale
	// capabilities.mcp names and both-empty entries. After the legacy
	// migration, whose folded values are already canonical.
	capWarnings := normalizeCapabilities(&cfg)

	// Warnings collected before validation, in a deterministic order.
	warnings := autonomyWarnings
	warnings = append(warnings, shellExecWarnings...)
	warnings = append(warnings, webAccessWarnings...)
	warnings = append(warnings, mcpWarnings...)
	warnings = append(warnings, mcpModeWarnings...)
	warnings = append(warnings, capWarnings...)

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return &LoadResult{
			Config:     &cfg,
			LoadErrors: append(warnings, "Config validation failed: "+err.Error()),
		}, fmt.Errorf("config validation failed: %w", err)
	}

	return &LoadResult{Config: &cfg, LoadErrors: warnings}, nil
}

// Save writes the configuration to a YAML file atomically.
func Save(cfg *Config, path string) error {
	// Fail-closed web_access gate: refuse to persist an enabled section with
	// an invalid bind or out-of-range port. The load path heals an invalid
	// state, so hitting this means an in-memory mutation tried to persist a
	// section the sidecar must never run with. See web_access.go / ADR-101.
	if err := validateWebAccessForSave(cfg); err != nil {
		return fmt.Errorf("refusing to save config: %w", err)
	}
	// Marshal as-is: the execute blocklist is empty by default and purely
	// user-authored — what is stored is what is written.
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically via safeio: a randomized staging name (no plantable
	// fixed "<path>.tmp" — a planted FIFO blocked write-open forever, a
	// planted symlink was followed), and the final rename replaces a planted
	// symlink at path instead of writing through it. The mode is an
	// unconditional 0o600, deliberately NOT the historical 0o644:
	// WriteFileAtomic fchmods the staging file exactly (no umask filtering),
	// so a literal 0o644 would silently publish the API-key-bearing file
	// world-readable regardless of the ambient umask.
	if err := safeio.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

// validate checks that the configuration is valid.
func validate(cfg *Config) error {
	if cfg.Git.WorktreesRoot != "" && (!filepath.IsAbs(cfg.Git.WorktreesRoot) || filepath.Clean(cfg.Git.WorktreesRoot) != cfg.Git.WorktreesRoot) {
		return errors.New("git.worktrees_root must be an absolute clean path")
	}
	// Validate search.base_url for the searxng provider (raw value — an
	// ${ENV_VAR} placeholder passes and resolves at runtime; the expanded
	// value is re-checked fail-closed at consumption by
	// core/tools.CreateSearchProviderWithClient, so garbage still surfaces an
	// explicit error instead of a silently missing web_search tool).
	if err := ValidateSearchBaseURL(cfg.Search.Provider, cfg.Search.BaseURL); err != nil {
		return err
	}
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

	// Validate the chatgpt auth-mode enum: exactly "api_key" | "oauth"
	// (empty reads as the default "api_key" for configs that bypassed
	// ApplyDefaults). An unknown value must fail the load: the mode decides
	// whether requests carry subscription credentials or a static key, and a
	// typo must not leave the operator believing the opposite of what runs.
	switch cfg.LLM.ChatGPT.Auth.Mode {
	case "", ChatGPTAuthModeAPIKey, ChatGPTAuthModeOAuth:
	default:
		return fmt.Errorf(
			"llm.chatgpt.auth.mode %q is not valid; must be one of: %s, %s",
			cfg.LLM.ChatGPT.Auth.Mode, ChatGPTAuthModeAPIKey, ChatGPTAuthModeOAuth,
		)
	}

	// Validate per-provider auto_retry_seconds (ADR-065): [0, 3600] seconds;
	// 0 disables. Out-of-range values would arm time.AfterFunc on an unusable
	// window (years) while the banner still promises an auto-resend. The
	// Settings UI clamps to the same range; YAML/RPC edits go through this
	// check instead of trusting the clamp.
	for _, p := range cfg.LLM.allProviderEntries() {
		if p.autoRetrySeconds < 0 || p.autoRetrySeconds > maxAutoRetrySeconds {
			return fmt.Errorf(
				"llm provider %q auto_retry_seconds must be within [0, %d], got %d",
				p.name, maxAutoRetrySeconds, p.autoRetrySeconds,
			)
		}
		// Full timeout_class rules (ADR-071 D3) — see
		// ValidateProviderTimeoutClass, the single gate shared with the
		// UpdateLLMConfig trust boundary.
		if err := ValidateProviderTimeoutClass(p.name, p.timeoutClass); err != nil {
			return err
		}
	}

	// Range-check the llm.models context_window overrides: 0 = inherit
	// (legal), anything else within 1..MaxModelContextWindow — a TIER-1 value
	// that shadows the lazy probe and silently disables compaction when
	// absurd (see MaxModelContextWindow). Keys are sorted so a config with
	// several bad overrides reports the same one every run.
	modelNames := make([]string, 0, len(cfg.LLM.Models))
	for name := range cfg.LLM.Models {
		modelNames = append(modelNames, name)
	}
	sort.Strings(modelNames)
	for _, name := range modelNames {
		window := cfg.LLM.Models[name].ContextWindow
		if window == 0 {
			continue
		}
		if window < 0 || window > MaxModelContextWindow {
			return fmt.Errorf(
				"llm.models.%q.context_window %d is not valid; must be within 1-%d tokens, or 0/unset to inherit the built-in metadata",
				name, window, MaxModelContextWindow,
			)
		}
	}

	// Validate the per-model request_timeout override (ADR-071 D5): [0, 3600]
	// seconds; 0 = "no opinion", positive armed EXACTLY (never escalated —
	// an absurd value would silently disable the stalled-upstream protection).
	// Separate loop from the context_window check: a model without a window
	// override still gets its deadline validated.
	for _, name := range modelNames {
		timeout := cfg.LLM.Models[name].RequestTimeout
		if timeout < 0 || timeout > maxAutoRetrySeconds {
			return fmt.Errorf(
				"llm.models.%q.request_timeout %d is not valid; must be within [0, %d] seconds, or 0/unset for the adaptive budget",
				name, timeout, maxAutoRetrySeconds,
			)
		}
	}

	// Validate the security.groups schema: only the fixed configurable groups,
	// never the reserved "system" group, group-enum policies only, and the
	// blocklist is an execute-only feature.
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

	// Validate security.silent_mode sub-policy enums: unset is valid here
	// (ApplyDefaults seeded the defaults); explicit values must come from
	// the sub-policy's enum.
	if err := ValidateSilentMode(cfg.Security.SilentMode); err != nil {
		return err
	}

	// Validate vector_index.content_filter thresholds: non-negative values,
	// whitespace ratio within [0, 1] — a mis-typed threshold would silently
	// disable a detector heuristic, so it fails fast at load.
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
	// security.harden_git_repos. Both hold absolute repository roots
	// (compared literally, after Clean, against scanned work-tree roots — a
	// relative entry could never match and is rejected instead of staying
	// dead config); cleaned in place, duplicates rejected, and the two lists
	// are mutually exclusive across the SAME trust-key forms the runtime
	// matcher probes (workspace.TrustProbePaths) — best effort where paths do
	// not resolve on this machine, so the same repository listed trusted
	// under one key form and hardened under another is rejected exactly like
	// the literal collision instead of letting trust win repo-wide at
	// runtime; the runtime transitions enforce the same exclusion across
	// every key form once the repositories exist.
	seenTrusted := make(map[string]struct{}, len(cfg.Security.TrustedGitRepos))
	trustedForms := make(map[string]struct{}, 2*len(cfg.Security.TrustedGitRepos))
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
		if err := validateGitSemanticFingerprint(repo.SemanticFingerprint); err != nil {
			return fmt.Errorf("security.trusted_git_repos entry %q: %w", cleaned, err)
		}
		if _, dup := seenTrusted[cleaned]; dup {
			return fmt.Errorf("security.trusted_git_repos contains duplicate path %q", cleaned)
		}
		seenTrusted[cleaned] = struct{}{}
		for _, form := range workspace.TrustProbePaths(cleaned) {
			trustedForms[form] = struct{}{}
		}
		cleanTrusted = append(cleanTrusted, TrustedGitRepo{Path: cleaned, Fingerprint: repo.Fingerprint, SemanticFingerprint: repo.SemanticFingerprint})
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
		for _, form := range workspace.TrustProbePaths(cleaned) {
			if _, conflict := trustedForms[form]; conflict {
				return fmt.Errorf("repository %q cannot be both trusted and hardened", cleaned)
			}
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

	// Validate goal_loop.suggest bounds: the consult floor is a complexity
	// band value ([1,5]); negative/absurd values are config mistakes.
	if cfg.GoalLoop.Suggest.MinComplexity < 0 || cfg.GoalLoop.Suggest.MinComplexity > 5 {
		return fmt.Errorf(
			"goal_loop.suggest.min_complexity %d is not valid; must be within [0, 5] (0 = use the default)",
			cfg.GoalLoop.Suggest.MinComplexity,
		)
	}

	// Validate the E2S section: explicit numerics must be non-negative (only
	// hand-written YAML can go below the seeded defaults), and the anti-spin
	// thresholds keep their ordering — at least one corrective nudge before
	// the abort. Fail fast at load rather than misbehaving mid-run.
	for name, v := range map[string]int{
		"max_steps":                 cfg.E2S.MaxSteps,
		"state_byte_limit":          cfg.E2S.StateByteLimit,
		"patch_retries":             cfg.E2S.PatchRetries,
		"observation_truncate":      cfg.E2S.ObservationTruncate,
		"observation_budget_tokens": cfg.E2S.ObservationBudgetTokens,
		"repeat_nudge_threshold":    cfg.E2S.RepeatNudgeThreshold,
		"repeat_abort_threshold":    cfg.E2S.RepeatAbortThreshold,
	} {
		if v < 0 {
			return fmt.Errorf("e2s.%s must be >= 0, got %d", name, v)
		}
	}
	if cfg.E2S.ObservationFillFraction < 0 {
		return fmt.Errorf("e2s.observation_fill_fraction must be >= 0, got %v", cfg.E2S.ObservationFillFraction)
	}
	if cfg.E2S.RepeatNudgeThreshold > cfg.E2S.RepeatAbortThreshold {
		return fmt.Errorf(
			"e2s.repeat_nudge_threshold (%d) must be <= e2s.repeat_abort_threshold (%d) so a nudge always precedes the abort",
			cfg.E2S.RepeatNudgeThreshold, cfg.E2S.RepeatAbortThreshold,
		)
	}
	// Validate the E2S tool-catalog narrowing: an unknown preset is a typo,
	// not a sentinel — rejected at load (the core filter fails closed to
	// "core" for anything but "all", so a silent fallback would hide the
	// mistake). allow/deny entries must name tools.
	switch cfg.E2S.Tools.Preset {
	case E2SToolsPresetCore, E2SToolsPresetAll:
		// valid
	default:
		return fmt.Errorf(
			"e2s.tools.preset %q is not valid; must be one of: %s, %s",
			cfg.E2S.Tools.Preset, E2SToolsPresetCore, E2SToolsPresetAll,
		)
	}
	for _, name := range cfg.E2S.Tools.Allow {
		if strings.TrimSpace(name) == "" {
			return errors.New("e2s.tools.allow entries must be non-empty tool names")
		}
	}
	for _, name := range cfg.E2S.Tools.Deny {
		if strings.TrimSpace(name) == "" {
			return errors.New("e2s.tools.deny entries must be non-empty tool names")
		}
	}

	// Validate vector_index.execution_provider enum: ApplyDefaults has
	// already normalized empty to "auto", so anything else is user-authored.
	switch cfg.VectorIndex.ExecutionProvider {
	case VectorIndexProviderAuto, VectorIndexProviderCPU, VectorIndexProviderCUDA, VectorIndexProviderOpenVINO:
		// valid
	default:
		return fmt.Errorf(
			"vector_index.execution_provider %q is not valid; must be one of: %s, %s, %s, %s",
			cfg.VectorIndex.ExecutionProvider,
			VectorIndexProviderAuto, VectorIndexProviderCPU, VectorIndexProviderCUDA, VectorIndexProviderOpenVINO,
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

	// Validate vector_index.park_budget_mb (unset already resolved to the
	// 1024 MiB default, so an explicit 0 is hand-authored and ambiguous —
	// read literally it evicts every parked state the moment it parks — and
	// rejected): use a positive budget or -1 to disable the byte budget.
	// Only -1 is a legal negative (mirroring runtime.memory_soft_limit_mb);
	// anything below is a typo and fails fast.
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

	// Validate runtime.memory_soft_limit_mb: tri-state 0/unset = auto,
	// >0 = explicit MiB, -1 = off; only -1 is a legal negative (anything
	// below is a typo, not a sentinel). The upper bound guards the consumer's
	// int64 MiB→bytes shift (desktop/memlimit.go): it overflows to a negative,
	// and a negative SetMemoryLimit is ignored, silently dropping the limit.
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

	// Validate the embedded_llm section (port range / install consistency and
	// the idle-unload budget).
	if err := validateEmbeddedLLM(&cfg.EmbeddedLLM); err != nil {
		return err
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
