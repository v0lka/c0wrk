//go:build linux

package backend

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// gnomeFontTimeout bounds the gsettings child process behind
// readSystemFontName: a wedged desktop settings daemon must never hang an
// RPC. The read happens once per app launch (the frontend asks after
// backend:ready), so the cost of the generous bound is negligible.
const gnomeFontTimeout = 3 * time.Second

// readSystemFontName returns the raw GNOME interface font description
// (org.gnome.desktop.interface font-name, e.g. "'Noto Sans 11'") for
// parseGnomeFontName. An error means "no detectable system UI font" — gsettings
// missing from PATH or the schema absent (KDE, a bare WM): both are normal
// outcomes the RPC reports as unavailable, never as failures.
//
// gsettings is exec'd rather than linked via GSettings cgo: it is the
// established pattern for desktop-integration probes in this codebase (see
// config/shell_env.go, session/clipboard_linux.go), keeps the build
// cgo-neutral, and the value is read once per launch so the spawn cost is
// irrelevant. The command and arguments are compile-time constants — nothing
// user- or model-controlled ever reaches the argv.
func readSystemFontName() (string, error) {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return "", fmt.Errorf("gsettings not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gnomeFontTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", "font-name").Output()
	if err != nil {
		return "", fmt.Errorf("gsettings get org.gnome.desktop.interface font-name: %w", err)
	}
	return string(out), nil
}
