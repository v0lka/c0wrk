//go:build !windows

package builtins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

const toolBashDescription = `Execute shell commands via bash -c. Use this for build commands, running scripts, installing packages, git operations, and system tasks not covered by dedicated tools. Prefer read_file/write_file/edit_file for file operations, glob for finding files by name, and ripgrep for searching file contents — only fall back to bash_exec when no higher-level tool fits. DO NOT use bash_exec for operations that can be performed using higher-level tools. Returns combined stdout and stderr. Commands time out after 60 seconds by default (configurable up to 120s). An optional working_directory can be set for the command's execution context.`

// BashExecTool executes bash commands in a shell.
type BashExecTool struct {
	*tools.BaseTool
	blacklist []string
	compiled  []*regexp.Regexp
	timeouts  BashTimeouts
	rtkPath   string
	rtkMu     sync.RWMutex
}

// NewBashExecTool creates a new BashExecTool with the given blacklist.
func NewBashExecTool(blacklist []string) *BashExecTool {
	return NewBashExecToolWithTimeouts(blacklist, DefaultBashTimeouts(), "")
}

// NewBashExecToolWithTimeouts creates a new BashExecTool with the given blacklist and timeouts.
func NewBashExecToolWithTimeouts(blacklist []string, timeouts BashTimeouts, rtkPath string) *BashExecTool {
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
			Schema:          json.RawMessage(`{"type": "object", "properties": {"command": {"type": "string", "description": "The bash command to execute. Supports pipes, redirects, and chained commands."}, "timeout": {"type": "string", "description": "Timeout as a Go duration string, e.g. \"30s\" or \"2m\". Default: 60s, maximum: 120s."}, "working_directory": {"type": "string", "description": "Absolute path to use as the working directory for command execution. If omitted, defaults to the workspace root when available."}}, "required": ["command"]}`),
			Policy:          tools.PolicyUserConfirm,
		},
		blacklist: blacklist,
		compiled:  compiled,
		timeouts:  timeouts,
		rtkPath:   rtkPath,
	}
}

// SetRtkPath updates the rtk binary path at runtime (thread-safe).
func (t *BashExecTool) SetRtkPath(path string) {
	t.rtkMu.Lock()
	defer t.rtkMu.Unlock()
	t.rtkPath = path
}

// getRtkPath returns the current rtk binary path (thread-safe).
func (t *BashExecTool) getRtkPath() string {
	t.rtkMu.RLock()
	defer t.rtkMu.RUnlock()
	return t.rtkPath
}

// rtkRewrite calls `rtk rewrite` to get an optimized version of the command.
// Returns the rewritten command, or empty string if no rewrite applies or on any error.
func (t *BashExecTool) rtkRewrite(ctx context.Context, rtkPath, command string) string {
	rtkTimeout := t.timeouts.RtkTimeout
	if rtkTimeout == 0 {
		rtkTimeout = 500 * time.Millisecond
	}
	rewriteCtx, cancel := context.WithTimeout(ctx, rtkTimeout)
	defer cancel()

	cmd := exec.CommandContext(rewriteCtx, rtkPath, "rewrite", command)
	output, err := cmd.Output()
	if err != nil {
		slog.Debug("rtk rewrite failed, using original command", "error", err)
		return ""
	}

	rewritten := strings.TrimSpace(string(output))
	if rewritten == "" || rewritten == command {
		return ""
	}

	slog.Debug("rtk rewrote command", "original", command, "rewritten", rewritten)
	return rewritten
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

	// RTK command rewrite (if available)
	command := params.Command
	if rtkPath := t.getRtkPath(); rtkPath != "" {
		if rewritten := t.rtkRewrite(ctx, rtkPath, command); rewritten != "" {
			command = rewritten
		}
	}

	// Parse timeout (default 60s, max from config)
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

	// Pre-bash snapshot (if tracker available)
	tracker := agent.FileTrackerFromContext(ctx)
	var snapshot *agent.WorkspaceSnapshot
	if tracker != nil {
		snapshot = tracker.TakeSnapshot()
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

	// Set working directory: prefer explicit param, fall back to workspace root
	workDir := params.WorkingDirectory
	if workDir == "" {
		workDir = tools.WorkspacePathFrom(ctx)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Execute and capture combined output
	output, err := cmd.CombinedOutput()

	// Post-bash change detection
	if tracker != nil && snapshot != nil {
		tracker.DetectChangesFrom(ctx, snapshot)
	}

	if err != nil {
		result := string(output) + "\n" + err.Error()
		if timeoutCtx.Err() == context.DeadlineExceeded ||
			strings.Contains(err.Error(), "signal: killed") {
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
