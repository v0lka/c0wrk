// Package core provides the orchestration engine that routes, plans, executes, evaluates, and reflects on agent tasks.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/skills"
	"github.com/v0lka/c0wrk/sdk/strutil"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
)

type planModeKeyType struct{}

// PlanModeKey is the context key for signaling plan-execute mode to buildSystemPrompt.
var PlanModeKey = planModeKeyType{}

// DefaultAgentsMDMaxBytes is the default cap on AGENTS.md content injected into
// prompts. AGENTS.md is treated as untrusted, user-controlled input; an
// unbounded read would let a workspace inject arbitrarily large content into
// every system prompt.
const DefaultAgentsMDMaxBytes = 65536

// agentsMDCacheEntry holds a cached read of AGENTS.md from a workspace.
type agentsMDCacheEntry struct {
	content string
	modTime time.Time
	err     error
}

type injectionDefenseKeyType struct{}

// InjectionDefenseKey is the context key for signaling injection defense is enabled.
// When set to a non-nil bool pointer in the context, buildSystemPrompt reads it to decide
// whether to include the injection defense prompt text.
var InjectionDefenseKey = injectionDefenseKeyType{}

// OrchestratorConfig holds configuration for the Orchestrator.
type OrchestratorConfig struct {
	MaxSteps                  int
	SubagentMaxSteps          int    // max ReAct iterations per delegation (default: 50)
	MaxRedelegationDepth      int    // cap on recursive delegation when allow_redelegate is true (default: 2)
	KeepFirst                 int    // for sliding window compaction
	KeepLast                  int    // for sliding window compaction
	MaxDependencyContextChars int    // max chars for dependency context in delegation tasks (default: 8000)
	Model                     string // active model name for ModelRegistry.Resolve()

	// ReasoningEffort is the reasoning effort applied to step executors.
	// When non-empty, each executor gets this value directly (no role adaptation).
	ReasoningEffort string

	// HITLHandler is called when an executor reaches its step limit.
	// If nil, a default handler is used (deny all extensions).
	HITLHandler agent.HITLHandler

	// PreWarningPercent is the context fill % that triggers the pre-compaction
	// store_fact nudge. When fill reaches this threshold (but below predictive),
	// a warning listing vulnerable tool outputs is appended to the observation.
	PreWarningPercent int

	// InjectionDefenseEnabled gates the prompt injection defense prompt text
	// and the <untrusted-content> wrapping of tool outputs in the context window.
	InjectionDefenseEnabled bool

	// AgentsMDMaxBytes caps the AGENTS.md content size injected into prompts.
	// 0 means use the default (DefaultAgentsMDMaxBytes = 65536).
	// A negative value disables the cap entirely.
	AgentsMDMaxBytes int

	// ConductorHistoryWindow is the maximum number of recent conversation
	// messages injected into the Conductor's context. 0 = use default (20).
	// Keeps the prompt bounded for long sessions while preserving enough
	// dialogue context for the agent to understand follow-up references.
	ConductorHistoryWindow int
}

// ContextManagerFactory creates a ContextManager for a new task.
// The compactionStrategy parameter allows selecting the appropriate strategy.
// pruningOverrides, when provided, override the global pruning configuration.
// This allows the Orchestrator to remain decoupled from the memory package.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...orchestration.PruningOverride) ContextManager

// BlackboardFactory creates a Blackboard for a new task.
// The taskID is a unique identifier for the orchestration request.
type BlackboardFactory func(taskID string) orchestration.Blackboard

// Orchestrator coordinates the agent's reasoning cycle.
// It handles c0wrk-specific concerns (routing, PersistentBlackboard,
// conversation history) and delegates the Plan&Execute loop to the SDK engine.
//
// Lifecycle: One Orchestrator is created per active session via Builder.Build().
// It lives for the duration of the session and is discarded when the session ends.
//
// Concurrency: Only one HandleMessage call may be active at a time per Orchestrator
// instance. The session manager enforces this contract — concurrent requests to the
// same session are rejected before reaching HandleMessage.
type Orchestrator struct {
	router              *router.Router
	llm                 agent.LLMCaller
	modelSwitcher       *llm.Router // raw LLM router for per-message model override
	toolRegistry        *sdktools.ToolRegistry
	toolExec            agent.ToolExecutor  // executor tool surface (per-session policy view)
	coreToolRegistry    *tools.ToolRegistry // core registry with policy support
	config              OrchestratorConfig
	contextFactory      ContextManagerFactory
	logger              *slog.Logger
	emitter             Emitter
	modelRegistry       *llm.ModelRegistry
	bbFactory           BlackboardFactory
	conversationHistory []llm.Message
	taskStore           TaskPersistence       // optional, for ContinueTask blackboard restoration
	bbRestoreFunc       BlackboardRestoreFunc // optional, restores PersistableBlackboard from store
	trackingCaller      *llm.TrackingCaller   // for per-step context tracker wiring
	tokenCounter        llm.TokenCounter      // for token counting in planner history compaction
	vectorSearchFunc    builtins.VectorSearchFunc
	skillManager        *skills.SkillManager // for skill discovery and activation

	// Conductor dependencies (stored at construction time for runConductor).
	reflector        *reflector.Reflector
	providerName     string
	stepDumpTracker  *orchestration.StepDumpTracker
	toolCache        *agent.ToolResultCache
	perToolTrunc     map[string]agent.ToolTruncationConfig
	toolResultBudget agent.ToolResultBudget
	circuitBreaker   agent.CircuitBreakerConfig

	// isNoProject is set to true when this orchestrator runs inside the
	// "No Project" pseudo-project. When true, the routing domain is
	// overridden from "code" to "general" so that code-oriented planning
	// and execution strategies are not applied.
	isNoProject bool

	// requestInFlight enforces the documented "one active request per
	// *Orchestrator" invariant. HandleMessage CompareAndSwap's it to true on
	// entry; a concurrent caller observes false->true refused and gets
	// ErrRequestInFlight back. The defer in HandleMessage stores false again.
	requestInFlight atomic.Bool

	// agentsMDCache caches AGENTS.md content per workspace path with mtime
	// check to avoid repeated disk reads on every request. Only accessed from
	// injectVectorSearchHints called from HandleMessage, which is
	// single-flight per instance.
	agentsMDCache map[string]agentsMDCacheEntry
}

// ErrRequestInFlight is returned by HandleMessage when another HandleMessage
// call on the same *Orchestrator is already running. The orchestrator is
// designed for one concurrent request per instance — the session layer is
// responsible for serializing per-session.
var ErrRequestInFlight = errors.New("orchestrator: request already in flight")

// OrchestratorDeps holds the runtime dependencies for the Orchestrator.
// Grouping them into a single struct improves readability when the constructor
// is called (one struct literal instead of 19 positional arguments).
type OrchestratorDeps struct {
	Router           *router.Router
	LLM              agent.LLMCaller
	ToolExec         agent.ToolExecutor
	ToolRegistry     *sdktools.ToolRegistry
	TokenCounter     llm.TokenCounter
	ContextFactory   ContextManagerFactory
	Reflector        *reflector.Reflector // optional, nil-safe
	Logger           *slog.Logger         // optional, nil-safe
	Emitter          Emitter              // optional, uses noopEmitter if nil
	ModelRegistry    *llm.ModelRegistry   // optional, nil-safe
	ToolResultBudget agent.ToolResultBudget
	CircuitBreaker   agent.CircuitBreakerConfig
	BBFactory        BlackboardFactory         // optional, nil = default MapBlackboard
	TrackingCaller   *llm.TrackingCaller       // optional, for per-step context tracker wiring
	VectorSearchFunc builtins.VectorSearchFunc // optional, for auto-RAG hint generation
	SkillManager     *skills.SkillManager      // optional, for skill discovery and activation
	CoreToolRegistry *tools.ToolRegistry       // core tool registry for skill policy overrides
	ModelSwitcher    *llm.Router               // raw LLM router for per-message model override

	// Tool result caching and per-tool truncation.
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig

	// StepDumpTracker manages per-step LLM dump files. Created by the session layer.
	// If nil, per-step dumps are disabled.
	StepDumpTracker *orchestration.StepDumpTracker

	// ProviderName is the active provider name for DEBUG-level LLM logging
	// in per-step callers (LoggingLLMCaller wrapping).
	ProviderName string
}

// NewOrchestrator creates a new Orchestrator with all components.
// reflector, logger, and emitter are optional (nil-safe).
func NewOrchestrator(cfg OrchestratorConfig, deps OrchestratorDeps) *Orchestrator {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 80
	}
	if cfg.SubagentMaxSteps == 0 {
		cfg.SubagentMaxSteps = 50
	}
	if cfg.MaxRedelegationDepth == 0 {
		cfg.MaxRedelegationDepth = 2
	}
	if cfg.KeepFirst == 0 {
		cfg.KeepFirst = 3
	}
	if cfg.KeepLast == 0 {
		cfg.KeepLast = 10
	}
	if cfg.MaxDependencyContextChars == 0 {
		cfg.MaxDependencyContextChars = 8000
	}
	if cfg.ConductorHistoryWindow == 0 {
		cfg.ConductorHistoryWindow = 20
	}
	emitter := deps.Emitter
	if emitter == nil {
		emitter = &noopEmitter{}
	}

	o := &Orchestrator{
		router:           deps.Router,
		llm:              deps.LLM,
		modelSwitcher:    deps.ModelSwitcher,
		toolRegistry:     deps.ToolRegistry,
		toolExec:         deps.ToolExec,
		config:           cfg,
		contextFactory:   deps.ContextFactory,
		logger:           deps.Logger,
		emitter:          emitter,
		modelRegistry:    deps.ModelRegistry,
		bbFactory:        deps.BBFactory,
		trackingCaller:   deps.TrackingCaller,
		tokenCounter:     deps.TokenCounter,
		vectorSearchFunc: deps.VectorSearchFunc,
		skillManager:     deps.SkillManager,
		coreToolRegistry: deps.CoreToolRegistry,
		reflector:        deps.Reflector,
		providerName:     deps.ProviderName,
		stepDumpTracker:  deps.StepDumpTracker,
		toolCache:        deps.ToolCache,
		perToolTrunc:     deps.PerToolTruncation,
		toolResultBudget: deps.ToolResultBudget,
		circuitBreaker:   deps.CircuitBreaker,
	}

	return o
}

// Cleanup releases all resources held by the orchestrator (e.g., per-step dump files).
// Idempotent — safe to call multiple times.
func (o *Orchestrator) Cleanup() {
	// StepDumpTracker cleanup is owned by the session layer; the orchestrator
	// only holds a reference for per-step dump wiring. No-op here.
}

// logInfo logs an INFO level message if logger is not nil.
func (o *Orchestrator) logInfo(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Info(msg, args...)
	}
}

// mergeSkillNames combines router-matched skill names with user-specified skill names,
// deduplicating by name. Router-matched names come first, user names add any extras.
func mergeSkillNames(routerMatched, userSpecified []string) []string {
	if len(routerMatched) == 0 && len(userSpecified) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(routerMatched)+len(userSpecified))
	merged := make([]string, 0, len(routerMatched)+len(userSpecified))
	for _, name := range routerMatched {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	for _, name := range userSpecified {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	return merged
}

// buildSkillAugmentedRoutingMessage constructs a routing-specific message that
// restores the skill invocation context stripped by preprocessMessageText.
// When the user types "/vibespec-check entire codebase", the preprocessor
// strips the /skill ref, leaving "entire codebase". The router would classify
// this without context, producing unreliable domain/complexity. This method
// prepends the skill invocation with its description so the router can
// classify based on the actual task semantics.
func (o *Orchestrator) buildSkillAugmentedRoutingMessage(message string, userSkills []string) string {
	parts := make([]string, 0, len(userSkills))
	for _, name := range userSkills {
		if s, ok := o.skillManager.Get(name); ok {
			parts = append(parts, fmt.Sprintf("/%s (skill: %s)", name, s.Metadata.Description))
		} else {
			parts = append(parts, "/"+name)
		}
	}

	// Reconstruct: "/vibespec-check (skill: ...) entire codebase"
	prefix := strings.Join(parts, " ")

	if message == "" {
		return prefix
	}
	return prefix + " " + message
}

// logDebug logs a DEBUG level message if logger is not nil.
func (o *Orchestrator) logDebug(msg string, args ...any) {
	if o.logger != nil {
		o.logger.Debug(msg, args...)
	}
}

// Resume continues execution of a previously interrupted task from its checkpoint state.
// The blackboard must be pre-loaded with the task's persisted state (via RestoreBlackboard).
//
// Error semantics: when the SDK engine returns orchestration.ErrExecutionIncomplete with
// a non-nil ExecutionResult, Resume returns a valid *HandleResult alongside the error.
// This indicates partial success — the task made progress but did not finish (e.g., step
// limit reached). Callers should check errors.Is(err, orchestration.ErrExecutionIncomplete)
// and use the returned HandleResult for partial output, plan state, and blackboard.
// All other errors indicate complete failure; the task is marked failed and nil is returned.
func (o *Orchestrator) Resume(ctx context.Context, bb orchestration.Blackboard, routing *router.RoutingDecision, plansDir string) (result *HandleResult, err error) {
	o.logDebug("orchestrator: resume started")

	// Record the resumed execution's outcome in the in-memory conversation
	// history so future routing and continuation planning see this exchange.
	// The user message that spawned the task was recorded when the task first
	// ran (or restored from the message store after a restart).
	defer func() {
		o.recordResumeOutcome(ctx, bb, result, err)
	}()

	// Generate RAG hints from vector index using the original request.
	if origReq := bb.GetOriginalRequest(); origReq != "" {
		ctx = o.injectVectorSearchHints(ctx, origReq)
	}

	// Wire emitter into restored PersistentBlackboard so persistence warnings
	// are surfaced to the user (the backend creates the BB without an emitter).
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetEmitter(o.emitter)
	}

	// Emit initial context_fill
	o.emitInitialContextFill(ctx)

	// Emit routing decision so the frontend can display the resumed context.
	if routing != nil {
		o.emitter.Routing("conductor", routing.Domain, strconv.Itoa(routing.Complexity))
	}

	o.logInfo("resume_task", "reflections", len(bb.GetReflections()))

	// Delegate to the Conductor. The restored blackboard carries facts and
	// step results from the prior run; the Conductor reads them via tools
	// (search_facts, read_step_output) and continues toward completion.
	availableTools := o.toolRegistry.List()
	if o.coreToolRegistry != nil {
		availableTools = o.toolRegistry.ListFiltered(o.coreToolRegistry.DisabledTools())
	}
	execResult, err := o.runConductor(ctx, bb.GetOriginalRequest(), bb, availableTools, plansDir, nil)
	var incompleteErr error
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		if pbb, ok := bb.(PersistableBlackboard); ok {
			pbb.FailTask()
		}
		return nil, err
	}
	incompleteErr = err
	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	result = &HandleResult{
		Output:          execResult.Output,
		RoutingDecision: routing,
		Plan:            execResult.Plan,
		Blackboard:      execResult.Blackboard,
		Reflections:     execResult.Reflections,
		Status:          execResult.Status,
	}

	// Persist the task outcome according to the typed execution status.
	// Partial executions stay resumable; see persistTaskOutcome.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		persistTaskOutcome(pbb, execResult)
	}

	o.logDebug("orchestrator: resume completed")
	// Propagate ErrExecutionIncomplete alongside the best-effort result, as
	// documented: callers must errors.Is-check and still use the result.
	return result, incompleteErr
}

// lastReasoningContent extracts the last non-empty reasoning content from the
// blackboard's step results. This is needed so that assistant messages echoed
// back to reasoning models (e.g. DeepSeek) include the reasoning_content field
// those models require.
func lastReasoningContent(bb orchestration.Blackboard) string {
	var last string
	for _, sr := range bb.GetAllStepResults() {
		for _, step := range sr.Steps {
			if step.ReasoningContent != "" {
				last = step.ReasoningContent
			}
		}
	}
	return last
}

// buildSkillPolicyOverrides creates tool policy overrides from active skills.
// Tools listed in a skill's allowed-tools field get PolicyAlwaysAllow,
// enabling the agent to use them without manual confirmation.
func (o *Orchestrator) buildSkillPolicyOverrides(activeSkills []*skills.Skill) map[string]sdktools.ToolPolicy {
	overrides := make(map[string]sdktools.ToolPolicy)
	for _, s := range activeSkills {
		for _, toolName := range s.Metadata.AllowedToolList() {
			// Only set if not already overridden by config
			overrides[toolName] = sdktools.PolicyAlwaysAllow
		}
	}
	return overrides
}

// injectVectorSearchHints queries the vector index for relevant files and
// attaches the results as VectorSearchHints on the returned context.
// It also reads AGENTS.md from the workspace root and injects its full
// content via WithAgentsMD, as well as prepending it as the first hint.
// If the vector search function is nil or the search fails, hints are
// still injected if AGENTS.md exists. Hints are a nice-to-have, not critical.
func (o *Orchestrator) injectVectorSearchHints(ctx context.Context, query string) context.Context {
	var hints *VectorSearchHints

	// Vector search (optional, non-blocking). Skipped entirely for No
	// Project (CHAT mode), where the vector index is disabled: no
	// collection is built, so querying would only yield stale results from
	// a previously-active CODE project. AGENTS.md injection below is
	// unaffected (it is workspace-local, not vector-index-dependent).
	if !o.isNoProject && o.vectorSearchFunc != nil {
		ragCtx, ragCancel := context.WithTimeout(ctx, 2*time.Second)
		results, err := o.vectorSearchFunc(ragCtx, builtins.VectorSearchOptions{Query: query, TopK: 5})
		ragCancel()

		if err != nil {
			o.logDebug("vector search hints skipped", "error", err)
		} else if len(results) > 0 {
			hints = &VectorSearchHints{}
			for _, r := range results {
				summary := strutil.TruncateUTF8(r.Content, 100)
				hints.Files = append(hints.Files, VectorSearchHint{
					FilePath: r.FilePath,
					Summary:  summary,
				})
			}
			o.logDebug("vector search hints injected", "count", len(hints.Files))
		}
	}

	// Always try to read AGENTS.md from workspace root (cached per workspace with mtime check).
	if wsPath := sdktools.WorkspacePathFrom(ctx); wsPath != "" {
		agentsMDPath := filepath.Join(wsPath, "AGENTS.md")
		contentStr, err := o.readAgentsMD(agentsMDPath)
		if err != nil {
			o.logDebug("AGENTS.md not found in workspace", "path", agentsMDPath)
		} else {
			ctx = WithAgentsMD(ctx, &AgentsMD{Content: contentStr})
			o.logDebug("AGENTS.md found and injected", "path", agentsMDPath, "size", len(contentStr))

			// Prepend AGENTS.md as the first hint so it always appears in
			// the "Relevant Project Files" section for executors.
			if hints == nil {
				hints = &VectorSearchHints{}
			}
			summary := strutil.TruncateUTF8(contentStr, 100)
			agentsHint := VectorSearchHint{
				FilePath: "AGENTS.md",
				Summary:  summary,
			}
			hints.Files = append([]VectorSearchHint{agentsHint}, hints.Files...)
		}
	}

	if hints != nil && len(hints.Files) > 0 {
		ctx = WithVectorSearchHints(ctx, hints)
	}

	return ctx
}

// readAgentsMD reads AGENTS.md from disk with a per-workspace-path cache that
// invalidates on mtime change. This avoids repeated disk reads on every
// HandleMessage call while still picking up edits. Write-through: every cache
// miss re-reads the file and updates modTime.
func (o *Orchestrator) readAgentsMD(path string) (string, error) {
	if o.agentsMDCache == nil {
		o.agentsMDCache = make(map[string]agentsMDCacheEntry)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		// File doesn't exist or is inaccessible — evict stale positive cache.
		delete(o.agentsMDCache, path)
		return "", statErr
	}

	if cached, ok := o.agentsMDCache[path]; ok && cached.modTime.Equal(info.ModTime()) {
		if cached.err != nil {
			return "", cached.err
		}
		return cached.content, nil
	}

	content, readErr := os.ReadFile(path)
	if readErr != nil {
		o.agentsMDCache[path] = agentsMDCacheEntry{err: readErr, modTime: info.ModTime()}
		return "", readErr
	}

	// Apply truncation cap (same logic as before).
	originalSize := len(content)
	contentStr := string(content)
	maxBytes := o.config.AgentsMDMaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultAgentsMDMaxBytes
	}
	if maxBytes > 0 && originalSize > maxBytes {
		trimmed := contentStr[:maxBytes]
		if idx := strings.LastIndex(trimmed, "\n"); idx > 0 {
			trimmed = trimmed[:idx]
		}
		contentStr = trimmed + fmt.Sprintf("\n\n[…AGENTS.md truncated at %d bytes; original was %d bytes]", maxBytes, originalSize)
		o.logDebug("AGENTS.md truncated", "path", path, "originalSize", originalSize, "cap", maxBytes)
	}

	o.agentsMDCache[path] = agentsMDCacheEntry{content: contentStr, modTime: info.ModTime()}
	return contentStr, nil
}

// emitInitialContextFill emits a 0% context_fill so the frontend has a baseline.
func (o *Orchestrator) emitInitialContextFill(ctx context.Context) {
	var effectiveMax int
	if o.modelRegistry != nil {
		model := o.config.Model
		meta, _ := o.modelRegistry.Resolve(ctx, model)
		if meta.ContextWindow > 0 {
			safetyMargin := meta.ContextWindow * 5 / 100
			effectiveMax = meta.ContextWindow - meta.OutputLimit - safetyMargin
		}
	}
	o.emitter.ContextFill(0, 0, effectiveMax, "ok", "")
}

// SetNoProjectMode configures this orchestrator for No Project mode:
// disables code-oriented tools, adds extended bash command blacklist,
// and marks the orchestrator to override code domain to general.
func (o *Orchestrator) SetNoProjectMode() {
	o.isNoProject = true
	if o.coreToolRegistry == nil {
		if o.logger != nil {
			o.logger.Warn("SetNoProjectMode: coreToolRegistry is nil, skipping tool configuration")
		}
		return
	}
	o.coreToolRegistry.SetDisabledTools(NoProjectDisabledTools)
	if err := o.coreToolRegistry.SetExtraBashBlacklist(NoProjectBashBlacklist); err != nil {
		if o.logger != nil {
			o.logger.Warn("failed to set extra bash blacklist for No Project", "error", err)
		}
	}
}

// Emitter returns the orchestrator's event emitter for use by external callers
// that need to emit plan step events (e.g., after feedback-driven replanning).
// Returns a noop emitter when the orchestrator has no emitter configured,
// preventing nil-pointer panics in callers.
func (o *Orchestrator) Emitter() Emitter {
	if o.emitter == nil {
		return &noopEmitter{}
	}
	return o.emitter
}

// LookupSkillDescriptors converts skill names to SkillDescriptors using the
// orchestrator's skill manager. Unknown names are silently skipped. Returns nil
// if the skill manager is not available.
func (o *Orchestrator) LookupSkillDescriptors(names []string) []skills.SkillDescriptor {
	if o.skillManager == nil || len(names) == 0 {
		return nil
	}
	var result []skills.SkillDescriptor
	for _, name := range names {
		if s, ok := o.skillManager.Get(name); ok {
			result = append(result, s.Descriptor())
		}
	}
	return result
}

// SetReasoningEffort propagates the per-request reasoning effort to all components
// that make LLM calls: router, reflector, and the direct LLM caller.
func (o *Orchestrator) SetReasoningEffort(effort string) {
	o.config.ReasoningEffort = effort
	if o.router != nil {
		o.router.SetReasoningEffort(effort)
	}
	if o.reflector != nil {
		o.reflector.SetReasoningEffort(effort)
	}
	if setter, ok := o.llm.(interface{ SetReasoningEffort(string) }); ok {
		setter.SetReasoningEffort(effort)
	}
}

// SetTaskStore sets the TaskPersistence store for blackboard restoration.
// This is used by HandleMessage continuations to restore a completed task's blackboard.
func (o *Orchestrator) SetTaskStore(store TaskPersistence) {
	o.taskStore = store
}

// SetConversationHistory sets the full conversation history for the session.
// Call this during session restore to pre-populate the history from persistent storage,
// ensuring the planner sees all previous messages even after a backend restart.
func (o *Orchestrator) SetConversationHistory(history []llm.Message) {
	o.conversationHistory = history
}

// ConversationHistory returns the current conversation history (for testing).
func (o *Orchestrator) ConversationHistory() []llm.Message {
	return o.conversationHistory
}

// HistoryNoteCancelled is the assistant-side conversation-history note
// recorded when a task is cancelled before completion. The backend uses the
// same note when reconstructing history from the message store so live and
// restored histories stay identical.
const HistoryNoteCancelled = "[Task was cancelled before completion]"

// historyNoteFailedPrefix is the prefix of failure notes produced by
// HistoryNoteFailed; used to recognize a previously recorded failed attempt.
const historyNoteFailedPrefix = "[Task failed before completion: "

// HistoryNoteFailed formats the assistant-side conversation-history note
// recorded when a task fails before completion. Shared with the backend's
// history reconstruction (see HistoryNoteCancelled).
func HistoryNoteFailed(errText string) string {
	return historyNoteFailedPrefix + errText + "]"
}

// recordConversationOutcome appends the current exchange to the in-memory
// conversation history. It is invoked (via defer) for EVERY terminal outcome
// of HandleMessage — success, partial success, failure, cancellation —
// failure, and cancellation — so the router and continuation planner always
// see the full dialogue, not just successful exchanges.
func (o *Orchestrator) recordConversationOutcome(ctx context.Context, message string, result *HandleResult, err error) {
	userMsg := llm.Message{Role: "user", Content: message}

	// Continuation fallback: the session manager retries a failed
	// continuation as a fresh workflow with the same message. If the last
	// recorded exchange is a failed attempt of this exact message, replace
	// it with the retry's outcome instead of duplicating the user message.
	// This also matches how the persisted store reads after a restart
	// (consecutive assistant entries collapse to the most recent one).
	if n := len(o.conversationHistory); n >= 2 {
		prev, last := o.conversationHistory[n-2], o.conversationHistory[n-1]
		if prev.Role == "user" && prev.Content == message &&
			last.Role == "assistant" && strings.HasPrefix(last.Content, historyNoteFailedPrefix) {
			o.conversationHistory = o.conversationHistory[:n-2]
		}
	}

	switch {
	case err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete):
		if result == nil {
			o.conversationHistory = append(o.conversationHistory, userMsg)
			return
		}
		var reasoning string
		if result.Blackboard != nil {
			reasoning = lastReasoningContent(result.Blackboard)
		}
		// Failure-mode guard: if the assistant output contains tool-call
		// syntax printed as text (```bash_exec etc. instead of a tool_use
		// block), the "finish" was a stuck-model artifact, not a real
		// answer. Record a failure note instead of the hallucinated text
		// so future routing/planning sees an honest failure, not garbage.
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: reasoning})
	case ctx.Err() != nil:
		// Cancellation (or deadline): the manager persists a task_cancelled
		// event; mirror it in the in-memory history.
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	default:
		// Hard failure: the manager persists an error event; mirror it so
		// the rejected request stays visible to future routing/planning.
		o.conversationHistory = append(o.conversationHistory, userMsg,
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// recordResumeOutcome appends the outcome of a resumed execution (interrupted
// task) to the in-memory conversation history. The
// user message that spawned the task was recorded when the task first ran (or
// restored from the message store after a restart), so only the assistant
// side is appended here.
func (o *Orchestrator) recordResumeOutcome(ctx context.Context, bb orchestration.Blackboard, result *HandleResult, err error) {
	switch {
	case (err == nil || errors.Is(err, orchestration.ErrExecutionIncomplete)) && result != nil:
		assistantContent := result.Output
		if agent.DetectToolCallSyntaxInContent(assistantContent) {
			assistantContent = HistoryNoteFailed("task ended in failure-mode: model printed tool-call syntax as text instead of using tool_use blocks")
		}
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: assistantContent, ReasoningContent: lastReasoningContent(bb)})
	case ctx.Err() != nil:
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: HistoryNoteCancelled})
	case err != nil:
		o.conversationHistory = append(o.conversationHistory,
			llm.Message{Role: "assistant", Content: HistoryNoteFailed(err.Error())})
	}
}

// SetBlackboardRestoreFunc sets the function used to restore a PersistableBlackboard from persistence.
func (o *Orchestrator) SetBlackboardRestoreFunc(fn BlackboardRestoreFunc) {
	o.bbRestoreFunc = fn
}

// truncateHistory returns the last window messages of history. If window <= 0
// or len(history) <= window, history is returned as-is. The most recent
// messages are preserved so the agent sees the dialogue context leading up to
// the current message.
func truncateHistory(history []llm.Message, window int) []llm.Message {
	if window <= 0 || len(history) <= window {
		return history
	}
	truncated := make([]llm.Message, window)
	copy(truncated, history[len(history)-window:])
	return truncated
}

// HandleMessage is the unified entry point for processing user messages.
// It supports two flows: first message and continuation.
// For first messages, a plan is always generated first, then executed via Plan&Execute.
// For continuations (TaskID != ""), the P&E continuation path is used.
//   - TaskID="": First message (create new blackboard)
//   - TaskID!="": Continuation (restore existing blackboard)
func (o *Orchestrator) HandleMessage(ctx context.Context, message, sessionID string, opts HandleOptions) (result *HandleResult, err error) {
	// W-8: Enforce single-flight — only one HandleMessage per *Orchestrator.
	if !o.requestInFlight.CompareAndSwap(false, true) {
		return nil, ErrRequestInFlight
	}
	defer o.requestInFlight.Store(false)

	// Record the exchange in the in-memory conversation history for EVERY
	// terminal outcome (success, failure, cancellation) of HandleMessage
	// cancellation) so future routing and continuation planning always see
	// the full dialogue. Registered after the single-flight guard so a
	// rejected concurrent request is not recorded.
	defer func() {
		o.recordConversationOutcome(ctx, message, result, err)
	}()

	// 0. Apply per-request overrides to all LLM-calling components.
	o.SetReasoningEffort(opts.ReasoningEffort)
	if opts.ModelOverride != "" && o.modelSwitcher != nil {
		if err := o.modelSwitcher.SetModel(ctx, opts.ModelOverride); err != nil {
			if o.logger != nil {
				o.logger.Warn("failed to apply model override", "model", opts.ModelOverride, "error", err)
			}
		} else {
			o.config.Model = llm.BareModel(opts.ModelOverride)
		}
	}

	// 1. Prepare context (plan-mode key, injection-defense, vector hints, initial context_fill).
	o.logDebug("orchestrator: handle_message started", "messageLength", len(message), "taskID", opts.TaskID)
	ctx = o.prepareRequestContext(ctx, message)

	// 2. Setup blackboard (fresh or restored).
	bb, err := o.setupBlackboard(message, sessionID, opts.TaskID)
	if err != nil {
		return nil, err
	}

	// 2. Get available tools (exclude disabled tools in No Project mode).
	var availableTools []sdktools.ToolDescriptor
	if o.coreToolRegistry != nil {
		availableTools = o.toolRegistry.ListFiltered(o.coreToolRegistry.DisabledTools())
	} else {
		availableTools = o.toolRegistry.List()
	}
	mcpCount := 0
	for _, t := range availableTools {
		if t.SourceCategory == sdktools.SourceCategoryMCP {
			mcpCount++
		}
	}
	o.logDebug("orchestrator: tools loaded from registry", "total", len(availableTools), "mcp", mcpCount)

	// 3. Route and activate skills.
	// Continuation fast-path: routeOrContinue skips the router when a restored
	// task has an existing plan + routing (the router is blind to the plan and
	// would misclassify continuation messages).
	ctx, routing, _, _, err := o.routeOrContinue(ctx, message, opts, bb, availableTools)
	if err != nil {
		return nil, err
	}

	// 4. Enrich context with domain/complexity/user-skills for execution.
	ctx = WithDomain(ctx, routing.Domain)
	ctx = WithComplexity(ctx, routing.Complexity)
	if len(opts.UserSkills) > 0 {
		ctx = WithUserSkills(ctx, opts.UserSkills)
	}

	// 5. Execute via the Conductor (single ReAct loop owning the task).
	// Persist the routing decision on the blackboard so the Conductor and
	// finalization can retrieve it.
	if pbb, ok := bb.(PersistableBlackboard); ok {
		pbb.SetRouting(routing)
	}

	// Truncate conversation history to the configured window so long
	// sessions don't overflow the Conductor's context. The most recent
	// messages are kept — they carry the dialogue context the agent needs
	// to understand follow-up references (e.g. "implement variant a").
	conductorHistory := truncateHistory(o.conversationHistory, o.config.ConductorHistoryWindow)

	plansDir := opts.SessionPlansDir
	execResult, err := o.runConductor(ctx, message, bb, availableTools, plansDir, conductorHistory)
	// C-5: propagate ErrExecutionIncomplete alongside best-effort result.
	if err != nil && !errors.Is(err, orchestration.ErrExecutionIncomplete) {
		return nil, err
	}
	incompleteErr := err

	// Guard against nil execResult.
	if execResult == nil {
		execResult = &orchestration.ExecutionResult{}
	}

	// 6. Finalize: persist routing and build result. The conversation
	// history is updated by the recordConversationOutcome defer above.
	result = o.finalizeResult(bb, routing, execResult)

	if incompleteErr != nil {
		return result, incompleteErr
	}
	return result, nil
}
