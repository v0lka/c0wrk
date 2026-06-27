package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestParseToolPolicy(t *testing.T) {
	tests := []struct {
		input    string
		expected ToolPolicy
	}{
		{"always_allow", PolicyAlwaysAllow},
		{"always_deny", PolicyAlwaysDeny},
		{"user_confirm", PolicyUserConfirm},
		{"auto", PolicyUserConfirm},
		{"", PolicyUserConfirm},
		{"unknown_value", PolicyUserConfirm},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := ParseToolPolicy(tc.input)
			if got != tc.expected {
				t.Errorf("ParseToolPolicy(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestErrorResult(t *testing.T) {
	res := ErrorResult("something failed: %d", 42)
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	if res.Content != "something failed: 42" {
		t.Fatalf("unexpected content: %s", res.Content)
	}
}

func TestParseInputError(t *testing.T) {
	var dummy any
	res, err := ParseInputError(json.Unmarshal([]byte("!!!"), &dummy))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true")
	}
	if res.Content == "" {
		t.Fatal("expected non-empty content")
	}
}

func TestBaseTool(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	bt := &BaseTool{
		ToolName:        "my_tool",
		ToolDescription: "does stuff",
		Schema:          schema,
		Policy:          PolicyUserConfirm,
	}

	if bt.Name() != "my_tool" {
		t.Errorf("Name() = %q", bt.Name())
	}
	if bt.Description() != "does stuff" {
		t.Errorf("Description() = %q", bt.Description())
	}
	if string(bt.InputSchema()) != `{"type":"object"}` {
		t.Errorf("InputSchema() = %s", bt.InputSchema())
	}
	if bt.DefaultPolicy() != PolicyUserConfirm {
		t.Errorf("DefaultPolicy() = %d", bt.DefaultPolicy())
	}
}

func TestWithWorkspacePath(t *testing.T) {
	ctx := context.Background()
	if got := WorkspacePathFrom(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	ctx = WithWorkspacePath(ctx, "/tmp/ws")
	if got := WorkspacePathFrom(ctx); got != "/tmp/ws" {
		t.Fatalf("expected /tmp/ws, got %q", got)
	}
}

func TestWithTempDir(t *testing.T) {
	ctx := context.Background()
	if got := TempDirFrom(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	ctx = WithTempDir(ctx, "/tmp/temp")
	if got := TempDirFrom(ctx); got != "/tmp/temp" {
		t.Fatalf("expected /tmp/temp, got %q", got)
	}
}

func TestWithTaskContext(t *testing.T) {
	ctx := context.Background()
	if got := TaskContextFrom(ctx); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	ctx = WithTaskContext(ctx, "build the thing")
	if got := TaskContextFrom(ctx); got != "build the thing" {
		t.Fatalf("expected 'build the thing', got %q", got)
	}
}

func TestToolPolicyConstants(t *testing.T) {
	// Verify iota ordering
	if PolicyAlwaysAllow != 0 {
		t.Errorf("PolicyAlwaysAllow = %d, want 0", PolicyAlwaysAllow)
	}
	if PolicyAlwaysDeny != 1 {
		t.Errorf("PolicyAlwaysDeny = %d, want 1", PolicyAlwaysDeny)
	}
	if PolicyUserConfirm != 2 {
		t.Errorf("PolicyUserConfirm = %d, want 2", PolicyUserConfirm)
	}
}

func TestBaseTool_IsUntrusted(t *testing.T) {
	// Default: not untrusted.
	bt := &BaseTool{}
	if bt.IsUntrusted() {
		t.Error("expected false for default BaseTool")
	}

	// Explicitly untrusted.
	bt.Untrusted = true
	if !bt.IsUntrusted() {
		t.Error("expected true when Untrusted is set to true")
	}
}
