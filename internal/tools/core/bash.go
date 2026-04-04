package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"time"

	"github.com/user/agent/internal/tools"
)

const toolBashDescription = "Execute bash commands in a shell"

// BashExecTool executes bash commands in a shell.
type BashExecTool struct {
	*tools.BaseTool
	blacklist []string
	compiled  []*regexp.Regexp
}

// NewBashExecTool creates a new BashExecTool with the given blacklist.
func NewBashExecTool(blacklist []string) *BashExecTool {
	compiled := make([]*regexp.Regexp, 0, len(blacklist))
	for _, pattern := range blacklist {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue // Skip invalid patterns
		}
		compiled = append(compiled, re)
	}
	return &BashExecTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "bash_exec",
			ToolDescription: toolBashDescription,
			Schema:          json.RawMessage(`{"type": "object", "properties": {"command": {"type": "string", "description": "The bash command to execute"}, "timeout": {"type": "string", "description": "Timeout duration (default: 120s)"}, "working_directory": {"type": "string", "description": "Working directory for command execution"}}, "required": ["command"]}`),
			Policy:          tools.PolicyUserConfirm,
		},
		blacklist: blacklist,
		compiled:  compiled,
	}
}

// bashInput represents the input parameters for bash command execution.
type bashInput struct {
	Command          string `json:"command"`
	Timeout          string `json:"timeout"`
	WorkingDirectory string `json:"working_directory"`
}

// Judge evaluates whether a bash command is safe to execute.
// It checks the command against compiled blacklist patterns.
func (t *BashExecTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params bashInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, "" // Defer to LLM Judge on parse error
	}

	for i, re := range t.compiled {
		if re.MatchString(params.Command) {
			return false, "command matches blacklist pattern: " + t.blacklist[i]
		}
	}

	return false, "" // No match, defer to LLM Judge
}

// Execute runs the bash command and returns the result.
func (t *BashExecTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params bashInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	// Parse timeout (default 120s)
	timeoutStr := params.Timeout
	if timeoutStr == "" {
		timeoutStr = "120s"
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid timeout duration: %v", err),
			IsError: true,
		}, nil
	}

	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", params.Command)

	// Set working directory if specified
	if params.WorkingDirectory != "" {
		cmd.Dir = params.WorkingDirectory
	}

	// Execute and capture combined output
	output, err := cmd.CombinedOutput()
	if err != nil {
		return tools.ToolResult{
			Content: string(output) + "\n" + err.Error(),
			IsError: true,
		}, nil
	}

	return tools.ToolResult{
		Content: string(output),
		IsError: false,
	}, nil
}
