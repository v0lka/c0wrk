package agent

import "context"

type stepIDKey struct{}

// WithStepID returns a new context with the step ID attached.
func WithStepID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, stepIDKey{}, id)
}

// StepIDFromContext extracts the step ID from the context.
// Returns empty string if not found.
func StepIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(stepIDKey{}).(string); ok {
		return id
	}
	return ""
}
