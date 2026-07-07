package agent

import (
	"context"
)

// ---------------------------------------------------------------------------
// StepOutputStore — read-only access to completed step outputs
// ---------------------------------------------------------------------------

// StepOutputStore provides read access to completed step outputs.
// Implementations must be safe for concurrent use.
// This interface lives in the agent package to avoid a cyclic import between
// the tools and orchestration packages; the concrete adapter wraps the
// orchestration blackboard.
type StepOutputStore interface {
	// GetStepOutput returns the full output of a completed step.
	// Returns ("", false) if the step has no output or does not exist.
	GetStepOutput(stepID string) (string, bool)
	// ListStepOutputs returns entries for all completed steps that produced output.
	// The order is deterministic (sorted by step ID).
	ListStepOutputs() []StepOutputEntry
}

// StepOutputEntry describes a completed step's output for listing.
type StepOutputEntry struct {
	StepID     string
	FullOutput string
}

type stepOutputStoreKey struct{}

// WithStepOutputStore returns a context carrying the given StepOutputStore.
func WithStepOutputStore(ctx context.Context, store StepOutputStore) context.Context {
	return context.WithValue(ctx, stepOutputStoreKey{}, store)
}

// StepOutputStoreFromContext returns the StepOutputStore from context, or nil.
func StepOutputStoreFromContext(ctx context.Context) StepOutputStore {
	if s, ok := ctx.Value(stepOutputStoreKey{}).(StepOutputStore); ok {
		return s
	}
	return nil
}

// ---------------------------------------------------------------------------
// FactStore — inter-step fact memory (minimal interface to avoid circular imports)
// ---------------------------------------------------------------------------

// FactStore provides keyword-tagged fact storage for inter-step communication.
// This is a minimal interface to avoid circular imports with orchestration.
type FactStore interface {
	StoreFact(keywords []string, content, author string)
	SearchFacts(keywords []string) []FactEntry
}

// FactEntry represents a stored fact returned by SearchFacts.
type FactEntry struct {
	Keywords []string
	Content  string
	Author   string
}

type factStoreKeyType struct{}

var factStoreKey = factStoreKeyType{}

// WithFactStore returns a context carrying the given FactStore.
func WithFactStore(ctx context.Context, fs FactStore) context.Context {
	return context.WithValue(ctx, factStoreKey, fs)
}

// FactStoreFromContext returns the FactStore from context, or nil.
func FactStoreFromContext(ctx context.Context) FactStore {
	fs, _ := ctx.Value(factStoreKey).(FactStore)
	return fs
}

// ---------------------------------------------------------------------------
// FinalResultStore — read-only access to the prior task's final result
// ---------------------------------------------------------------------------

// FinalResultStore provides read access to the final result of a previously
// completed task on the blackboard. This lets a continuation agent retrieve
// the prior exchange's outcome when it isn't visible in the conversation
// history (e.g. after a restart, or when the result was too large to inject
// verbatim). Implementations must be safe for concurrent use.
type FinalResultStore interface {
	// GetFinalResult returns the final result of the prior task, or ("", false)
	// if no final result is recorded on the blackboard.
	GetFinalResult() (string, bool)
}

type finalResultStoreKey struct{}

// WithFinalResultStore returns a context carrying the given FinalResultStore.
func WithFinalResultStore(ctx context.Context, store FinalResultStore) context.Context {
	return context.WithValue(ctx, finalResultStoreKey{}, store)
}

// FinalResultStoreFromContext returns the FinalResultStore from context, or nil.
func FinalResultStoreFromContext(ctx context.Context) FinalResultStore {
	s, _ := ctx.Value(finalResultStoreKey{}).(FinalResultStore)
	return s
}

// ---------------------------------------------------------------------------
// ToolResultCache — read access to cached tool results (for tool_result_read)
// ---------------------------------------------------------------------------

type toolResultCacheKey struct{}

// WithToolResultCache returns a context carrying the given ToolResultCache.
func WithToolResultCache(ctx context.Context, cache *ToolResultCache) context.Context {
	return context.WithValue(ctx, toolResultCacheKey{}, cache)
}

// ToolResultCacheFromContext returns the ToolResultCache from context, or nil.
func ToolResultCacheFromContext(ctx context.Context) *ToolResultCache {
	c, _ := ctx.Value(toolResultCacheKey{}).(*ToolResultCache)
	return c
}

// ---------------------------------------------------------------------------
// Per-tool truncation config — for tool_result_read num_lines enforcement
// ---------------------------------------------------------------------------

type perToolTruncationKey struct{}

// WithPerToolTruncation returns a context carrying the per-tool truncation config.
func WithPerToolTruncation(ctx context.Context, cfg map[string]ToolTruncationConfig) context.Context {
	return context.WithValue(ctx, perToolTruncationKey{}, cfg)
}

// PerToolTruncationFromContext returns the per-tool truncation config from context, or nil.
func PerToolTruncationFromContext(ctx context.Context) map[string]ToolTruncationConfig {
	c, _ := ctx.Value(perToolTruncationKey{}).(map[string]ToolTruncationConfig)
	return c
}

// ---------------------------------------------------------------------------
// StepTodoUpdateFunc — callback for update_checklist tool to emit events
// ---------------------------------------------------------------------------

// TodoItem represents a single checklist item parsed from the LLM's to-do list.
type TodoItem struct {
	Text    string
	Checked bool
}

// StepTodoUpdateFunc is the callback signature for emitting to-do updates.
type StepTodoUpdateFunc func(stepID string, items []TodoItem)

type todoUpdateFuncKeyType struct{}

var todoUpdateFuncKey = todoUpdateFuncKeyType{}

// WithStepTodoUpdateFunc returns a context carrying the given to-do update callback.
func WithStepTodoUpdateFunc(ctx context.Context, fn StepTodoUpdateFunc) context.Context {
	return context.WithValue(ctx, todoUpdateFuncKey, fn)
}

// StepTodoUpdateFuncFromContext returns the StepTodoUpdateFunc from context, or nil.
func StepTodoUpdateFuncFromContext(ctx context.Context) StepTodoUpdateFunc {
	fn, _ := ctx.Value(todoUpdateFuncKey).(StepTodoUpdateFunc)
	return fn
}

// ---------------------------------------------------------------------------
// ChecklistGuardFunc — validates update_checklist calls before they take effect
// ---------------------------------------------------------------------------

// ChecklistGuardFunc validates an update_checklist call for the given step ID.
// Returning a non-empty string rejects the call with that message as the tool
// result (an error surfaced to the LLM). Returning an empty string allows the
// call to proceed. The guard is consulted after parsing succeeds and before
// the update callback is invoked, so it can enforce invariants such as "no
// standalone checklist when a plan is declared". stepID is empty for a
// standalone (plan-less) checklist.
type ChecklistGuardFunc func(stepID string) string

type checklistGuardKeyType struct{}

var checklistGuardKey = checklistGuardKeyType{}

// WithChecklistGuard returns a context carrying the given checklist guard.
func WithChecklistGuard(ctx context.Context, guard ChecklistGuardFunc) context.Context {
	return context.WithValue(ctx, checklistGuardKey, guard)
}

// ChecklistGuardFromContext returns the ChecklistGuardFunc from context, or nil.
func ChecklistGuardFromContext(ctx context.Context) ChecklistGuardFunc {
	g, _ := ctx.Value(checklistGuardKey).(ChecklistGuardFunc)
	return g
}
