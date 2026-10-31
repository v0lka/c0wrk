package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"

	"github.com/v0lka/c0wrk/core/tools"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// verifyOnEditDefaultTimeout is the fallback for an empty/invalid
// executor.verify_on_edit.timeout. See specs/domains/verify-on-edit.md.
const verifyOnEditDefaultTimeout = 2 * time.Minute

// exitStatusRe matches Go's exec.ExitError text ("exit status N"), which the
// platform shell tool (bash_exec/posh_exec) appends to a failed command's
// output on both halves of the build-tagged split.
var exitStatusRe = regexp.MustCompile(`exit status (\d+)$`)

// parseVerifyOnEditTimeout parses the configured duration; empty or invalid
// falls back to the default (invalid logs at the caller). See specs/domains/verify-on-edit.md.
func parseVerifyOnEditTimeout(raw string) (time.Duration, bool) {
	if strings.TrimSpace(raw) == "" {
		return verifyOnEditDefaultTimeout, true
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return verifyOnEditDefaultTimeout, false
	}
	return d, true
}

// buildEditVerifyRunner builds the agent.EditVerifyRunner behind
// executor.verify_on_edit (SetVerifyOnEdit): the USER-CONFIGURED command runs
// through the platform shell tool via ExecuteUnattended — confirmation skipped
// (configuring IS the approval; hard gates intact) — timeout clamped to
// timeouts.bashMaxTimeout (warned; echoed in the result). Nil when not
// configured (executor completely unhooked). See specs/domains/verify-on-edit.md.
func buildEditVerifyRunner(
	registry *tools.ToolRegistry,
	workspace string,
	command string,
	timeoutRaw string,
	bashMaxTimeout time.Duration,
	logger *slog.Logger,
) agent.EditVerifyRunner {
	if registry == nil || strings.TrimSpace(command) == "" {
		return nil
	}
	timeout, _ := parseVerifyOnEditDurationLogged(timeoutRaw, logger)
	if bashMaxTimeout > 0 && timeout > bashMaxTimeout {
		if logger != nil {
			logger.Warn("verify-on-edit: timeout exceeds timeouts.bashMaxTimeout, clamping to the effective limit",
				"configured", timeout.String(),
				"effective", bashMaxTimeout.String())
		}
		timeout = bashMaxTimeout
	}
	command = strings.TrimSpace(command)
	timeoutStr := timeout.String()

	return func(ctx context.Context) agent.EditVerifyResult {
		// Attach the session workspace: cwd validation behaves as for model calls.
		ctx = sdktools.WithWorkspacePathNoProbe(ctx, workspace)

		input, err := json.Marshal(map[string]string{
			"command":           command,
			"timeout":           timeoutStr,
			"working_directory": workspace,
		})
		if err != nil {
			return agent.EditVerifyResult{Err: fmt.Errorf("verify-on-edit: marshaling tool input: %w", err), Timeout: timeout}
		}

		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		res, err := registry.ExecuteUnattended(ctx, tools.ShellExecToolName(), input)
		if err != nil {
			return agent.EditVerifyResult{Err: fmt.Errorf("verify-on-edit: %w", err), Timeout: timeout}
		}
		if res.IsError {
			// Deadline kill — the marker is identical for bash_exec and posh_exec.
			if strings.Contains(res.Content, "[Process killed: timeout exceeded]") {
				return agent.EditVerifyResult{
					Output:   res.Content,
					ExitCode: -1,
					TimedOut: true,
					Timeout:  timeout,
				}
			}
			// Policy/blocklist block or non-zero exit: parse the trailing exit status.
			return agent.EditVerifyResult{
				Output:   res.Content,
				ExitCode: parseExitStatus(res.Content),
				Timeout:  timeout,
			}
		}
		return agent.EditVerifyResult{Output: res.Content, ExitCode: 0, Timeout: timeout}
	}
}

// parseVerifyOnEditDurationLogged warns when the configured value is invalid
// and the default is applied.
func parseVerifyOnEditDurationLogged(raw string, logger *slog.Logger) (time.Duration, bool) {
	d, ok := parseVerifyOnEditTimeout(raw)
	if !ok && logger != nil {
		logger.Warn("verify-on-edit: invalid timeout value, using default",
			"value", raw, "default", verifyOnEditDefaultTimeout.String())
	}
	return d, ok
}

// verifyOnEditForMode gates the runner by mode: nil in CHAT (no file-edit
// tools, so verification can never be meaningful); goal-loop and specialized
// passes suppress separately. See specs/domains/verify-on-edit.md (suppression).
func (o *Orchestrator) verifyOnEditForMode() agent.EditVerifyRunner {
	if o.isNoProject {
		return nil
	}
	return o.verifyOnEdit
}

// parseExitStatus extracts the exit code from the shell tool's trailing
// "exit status N"; -1 when absent (policy blocks, signal kills).
func parseExitStatus(content string) int {
	if m := exitStatusRe.FindStringSubmatch(strings.TrimRight(content, "\n")); m != nil {
		var code int
		if _, err := fmt.Sscanf(m[1], "%d", &code); err == nil {
			return code
		}
	}
	return -1
}
