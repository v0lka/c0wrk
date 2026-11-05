package config

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/internal/shellresolver"
)

// LoadShellEnvironment loads environment variables from the user's login
// shell — necessary on macOS, where Finder/Dock-launched apps don't inherit
// shell env; it runs before any other init (specs/domains/llm-providers.md).
// Best-effort: failures are logged but don't block startup.
func LoadShellEnvironment(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}

	// Only needed on macOS; Linux inherits environment normally
	if runtime.GOOS != "darwin" {
		return
	}

	shell := shellresolver.Resolve()

	// Run shell with -l (login) flag to source profile files
	// We avoid -i (interactive) to prevent extra output from .zshrc/.bashrc
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-l", "-c", "printenv")
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf // Capture stderr for diagnostics

	output, err := cmd.Output()
	if err != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if ctx.Err() == context.DeadlineExceeded {
			logger.Warn("timeout loading shell environment", "shell", shell)
		} else {
			logAttrs := []any{"shell", shell, "error", err}
			if stderrStr != "" {
				logAttrs = append(logAttrs, "stderr", stderrStr)
			}
			logger.Warn("failed to load shell environment", logAttrs...)
		}
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	loaded := 0
	var setErrors int
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			continue
		}

		eqIdx := strings.Index(line, "=")
		if eqIdx <= 0 {
			// Line without '=' or starting with '=' is invalid; skip
			continue
		}

		key := line[:eqIdx]
		value := line[eqIdx+1:]

		// Don't override already-set variables (respects explicit env vars set by launcher),
		// EXCEPT for PATH: on macOS Finder/Dock launches, the inherited PATH is minimal
		// (/usr/bin:/bin:/usr/sbin:/sbin) and lacks user-specific directories like
		// ~/.local/bin or ~/go/bin. The shell login profile provides the authoritative PATH.
		if key != "PATH" && os.Getenv(key) != "" {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			setErrors++
			// Log but continue - some vars may not be settable
			logger.Debug("failed to set env var", "key", key, "error", err)
			continue
		}
		loaded++
	}

	if scanErr := scanner.Err(); scanErr != nil {
		logger.Warn("scanner error parsing shell environment output", "error", scanErr)
	}

	if setErrors > 0 {
		logger.Warn("some shell environment variables could not be set", "failed", setErrors, "loaded", loaded)
	}

	if loaded > 0 {
		logger.Debug("loaded shell environment variables", "count", loaded, "shell", shell)
	}
}
