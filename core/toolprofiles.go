package core

import (
	"encoding/json"

	tools "github.com/user/agent/sdk/tools"
)

// ToolProfiles defines the allowed tools per agent role.
// nil means all tools (no filtering). Empty slice means no tools.
var ToolProfiles = map[string][]string{
	"router":    {},                              // no tools needed - router only classifies requests
	"planner":   {"file_ops", "ripgrep", "glob"}, // read-only tools for gathering context
	"reflector": {"file_ops", "ripgrep", "glob", "read_evidence"}, // read-only + evidence access
	// "evaluator" — has its own filtering mechanism, don't override
	// "executor" — gets all tools (nil)
}

// readOnlyFileOpsActions lists the read-only actions in file_ops tool.
var readOnlyFileOpsActions = map[string]bool{
	"read_file":      true,
	"list_directory": true,
	"search_files":   true,
	"search_content": true,
}

// FilterToolsByProfile filters a list of tool descriptors based on the allowed tool names.
// If allowedNames is nil, returns all tools (no filtering).
// If allowedNames is empty slice, returns empty (no tools).
// For roles that include "file_ops", the file_ops schema is modified to only include read-only actions.
func FilterToolsByProfile(allTools []tools.ToolDescriptor, allowedNames []string) []tools.ToolDescriptor {
	// nil means all tools (no filtering)
	if allowedNames == nil {
		return allTools
	}

	// empty slice means no tools
	if len(allowedNames) == 0 {
		return []tools.ToolDescriptor{}
	}

	// Build lookup set for allowed tools
	allowSet := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowSet[name] = true
	}

	// Filter tools
	var filtered []tools.ToolDescriptor
	for _, tool := range allTools {
		if !allowSet[tool.Name] {
			continue
		}

		// For file_ops, strip write operations from schema if needed
		if tool.Name == "file_ops" {
			tool = filterFileOpsDescriptor(tool)
		}

		filtered = append(filtered, tool)
	}

	return filtered
}

// filterFileOpsDescriptor creates a read-only version of the file_ops tool descriptor
// by modifying the JSON schema to only include read-only actions.
func filterFileOpsDescriptor(original tools.ToolDescriptor) tools.ToolDescriptor {
	// Parse the schema
	var schema map[string]interface{}
	if err := json.Unmarshal(original.InputSchema, &schema); err != nil {
		// If we can't parse the schema, return as-is (shouldn't happen)
		return original
	}

	// Get the properties
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return original
	}

	// Get the action property
	actionProp, ok := properties["action"].(map[string]interface{})
	if !ok {
		return original
	}

	// Get the enum and filter to read-only actions
	enumRaw, ok := actionProp["enum"].([]interface{})
	if !ok {
		return original
	}

	var readOnlyEnum []interface{}
	for _, v := range enumRaw {
		if action, ok := v.(string); ok && readOnlyFileOpsActions[action] {
			readOnlyEnum = append(readOnlyEnum, v)
		}
	}

	// Update the enum with only read-only actions
	actionProp["enum"] = readOnlyEnum

	// Marshal back to JSON
	filteredSchema, err := json.Marshal(schema)
	if err != nil {
		// If marshaling fails, return original
		return original
	}

	return tools.ToolDescriptor{
		Name:        original.Name,
		Description: original.Description,
		InputSchema: filteredSchema,
		Source:      original.Source,
	}
}
