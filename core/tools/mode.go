package tools

import "context"

// noProjectKey is the context key signalling No Project (CHAT) mode.
// When set, privacy-sensitive info (e.g. home directory) must be redacted
// from system prompts, and code‑oriented tools are disabled.
// See specs/domains/orchestration/README.md (Invariants).
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

// researchKey is the context key signalling RESEARCH mode for a real project
// with a non-empty research root. Unlike No Project mode, RESEARCH keeps the
// full CODE toolset — the flag drives prompt/router/UI awareness only.
// See specs/domains/research.md (Invariants).
type researchKey struct{}

// WithResearch marks the context RESEARCH mode; the caller applies it only to
// real projects with a non-empty research root (never No Project sessions).
func WithResearch(ctx context.Context) context.Context {
	return context.WithValue(ctx, researchKey{}, true)
}

// IsResearch reports whether the context is marked as RESEARCH mode.
func IsResearch(ctx context.Context) bool {
	v, _ := ctx.Value(researchKey{}).(bool)
	return v
}

// researchRootKey carries the on-disk research root (config.ProjectResearchPath,
// `<workspace>/.research`); set alongside WithResearch by the session manager —
// core may not import backend/config. Only for real projects with a root.
type researchRootKey struct{}

// WithResearchRoot returns a new context carrying the research-root path;
// an empty path means no research context block.
func WithResearchRoot(ctx context.Context, path string) context.Context {
	if path == "" {
		return ctx
	}
	return context.WithValue(ctx, researchRootKey{}, path)
}

// ResearchRootPathFrom reports the research-root path carried by the context,
// or "" when none is set.
func ResearchRootPathFrom(ctx context.Context) string {
	v, _ := ctx.Value(researchRootKey{}).(string)
	return v
}
