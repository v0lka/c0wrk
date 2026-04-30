package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdktools "github.com/user/agent/sdk/tools"
)

const toolReadSkillResourceDesc = `Reads a resource file from an activated skill's directory. Use this to access reference materials, scripts, or other files bundled with a skill. The skill must be currently active (matched by the router). Path traversal attempts are blocked.`

// SkillPathResolver resolves a skill name to its directory path.
// Returns ("", false) if the skill is not found or not active.
type SkillPathResolver func(skillName string) (dirPath string, ok bool)

// ReadSkillResourceTool reads files from activated skill directories.
type ReadSkillResourceTool struct {
	*sdktools.BaseTool
	resolvePath SkillPathResolver
}

// NewReadSkillResourceTool creates a tool that reads resources from skill directories.
func NewReadSkillResourceTool(resolver SkillPathResolver) *ReadSkillResourceTool {
	return &ReadSkillResourceTool{
		BaseTool: &sdktools.BaseTool{
			ToolName:        "read_skill_resource",
			ToolDescription: toolReadSkillResourceDesc,
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill": {
						"type": "string",
						"description": "Name of the active skill containing the resource."
					},
					"path": {
						"type": "string",
						"description": "Relative path within the skill directory (e.g., 'references/api.md', 'scripts/setup.sh')."
					}
				},
				"required": ["skill", "path"]
			}`),
			Policy: sdktools.PolicyAlwaysAllow, // skill resources are read-only, safe
		},
		resolvePath: resolver,
	}
}

// readSkillResourceInput holds the parsed tool input.
type readSkillResourceInput struct {
	Skill string `json:"skill"`
	Path  string `json:"path"`
}

// Execute reads the requested resource file from the skill directory.
func (t *ReadSkillResourceTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var parsed readSkillResourceInput
	if err := json.Unmarshal(input, &parsed); err != nil {
		return sdktools.ParseInputError(err)
	}

	if parsed.Skill == "" {
		return sdktools.ErrorResult("skill name is required"), nil
	}
	if parsed.Path == "" {
		return sdktools.ErrorResult("resource path is required"), nil
	}

	// Resolve the skill's directory path via the manager
	skillDir, ok := t.resolvePath(parsed.Skill)
	if !ok {
		return sdktools.ErrorResult("skill %q not found or not active", parsed.Skill), nil
	}

	// Resolve the resource path safely (prevent path traversal)
	absPath, err := resolveResourcePath(skillDir, parsed.Path)
	if err != nil {
		return sdktools.ErrorResult("invalid resource path: %v", err), nil
	}

	// Read the file
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return sdktools.ErrorResult("resource %q not found in skill %q", parsed.Path, parsed.Skill), nil
		}
		return sdktools.ErrorResult("failed to read resource: %v", err), nil
	}

	return sdktools.ToolResult{Content: string(data)}, nil
}

// resolveResourcePath resolves a relative path within a skill directory,
// preventing path traversal attacks.
func resolveResourcePath(skillDir, relPath string) (string, error) {
	absPath := filepath.Clean(filepath.Join(skillDir, relPath))
	cleanSkillDir := filepath.Clean(skillDir)

	rel, err := filepath.Rel(cleanSkillDir, absPath)
	if err != nil {
		return "", fmt.Errorf("path %q escapes skill directory", relPath)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes skill directory", relPath)
	}
	// rel == "." means absPath equals skillDir itself, which is acceptable
	return absPath, nil
}
