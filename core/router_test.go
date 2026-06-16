package core

import (
	"context"
	"testing"

	"github.com/v0lka/c0wrk/sdk/llm"
)

func TestResolveBaseEffort(t *testing.T) {
	// nil registry returns empty
	if got := resolveBaseEffort(context.Background(), "any-model", nil, &BuilderConfig{}); got != "" {
		t.Errorf("nil registry: expected empty, got %q", got)
	}

	// unknown model returns empty
	reg := llm.NewModelRegistry(nil)
	if got := resolveBaseEffort(context.Background(), "unknown-model-xyz", reg, &BuilderConfig{}); got != "" {
		t.Errorf("unknown model: expected empty, got %q", got)
	}

	// non-reasoning model (claude-sonnet has Temperature=true, Reasoning=false)
	if got := resolveBaseEffort(context.Background(), "claude-sonnet-4-20250514", reg, &BuilderConfig{}); got != "" {
		t.Errorf("non-reasoning model: expected empty, got %q", got)
	}

	// reasoning model with default config (empty BaseEffort → ReasoningHigh)
	if got := resolveBaseEffort(context.Background(), "o3", reg, &BuilderConfig{}); got != llm.ReasoningHigh {
		t.Errorf("reasoning model default: expected %q, got %q", llm.ReasoningHigh, got)
	}

	// reasoning model with explicit BaseEffort
	if got := resolveBaseEffort(context.Background(), "o3", reg, &BuilderConfig{Reasoning: BuilderReasoningConfig{BaseEffort: "medium"}}); got != llm.ReasoningMedium {
		t.Errorf("reasoning model medium: expected %q, got %q", llm.ReasoningMedium, got)
	}

	// reasoning model with off
	if got := resolveBaseEffort(context.Background(), "o3", reg, &BuilderConfig{Reasoning: BuilderReasoningConfig{BaseEffort: "off"}}); got != llm.ReasoningOff {
		t.Errorf("reasoning model off: expected %q, got %q", llm.ReasoningOff, got)
	}
}
