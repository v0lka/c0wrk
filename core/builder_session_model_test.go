package core

import (
	"context"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// compositeSessionModelTestRouter builds a Router with two providers whose
// model lists the composition cases pin against.
func compositeSessionModelTestRouter(t *testing.T) *llm.Router {
	t.Helper()
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "local-llm", ProviderType: "openai", BaseURL: "http://localhost:9998", Models: []string{"qwen3:8b"}},
			{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"gpt-4o"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}
	return router
}

// TestCompositeSessionModel pins the session_tokens.model composition: a
// provider-echoed bare name gains the session's active provider (the same
// first-match SetModel resolution the session routes by), while ids that
// already carry a provider prefix and models no provider lists pass through
// unchanged so the frontend's badge falls back to bare-name comparison for
// them (ADR-123 §7).
func TestCompositeSessionModel(t *testing.T) {
	router := compositeSessionModelTestRouter(t)

	if got := compositeSessionModel(router, "qwen3:8b"); got != "local-llm/qwen3:8b" {
		t.Errorf("compositeSessionModel(qwen3:8b) = %q, want local-llm/qwen3:8b", got)
	}
	// An id that already contains "/" passes through verbatim (OpenRouter-style
	// echoed ids; the badge compares bare names when either side is bare).
	if got := compositeSessionModel(router, "org/path/model"); got != "org/path/model" {
		t.Errorf("compositeSessionModel(org/path/model) = %q, want unchanged", got)
	}
	// A model no provider lists passes through bare.
	if got := compositeSessionModel(router, "unknown-model"); got != "unknown-model" {
		t.Errorf("compositeSessionModel(unknown-model) = %q, want unchanged", got)
	}
	if got := compositeSessionModel(router, ""); got != "" {
		t.Errorf("compositeSessionModel(empty) = %q, want empty", got)
	}
	// No router (tests / direct construction) leaves the value untouched.
	if got := compositeSessionModel(nil, "qwen3:8b"); got != "qwen3:8b" {
		t.Errorf("compositeSessionModel(nil router) = %q, want unchanged", got)
	}
}

// TestCompositeSessionModel_ActiveCompositeWinsOverFirstMatch pins the
// active-composite attribution rule: with two providers listing the same
// bare model, a session switched to the SECOND (composite SetModel) must
// report the ACTIVE provider — not ProviderForModel's first match, which
// would silently misattribute every usage report and defeat the ADR-123 §7
// pin-vs-session badge comparison on exactly the case it was added for.
func TestCompositeSessionModel_ActiveCompositeWinsOverFirstMatch(t *testing.T) {
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "local-llm", ProviderType: "openai", BaseURL: "http://localhost:9998", Models: []string{"qwen3:8b"}},
			{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"qwen3:8b"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}

	// Fresh router: nothing has been switched yet, the first-match scan
	// applies and both attributions agree on the first provider.
	if got := compositeSessionModel(router, "qwen3:8b"); got != "local-llm/qwen3:8b" {
		t.Errorf("compositeSessionModel(qwen3:8b) on a fresh router = %q, want local-llm/qwen3:8b", got)
	}

	// Switch the session to the SECOND provider via the composite selector:
	// the echoed bare name must now carry the active provider.
	if err := router.SetModel(context.Background(), "openai/qwen3:8b"); err != nil {
		t.Fatalf("SetModel(openai/qwen3:8b): %v", err)
	}
	if got := compositeSessionModel(router, "qwen3:8b"); got != "openai/qwen3:8b" {
		t.Errorf("compositeSessionModel(qwen3:8b) after SetModel(openai/qwen3:8b) = %q, want openai/qwen3:8b (the active composite, not the first match)", got)
	}

	// A bare name the active composite does not carry still falls through to
	// the first-match scan / pass-through after the switch.
	if got := compositeSessionModel(router, "unknown-model"); got != "unknown-model" {
		t.Errorf("compositeSessionModel(unknown-model) after the switch = %q, want unchanged", got)
	}
}
