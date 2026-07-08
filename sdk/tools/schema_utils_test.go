package tools

import (
	"encoding/json"
	"testing"
)

func TestStripParamsFromSchema_NoParamsToRemove(t *testing.T) {
	orig := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	result, err := stripParamsFromSchema(orig, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(orig) {
		t.Errorf("expected unchanged schema when paramsToRemove is nil, got %s", result)
	}
}

func TestStripParamsFromSchema_StripsProject(t *testing.T) {
	orig := json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"},"query":{"type":"string"}},"required":["project","query"]}`)
	result, err := stripParamsFromSchema(orig, map[string]bool{"project": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var schema map[string]json.RawMessage
	if err := json.Unmarshal(result, &schema); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// "project" should be gone from properties
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

	// "project" should be gone from required
	var required []string
	if err := json.Unmarshal(schema["required"], &required); err != nil {
		t.Fatalf("required is not valid JSON: %v", err)
	}
	for _, r := range required {
		if r == "project" {
			t.Error("'project' should be stripped from required")
		}
	}
}

func TestStripParamsFromSchema_NoProperties(t *testing.T) {
	orig := json.RawMessage(`{"type":"object"}`)
	result, err := stripParamsFromSchema(orig, map[string]bool{"project": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(orig) {
		t.Errorf("expected unchanged schema when no properties key, got %s", result)
	}
}

func TestStripParamsFromSchema_ParamNotPresent(t *testing.T) {
	orig := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	result, err := stripParamsFromSchema(orig, map[string]bool{"project": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(orig) {
		t.Errorf("expected unchanged schema when param not present, got %s", result)
	}
}

func TestStripParamsFromSchema_InvalidJSON(t *testing.T) {
	orig := json.RawMessage(`not json`)
	_, err := stripParamsFromSchema(orig, map[string]bool{"project": true})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
