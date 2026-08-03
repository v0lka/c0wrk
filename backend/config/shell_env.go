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

// LoadShellEnvironment loads environment variables from the user's shell profile.
// This is necessary on macOS where apps launched from Finder/Dock don't inherit
// shell environment variables (like those set in .zshrc/.bash_profile).
// The function is best-effort: failures are logged but don't block startup.
func LoadShellEnvironment(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}

	// Only needed on macOS; Linux inherits environment normally
	if runtime.GOOS != "darwin" {
		return
	}

	// Resolve the user's shell via the shared resolver.
	// Uses SHELL env → LookPath → stat fallback chain.
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

	// Parse KEY=VALUE lines and set environment variables
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	loaded := 0
	var setErrors int
	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Find first '=' to split key and value
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
