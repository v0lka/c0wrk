package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

func TestFinishTool_Metadata(t *testing.T) {
	ft := NewFinishTool()

	if ft.Name() != "finish" {
		t.Errorf("Name() = %q, want %q", ft.Name(), "finish")
	}
	if ft.Description() == "" {
		t.Error("Description() should not be empty")
	}
	if ft.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("DefaultPolicy() = %v, want PolicyAlwaysAllow", ft.DefaultPolicy())
	}

	schema := ft.InputSchema()
	if !json.Valid(schema) {
		t.Error("InputSchema() is not valid JSON")
	}

	var s map[string]interface{}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if s["type"] != "object" {
		t.Errorf("schema type = %v, want object", s["type"])
	}
}

func TestFinishTool_Execute_Success(t *testing.T) {
	ft := NewFinishTool()
	input := json.RawMessage(`{"answer": "42"}`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("result.IsError = true, want false")
	}
	if result.Content != "42" {
		t.Errorf("result.Content = %q, want %q", result.Content, "42")
	}
}

func TestFinishTool_Execute_InvalidJSON(t *testing.T) {
	ft := NewFinishTool()
	input := json.RawMessage(`not json`)
	result, err := ft.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid JSON input")
	}
	if result.Content == "" {
		t.Error("expected non-empty error content")
	}
}

func TestFinishTool_IsUntrusted(t *testing.T) {
	ft := NewFinishTool()
	if ft.IsUntrusted() {
		t.Error("FinishTool.IsUntrusted() should return false — finish is a trusted internal tool")
	}
}
