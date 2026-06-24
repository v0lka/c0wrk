package tools

import "context"

// noProjectKey is the context key signalling No Project (CHAT) mode.
// When set, privacy-sensitive info (e.g. home directory) must be redacted
// from system prompts, and code‑oriented tools are disabled.
type noProjectKey struct{}

// WithNoProject returns a new context marked as No Project mode.
func WithNoProject(ctx context.Context) context.Context {
	return context.WithValue(ctx, noProjectKey{}, true)
}

// IsNoProject reports whether the context is marked as No Project mode.
func IsNoProject(ctx context.Context) bool {
	v, _ := ctx.Value(noProjectKey{}).(bool)
	return v
}
