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

// verifyOnEditDefaultTimeout is the fallback for an empty or invalid
// executor.verify_on_edit.timeout config value.
const verifyOnEditDefaultTimeout = 2 * time.Minute

// exitStatusRe matches Go's exec.ExitError text ("exit status N"), which the
// platform shell tool (bash_exec/posh_exec) appends to a failed command's
// output on both halves of the build-tagged split.
var exitStatusRe = regexp.MustCompile(`exit status (\d+)$`)

// parseVerifyOnEditTimeout parses the user-configured duration string. Empty
// or invalid values fall back to verifyOnEditDefaultTimeout (invalid values
// are logged by the caller-side constructor, not here).
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

// buildEditVerifyRunner builds the agent.EditVerifyRunner handed to the
// executor (SetVerifyOnEdit) when executor.verify_on_edit.enabled is true.
//
// The runner executes the USER-CONFIGURED command (config.yaml, never the
// model) through the session registry's platform shell tool (bash_exec on
// Unix, posh_exec on Windows — tools.ShellExecToolName) via
// ExecuteUnattended: group-deny policy, the command blacklist, and
// symlink/SSRF hard reasons still block, while interactive confirmation is
// skipped — the user already approved the command by configuring it. The
// output is returned raw; truncation to MaxOutputChars happens in the SDK
// hook (agent/verify_on_edit).
//
// bashMaxTimeout (from timeouts.bashMaxTimeout, <= 0 = unknown) clamps the
// configured timeout: the bash tool silently enforces its own MaxTimeout on
// every command, so without this clamp a larger verify_on_edit.timeout would
// never take effect and the timeout note would point at a dead knob. The
// clamp is applied here — with a warning — so the effective limit is visible
// in the log and echoed in EditVerifyResult.Timeout.
//
// Returns nil when the feature is not configured, which leaves the executor
// completely unhooked (identical behavior to a disabled build).
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
		// Attach the session workspace so the bash tool's working_directory
		// validation and cwd resolution behave exactly as for model calls.
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
			// Timeout: the shell tool marks a deadline kill distinctly (the
			// marker text is identical for bash_exec and posh_exec).
			if strings.Contains(res.Content, "[Process killed: timeout exceeded]") {
				return agent.EditVerifyResult{
					Output:   res.Content,
					ExitCode: -1,
					TimedOut: true,
					Timeout:  timeout,
				}
			}
			// Blocked by policy/blacklist, or non-zero exit. Extract the exit
			// code from the trailing exec error the shell tool appends.
			return agent.EditVerifyResult{
				Output:   res.Content,
				ExitCode: parseExitStatus(res.Content),
				Timeout:  timeout,
			}
		}
		return agent.EditVerifyResult{Output: res.Content, ExitCode: 0, Timeout: timeout}
	}
}

// parseVerifyOnEditDurationLogged wraps parseVerifyOnEditTimeout with a
// warning when the configured value is invalid (fallback applied).
func parseVerifyOnEditDurationLogged(raw string, logger *slog.Logger) (time.Duration, bool) {
	d, ok := parseVerifyOnEditTimeout(raw)
	if !ok && logger != nil {
		logger.Warn("verify-on-edit: invalid timeout value, using default",
			"value", raw, "default", verifyOnEditDefaultTimeout.String())
	}
	return d, ok
}

// verifyOnEditForMode gates the edit-verification runner by orchestrator
// mode: nil in No Project (CHAT) mode — CHAT exposes no file-edit tools, so
// verification can never be meaningful there — and the configured runner
// otherwise. Goal-loop suppression is handled separately: specialized passes
// via RunConductor's systemPromptOverride check, plain goal turns via
// defaultGoalTurnRunner.
func (o *Orchestrator) verifyOnEditForMode() agent.EditVerifyRunner {
	if o.isNoProject {
		return nil
	}
	return o.verifyOnEdit
}

// parseExitStatus extracts the exit code from a bash tool failure payload
// ("...output...\nexit status N"). Returns -1 when no exit status is present
// (e.g. policy blocks, signal kills).
func parseExitStatus(content string) int {
	if m := exitStatusRe.FindStringSubmatch(strings.TrimRight(content, "\n")); m != nil {
		var code int
		if _, err := fmt.Sscanf(m[1], "%d", &code); err == nil {
			return code
		}
	}
	return -1
}
