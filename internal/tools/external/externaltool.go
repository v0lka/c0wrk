package external

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/user/agent/internal/tools"
)

// Compile-time check that ExternalTool implements tools.Tool.
var _ tools.Tool = (*ExternalTool)(nil)

// Compile-time check that ExternalTool implements tools.ToolJudger.
var _ tools.ToolJudger = (*ExternalTool)(nil)

// ExternalTool wraps an external script (Python or Bash) as a tools.Tool.
type ExternalTool struct {
	manifest *ToolManifest
	toolDir  string // filesystem path to the tool directory
}

// NewExternalTool creates a new ExternalTool from a parsed manifest and directory path.
func NewExternalTool(manifest *ToolManifest, toolDir string) *ExternalTool {
	return &ExternalTool{
		manifest: manifest,
		toolDir:  toolDir,
	}
}

// Name returns the tool name from the manifest.
func (t *ExternalTool) Name() string {
	return t.manifest.Name
}

// Description returns the tool description from the manifest.
func (t *ExternalTool) Description() string {
	return t.manifest.Description
}

// InputSchema returns the JSON-encoded input schema from the manifest.
func (t *ExternalTool) InputSchema() json.RawMessage {
	data, err := json.Marshal(t.manifest.InputSchema)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

// DefaultPolicy maps the manifest's default_policy string to a tools.ToolPolicy constant.
func (t *ExternalTool) DefaultPolicy() tools.ToolPolicy {
	return tools.ParseToolPolicy(t.manifest.DefaultPolicy)
}

// Execute runs the external tool by invoking its entry point script with the given input as stdin.
func (t *ExternalTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	// Determine interpreter based on language.
	var interpreter string
	switch t.manifest.Language {
	case "python":
		interpreter = "python3"
	case "bash":
		interpreter = "bash"
	default:
		return tools.ToolResult{
			Content: "unsupported language: " + t.manifest.Language,
			IsError: true,
		}, nil
	}

	// Build full path to the entry point script.
	entryPath := filepath.Join(t.toolDir, t.manifest.EntryPoint)

	cmd := exec.CommandContext(ctx, interpreter, entryPath)
	cmd.Dir = t.toolDir
	cmd.Stdin = bytes.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Distinguish context cancellation from script failure.
		if ctx.Err() != nil {
			return tools.ToolResult{}, ctx.Err()
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return tools.ToolResult{
			Content: errMsg,
			IsError: true,
		}, nil
	}

	return tools.ToolResult{
		Content: strings.TrimSpace(stdout.String()),
		IsError: false,
	}, nil
}

// Judge implements tools.ToolJudger for external tools.
// External scripts are opaque, so we always defer to the LLM Judge.
func (t *ExternalTool) Judge(_ context.Context, _ json.RawMessage) (allowed bool, reason string) {
	return false, "" // defer to LLM Judge
}
