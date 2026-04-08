package core

import "context"

// BlackboardContextKey is the context key used to attach a Blackboard to a context.
// Exported so that sub-packages (core/coretools) can use the same key.
type BlackboardContextKey struct{}

// WithBlackboard returns a context with the blackboard attached.
func WithBlackboard(ctx context.Context, bb Blackboard) context.Context {
	return context.WithValue(ctx, BlackboardContextKey{}, bb)
}

// BlackboardFromContext retrieves the blackboard from context.
// Returns nil if no blackboard is present.
func BlackboardFromContext(ctx context.Context) Blackboard {
	if v, ok := ctx.Value(BlackboardContextKey{}).(Blackboard); ok {
		return v
	}
	return nil
}
