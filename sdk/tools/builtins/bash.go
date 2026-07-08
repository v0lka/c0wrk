//go:build !windows

package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

const toolBashDescription = `Execute shell commands via bash -c. Use this for build commands, running scripts, installing packages, git operations, and system tasks. Returns combined stdout and stderr. Commands time out after 60 seconds by default (configurable up to 120s). An optional working_directory can be set for the command's execution context.`

// BashExecTool executes bash commands in a shell.
type BashExecTool struct {
	*tools.BaseTool
	blacklist []string
	compiled  []*regexp.Regexp
	timeouts  BashTimeouts
}

// NewBashExecTool creates a new BashExecTool with the given blacklist.
func NewBashExecTool(blacklist []string) (*BashExecTool, error) {
	return NewBashExecToolWithTimeouts(blacklist, DefaultBashTimeouts())
}

// NewBashExecToolWithTimeouts creates a new BashExecTool with the given blacklist and timeouts.
func NewBashExecToolWithTimeouts(blacklist []string, timeouts BashTimeouts) (*BashExecTool, error) {
	compiled := make([]*regexp.Regexp, 0, len(blacklist))
	for _, pattern := range blacklist {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid bash blacklist pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	return &BashExecTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "bash_exec",
			ToolDescription: toolBashDescription,
			Schema:          json.RawMessage(`{"type": "object", "properties": {"command": {"type": "string", "description": "The bash command to execute. Supports pipes, redirects, and chained commands."}, "timeout": {"type": "string", "description": "Timeout as a Go duration string, e.g. \"30s\" or \"2m\". Default: 60s, maximum: 120s."}, "working_directory": {"type": "string", "description": "Absolute path to use as the working directory for command execution. If omitted, defaults to the workspace root when available."}}, "required": ["command"]}`),
			Policy:          tools.PolicyUserConfirm,
			Untrusted:       true,
		},
		blacklist: blacklist,
		compiled:  compiled,
		timeouts:  timeouts,
	}, nil
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

	if params.Command == "" {
		return tools.ToolResult{Content: "validation error: command is required", IsError: true}, nil
	}

	// Parse timeout (default 60s, max from config)
	command := params.Command
	timeoutStr := params.Timeout
	if timeoutStr == "" {
		timeoutStr = "60s"
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return tools.ToolResult{
			Content: fmt.Sprintf("invalid timeout duration: %v", err),
			IsError: true,
		}, nil
	}
	// Enforce maximum timeout from config
	if timeout > t.timeouts.MaxTimeout {
		timeout = t.timeouts.MaxTimeout
	}

	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Create command
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", command)

	// Put the command and all children in a new process group so we can
	// kill the entire tree on timeout (exec.CommandContext only kills the
	// parent, leaving orphaned children that hold pipes open).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Cancel kills the entire process group instead of just the parent.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Grace period for pipe readers to drain after the process group is killed.
	cmd.WaitDelay = t.timeouts.WaitDelay

	// Set working directory: prefer explicit param, fall back to workspace root.
	// Validate that the resolved directory is within the workspace or the system
	// temp directory to prevent arbitrary filesystem access (S-2).
	workDir := params.WorkingDirectory
	if workDir == "" {
		workDir = tools.WorkspacePathFrom(ctx)
	}
	if workDir != "" {
		if err := validateWorkDir(workDir, tools.WorkspacePathFrom(ctx), tools.TempDirFrom(ctx)); err != nil {
			return tools.ToolResult{
				Content: fmt.Sprintf("working_directory rejected: %v", err),
				IsError: true,
			}, nil
		}
		cmd.Dir = workDir
	}

	// Execute and capture combined output
	output, err := cmd.CombinedOutput()

	if err != nil {
		result := string(output) + "\n" + err.Error()
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			result += "\n[Process killed: timeout exceeded]"
		}
		return tools.ToolResult{
			Content: result,
			IsError: true,
		}, nil
	}

	return tools.ToolResult{
		Content: string(output),
		IsError: false,
	}, nil
}
