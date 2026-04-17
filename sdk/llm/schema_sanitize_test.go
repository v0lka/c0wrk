package llm

import (
	"encoding/json"
	"testing"
)

func TestSanitizeSchemaForGemini(t *testing.T) {
	t.Run("GeminiConvertsIntegerEnums", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"priority": {
					"type": "integer",
					"enum": [1, 2, 3]
				}
			}
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		props, ok := parsed["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("properties not found")
		}

		priority, ok := props["priority"].(map[string]interface{})
		if !ok {
			t.Fatal("priority property not found")
		}

		enum, ok := priority["enum"].([]interface{})
		if !ok {
			t.Fatal("enum not found")
		}

		// Verify all enum values are strings
		for i, v := range enum {
			if _, ok := v.(string); !ok {
				t.Errorf("enum[%d] = %v (%T), expected string", i, v, v)
			}
		}

		// Verify the converted values
		expected := []string{"1", "2", "3"}
		for i, v := range enum {
			if s, _ := v.(string); s != expected[i] {
				t.Errorf("enum[%d] = %q, expected %q", i, s, expected[i])
			}
		}
	})

	t.Run("GeminiStripsPropertiesFromNonObject", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "string",
			"properties": {
				"foo": {"type": "string"}
			},
			"required": ["foo"]
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// type should still be string
		if typ, _ := parsed["type"].(string); typ != "string" {
			t.Errorf("type = %q, expected 'string'", typ)
		}

		// properties should be removed
		if _, exists := parsed["properties"]; exists {
			t.Error("properties should be removed from non-object type")
		}

		// required should be removed
		if _, exists := parsed["required"]; exists {
			t.Error("required should be removed from non-object type")
		}
	})

	t.Run("GeminiEnsuresArrayItemType", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "array",
			"items": {}
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		items, ok := parsed["items"].(map[string]interface{})
		if !ok {
			t.Fatal("items not found")
		}

		if typ, _ := items["type"].(string); typ != "object" {
			t.Errorf("items.type = %q, expected 'object'", typ)
		}
	})

	t.Run("GeminiRemovesUnsupportedKeywords", func(t *testing.T) {
		schema := json.RawMessage(`{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"$id": "https://example.com/schema.json",
			"$ref": "#/definitions/foo",
			"$comment": "this is a comment",
			"type": "object",
			"properties": {}
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		for _, key := range []string{"$schema", "$id", "$ref", "$comment"} {
			if _, exists := parsed[key]; exists {
				t.Errorf("key %q should be removed", key)
			}
		}

		// type and properties should remain
		if _, exists := parsed["type"]; !exists {
			t.Error("type should be preserved")
		}
		if _, exists := parsed["properties"]; !exists {
			t.Error("properties should be preserved for object type")
		}
	})

	t.Run("GeminiHandlesNestedObjects", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"user": {
					"type": "object",
					"properties": {
						"profile": {
							"type": "object",
							"properties": {
								"age": {
									"type": "integer",
									"enum": [18, 21, 25]
								}
							}
						}
					}
				},
				"tags": {
					"type": "array",
					"items": {
						"type": "string",
						"properties": {"extra": {"type": "string"}}
					}
				}
			}
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Verify nested enum conversion
		props := parsed["properties"].(map[string]interface{})              //nolint:errcheck // test: schema structure is known
		user := props["user"].(map[string]interface{})                     //nolint:errcheck // test: schema structure is known
		userProps := user["properties"].(map[string]interface{})           //nolint:errcheck // test: schema structure is known
		profile := userProps["profile"].(map[string]interface{})           //nolint:errcheck // test: schema structure is known
		profileProps := profile["properties"].(map[string]interface{})     //nolint:errcheck // test: schema structure is known
		age := profileProps["age"].(map[string]interface{})                //nolint:errcheck // test: schema structure is known
		enum := age["enum"].([]interface{})                                //nolint:errcheck // test: schema structure is known

		for i, v := range enum {
			if _, ok := v.(string); !ok {
				t.Errorf("nested enum[%d] = %v (%T), expected string", i, v, v)
			}
		}

		// Verify array items properties stripped from non-object
		tags := props["tags"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		items := tags["items"].(map[string]interface{})        //nolint:errcheck // test: schema structure is known
		if _, exists := items["properties"]; exists {
			t.Error("items properties should be stripped from string type")
		}
	})

	t.Run("GeminiPassthroughOnInvalidJSON", func(t *testing.T) {
		invalidJSON := json.RawMessage(`{invalid json}`)
		result := SanitizeSchemaForGemini(invalidJSON)

		if string(result) != string(invalidJSON) {
			t.Errorf("invalid JSON should be returned unchanged, got %q", string(result))
		}
	})

	t.Run("GeminiHandlesEmptySchema", func(t *testing.T) {
		result := SanitizeSchemaForGemini(json.RawMessage{})
		if len(result) != 0 {
			t.Errorf("empty schema should return empty, got %q", string(result))
		}
	})

	t.Run("GeminiHandlesNilSchema", func(t *testing.T) {
		result := SanitizeSchemaForGemini(nil)
		if result != nil {
			t.Errorf("nil schema should return nil, got %q", string(result))
		}
	})

	t.Run("GeminiHandlesAnyOfOneOfAllOf", func(t *testing.T) {
		schema := json.RawMessage(`{
			"anyOf": [
				{
					"type": "integer",
					"enum": [1, 2]
				},
				{
					"type": "string"
				}
			],
			"oneOf": [
				{
					"type": "integer",
					"enum": [3, 4]
				}
			]
		}`)

		result := SanitizeSchemaForGemini(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Verify anyOf enum conversion
		anyOf := parsed["anyOf"].([]interface{})                      //nolint:errcheck // test: schema structure is known
		first := anyOf[0].(map[string]interface{})                    //nolint:errcheck // test: schema structure is known
		enum := first["enum"].([]interface{})                         //nolint:errcheck // test: schema structure is known
		for i, v := range enum {
			if _, ok := v.(string); !ok {
				t.Errorf("anyOf enum[%d] = %v (%T), expected string", i, v, v)
			}
		}

		// Verify oneOf enum conversion
		oneOf := parsed["oneOf"].([]interface{})                            //nolint:errcheck // test: schema structure is known
		oneOfEnum := oneOf[0].(map[string]interface{})["enum"].([]interface{}) //nolint:errcheck // test: schema structure is known
		for i, v := range oneOfEnum {
			if _, ok := v.(string); !ok {
				t.Errorf("oneOf enum[%d] = %v (%T), expected string", i, v, v)
			}
		}
	})
}

func TestSanitizeSchemaForOpenAI(t *testing.T) {
	t.Run("OpenAIAddsAdditionalProperties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		additionalProps, exists := parsed["additionalProperties"]
		if !exists {
			t.Error("additionalProperties should be added")
		}
		if additionalProps != false {
			t.Errorf("additionalProperties = %v, expected false", additionalProps)
		}
	})

	t.Run("OpenAIPreservesExistingAdditionalProperties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			},
			"additionalProperties": true
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		additionalProps := parsed["additionalProperties"]
		if additionalProps != true {
			t.Errorf("additionalProperties = %v, expected true (should be preserved)", additionalProps)
		}
	})

	t.Run("OpenAIRecursiveNestedObjects", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"user": {
					"type": "object",
					"properties": {
						"profile": {
							"type": "object",
							"properties": {
								"age": {"type": "integer"}
							}
						}
					}
				},
				"items": {
					"type": "array",
					"items": {
						"type": "object",
						"properties": {
							"id": {"type": "string"}
						}
					}
				}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Root level
		if parsed["additionalProperties"] != false {
			t.Error("root should have additionalProperties: false")
		}

		// Nested user object
		props := parsed["properties"].(map[string]interface{})     //nolint:errcheck // test: schema structure is known
		user := props["user"].(map[string]interface{})             //nolint:errcheck // test: schema structure is known
		if user["additionalProperties"] != false {
			t.Error("user should have additionalProperties: false")
		}

		// Nested profile object
		userProps := user["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		profile := userProps["profile"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		if profile["additionalProperties"] != false {
			t.Error("profile should have additionalProperties: false")
		}

		// Array items object
		items := props["items"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		itemsSchema := items["items"].(map[string]interface{})   //nolint:errcheck // test: schema structure is known
		if itemsSchema["additionalProperties"] != false {
			t.Error("array items should have additionalProperties: false")
		}
	})

	t.Run("OpenAIValidatesRequiredProperties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string"}
			},
			"required": ["a", "b"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}

		// Only "a" should remain since "b" doesn't exist in properties
		if len(required) != 1 {
			t.Errorf("required should have 1 element, got %d", len(required))
		}

		if required[0] != "a" {
			t.Errorf("required[0] = %v, expected 'a'", required[0])
		}
	})

	t.Run("OpenAIRemovesInvalidAndAddsMissingRequired", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string"}
			},
			"required": ["b", "c"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// "b" and "c" are invalid, but "a" exists in properties and must be added
		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}
		if len(required) != 1 {
			t.Errorf("required should have 1 element, got %d: %v", len(required), required)
		}
		if required[0] != "a" {
			t.Errorf("required[0] = %v, expected 'a'", required[0])
		}
	})

	t.Run("OpenAIHandlesNonObjectTypes", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "string"
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Non-object types should not get additionalProperties
		if _, exists := parsed["additionalProperties"]; exists {
			t.Error("additionalProperties should not be added to non-object types")
		}
	})

	t.Run("OpenAIHandlesInvalidJSON", func(t *testing.T) {
		invalidJSON := json.RawMessage(`{invalid json}`)
		result := SanitizeSchemaForOpenAI(invalidJSON)

		if string(result) != string(invalidJSON) {
			t.Errorf("invalid JSON should be returned unchanged, got %q", string(result))
		}
	})

	t.Run("OpenAIHandlesEmptySchema", func(t *testing.T) {
		result := SanitizeSchemaForOpenAI(json.RawMessage{})
		if len(result) != 0 {
			t.Errorf("empty schema should return empty, got %q", string(result))
		}
	})

	t.Run("OpenAIHandlesNilSchema", func(t *testing.T) {
		result := SanitizeSchemaForOpenAI(nil)
		if result != nil {
			t.Errorf("nil schema should return nil, got %q", string(result))
		}
	})

	t.Run("OpenAIAddsAllPropertiesWhenRequiredMissing", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string"},
				"b": {"type": "integer"},
				"c": {"type": "boolean"}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should be created from properties")
		}
		if len(required) != 3 {
			t.Fatalf("required should have 3 elements, got %d: %v", len(required), required)
		}
		// Sorted order: a, b, c
		expected := []string{"a", "b", "c"}
		for i, exp := range expected {
			if required[i] != exp {
				t.Errorf("required[%d] = %v, expected %q", i, required[i], exp)
			}
		}
	})

	t.Run("OpenAIAddsMissingPropertiesToPartialRequired", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string"},
				"b": {"type": "integer"},
				"c": {"type": "boolean"}
			},
			"required": ["a"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}
		if len(required) != 3 {
			t.Fatalf("required should have 3 elements, got %d: %v", len(required), required)
		}
		expected := []string{"a", "b", "c"}
		for i, exp := range expected {
			if required[i] != exp {
				t.Errorf("required[%d] = %v, expected %q", i, required[i], exp)
			}
		}
	})

	t.Run("OpenAINestedObjectMissingRequired", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"outer": {
					"type": "object",
					"properties": {
						"x": {"type": "string"},
						"y": {"type": "integer"}
					}
				}
			},
			"required": ["outer"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Root required should contain "outer"
		rootReq, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("root required should exist")
		}
		if len(rootReq) != 1 || rootReq[0] != "outer" {
			t.Errorf("root required = %v, expected [outer]", rootReq)
		}

		// Nested "outer" should have required: ["x", "y"]
		props := parsed["properties"].(map[string]interface{})     //nolint:errcheck // test: schema structure is known
		outer := props["outer"].(map[string]interface{})           //nolint:errcheck // test: schema structure is known
		nestedReq, ok := outer["required"].([]interface{})
		if !ok {
			t.Fatal("nested required should be created")
		}
		if len(nestedReq) != 2 {
			t.Fatalf("nested required should have 2 elements, got %d: %v", len(nestedReq), nestedReq)
		}
		if nestedReq[0] != "x" || nestedReq[1] != "y" {
			t.Errorf("nested required = %v, expected [x, y]", nestedReq)
		}
	})

	t.Run("OpenAIRequiredWithNonExistentAndMissing", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"a": {"type": "string"},
				"b": {"type": "integer"}
			},
			"required": ["a", "nonexistent"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}
		// "nonexistent" removed, "b" added → ["a", "b"]
		if len(required) != 2 {
			t.Fatalf("required should have 2 elements, got %d: %v", len(required), required)
		}
		if required[0] != "a" || required[1] != "b" {
			t.Errorf("required = %v, expected [a, b]", required)
		}
	})

	t.Run("OpenAIHandlesAnyOfOneOfAllOf", func(t *testing.T) {
		schema := json.RawMessage(`{
			"anyOf": [
				{
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					}
				}
			],
			"oneOf": [
				{
					"type": "object",
					"properties": {
						"id": {"type": "integer"}
					}
				}
			]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Verify anyOf has additionalProperties
		anyOf := parsed["anyOf"].([]interface{})                      //nolint:errcheck // test: schema structure is known
		anyOfObj := anyOf[0].(map[string]interface{})                 //nolint:errcheck // test: schema structure is known
		if anyOfObj["additionalProperties"] != false {
			t.Error("anyOf object should have additionalProperties: false")
		}

		// Verify oneOf has additionalProperties
		oneOf := parsed["oneOf"].([]interface{})                      //nolint:errcheck // test: schema structure is known
		oneOfObj := oneOf[0].(map[string]interface{})                 //nolint:errcheck // test: schema structure is known
		if oneOfObj["additionalProperties"] != false {
			t.Error("oneOf object should have additionalProperties: false")
		}
	})
}

func TestSanitizeSchemaForAnthropic(t *testing.T) {
	t.Run("AnthropicPassthrough", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			},
			"additionalProperties": true
		}`)

		result := SanitizeSchemaForAnthropic(schema)

		// Should return the exact same bytes
		if string(result) != string(schema) {
			t.Errorf("Anthropic should return schema unchanged.\nExpected: %s\nGot: %s", string(schema), string(result))
		}
	})

	t.Run("AnthropicHandlesNil", func(t *testing.T) {
		result := SanitizeSchemaForAnthropic(nil)
		if result != nil {
			t.Errorf("nil should return nil, got %q", string(result))
		}
	})

	t.Run("AnthropicHandlesEmpty", func(t *testing.T) {
		result := SanitizeSchemaForAnthropic(json.RawMessage{})
		if len(result) != 0 {
			t.Errorf("empty should return empty, got %q", string(result))
		}
	})
}
