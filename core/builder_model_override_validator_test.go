package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// modelOverrideValidatorTestRouter builds a real Router with two providers
// whose configured model lists the tests pin against.
func modelOverrideValidatorTestRouter(t *testing.T) *llm.Router {
	t.Helper()
	router, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "local-llm", ProviderType: "openai", BaseURL: "http://localhost:9998", Models: []string{"qwen3:8b"}},
			{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"gpt-4o"}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}
	return router
}

// TestModelOverrideValidator_CompositeKnownProviderAccepts verifies the
// closure accepts a composite naming a configured provider.
func TestModelOverrideValidator_CompositeKnownProviderAccepts(t *testing.T) {
	validate := modelOverrideValidator(modelOverrideValidatorTestRouter(t))
	if err := validate("reviewer", "local-llm/qwen3:8b"); err != nil {
		t.Errorf("known provider must validate, got: %v", err)
	}
}

// TestModelOverrideValidator_CompositeUnknownProviderFailsClosed verifies the
// fail-closed error names the profile, the model, the offending provider, and
// the known providers.
func TestModelOverrideValidator_CompositeUnknownProviderFailsClosed(t *testing.T) {
	validate := modelOverrideValidator(modelOverrideValidatorTestRouter(t))
	err := validate("reviewer", "ghost-llm/qwen3:8b")
	if err == nil {
		t.Fatal("unknown provider must fail closed")
	}
	for _, want := range []string{"reviewer", "ghost-llm/qwen3:8b", "ghost-llm", "local-llm, openai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must name %q", err.Error(), want)
		}
	}
}

// TestModelOverrideValidator_BareNamesPassThrough verifies bare model names
// are never validated — even a name no provider lists passes (they ride the
// session's active provider, which never requires naming one).
func TestModelOverrideValidator_BareNamesPassThrough(t *testing.T) {
	validate := modelOverrideValidator(modelOverrideValidatorTestRouter(t))
	for _, bare := range []string{"qwen3:8b", "no-such-model-anywhere"} {
		if err := validate("reviewer", bare); err != nil {
			t.Errorf("bare %q must pass through, got: %v", bare, err)
		}
	}
}

// TestModelOverrideValidator_NilRouterYieldsNil verifies a nil router yields a
// nil validator (no pre-launch validation; the router's per-request provider
// miss remains the backstop).
func TestModelOverrideValidator_NilRouterYieldsNil(t *testing.T) {
	if got := modelOverrideValidator(nil); got != nil {
		t.Error("nil router must yield a nil validator")
	}
}
