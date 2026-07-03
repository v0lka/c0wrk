package tools

import (
	"context"
	"testing"
)

// mockCoherenceChecker implements FileCoherenceChecker for testing.
type mockCoherenceChecker struct{}

func (m *mockCoherenceChecker) CheckRead(_ context.Context, _ string) *CoherenceConflict  { return nil }
func (m *mockCoherenceChecker) CheckWrite(_ context.Context, _ string) *CoherenceConflict { return nil }
func (m *mockCoherenceChecker) RecordWrite(_ context.Context, _ string)                   {}
func (m *mockCoherenceChecker) RecordDelete(_ context.Context, _ string)                  {}
func (m *mockCoherenceChecker) PurgeSession(_ string)                                     {}
func (m *mockCoherenceChecker) Lock(_ string)                                             {}
func (m *mockCoherenceChecker) Unlock(_ string)                                           {}

func TestWithCoherence(t *testing.T) {
	ctx := context.Background()

	// Should return nil from bare context.
	if got := CoherenceFrom(ctx); got != nil {
		t.Errorf("expected nil from bare context, got %v", got)
	}

	// Round-trip: same checker returned.
	checker := &mockCoherenceChecker{}
	ctx = WithCoherence(ctx, checker)
	got := CoherenceFrom(ctx)
	if got != checker {
		t.Errorf("expected same checker instance, got %p want %p", got, checker)
	}
}

func TestCoherenceFrom_NilOnWrongType(t *testing.T) {
	// If something else is stored under the same key type, should return nil.
	ctx := context.WithValue(context.Background(), coherenceKey{}, "not a checker")
	if got := CoherenceFrom(ctx); got != nil {
		t.Errorf("expected nil for wrong type, got %v", got)
	}
}
