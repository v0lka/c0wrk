//go:build linux

package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

// relaunchDetachedSelf spawns the current executable detached (new session)
// so the relaunched app survives the quitting instance; the child carries
// GpuRelaunchWaitEnvVar=1 (specs/domains/webview-gpu-policy.md).
func relaunchDetachedSelf(logger *slog.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}
	cmd := exec.CommandContext(context.Background(), exe)
	cmd.Env = append(os.Environ(), GpuRelaunchWaitEnvVar+"=1")
	// Stdio stays unconnected: the child's crashlog.Install re-captures fd 1/2
	// into a fresh stderr.log of its own.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}
	// Release the child so it is not reaped when this process exits.
	if err := cmd.Process.Release(); err != nil {
		logger.Debug("could not release relaunched process (best-effort)", "error", err)
	}
	return nil
}
