package llm

import (
	"encoding/json"
	"reflect"
	"sort"
)

// getTypes extracts type values as a slice of strings.
func getTypes(typeVal any) []string {
	if typeVal == nil {
		return nil
	}

	switch v := typeVal.(type) {
	case string:
		return []string{v}
	case []any:
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

// SanitizeSchemaForOpenAI ensures strict mode compliance for OpenAI.
//   - Resolves $ref references against $defs/definitions
//   - Filters out forbidden JSON Schema keywords ($schema, $id, $comment, $defs, definitions, default, examples)
//   - Infers "type": "object" when properties or required are present but type is missing
//   - Adds "additionalProperties": false to all object-type schemas (recursively)
//   - Ensures the required array contains ALL property names from properties
//   - Removes required entries that reference non-existent properties
func SanitizeSchemaForOpenAI(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}

	defs := extractDefs(schema)
	sanitized := sanitizeOpenAISchemaWithDefs(schema, defs)
	result, err := json.Marshal(sanitized)
	if err != nil {
		return raw
	}
	return result
}

// extractDefs pulls $defs or definitions from the schema for $ref resolution.
func extractDefs(schema map[string]any) map[string]any {
	if d, ok := schema["$defs"].(map[string]any); ok {
		return d
	}
	if d, ok := schema["definitions"].(map[string]any); ok {
		return d
	}
	return nil
}

// resolveRef replaces a $ref schema with the referenced definition.
// If the $ref target is not found, returns a safe strict-mode fallback.
func resolveRef(schema, defs map[string]any) map[string]any {
	ref, ok := schema["$ref"].(string)
	if !ok || defs == nil {
		return schema
	}

	// Parse JSON Pointer: "#/$defs/Name" or "#/definitions/Name"
	parts := splitRefPath(ref)
	if len(parts) == 0 {
		return safeFallbackSchema()
	}
	name := parts[len(parts)-1]

	defRaw, found := defs[name]
	if !found {
		return safeFallbackSchema()
	}
	defMap, ok := defRaw.(map[string]any)
	if !ok {
		return safeFallbackSchema()
	}

	// Return a copy so we don't mutate the definitions
	cp := make(map[string]any, len(defMap))
	for k, v := range defMap {
		cp[k] = v
	}
	return cp
}

// resolveRefRecursive replaces $ref with the referenced definition and recurses
// to resolve any nested $ref chains within the resolved definition itself.
func resolveRefRecursive(schema, defs map[string]any) map[string]any {
	return resolveRefRecursiveWithVisited(schema, defs, nil)
}

// resolveRefRecursiveWithVisited is the internal implementation that tracks
// visited definition names to detect and break cycles.
//
// Cycle detection: when a schema is a $ref, the target definition name is
// recorded on a private copy of the visited set before descending into the
// resolved content. This serves two purposes:
//   - A self-referential schema (e.g. a tree node whose "children" items
//     reference the same definition) terminates with a safe fallback instead
//     of recursing forever. Without this, the visited set is never populated
//     because the resolved definition is a full object (not a bare $ref), so
//     the chain-detection loop below never runs.
//   - Sibling properties do not pollute each other's visited sets: each $ref
//     resolution copies the ancestor set, so one sibling's resolution cannot
//     cause a false cycle in another sibling.
func resolveRefRecursiveWithVisited(schema, defs map[string]any, visited map[string]struct{}) map[string]any {
	// Record the current $ref target on a copy of the visited set so that
	// nested back-references to the same definition are detected as cycles.
	if ref, ok := schema["$ref"].(string); ok && defs != nil {
		if parts := splitRefPath(ref); len(parts) > 0 {
			name := parts[len(parts)-1]
			if visited == nil {
				visited = make(map[string]struct{})
			}
			if _, seen := visited[name]; seen {
				return safeFallbackSchema()
			}
			visited = copyVisitedSet(visited)
			visited[name] = struct{}{}
		}
	}

	resolved := resolveRef(schema, defs)

	// Re-resolve if the resolved schema is itself just another $ref chain.
	// Track visited definition names to break cycles (e.g. A→B→C→A).
	if visited == nil {
		visited = make(map[string]struct{})
	}
	for {
		ref, ok := resolved["$ref"].(string)
		if !ok {
			break
		}
		parts := splitRefPath(ref)
		if len(parts) == 0 {
			break
		}
		name := parts[len(parts)-1]
		if _, seen := visited[name]; seen {
			// Cycle detected: the resolved definition points back to
			// an already-visited name. Return a safe fallback schema
			// instead of the bare $ref (which would be filtered to
			// an empty map by callers).
			return safeFallbackSchema()
		}
		visited[name] = struct{}{}
		next := resolveRef(resolved, defs)
		if reflect.DeepEqual(next, resolved) {
			// No progress; self-reference or already fully resolved.
			// If the result is still just a bare $ref with no useful
			// content, return a safe fallback.
			if _, stillRef := resolved["$ref"]; stillRef && len(resolved) == 1 {
				return safeFallbackSchema()
			}
			break
		}
		resolved = next
	}

	// Recursively process individual property values (not the container itself).
	if props, ok := resolved["properties"].(map[string]any); ok {
		newProps := make(map[string]any, len(props))
		changed := false
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				newProps[propName] = resolveRefRecursiveWithVisited(propMap, defs, visited)
				changed = true
			} else {
				newProps[propName] = propVal
			}
		}
		if changed {
			resolved = copyMap(resolved)
			resolved["properties"] = newProps
		}
	}

	// Recursively process items (both object and tuple form).
	if items, ok := resolved["items"]; ok {
		switch v := items.(type) {
		case map[string]any:
			resolved = copyMap(resolved)
			resolved["items"] = resolveRefRecursiveWithVisited(v, defs, visited)
		case []any:
			newItems := make([]any, len(v))
			changed := false
			for i, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					newItems[i] = resolveRefRecursiveWithVisited(itemMap, defs, visited)
					changed = true
				} else {
					newItems[i] = item
				}
			}
			if changed {
				resolved = copyMap(resolved)
				resolved["items"] = newItems
			}
		}
	}

	// Recursively process additionalProperties if it's a schema object.
	if addProps, ok := resolved["additionalProperties"].(map[string]any); ok {
		resolved = copyMap(resolved)
		resolved["additionalProperties"] = resolveRefRecursiveWithVisited(addProps, defs, visited)
	}

	// Recursively process composition keywords.
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := resolved[key].([]any); ok {
			newArr := make([]any, len(arr))
			changed := false
			for i, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					newArr[i] = resolveRefRecursiveWithVisited(itemMap, defs, visited)
					changed = true
				} else {
					newArr[i] = item
				}
			}
			if changed {
				resolved = copyMap(resolved)
				resolved[key] = newArr
			}
		}
	}

	return resolved
}

// copyMap creates a shallow copy of a map.
func copyMap(m map[string]any) map[string]any {
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// copyVisitedSet returns a shallow copy of a cycle-detection visited set.
func copyVisitedSet(visited map[string]struct{}) map[string]struct{} {
	cp := make(map[string]struct{}, len(visited)+1)
	for k := range visited {
		cp[k] = struct{}{}
	}
	return cp
}

// splitRefPath splits a $ref string like "#/$defs/Foo" into path segments after "#".
func splitRefPath(ref string) []string {
	// Trim leading "#/"
	if len(ref) < 2 || ref[0] != '#' {
		return nil
	}
	trimmed := ref[1:]
	if trimmed != "" && trimmed[0] == '/' {
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return nil
	}

	// Split on "/"
	var parts []string
	start := 0
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '/' {
			if i > start {
				parts = append(parts, trimmed[start:i])
			}
			start = i + 1
		}
	}
	if start < len(trimmed) {
		parts = append(parts, trimmed[start:])
	}
	return parts
}

// safeFallbackSchema returns a minimal strict-mode-compatible object schema.
func safeFallbackSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"required":             []any{},
		"additionalProperties": false,
	}
}

// sanitizeOpenAISchemaWithDefs recursively processes a schema map for OpenAI strict mode,
// carrying top-level definitions for $ref resolution.
func sanitizeOpenAISchemaWithDefs(schema, defs map[string]any) map[string]any {
	if schema == nil {
		return nil
	}

	// Resolve $ref before processing, including nested $ref chains
	schema = resolveRefRecursive(schema, defs)

	// Copy keys, filtering out forbidden keywords
	result := make(map[string]any)
	for key, value := range schema {
		switch key {
		case "$schema", "$id", "$comment", "$defs", "definitions", "default", "examples":
			continue
		default:
			result[key] = value
		}
	}

	// Infer object type if type is missing/empty but has object indicators
	typeVal := result["type"]
	types := getTypes(typeVal)
	_, hasProperties := result["properties"]
	_, hasRequired := result["required"]

	if (len(types) == 0 || (len(types) == 1 && types[0] == "")) && (hasProperties || hasRequired) {
		result["type"] = "object"
		types = []string{"object"}
	}
	// Clean up empty type string
	if len(types) == 1 && types[0] == "" {
		delete(result, "type")
	}

	isObjectType := len(types) == 1 && types[0] == "object"

	// For object types, enforce strict mode constraints
	if isObjectType {
		// ALWAYS force additionalProperties to false
		result["additionalProperties"] = false

		// Ensure properties exists
		if _, hasProps := result["properties"].(map[string]any); !hasProps {
			result["properties"] = map[string]any{}
		}

		// Ensure required array contains ALL property names (strict mode).
		// Phase 1: filter out required entries that don't exist in properties.
		// Phase 2: add any property names missing from required.
		props, propsOK := result["properties"].(map[string]any)
		if propsOK && len(props) > 0 {
			// Phase 1: keep only valid existing required entries
			requiredSet := make(map[string]struct{})
			if required, ok := result["required"].([]any); ok {
				for _, req := range required {
					if reqStr, ok := req.(string); ok {
						if _, exists := props[reqStr]; exists {
							requiredSet[reqStr] = struct{}{}
						}
					}
				}
			}

			// Phase 2: add any missing property names
			for propName := range props {
				requiredSet[propName] = struct{}{}
			}

			// Build sorted required list for deterministic output
			sortedNames := make([]string, 0, len(requiredSet))
			for name := range requiredSet {
				sortedNames = append(sortedNames, name)
			}
			sort.Strings(sortedNames)
			allRequired := make([]any, len(sortedNames))
			for i, name := range sortedNames {
				allRequired[i] = name
			}
			result["required"] = allRequired
		} else {
			// No properties or empty properties: strict mode still requires the array
			result["required"] = []any{}
		}
	}

	// Process nested objects in properties
	if props, ok := result["properties"].(map[string]any); ok {
		newProps := make(map[string]any)
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				newProps[propName] = sanitizeOpenAISchemaWithDefs(propMap, defs)
			} else {
				newProps[propName] = propVal
			}
		}
		result["properties"] = newProps
	}

	// Process items for array type
	if items, ok := result["items"]; ok {
		switch v := items.(type) {
		case map[string]any:
			result["items"] = sanitizeOpenAISchemaWithDefs(v, defs)
		case []any:
			newItems := make([]any, len(v))
			for i, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					newItems[i] = sanitizeOpenAISchemaWithDefs(itemMap, defs)
				} else {
					newItems[i] = item
				}
			}
			result["items"] = newItems
		}
	}

	// Process anyOf, oneOf, allOf recursively
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]any); ok {
			newArr := make([]any, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					newArr[i] = sanitizeOpenAISchemaWithDefs(itemMap, defs)
				} else {
					newArr[i] = item
				}
			}
			result[key] = newArr
		}
	}

	return result
}

// SanitizeSchemaForAnthropic normalizes JSON Schema for Anthropic API compatibility.
// Anthropic has specific requirements:
//   - Unsupported keywords ($schema, $id, $comment, patternProperties) are removed
//   - $ref references are resolved inline from $defs/definitions
//   - $defs/definitions are removed after resolution
//   - Top-level allOf single item is unwrapped; multiple items are merged
//   - Top-level oneOf/anyOf picks the first variant
//   - Type "object" is inferred when properties/required present but type missing
//   - Recursively processes properties, items, and additionalProperties
func SanitizeSchemaForAnthropic(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}

	defs := extractDefs(schema)
	sanitized := sanitizeAnthropicSchema(schema, defs)
	result, err := json.Marshal(sanitized)
	if err != nil {
		return raw
	}
	return result
}

// sanitizeAnthropicSchema recursively processes a schema map for Anthropic compatibility.
func sanitizeAnthropicSchema(schema, defs map[string]any) map[string]any {
	if schema == nil {
		return nil
	}

	// Resolve $ref before processing, including nested $ref chains
	schema = resolveRefRecursive(schema, defs)

	// Flatten top-level composition keywords before filtering
	schema = flattenAnthropicComposition(schema, defs)

	// Copy keys, filtering out unsupported keywords
	result := make(map[string]any)
	for key, value := range schema {
		switch key {
		case "$schema", "$id", "$comment", "$defs", "definitions", "patternProperties", "$ref":
			continue
		default:
			result[key] = value
		}
	}

	// Infer object type if type is missing but has object indicators
	typeVal := result["type"]
	types := getTypes(typeVal)
	_, hasProperties := result["properties"]
	_, hasRequired := result["required"]

	if len(types) == 0 && (hasProperties || hasRequired) {
		result["type"] = "object"
	}

	// Process nested objects in properties
	if props, ok := result["properties"].(map[string]any); ok {
		newProps := make(map[string]any)
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				newProps[propName] = sanitizeAnthropicSchema(propMap, defs)
			} else {
				newProps[propName] = propVal
			}
		}
		result["properties"] = newProps
	}

	// Process items for array type
	if items, ok := result["items"]; ok {
		switch v := items.(type) {
		case map[string]any:
			result["items"] = sanitizeAnthropicSchema(v, defs)
		case []any:
			newItems := make([]any, len(v))
			for i, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					newItems[i] = sanitizeAnthropicSchema(itemMap, defs)
				} else {
					newItems[i] = item
				}
			}
			result["items"] = newItems
		}
	}

	// Process additionalProperties if it's a schema object
	if addProps, ok := result["additionalProperties"].(map[string]any); ok {
		result["additionalProperties"] = sanitizeAnthropicSchema(addProps, defs)
	}

	// Process anyOf, oneOf, allOf recursively (nested, non-top-level)
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]any); ok {
			newArr := make([]any, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					newArr[i] = sanitizeAnthropicSchema(itemMap, defs)
				} else {
					newArr[i] = item
				}
			}
			result[key] = newArr
		}
	}

	return result
}

// flattenAnthropicComposition flattens top-level allOf/oneOf/anyOf for Anthropic.
//   - allOf single item → unwrap
//   - allOf multiple → merge properties (union)
//   - oneOf/anyOf → pick first variant
func flattenAnthropicComposition(schema, defs map[string]any) map[string]any {
	// Handle allOf
	if allOf, ok := schema["allOf"].([]any); ok && len(allOf) > 0 {
		if len(allOf) == 1 {
			// Single item: unwrap into parent
			if item, ok := allOf[0].(map[string]any); ok {
				merged := mergeSchemas(schema, resolveRef(item, defs))
				delete(merged, "allOf")
				return merged
			}
		} else {
			// Multiple items: merge all into parent
			merged := copySchemaExcluding(schema, "allOf")
			for _, entry := range allOf {
				if item, ok := entry.(map[string]any); ok {
					merged = mergeSchemas(merged, resolveRef(item, defs))
				}
			}
			return merged
		}
	}

	// Handle oneOf: pick first variant
	if oneOf, ok := schema["oneOf"].([]any); ok && len(oneOf) > 0 {
		if item, ok := oneOf[0].(map[string]any); ok {
			merged := mergeSchemas(schema, resolveRef(item, defs))
			delete(merged, "oneOf")
			return merged
		}
	}

	// Handle anyOf: pick first variant
	if anyOf, ok := schema["anyOf"].([]any); ok && len(anyOf) > 0 {
		if item, ok := anyOf[0].(map[string]any); ok {
			merged := mergeSchemas(schema, resolveRef(item, defs))
			delete(merged, "anyOf")
			return merged
		}
	}

	return schema
}

// mergeSchemas merges src into dst. Properties are unioned; other fields from src
// overwrite dst only if not already set (dst takes precedence for scalars).
func mergeSchemas(dst, src map[string]any) map[string]any {
	result := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		result[k] = v
	}

	for k, v := range src {
		if k == "properties" {
			// Union properties
			dstProps, _ := result["properties"].(map[string]any)
			srcProps, _ := v.(map[string]any)
			if srcProps != nil {
				if dstProps == nil {
					dstProps = make(map[string]any)
				}
				for propName, propVal := range srcProps {
					if _, exists := dstProps[propName]; !exists {
						dstProps[propName] = propVal
					}
				}
				result["properties"] = dstProps
			}
		} else if k == "required" {
			// Union required arrays
			dstReq := toStringSlice(result["required"])
			srcReq := toStringSlice(v)
			seen := make(map[string]struct{})
			for _, r := range dstReq {
				seen[r] = struct{}{}
			}
			for _, r := range srcReq {
				if _, exists := seen[r]; !exists {
					dstReq = append(dstReq, r)
					seen[r] = struct{}{}
				}
			}
			anySlice := make([]any, len(dstReq))
			for i, s := range dstReq {
				anySlice[i] = s
			}
			result["required"] = anySlice
		} else if _, exists := result[k]; !exists {
			result[k] = v
		}
	}
	return result
}

// copySchemaExcluding copies a schema map excluding a specific key.
func copySchemaExcluding(schema map[string]any, excludeKey string) map[string]any {
	result := make(map[string]any, len(schema))
	for k, v := range schema {
		if k != excludeKey {
			result[k] = v
		}
	}
	return result
}

// toStringSlice extracts a []string from an any that is expected to be []any of strings.
func toStringSlice(val any) []string {
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
