package core

import (
	"context"

	"github.com/user/agent/sdk/orchestration"
)

// BlackboardContextKey is re-exported from sdk/orchestration.
type BlackboardContextKey = orchestration.BlackboardContextKey

// WithBlackboard returns a context with the blackboard attached.
func WithBlackboard(ctx context.Context, bb Blackboard) context.Context {
	return orchestration.WithBlackboard(ctx, bb)
}

// BlackboardFromContext retrieves the blackboard from context.
func BlackboardFromContext(ctx context.Context) Blackboard {
	return orchestration.BlackboardFromContext(ctx)
}
