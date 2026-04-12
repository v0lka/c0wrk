package llm

import (
	"encoding/json"
	"fmt"
)

// SanitizeSchemaForGemini normalizes JSON Schema for Gemini API compatibility.
// Gemini has specific requirements:
//   - Enum values must be strings (integer/number enums are converted)
//   - properties and required fields must not exist on non-object types
//   - Array items must have explicit type field
//   - Unsupported keywords ($schema, $id, $ref, $comment) are removed
func SanitizeSchemaForGemini(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		// If parsing fails, return original unchanged
		return raw
	}

	sanitized := sanitizeGeminiSchema(schema)
	result, err := json.Marshal(sanitized)
	if err != nil {
		return raw
	}
	return result
}

// sanitizeGeminiSchema recursively processes a schema map for Gemini compatibility.
func sanitizeGeminiSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	result := make(map[string]interface{})

	// Remove unsupported JSON Schema keywords
	for key, value := range schema {
		switch key {
		case "$schema", "$id", "$ref", "$comment":
			// Skip unsupported keywords
			continue
		default:
			result[key] = value
		}
	}

	// Get the type(s)
	typeVal, hasType := result["type"]
	types := getTypes(typeVal)
	isObjectType := len(types) == 1 && types[0] == "object"

	// Convert integer/number enum values to strings
	if enumVal, ok := result["enum"]; ok {
		result["enum"] = convertEnumToStrings(enumVal)
	}

	// Strip properties and required from non-object types
	if hasType && !isObjectType {
		delete(result, "properties")
		delete(result, "required")
	}

	// Process nested objects in properties
	if props, ok := result["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{})
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]interface{}); ok {
				newProps[propName] = sanitizeGeminiSchema(propMap)
			} else {
				newProps[propName] = propVal
			}
		}
		result["properties"] = newProps
	}

	// Process items for array type
	if items, ok := result["items"]; ok {
		switch v := items.(type) {
		case map[string]interface{}:
			// Ensure items has explicit type
			if _, hasItemsType := v["type"]; !hasItemsType {
				// Default to "object" if no type specified
				v["type"] = "object"
			}
			result["items"] = sanitizeGeminiSchema(v)
		case []interface{}:
			// Tuple items - process each
			newItems := make([]interface{}, len(v))
			for i, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if _, hasItemsType := itemMap["type"]; !hasItemsType {
						itemMap["type"] = "object"
					}
					newItems[i] = sanitizeGeminiSchema(itemMap)
				} else {
					newItems[i] = item
				}
			}
			result["items"] = newItems
		}
	}

	// Process anyOf, oneOf, allOf recursively
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]interface{}); ok {
			newArr := make([]interface{}, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]interface{}); ok {
					newArr[i] = sanitizeGeminiSchema(itemMap)
				} else {
					newArr[i] = item
				}
			}
			result[key] = newArr
		}
	}

	return result
}

// getTypes extracts type values as a slice of strings.
func getTypes(typeVal interface{}) []string {
	if typeVal == nil {
		return nil
	}

	switch v := typeVal.(type) {
	case string:
		return []string{v}
	case []interface{}:
		types := make([]string, 0, len(v))
		for _, t := range v {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
		return types
	default:
		return nil
	}
}

// convertEnumToStrings converts enum values to strings if they are numbers.
func convertEnumToStrings(enumVal interface{}) []interface{} {
	arr, ok := enumVal.([]interface{})
	if !ok {
		return []interface{}{enumVal}
	}

	result := make([]interface{}, len(arr))
	for i, val := range arr {
		switch v := val.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			result[i] = fmt.Sprintf("%v", v)
		default:
			result[i] = val
		}
	}
	return result
}

// SanitizeSchemaForOpenAI ensures strict mode compliance for OpenAI.
//   - Adds "additionalProperties": false to all object-type schemas (recursively)
//   - Ensures all properties listed in required actually exist in properties
func SanitizeSchemaForOpenAI(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var schema map[string]interface{}
	if err := json.Unmarshal(raw, &schema); err != nil {
		// If parsing fails, return original unchanged
		return raw
	}

	sanitized := sanitizeOpenAISchema(schema)
	result, err := json.Marshal(sanitized)
	if err != nil {
		return raw
	}
	return result
}

// sanitizeOpenAISchema recursively processes a schema map for OpenAI strict mode.
func sanitizeOpenAISchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}

	result := make(map[string]interface{})
	for k, v := range schema {
		result[k] = v
	}

	// Get the type(s)
	typeVal := result["type"]
	types := getTypes(typeVal)
	isObjectType := len(types) == 1 && types[0] == "object"

	// For object types, add additionalProperties: false if not already set
	if isObjectType {
		if _, exists := result["additionalProperties"]; !exists {
			result["additionalProperties"] = false
		}

		// Validate required properties exist
		if required, ok := result["required"].([]interface{}); ok {
			props, propsOK := result["properties"].(map[string]interface{})
			if propsOK && props != nil {
				validRequired := make([]interface{}, 0)
				for _, req := range required {
					if reqStr, ok := req.(string); ok {
						if _, exists := props[reqStr]; exists {
							validRequired = append(validRequired, reqStr)
						}
					}
				}
				if len(validRequired) > 0 {
					result["required"] = validRequired
				} else {
					delete(result, "required")
				}
			}
		}
	}

	// Process nested objects in properties
	if props, ok := result["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{})
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]interface{}); ok {
				newProps[propName] = sanitizeOpenAISchema(propMap)
			} else {
				newProps[propName] = propVal
			}
		}
		result["properties"] = newProps
	}

	// Process items for array type
	if items, ok := result["items"]; ok {
		switch v := items.(type) {
		case map[string]interface{}:
			result["items"] = sanitizeOpenAISchema(v)
		case []interface{}:
			// Tuple items - process each
			newItems := make([]interface{}, len(v))
			for i, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					newItems[i] = sanitizeOpenAISchema(itemMap)
				} else {
					newItems[i] = item
				}
			}
			result["items"] = newItems
		}
	}

	// Process anyOf, oneOf, allOf recursively
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]interface{}); ok {
			newArr := make([]interface{}, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]interface{}); ok {
					newArr[i] = sanitizeOpenAISchema(itemMap)
				} else {
					newArr[i] = item
				}
			}
			result[key] = newArr
		}
	}

	return result
}

// SanitizeSchemaForAnthropic is a no-op passthrough.
// Placeholder for future cache control headers on tool definitions.
func SanitizeSchemaForAnthropic(raw json.RawMessage) json.RawMessage {
	return raw
}
