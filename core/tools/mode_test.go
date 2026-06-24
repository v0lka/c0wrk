package tools

import (
	"context"
	"testing"
)

func TestWithNoProject(t *testing.T) {
	ctx := context.Background()

	if IsNoProject(ctx) {
		t.Error("bare context: expected IsNoProject=false")
	}

	ctx = WithNoProject(ctx)
	if !IsNoProject(ctx) {
		t.Error("after WithNoProject: expected IsNoProject=true")
	}
}

// otherKey is a named context key type used in TestIsNoProject_UnrelatedContext
// to verify that unrelated context values don't interfere with IsNoProject.
type otherKey struct{}

func TestIsNoProject_UnrelatedContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), otherKey{}, "something")
	if IsNoProject(ctx) {
		t.Error("unrelated context value: expected IsNoProject=false")
	}
}

func TestWithNoProject_Idempotent(t *testing.T) {
	ctx := context.Background()
	ctx = WithNoProject(ctx)
	ctx = WithNoProject(ctx) // double-wrap
	if !IsNoProject(ctx) {
		t.Error("double-wrapped: expected IsNoProject=true")
	}
}
