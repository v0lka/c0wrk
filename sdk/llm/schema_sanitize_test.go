package llm

import (
	"encoding/json"
	"testing"
)

func TestGetTypes(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  []string
	}{
		{"nil", nil, nil},
		{"string", "object", []string{"object"}},
		{"array of strings", []any{"string", "null"}, []string{"string", "null"}},
		{"int in array not string", []any{42}, nil},
		{"mixed array", []any{"string", 42}, []string{"string"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTypes(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("getTypes() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("getTypes()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
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

	t.Run("OpenAIForcesAdditionalPropertiesFalse", func(t *testing.T) {
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
		if additionalProps != false {
			t.Errorf("additionalProperties = %v, expected false (strict mode forces false)", additionalProps)
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
		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		user := props["user"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
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
		items := props["items"].(map[string]interface{})       //nolint:errcheck // test: schema structure is known
		itemsSchema := items["items"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
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
		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		outer := props["outer"].(map[string]interface{})       //nolint:errcheck // test: schema structure is known
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

	t.Run("OpenAIEmptyRequiredForObjectWithoutProperties", func(t *testing.T) {
		schema := json.RawMessage(`{"type": "object"}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist as an empty array for objects without properties")
		}
		if len(required) != 0 {
			t.Errorf("required should be empty, got %v", required)
		}
		if parsed["additionalProperties"] != false {
			t.Error("additionalProperties should be false")
		}
		props, ok := parsed["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("properties should exist as an empty map for objects without properties")
		}
		if len(props) != 0 {
			t.Errorf("properties should be empty, got %v", props)
		}
	})

	t.Run("OpenAIEmptyRequiredForObjectWithEmptyProperties", func(t *testing.T) {
		schema := json.RawMessage(`{"type": "object", "properties": {}}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist as an empty array for objects with empty properties")
		}
		if len(required) != 0 {
			t.Errorf("required should be empty, got %v", required)
		}
		if parsed["additionalProperties"] != false {
			t.Error("additionalProperties should be false")
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
		anyOf := parsed["anyOf"].([]interface{})      //nolint:errcheck // test: schema structure is known
		anyOfObj := anyOf[0].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		if anyOfObj["additionalProperties"] != false {
			t.Error("anyOf object should have additionalProperties: false")
		}

		// Verify oneOf has additionalProperties
		oneOf := parsed["oneOf"].([]interface{})      //nolint:errcheck // test: schema structure is known
		oneOfObj := oneOf[0].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		if oneOfObj["additionalProperties"] != false {
			t.Error("oneOf object should have additionalProperties: false")
		}
	})

	t.Run("OpenAIInfersObjectTypeFromProperties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"properties": {
				"name": {"type": "string"},
				"age": {"type": "integer"}
			},
			"required": ["name"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if parsed["type"] != "object" {
			t.Errorf("type = %v, expected 'object' (inferred from properties)", parsed["type"])
		}
		if parsed["additionalProperties"] != false {
			t.Error("additionalProperties should be false")
		}
		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}
		if len(required) != 2 || required[0] != "age" || required[1] != "name" {
			t.Errorf("required = %v, expected [age, name]", required)
		}
		if _, ok := parsed["properties"].(map[string]interface{}); !ok {
			t.Fatal("properties should be preserved")
		}
	})

	t.Run("OpenAIInfersObjectTypeFromEmptyType", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "",
			"properties": {
				"traces": {"type": "array", "items": {"type": "string"}}
			},
			"required": ["traces"]
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if parsed["type"] != "object" {
			t.Errorf("type = %v, expected 'object' (inferred from empty type with properties)", parsed["type"])
		}
		if parsed["additionalProperties"] != false {
			t.Error("additionalProperties should be false")
		}
		required, ok := parsed["required"].([]interface{})
		if !ok {
			t.Fatal("required should exist")
		}
		if len(required) != 1 || required[0] != "traces" {
			t.Errorf("required = %v, expected [traces]", required)
		}
	})

	t.Run("OpenAIResolvesRefFromDefs", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"trace": {"$ref": "#/$defs/Trace"}
			},
			"$defs": {
				"Trace": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"duration": {"type": "number"}
					}
				}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Root assertions
		if parsed["additionalProperties"] != false {
			t.Error("root should have additionalProperties: false")
		}
		rootReq, ok := parsed["required"].([]interface{})
		if !ok || len(rootReq) != 1 || rootReq[0] != "trace" {
			t.Errorf("root required = %v, expected [trace]", rootReq)
		}

		// $defs should be removed
		if _, exists := parsed["$defs"]; exists {
			t.Error("$defs should be removed from output")
		}

		// trace property should be resolved
		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		trace := props["trace"].(map[string]interface{})       //nolint:errcheck // test: schema structure is known
		if _, exists := trace["$ref"]; exists {
			t.Error("$ref should be resolved, not present")
		}
		if trace["type"] != "object" {
			t.Errorf("trace.type = %v, expected 'object'", trace["type"])
		}
		if trace["additionalProperties"] != false {
			t.Error("trace should have additionalProperties: false")
		}
		traceReq, ok := trace["required"].([]interface{})
		if !ok || len(traceReq) != 2 {
			t.Fatalf("trace.required should have 2 elements, got %v", traceReq)
		}
		if traceReq[0] != "duration" || traceReq[1] != "name" {
			t.Errorf("trace.required = %v, expected [duration, name]", traceReq)
		}
	})

	t.Run("OpenAIResolvesRefFromDefinitions", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"item": {"$ref": "#/definitions/Item"}
			},
			"definitions": {
				"Item": {
					"type": "object",
					"properties": {
						"id": {"type": "string"}
					}
				}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if _, exists := parsed["definitions"]; exists {
			t.Error("definitions should be removed from output")
		}

		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		item := props["item"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		if item["type"] != "object" {
			t.Errorf("item.type = %v, expected 'object'", item["type"])
		}
		if item["additionalProperties"] != false {
			t.Error("item should have additionalProperties: false")
		}
		itemReq, ok := item["required"].([]interface{})
		if !ok || len(itemReq) != 1 || itemReq[0] != "id" {
			t.Errorf("item.required = %v, expected [id]", itemReq)
		}
	})

	t.Run("OpenAIHandlesUnresolvableRef", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"data": {"$ref": "#/$defs/NonExistent"}
			},
			"$defs": {}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if _, exists := parsed["$defs"]; exists {
			t.Error("$defs should be removed from output")
		}

		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		data := props["data"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		if data["type"] != "object" {
			t.Errorf("data.type = %v, expected 'object' (safe fallback)", data["type"])
		}
		dataProps, ok := data["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("data.properties should exist")
		}
		if len(dataProps) != 0 {
			t.Errorf("data.properties should be empty, got %v", dataProps)
		}
		dataReq, ok := data["required"].([]interface{})
		if !ok || len(dataReq) != 0 {
			t.Errorf("data.required should be empty array, got %v", dataReq)
		}
		if data["additionalProperties"] != false {
			t.Error("data should have additionalProperties: false")
		}
	})

	t.Run("OpenAIRemovesForbiddenKeywords", func(t *testing.T) {
		schema := json.RawMessage(`{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"$id": "test-schema",
			"$comment": "a comment",
			"type": "object",
			"properties": {
				"name": {
					"type": "string",
					"default": "unnamed",
					"examples": ["foo", "bar"]
				}
			}
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		for _, key := range []string{"$schema", "$id", "$comment"} {
			if _, exists := parsed[key]; exists {
				t.Errorf("root key %q should be removed", key)
			}
		}

		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		name := props["name"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		if _, exists := name["default"]; exists {
			t.Error("properties.name should not have 'default'")
		}
		if _, exists := name["examples"]; exists {
			t.Error("properties.name should not have 'examples'")
		}

		if parsed["type"] != "object" {
			t.Errorf("type should be 'object', got %v", parsed["type"])
		}
		if parsed["additionalProperties"] != false {
			t.Error("additionalProperties should be false")
		}
		required, ok := parsed["required"].([]interface{})
		if !ok || len(required) != 1 || required[0] != "name" {
			t.Errorf("required = %v, expected [name]", required)
		}
	})

	t.Run("OpenAIForcesAdditionalPropertiesFalseOnNested", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"config": {
					"type": "object",
					"additionalProperties": true,
					"properties": {
						"key": {"type": "string"}
					}
				}
			},
			"additionalProperties": true
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		if parsed["additionalProperties"] != false {
			t.Error("root additionalProperties should be false")
		}

		props := parsed["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		config := props["config"].(map[string]interface{})     //nolint:errcheck // test: schema structure is known
		if config["additionalProperties"] != false {
			t.Error("config.additionalProperties should be false")
		}
	})

	t.Run("OpenAIDeeplyNestedMCPSchema", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "",
			"properties": {
				"traces": {
					"type": "array",
					"items": {
						"properties": {
							"name": {"type": "string", "default": "unknown"},
							"metadata": {
								"type": "object",
								"additionalProperties": true,
								"properties": {
									"tags": {
										"type": "array",
										"items": {"type": "string"}
									}
								}
							}
						},
						"required": ["name"]
					}
				}
			},
			"required": ["traces"],
			"$comment": "MCP tool schema"
		}`)

		result := SanitizeSchemaForOpenAI(schema)

		var parsed map[string]interface{}
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to parse result: %v", err)
		}

		// Root assertions
		if parsed["type"] != "object" {
			t.Errorf("root type = %v, expected 'object'", parsed["type"])
		}
		if parsed["additionalProperties"] != false {
			t.Error("root should have additionalProperties: false")
		}
		rootReq, ok := parsed["required"].([]interface{})
		if !ok || len(rootReq) != 1 || rootReq[0] != "traces" {
			t.Errorf("root required = %v, expected [traces]", rootReq)
		}
		if _, exists := parsed["$comment"]; exists {
			t.Error("$comment should be removed")
		}

		// traces.items assertions
		props := parsed["properties"].(map[string]interface{})  //nolint:errcheck // test: schema structure is known
		traces := props["traces"].(map[string]interface{})      //nolint:errcheck // test: schema structure is known
		tracesItems := traces["items"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		if tracesItems["type"] != "object" {
			t.Errorf("traces.items.type = %v, expected 'object' (inferred)", tracesItems["type"])
		}
		if tracesItems["additionalProperties"] != false {
			t.Error("traces.items should have additionalProperties: false")
		}
		itemsReq, ok := tracesItems["required"].([]interface{})
		if !ok || len(itemsReq) != 2 {
			t.Fatalf("traces.items.required should have 2 elements, got %v", itemsReq)
		}
		if itemsReq[0] != "metadata" || itemsReq[1] != "name" {
			t.Errorf("traces.items.required = %v, expected [metadata, name]", itemsReq)
		}

		// traces.items.properties.name should not have default
		itemProps := tracesItems["properties"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		nameField := itemProps["name"].(map[string]interface{})         //nolint:errcheck // test: schema structure is known
		if _, exists := nameField["default"]; exists {
			t.Error("traces.items.properties.name should not have 'default'")
		}

		// traces.items.properties.metadata assertions
		metadata := itemProps["metadata"].(map[string]interface{}) //nolint:errcheck // test: schema structure is known
		if metadata["additionalProperties"] != false {
			t.Error("metadata.additionalProperties should be false (forced from true)")
		}
		metaReq, ok := metadata["required"].([]interface{})
		if !ok || len(metaReq) != 1 || metaReq[0] != "tags" {
			t.Errorf("metadata.required = %v, expected [tags]", metaReq)
		}
	})
}

func TestSanitizeSchemaForAnthropic(t *testing.T) {
	t.Run("removes unsupported keywords", func(t *testing.T) {
		schema := json.RawMessage(`{
			"$schema": "http://json-schema.org/draft-07/schema#",
			"$id": "test",
			"$comment": "should be removed",
			"patternProperties": {"^S_": {"type": "string"}},
			"type": "object",
			"properties": {
				"name": {"type": "string"}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		for _, key := range []string{"$schema", "$id", "$comment", "patternProperties"} {
			if _, exists := parsed[key]; exists {
				t.Errorf("expected key %q to be removed", key)
			}
		}
		if parsed["type"] != "object" {
			t.Errorf("expected type to remain, got %v", parsed["type"])
		}
	})

	t.Run("resolves $ref and removes $defs", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"$ref": "#/$defs/Address"}
			},
			"$defs": {
				"Address": {
					"type": "object",
					"properties": {
						"street": {"type": "string"}
					}
				}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, exists := parsed["$defs"]; exists {
			t.Error("expected $defs to be removed")
		}

		props, _ := parsed["properties"].(map[string]any)
		addr, _ := props["address"].(map[string]any)
		if addr["type"] != "object" {
			t.Errorf("expected resolved address type to be 'object', got %v", addr["type"])
		}
		addrProps, _ := addr["properties"].(map[string]any)
		if addrProps == nil || addrProps["street"] == nil {
			t.Error("expected resolved address to have street property")
		}
	})

	t.Run("flattens allOf single item", func(t *testing.T) {
		schema := json.RawMessage(`{
			"allOf": [
				{
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					}
				}
			]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, exists := parsed["allOf"]; exists {
			t.Error("expected allOf to be removed after flattening")
		}
		if parsed["type"] != "object" {
			t.Errorf("expected type 'object', got %v", parsed["type"])
		}
		props, _ := parsed["properties"].(map[string]any)
		if props["name"] == nil {
			t.Error("expected 'name' property to be present")
		}
	})

	t.Run("flattens allOf multiple items merges properties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"allOf": [
				{
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					},
					"required": ["name"]
				},
				{
					"properties": {
						"age": {"type": "integer"}
					},
					"required": ["age"]
				}
			]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, exists := parsed["allOf"]; exists {
			t.Error("expected allOf to be removed after flattening")
		}
		props, _ := parsed["properties"].(map[string]any)
		if props["name"] == nil || props["age"] == nil {
			t.Errorf("expected merged properties, got %v", props)
		}
		required, _ := parsed["required"].([]any)
		if len(required) < 2 {
			t.Errorf("expected at least 2 required fields, got %v", required)
		}
	})

	t.Run("flattens oneOf picks first variant", func(t *testing.T) {
		schema := json.RawMessage(`{
			"oneOf": [
				{"type": "string", "description": "first"},
				{"type": "integer", "description": "second"}
			]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, exists := parsed["oneOf"]; exists {
			t.Error("expected oneOf to be removed after flattening")
		}
		if parsed["type"] != "string" {
			t.Errorf("expected type 'string' (first variant), got %v", parsed["type"])
		}
	})

	t.Run("infers object type from properties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"properties": {
				"name": {"type": "string"}
			},
			"required": ["name"]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if parsed["type"] != "object" {
			t.Errorf("expected inferred type 'object', got %v", parsed["type"])
		}
	})

	t.Run("recursive nested processing", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"inner": {
					"$comment": "should be removed",
					"type": "object",
					"properties": {
						"value": {"type": "string"}
					}
				}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		props, _ := parsed["properties"].(map[string]any)
		inner, _ := props["inner"].(map[string]any)
		if _, exists := inner["$comment"]; exists {
			t.Error("expected nested $comment to be removed")
		}
	})

	t.Run("handles nil input", func(t *testing.T) {
		result := SanitizeSchemaForAnthropic(nil)
		if result != nil {
			t.Errorf("nil should return nil, got %q", string(result))
		}
	})

	t.Run("handles empty input", func(t *testing.T) {
		result := SanitizeSchemaForAnthropic(json.RawMessage{})
		if len(result) != 0 {
			t.Errorf("empty should return empty, got %q", string(result))
		}
	})

	t.Run("handles invalid JSON", func(t *testing.T) {
		input := json.RawMessage(`{not valid json`)
		result := SanitizeSchemaForAnthropic(input)
		if string(result) != string(input) {
			t.Error("invalid JSON should be returned unchanged")
		}
	})

	t.Run("processes array items recursively", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "array",
			"items": {
				"$comment": "remove me",
				"type": "object",
				"properties": {
					"id": {"type": "integer"}
				}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		items, _ := parsed["items"].(map[string]any)
		if _, exists := items["$comment"]; exists {
			t.Error("expected $comment in items to be removed")
		}
	})

	t.Run("processes additionalProperties recursively", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"additionalProperties": {
				"$comment": "should go",
				"type": "string"
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		addProps, _ := parsed["additionalProperties"].(map[string]any)
		if _, exists := addProps["$comment"]; exists {
			t.Error("expected $comment in additionalProperties to be removed")
		}
		if addProps["type"] != "string" {
			t.Errorf("expected type 'string' in additionalProperties, got %v", addProps["type"])
		}
	})

	t.Run("flattens anyOf picks first variant", func(t *testing.T) {
		schema := json.RawMessage(`{
			"anyOf": [
				{"type": "boolean", "description": "first"},
				{"type": "string", "description": "second"}
			]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if _, exists := parsed["anyOf"]; exists {
			t.Error("expected anyOf to be removed after flattening")
		}
		if parsed["type"] != "boolean" {
			t.Errorf("expected type 'boolean' (first variant), got %v", parsed["type"])
		}
	})

	t.Run("processes nested anyOf inside properties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"field": {
					"anyOf": [
						{"type": "string"},
						{"type": "integer"}
					]
				}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		props, _ := parsed["properties"].(map[string]any)
		field, _ := props["field"].(map[string]any)
		// Nested anyOf should be preserved (not flattened - only top-level)
		if _, exists := field["anyOf"]; !exists {
			t.Log("nested anyOf may be preserved or processed")
		}
	})

	t.Run("processes nested allOf inside properties", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"field": {
					"allOf": [
						{"type": "object", "properties": {"x": {"type": "string"}}}
					]
				}
			}
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		// Must not panic, schema must be valid JSON
		if parsed["type"] != "object" {
			t.Errorf("root type should be object, got %v", parsed["type"])
		}
	})

	t.Run("processes tuple items recursively", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "array",
			"items": [
				{"$comment": "remove me", "type": "string"},
				{"$comment": "remove me too", "type": "integer"}
			]
		}`)

		result := SanitizeSchemaForAnthropic(schema)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		items, ok := parsed["items"].([]any)
		if !ok {
			t.Fatal("items should be an array (tuple)")
		}
		if len(items) != 2 {
			t.Fatalf("expected 2 tuple items, got %d", len(items))
		}
		first, _ := items[0].(map[string]any)
		if _, exists := first["$comment"]; exists {
			t.Error("expected $comment in first tuple item to be removed")
		}
		if first["type"] != "string" {
			t.Errorf("expected first item type 'string', got %v", first["type"])
		}
	})
}
