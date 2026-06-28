package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDefaultParamManager_SanitizeSchema(t *testing.T) {
	pm := DefaultParamManager()

	orig := json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"query":{"type":"string"}},"required":["project","query"]}`)
	result := pm.SanitizeSchema("mcp", orig)

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	var props map[string]json.RawMessage
	if err := json.Unmarshal(schema["properties"], &props); err != nil {
		t.Fatalf("properties is not valid JSON: %v", err)
	}
	if _, ok := props["project"]; ok {
		t.Error("'project' should be stripped from properties")
	}
	if _, ok := props["query"]; !ok {
		t.Error("'query' should still be present in properties")
	}
}

func TestDefaultParamManager_InjectParams_WithWorkspace(t *testing.T) {
	pm := DefaultParamManager()

	ctx := WithWorkspacePath(context.Background(), "/home/user/project")
	input := json.RawMessage(`{"query":"hello"}`)

	result := pm.InjectParams(ctx, "some_tool", "mcp", input)

	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if v, ok := m["project"]; !ok || v != "/home/user/project" {
		t.Errorf("expected project=/home/user/project, got %v", m)
	}
	if v, ok := m["query"]; !ok || v != "hello" {
		t.Errorf("expected original query=hello to be preserved, got %v", m)
	}
}

func TestDefaultParamManager_InjectParams_NoWorkspace(t *testing.T) {
	pm := DefaultParamManager()

	ctx := context.Background()
	input := json.RawMessage(`{"query":"hello"}`)

	result := pm.InjectParams(ctx, "some_tool", "mcp", input)

	if string(result) != string(input) {
		t.Errorf("expected input unchanged when no workspace in context, got %s", result)
	}
}

func TestDefaultParamManager_InjectParams_ProjectAlreadyPresent(t *testing.T) {
	pm := DefaultParamManager()

	ctx := WithWorkspacePath(context.Background(), "/auto/injected")
	input := json.RawMessage(`{"project":"/explicit/value"}`)

	result := pm.InjectParams(ctx, "some_tool", "mcp", input)

	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	// Must keep the explicit value, not override with auto-injected
	if v, ok := m["project"]; !ok || v != "/explicit/value" {
		t.Errorf("expected project=/explicit/value (explicit, not overridden), got %v", m)
	}
}

func TestDefaultParamManager_InjectParams_InvalidJSON(t *testing.T) {
	pm := DefaultParamManager()

	ctx := WithWorkspacePath(context.Background(), "/ws")
	input := json.RawMessage(`not json`)

	result := pm.InjectParams(ctx, "some_tool", "mcp", input)

	if string(result) != string(input) {
		t.Errorf("expected input unchanged for invalid JSON, got %s", result)
	}
}
