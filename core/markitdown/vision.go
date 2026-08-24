package markitdown

import (
	"context"
)

// VisionOptions carries per-document connection parameters for a
// vision-capable LLM used by markitdown to describe images embedded in
// converted documents (e.g. pictures inside pptx decks).
//
// The endpoint MUST speak the OpenAI Chat Completions wire format — that is
// the only interface markitdown's captioning client understands (it calls
// client.chat.completions.create(model, messages) with image_url data-URI
// content parts). Callers must therefore only produce VisionOptions for
// providers with a known OpenAI-compatible surface.
type VisionOptions struct {
	// APIKey is the bearer token for the endpoint. Passed to the conversion
	// subprocess via an environment variable — never via argv, where it would
	// be visible to every process on the machine (ps/world-readable argv).
	APIKey string

	// BaseURL is the OpenAI-compatible API root INCLUDING the version path
	// (e.g. "https://api.openai.com/v1"); "/chat/completions" is appended by
	// the driver.
	BaseURL string

	// Model is the bare model name sent to the endpoint.
	Model string

	// Prompt optionally overrides markitdown's default image-captioning
	// prompt ("Write a detailed caption for this image."). Empty keeps the
	// markitdown default.
	Prompt string
}

// complete reports whether all mandatory connection fields are set. An
// incomplete options struct is treated as "no vision" — the converter falls
// back to the plain CLI instead of spawning a driver doomed to fail.
func (v *VisionOptions) complete() bool {
	return v != nil && v.APIKey != "" && v.BaseURL != "" && v.Model != ""
}

// CacheKey returns a stable identity for the vision configuration used in
// conversion-cache keys: the same source file must not be served from a cache
// entry produced under a DIFFERENT vision model (or without one), because the
// captioned output differs. Returns "" when vision is not applied.
func (v *VisionOptions) CacheKey() string {
	if v == nil || !v.complete() {
		return ""
	}
	return v.BaseURL + "\x00" + v.Model + "\x00" + v.Prompt
}

// VisionResolver returns the connection parameters of the vision-capable LLM
// that should assist the NEXT document conversion, or nil when the currently
// active model must not be used for captioning (not vision-capable, provider
// without an OpenAI-compatible surface, missing credentials, …).
//
// Resolvers are invoked PER DOCUMENT so a mid-session model switch is picked
// up by the very next conversion — never cached.
type VisionResolver func() *VisionOptions

type visionResolverCtxKey struct{}

// WithVisionResolver attaches a VisionResolver to ctx. A nil resolver leaves
// the context unchanged. The value flows through the whole executor context
// chain (including subagent delegations), so every document conversion inside
// a task resolves the model active at ITS call time.
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
