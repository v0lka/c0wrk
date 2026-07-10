//go:build windows

package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"github.com/v0lka/sp4rk/tools"
)

const toolPoshDescription = `Execute commands via Windows PowerShell (powershell.exe -NoProfile -NonInteractive -Command). Use this for build commands, running scripts, installing packages, git operations, and system tasks on Windows. Returns combined stdout and stderr. Commands time out after 60 seconds by default (configurable up to 120s). An optional working_directory can be set for the command's execution context.`

// PoshExecTool executes PowerShell commands via powershell.exe on Windows.
// It is the Windows counterpart of BashExecTool: same blacklist/Judge model,
// same timeout/working_directory containment rules, but adapted to the
// Windows process model.
type PoshExecTool struct {
	*tools.BaseTool
	blacklist []string
	compiled  []*regexp.Regexp
	timeouts  BashTimeouts
}

// createNewProcessGroup is the Windows process creation flag (0x00000200)
// that places the child in a new process group, isolating it from the
// parent's console group so a Ctrl+C sent to the host does not propagate.
const createNewProcessGroup = 0x00000200

// NewPoshExecTool creates a new PoshExecTool with the given blacklist and
// default timeouts.
func NewPoshExecTool(blacklist []string) (*PoshExecTool, error) {
	return NewPoshExecToolWithTimeouts(blacklist, DefaultBashTimeouts())
}

// NewPoshExecToolWithTimeouts creates a new PoshExecTool with the given
// blacklist and timeouts.
func NewPoshExecToolWithTimeouts(blacklist []string, timeouts BashTimeouts) (*PoshExecTool, error) {
	compiled := make([]*regexp.Regexp, 0, len(blacklist))
	for _, pattern := range blacklist {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid posh blacklist pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	return &PoshExecTool{
		BaseTool: &tools.BaseTool{
			ToolName:        "posh_exec",
			ToolDescription: toolPoshDescription,
			Schema:          json.RawMessage(`{"type": "object", "properties": {"command": {"type": "string", "description": "The PowerShell command to execute. Supports pipes, redirects, and chained commands."}, "timeout": {"type": "string", "description": "Timeout as a Go duration string, e.g. \"30s\" or \"2m\". Default: 60s, maximum: 120s."}, "working_directory": {"type": "string", "description": "Absolute path to use as the working directory for command execution. If omitted, defaults to the workspace root when available."}}, "required": ["command"]}`),
			Policy:          tools.PolicyUserConfirm,
			Untrusted:       true,
		},
		blacklist: blacklist,
		compiled:  compiled,
		timeouts:  timeouts,
	}, nil
}

// poshInput represents the input parameters for PowerShell command execution.
type poshInput struct {
	Command          string `json:"command"`
	Timeout          string `json:"timeout"`
	WorkingDirectory string `json:"working_directory"`
}

// Judge evaluates whether a PowerShell command is safe to execute.
// It checks the command against compiled blacklist patterns.
func (t *PoshExecTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	var params poshInput
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

// Execute runs the PowerShell command and returns the result.
func (t *PoshExecTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params poshInput
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

	// Create command: Windows PowerShell, no profile, non-interactive.
	cmd := exec.CommandContext(timeoutCtx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", command)

	// Place the command in a new process group. On Windows, killing the
	// entire process tree on timeout requires a Job Object
	// (golang.org/x/sys/windows), which sp4rk avoids to stay stdlib-only.
	// We therefore kill just the parent powershell.exe process and rely on
	// WaitDelay to drain any pipe readers. PowerShell -NonInteractive
	// -Command typically does not spawn long-lived children that outlive it.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}

	// Cancel kills the parent process instead of just the parent.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	// Grace period for pipe readers to drain after the process is killed.
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
		if timeoutCtx.Err() == context.DeadlineExceeded {
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
