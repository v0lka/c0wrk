package external

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/user/agent/internal/tools"
)

func TestExternalTool_Name(t *testing.T) {
	m := &ToolManifest{Name: "test_tool"}
	et := NewExternalTool(m, "/tmp")
	if got := et.Name(); got != "test_tool" {
		t.Errorf("Name() = %q, want %q", got, "test_tool")
	}
}

func TestExternalTool_Description(t *testing.T) {
	m := &ToolManifest{Description: "A test tool"}
	et := NewExternalTool(m, "/tmp")
	if got := et.Description(); got != "A test tool" {
		t.Errorf("Description() = %q, want %q", got, "A test tool")
	}
}

func TestExternalTool_InputSchema(t *testing.T) {
	m := &ToolManifest{
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		},
	}
	et := NewExternalTool(m, "/tmp")
	schema := et.InputSchema()

	var parsed map[string]interface{}
	if err := json.Unmarshal(schema, &parsed); err != nil {
		t.Fatalf("failed to parse InputSchema: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("schema type = %v, want %q", parsed["type"], "object")
	}
}

func TestExternalTool_DefaultPolicy(t *testing.T) {
	tests := []struct {
		policyStr string
		want      tools.ToolPolicy
	}{
		{"always_allow", tools.PolicyAlwaysAllow},
		{"always_deny", tools.PolicyAlwaysDeny},
		{"user_confirm", tools.PolicyUserConfirm},
		{"auto", tools.PolicyAuto},
		{"", tools.PolicyAuto},
		{"unknown", tools.PolicyAuto},
	}

	for _, tc := range tests {
		m := &ToolManifest{DefaultPolicy: tc.policyStr}
		et := NewExternalTool(m, "/tmp")
		if got := et.DefaultPolicy(); got != tc.want {
			t.Errorf("DefaultPolicy(%q) = %v, want %v", tc.policyStr, got, tc.want)
		}
	}
}

func TestExternalTool_ExecutePython(t *testing.T) {
	dir := t.TempDir()
	script := `import sys, json
data = json.load(sys.stdin)
print(json.dumps({"echo": data}))
`
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &ToolManifest{
		Name:       "py_echo",
		Language:   "python",
		EntryPoint: "main.py",
	}
	et := NewExternalTool(m, dir)

	input := json.RawMessage(`{"hello": "world"}`)
	result, err := et.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content), &parsed); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}
	if parsed["echo"] == nil {
		t.Error("expected 'echo' key in output")
	}
}

func TestExternalTool_ExecuteBash(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
cat
`
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &ToolManifest{
		Name:       "bash_echo",
		Language:   "bash",
		EntryPoint: "run.sh",
	}
	et := NewExternalTool(m, dir)

	input := json.RawMessage(`{"key": "value"}`)
	result, err := et.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.IsError {
		t.Fatalf("Execute() returned error result: %s", result.Content)
	}
	if result.Content != `{"key": "value"}` {
		t.Errorf("Execute() content = %q, want %q", result.Content, `{"key": "value"}`)
	}
}

func TestExternalTool_ExecuteFailingScript(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
echo "something went wrong" >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "fail.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &ToolManifest{
		Name:       "failing_tool",
		Language:   "bash",
		EntryPoint: "fail.sh",
	}
	et := NewExternalTool(m, dir)

	result, err := et.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() should not return Go error for script failure, got: %v", err)
	}
	if !result.IsError {
		t.Error("Execute() should return IsError=true for failing script")
	}
	if result.Content != "something went wrong" {
		t.Errorf("Execute() content = %q, want %q", result.Content, "something went wrong")
	}
}

func TestExternalTool_ExecuteContextCancellation(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
sleep 10
`
	if err := os.WriteFile(filepath.Join(dir, "slow.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := &ToolManifest{
		Name:       "slow_tool",
		Language:   "bash",
		EntryPoint: "slow.sh",
	}
	et := NewExternalTool(m, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := et.Execute(ctx, json.RawMessage(`{}`))
	if err == nil {
		t.Error("Execute() should return error on context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Execute() error = %v, want %v", err, context.DeadlineExceeded)
	}
}
