package agent

import (
	"context"
)

// ---------------------------------------------------------------------------
// StepOutputStore — read-only access to completed step outputs
// ---------------------------------------------------------------------------

// StepOutputStore provides read access to completed step outputs.
// Implementations must be safe for concurrent use.
// This interface lives in sdk/agent to avoid a cyclic import between
// sdk/tools and sdk/orchestration; the concrete adapter wraps Blackboard.
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
