package tools

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// TestDelegateSchema_NoTypelessProperties is a regression test for the HTTP 400
// "schema must have a 'type' key" rejection from OpenAI (Responses API, strict
// mode) and OpenCode Zen. The delegate tool's `tasks[].tools` field is
// polymorphic (a string preset OR an array of tool names); it must be expressed
// with a valid JSON Schema combinator (anyOf) rather than left typeless.
//
// This test runs the real schema through the production sanitizer
// (SanitizeSchemaForOpenAI) and asserts that NO schema node anywhere in the
// tree is left without a `type` or a composition keyword (anyOf/oneOf/allOf) or
// `$ref`/`enum` — which is precisely what OpenAI strict validation demands.
func TestDelegateSchema_NoTypelessProperties(t *testing.T) {
	raw := NewDelegateTool().InputSchema()
	if len(raw) == 0 {
		t.Fatal("delegate tool schema is empty")
	}

	// The schema must be well-formed JSON to begin with.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("delegate schema is not valid JSON: %v", err)
	}

	// Sanity: the `tools` field must carry anyOf before sanitization.
	items, _ := parsed["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	props, _ := items["properties"].(map[string]any)
	toolsProp, ok := props["tools"].(map[string]any)
	if !ok {
		t.Fatal("tasks[].tools property missing from schema")
	}
	if _, ok := toolsProp["anyOf"].([]any); !ok {
		t.Fatalf("tasks[].tools must use anyOf (string-enum | string-array); got: %v", toolsProp)
	}

	// Run the production sanitizer — this is exactly what the OpenAI providers
	// apply before sending the schema to the API.
	sanitized := llm.SanitizeSchemaForOpenAI(raw)
	var s map[string]any
	if err := json.Unmarshal(sanitized, &s); err != nil {
		t.Fatalf("sanitized schema is not valid JSON: %v", err)
	}

	// Walk every schema node and assert none is typeless.
	var offenders []string
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		hasType := false
		for _, k := range []string{"type", "anyOf", "oneOf", "allOf", "$ref", "enum"} {
			if _, ok := node[k]; ok {
				hasType = true
				break
			}
		}
		if !hasType {
			offenders = append(offenders, path)
		}
		// Recurse into schema-bearing keys.
		if p, ok := node["properties"].(map[string]any); ok {
			for name, val := range p {
				if m, ok := val.(map[string]any); ok {
					walk(m, path+".properties."+name)
				}
			}
		}
		if it, ok := node["items"]; ok {
			switch v := it.(type) {
			case map[string]any:
				walk(v, path+".items")
			case []any:
				for i, item := range v {
					if m, ok := item.(map[string]any); ok {
						walk(m, path+".items["+strconv.Itoa(i)+"]")
					}
				}
			}
		}
		if ap, ok := node["additionalProperties"].(map[string]any); ok {
			walk(ap, path+".additionalProperties")
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if arr, ok := node[key].([]any); ok {
				for i, item := range arr {
					if m, ok := item.(map[string]any); ok {
						walk(m, path+"."+key+"["+strconv.Itoa(i)+"]")
					}
				}
			}
		}
	}
	walk(s, "root")

	if len(offenders) > 0 {
		t.Fatalf("typeless schema nodes after sanitization (OpenAI strict mode rejects these with HTTP 400): %v", offenders)
	}
}
