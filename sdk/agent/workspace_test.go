package agent

import (
	"context"
	"testing"
)

// mockStepOutputStore implements StepOutputStore for testing.
type mockStepOutputStore struct {
	entries []StepOutputEntry
}

func (m *mockStepOutputStore) GetStepOutput(stepID string) (string, bool) {
	for _, e := range m.entries {
		if e.StepID == stepID {
			return e.FullOutput, true
		}
	}
	return "", false
}

func (m *mockStepOutputStore) ListStepOutputs() []StepOutputEntry {
	return m.entries
}

func TestStepOutputStore_ContextRoundTrip(t *testing.T) {
	store := &mockStepOutputStore{
		entries: []StepOutputEntry{
			{StepID: "step_1", FullOutput: "output from step 1"},
		},
	}
	ctx := WithStepOutputStore(context.Background(), store)

	got := StepOutputStoreFromContext(ctx)
	if got == nil {
		t.Fatal("expected StepOutputStore in context")
	}

	output, ok := got.GetStepOutput("step_1")
	if !ok {
		t.Fatal("expected step_1 to exist")
	}
	if output != "output from step 1" {
		t.Errorf("got %q, want %q", output, "output from step 1")
	}
}

func TestStepOutputStoreFromContext_Nil(t *testing.T) {
	ctx := context.Background()
	got := StepOutputStoreFromContext(ctx)
	if got != nil {
		t.Error("expected nil when no StepOutputStore in context")
	}
}

func TestMockStepOutputStore_GetStepOutput_Missing(t *testing.T) {
	store := &mockStepOutputStore{}
	_, ok := store.GetStepOutput("nonexistent")
	if ok {
		t.Error("expected ok=false for missing step")
	}
}

func TestMockStepOutputStore_ListStepOutputs(t *testing.T) {
	store := &mockStepOutputStore{
		entries: []StepOutputEntry{
			{StepID: "step_1", FullOutput: "a"},
			{StepID: "step_2", FullOutput: "b"},
		},
	}
	entries := store.ListStepOutputs()
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestWithToolResultCache_ToolResultCacheFromContext(t *testing.T) {
	cache := NewToolResultCache(0)
	ctx := WithToolResultCache(context.Background(), cache)
	got := ToolResultCacheFromContext(ctx)
	if got != cache {
		t.Error("ToolResultCacheFromContext should return the set cache")
	}
}

func TestToolResultCacheFromContext_Nil(t *testing.T) {
	got := ToolResultCacheFromContext(context.Background())
	if got != nil {
		t.Error("ToolResultCacheFromContext should return nil when not set")
	}
}

func TestWithPerToolTruncation_PerToolTruncationFromContext(t *testing.T) {
	cfg := map[string]ToolTruncationConfig{"read_file": {MaxLines: 50}}
	ctx := WithPerToolTruncation(context.Background(), cfg)
	got := PerToolTruncationFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil config")
	}
	if got["read_file"].MaxLines != 50 {
		t.Errorf("MaxLines = %d, want 50", got["read_file"].MaxLines)
	}
}

func TestPerToolTruncationFromContext_Nil(t *testing.T) {
	got := PerToolTruncationFromContext(context.Background())
	if got != nil {
		t.Error("PerToolTruncationFromContext should return nil when not set")
	}
}

func TestWithStepTodoUpdateFunc_StepTodoUpdateFuncFromContext(t *testing.T) {
	called := false
	fn := func(stepID string, items []TodoItem) { called = true }
	ctx := WithStepTodoUpdateFunc(context.Background(), fn)
	got := StepTodoUpdateFuncFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil function")
	}
	got("step_1", nil)
	if !called {
		t.Error("callback should have been called")
	}
}

func TestStepTodoUpdateFuncFromContext_Nil(t *testing.T) {
	got := StepTodoUpdateFuncFromContext(context.Background())
	if got != nil {
		t.Error("StepTodoUpdateFuncFromContext should return nil when not set")
	}
}
