package llm

import (
	"encoding/json"
	"testing"
)

// TestSanitizeSchema_SelfReferentialRef verifies that a self-referential JSON
// schema (a tree node whose "children" items reference the same definition)
// terminates instead of overflowing the stack. Previously the cycle-detection
// visited set was only populated for bare $ref→$ref chains, so a $ref that
// resolved to a full object nesting a back-reference recursed forever.
func TestSanitizeSchema_SelfReferentialRef(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"root": {"$ref": "#/$defs/Node"}
		},
		"$defs": {
			"Node": {
				"type": "object",
				"properties": {
					"value": {"type": "string"},
					"children": {"type": "array", "items": {"$ref": "#/$defs/Node"}}
				}
			}
		}
	}`)

	result := SanitizeSchemaForOpenAI(schema)
	if len(result) == 0 {
		t.Fatal("expected non-empty sanitized schema")
	}
}

// TestSanitizeSchema_SiblingRefsNotPolluted verifies that two sibling
// properties referencing the same definition both resolve correctly. The
// cycle-detection visited set is copied per $ref resolution so one sibling's
// resolution cannot trigger a false cycle in another sibling.
func TestSanitizeSchema_SiblingRefsNotPolluted(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"a": {"$ref": "#/$defs/Item"},
			"b": {"$ref": "#/$defs/Item"}
		},
		"$defs": {
			"Item": {
				"type": "object",
				"properties": {
					"name": {"type": "string"}
				}
			}
		}
	}`)

	result := SanitizeSchemaForOpenAI(schema)
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in output")
	}
	for _, key := range []string{"a", "b"} {
		prop, ok := props[key].(map[string]any)
		if !ok {
			t.Fatalf("expected property %q to be an object", key)
		}
		if _, hasRef := prop["$ref"]; hasRef {
			t.Errorf("property %q: $ref should be resolved", key)
		}
		itemProps, ok := prop["properties"].(map[string]any)
		if !ok {
			t.Errorf("property %q: expected resolved properties from Item", key)
			continue
		}
		if _, hasName := itemProps["name"]; !hasName {
			t.Errorf("property %q: expected 'name' property from Item def", key)
		}
	}
}
