// Package coretools provides c0wrk-specific tool implementations including web search, web fetch, tool creation, and ask-user.
package coretools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	tools "github.com/user/agent/sdk/tools"
)

const toolToolcreatorDescription = "Create a new external tool (Python or Bash)"

// validNameRegex matches alphanumeric characters and underscores, starting with a letter.
var validNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// ToolCreatorTool creates new external tools (Python or Bash).
type ToolCreatorTool struct {
	*tools.BaseTool
	toolsDir string              // base dir, e.g. ~/.c0wrk/tools/
	registry *tools.ToolRegistry // registry for registering new tools
}

// NewToolCreatorTool creates a new ToolCreatorTool with the given configuration.
func NewToolCreatorTool(toolsDir string, registry *tools.ToolRegistry) *ToolCreatorTool {
	return &ToolCreatorTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "tool_creator",
			ToolDescription: toolToolcreatorDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Tool name (alphanumeric and underscores, starts with letter)"
		},
		"description": {
			"type": "string",
			"description": "What the tool does"
		},
		"code": {
			"type": "string",
			"description": "Source code for the tool entry point"
		},
		"language": {
			"type": "string",
			"description": "Programming language: python or bash (default: python)",
			"enum": ["python", "bash"]
		}
	},
	"required": ["name", "description", "code"]
}`),
			Policy: tools.PolicyAlwaysAllow,
		},
		toolsDir: toolsDir,
		registry: registry,
	}
}

// toolCreatorInput represents the input parameters for tool creation.
type toolCreatorInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Code        string `json:"code"`
	Language    string `json:"language"`
}

// toolCreatorManifest represents the tool.json manifest file.
type toolCreatorManifest struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Version     string         `json:"version"`
	Language    string         `json:"language"`
	EntryPoint  string         `json:"entry_point"`
	InputSchema map[string]any `json:"input_schema"`
	CreatedAt   string         `json:"created_at"`
	CreatedBy   string         `json:"created_by"`
}

// Execute creates a new tool with the given parameters.
func (t *ToolCreatorTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params toolCreatorInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Validate required parameters
	if params.Name == "" {
		return tools.ToolResult{
			Content: "missing required parameter: name",
			IsError: true,
		}, nil
	}
	if params.Description == "" {
		return tools.ToolResult{
			Content: "missing required parameter: description",
			IsError: true,
		}, nil
	}
	if params.Code == "" {
		return tools.ToolResult{
			Content: "missing required parameter: code",
			IsError: true,
		}, nil
	}

	// Default language to python
	if params.Language == "" {
		params.Language = "python"
	}

	// Validate language
	if params.Language != "python" && params.Language != "bash" {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid language '%s': must be 'python' or 'bash'", params.Language),
			IsError: true,
		}, nil
	}

	// Validate tool name
	if !validNameRegex.MatchString(params.Name) {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid tool name '%s': must contain only alphanumeric characters and underscores, and start with a letter", params.Name),
			IsError: true,
		}, nil
	}

	// Determine entry point based on language
	var entryPoint string
	if params.Language == "python" {
		entryPoint = "main.py"
	} else {
		entryPoint = "main.sh"
	}

	// Create tool directory
	toolDir := filepath.Join(t.toolsDir, params.Name)
	if _, err := os.Stat(toolDir); !os.IsNotExist(err) {
		return tools.ToolResult{
			Content: "tool directory already exists: " + toolDir,
			IsError: true,
		}, nil
	}

	// Create the directory
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to create tool directory: %v", err),
			IsError: true,
		}, nil
	}

	// Cleanup on failure
	var cleanupNeeded bool
	defer func() {
		if cleanupNeeded {
			_ = os.RemoveAll(toolDir)
		}
	}()

	// Write tool.json manifest
	manifest := toolCreatorManifest{
		Name:        params.Name,
		Description: params.Description,
		Version:     "1.0.0",
		Language:    params.Language,
		EntryPoint:  entryPoint,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		CreatedBy: "agent",
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to marshal manifest: %v", err),
			IsError: true,
		}, nil
	}

	manifestPath := filepath.Join(toolDir, "tool.json")
	if err := os.WriteFile(manifestPath, manifestJSON, 0o644); err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to write tool.json: %v", err),
			IsError: true,
		}, nil
	}

	// Write entry point file
	entryPointPath := filepath.Join(toolDir, entryPoint)
	if err := os.WriteFile(entryPointPath, []byte(params.Code), 0o644); err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to write %s: %v", entryPoint, err),
			IsError: true,
		}, nil
	}

	// Write to audit log
	if err := t.writeAuditLog(params.Name, "created"); err != nil {
		// Log warning but don't fail
		_ = err
	}

	// Success - don't cleanup
	cleanupNeeded = false

	return tools.ToolResult{
		Content: fmt.Sprintf("Tool '%s' created successfully at %s", params.Name, toolDir),
		IsError: false,
	}, nil
}

// writeAuditLog appends an entry to the audit log file.
func (t *ToolCreatorTool) writeAuditLog(toolName, action string) error {
	// Ensure tools directory exists
	if err := os.MkdirAll(t.toolsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create tools directory: %w", err)
	}

	auditLogPath := filepath.Join(t.toolsDir, "audit.log")
	f, err := os.OpenFile(auditLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer func() { _ = f.Close() }()

	timestamp := time.Now().UTC().Format(time.RFC3339)
	entry := fmt.Sprintf("%s\t%s\t%s\n", timestamp, toolName, action)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write audit log entry: %w", err)
	}

	return nil
}
