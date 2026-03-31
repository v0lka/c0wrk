package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/user/agent/internal/tools"
)

const toolSkillcreatorDescription = "Create a new Python skill with Docker isolation"

// SkillBuilder interface to avoid importing skills package directly.
type SkillBuilder interface {
	Build(ctx context.Context, skillDir string, name string, version string) (string, error)
}

// SkillCreatorTool creates new Python skills with Docker isolation.
type SkillCreatorTool struct {
	skillsDir string              // base dir, e.g. ~/.c0wrk/skills/
	registry  *tools.ToolRegistry // tool registry for registering new skills
	builder   SkillBuilder        // optional Docker builder
}

// NewSkillCreatorTool creates a new SkillCreatorTool with the given configuration.
func NewSkillCreatorTool(skillsDir string, registry *tools.ToolRegistry, builder SkillBuilder) *SkillCreatorTool {
	return &SkillCreatorTool{
		skillsDir: skillsDir,
		registry:  registry,
		builder:   builder,
	}
}

// skillCreatorInput represents the input parameters for skill creation.
type skillCreatorInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Code         string   `json:"code"`
	Dependencies []string `json:"dependencies"`
	Capabilities []string `json:"capabilities"`
}

// skillManifest represents the skill.json manifest file.
type skillManifest struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	Version      string                 `json:"version"`
	Language     string                 `json:"language"`
	EntryPoint   string                 `json:"entry_point"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	OutputSchema map[string]interface{} `json:"output_schema"`
	Dependencies []string               `json:"dependencies"`
	Capabilities []string               `json:"capabilities"`
}

// validNameRegex matches alphanumeric characters and underscores.
var validNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)

// Name returns the tool name.
func (t *SkillCreatorTool) Name() string {
	return "skill_creator"
}

// Description returns the tool description.
func (t *SkillCreatorTool) Description() string {
	return toolSkillcreatorDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *SkillCreatorTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Skill name (alphanumeric characters and underscores, must start with a letter)"
		},
		"description": {
			"type": "string",
			"description": "What the skill does"
		},
		"code": {
			"type": "string",
			"description": "Python code for main.py"
		},
		"dependencies": {
			"type": "array",
			"items": {"type": "string"},
			"description": "List of pip package names"
		},
		"capabilities": {
			"type": "array",
			"items": {"type": "string"},
			"description": "List of capabilities needed (e.g., 'network', 'filesystem')"
		}
	},
	"required": ["name", "description", "code"]
}`)
}

// DefaultPolicy returns PolicyAlwaysAllow because skill creation is a service tool.
func (t *SkillCreatorTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAlwaysAllow
}

// Execute creates a new skill with the given parameters.
func (t *SkillCreatorTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params skillCreatorInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to parse input: %v", err),
			IsError: true,
		}, nil
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

	// Validate skill name
	if !validNameRegex.MatchString(params.Name) {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid skill name '%s': must contain only alphanumeric characters and underscores, and start with a letter", params.Name),
			IsError: true,
		}, nil
	}

	// Create skill directory
	skillDir := filepath.Join(t.skillsDir, params.Name)
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		return tools.ToolResult{
			Content: fmt.Sprintf("skill directory already exists: %s", skillDir),
			IsError: true,
		}, nil
	}

	// Create the directory
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to create skill directory: %v", err),
			IsError: true,
		}, nil
	}

	// Cleanup on failure
	var cleanupNeeded bool
	defer func() {
		if cleanupNeeded {
			_ = os.RemoveAll(skillDir)
		}
	}()

	// Write skill.json manifest
	manifest := skillManifest{
		Name:        params.Name,
		Description: params.Description,
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		OutputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		Dependencies: params.Dependencies,
		Capabilities: params.Capabilities,
	}

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to marshal manifest: %v", err),
			IsError: true,
		}, nil
	}

	manifestPath := filepath.Join(skillDir, "skill.json")
	if err := os.WriteFile(manifestPath, manifestJSON, 0644); err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to write skill.json: %v", err),
			IsError: true,
		}, nil
	}

	// Write main.py
	mainPyPath := filepath.Join(skillDir, "main.py")
	if err := os.WriteFile(mainPyPath, []byte(params.Code), 0644); err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to write main.py: %v", err),
			IsError: true,
		}, nil
	}

	// Write requirements.txt
	requirementsContent := strings.Join(params.Dependencies, "\n")
	if len(params.Dependencies) > 0 {
		requirementsContent += "\n"
	}
	requirementsPath := filepath.Join(skillDir, "requirements.txt")
	if err := os.WriteFile(requirementsPath, []byte(requirementsContent), 0644); err != nil {
		cleanupNeeded = true
		return tools.ToolResult{
			Content: fmt.Sprintf("failed to write requirements.txt: %v", err),
			IsError: true,
		}, nil
	}

	// Build Docker image if builder is available
	var buildResult string
	if t.builder != nil {
		imageTag, err := t.builder.Build(ctx, skillDir, params.Name, "1.0.0")
		if err != nil {
			// Log warning but don't fail - skill files are already created
			buildResult = fmt.Sprintf("Warning: Docker build failed: %v", err)
		} else {
			buildResult = fmt.Sprintf("Docker image built: %s", imageTag)
		}
	} else {
		buildResult = "Docker builder not available, skipping image build"
	}

	// Write to audit log
	if err := t.writeAuditLog(params.Name, "created"); err != nil {
		// Log warning but don't fail
		buildResult += fmt.Sprintf("\nWarning: failed to write audit log: %v", err)
	}

	// Success - don't cleanup
	cleanupNeeded = false

	return tools.ToolResult{
		Content: fmt.Sprintf("Skill '%s' created successfully at %s\n%s", params.Name, skillDir, buildResult),
		IsError: false,
	}, nil
}

// writeAuditLog appends an entry to the audit log file.
func (t *SkillCreatorTool) writeAuditLog(skillName, action string) error {
	// Ensure skills directory exists
	if err := os.MkdirAll(t.skillsDir, 0755); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	auditLogPath := filepath.Join(t.skillsDir, "audit.log")
	f, err := os.OpenFile(auditLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open audit log: %w", err)
	}
	defer func() { _ = f.Close() }()

	timestamp := time.Now().UTC().Format(time.RFC3339)
	entry := fmt.Sprintf("%s\t%s\t%s\n", timestamp, skillName, action)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write audit log entry: %w", err)
	}

	return nil
}
