package markitdown

import (
	"context"
)

// VisionOptions carries per-document connection parameters for the
// vision-capable LLM captioning embedded images; the endpoint MUST speak
// the OpenAI Chat Completions wire format (specs/domains/session-lifecycle.md).
type VisionOptions struct {
	// APIKey is the bearer token, passed to the conversion subprocess via an
	// environment variable — never argv (world-readable via ps).
	APIKey string

	// BaseURL is the API root INCLUDING the version path; the driver appends
	// "/chat/completions".
	BaseURL string

	// Model is the bare model name sent to the endpoint.
	Model string

	// Prompt optionally overrides markitdown's default captioning prompt;
	// empty keeps the default.
	Prompt string
}

// complete reports whether all mandatory fields are set; an incomplete
// struct is treated as "no vision" (plain CLI path).
func (v *VisionOptions) complete() bool {
	return v != nil && v.APIKey != "" && v.BaseURL != "" && v.Model != ""
}

// CacheKey is the vision identity in conversion-cache keys — a conversion
// from a DIFFERENT vision model must never be served; "" when vision is not
// applied.
// See specs/domains/tool-system/builtins.md.
func (v *VisionOptions) CacheKey() string {
	if v == nil || !v.complete() {
		return ""
	}
	return v.BaseURL + "\x00" + v.Model + "\x00" + v.Prompt
}

// VisionResolver returns the connection parameters for the NEXT conversion,
// or nil when the active model must not caption (not vision-capable, no
// OpenAI-compatible surface, no credentials). Invoked PER DOCUMENT — never
// cached — so a mid-session model switch applies to the very next read
// (specs/domains/session-lifecycle.md).
type VisionResolver func() *VisionOptions

type visionResolverCtxKey struct{}

// WithVisionResolver attaches a VisionResolver to ctx (nil leaves ctx
// unchanged); the value flows through subagent delegations, so every
// conversion in the task resolves the model active at its own call time.
func WithVisionResolver(ctx context.Context, r VisionResolver) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, visionResolverCtxKey{}, r)
}

// VisionResolverFrom extracts the VisionResolver from ctx, or nil when none
// is attached.
func VisionResolverFrom(ctx context.Context) VisionResolver {
	if ctx == nil {
		return nil
	}
	if r, ok := ctx.Value(visionResolverCtxKey{}).(VisionResolver); ok {
		return r
	}
	return nil
}
