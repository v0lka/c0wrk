package core

import (
	"context"
	"testing"

	"github.com/v0lka/c0wrk/sdk/llm"
)

// TestReflector_ImplementsInterface is kept here to ensure the adapter
// creates a type that satisfies orchestration.Reflector (verified in sdk test too).
func TestCoreReflector_CreatesValidReflector(t *testing.T) {
	mock := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"summary":"ok","suggested_action":"retry"}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	r := newCoreReflector(mock)
	if r == nil {
		t.Fatal("newCoreReflector returned nil")
	}

	// Verify it works end-to-end with c0wrk prompt
	reflection, err := r.Reflect(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Reflect failed: %v", err)
	}
	if reflection == nil {
		t.Fatal("expected non-nil reflection")
	}
	if reflection.SuggestedAction != "retry" {
		t.Errorf("expected suggested_action='retry', got %q", reflection.SuggestedAction)
	}
}
