//go:build linux

package backend

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// gnomeFontTimeout bounds one gsettings child process behind
// readGSettingsKey: a wedged desktop settings daemon must never hang an RPC.
// Each of the two keys gets its own fresh bound (worst case 6 s for both
// reads), and the read happens once per app launch (the frontend asks after
// backend:ready), so the cost of the generous bound is negligible.
const gnomeFontTimeout = 3 * time.Second

// Compile-time command constants: gsettings is exec'd with a fixed argv —
// nothing user- or model-controlled ever reaches the process invocation.
const (
	gsettingsBinary  = "gsettings"
	gsettingsGetCmd  = "get"
	gnomeFontSchema  = "org.gnome.desktop.interface"
	gnomeUIFontKey   = "font-name"
	gnomeMonoFontKey = "monospace-font-name"
)

// readSystemFonts returns the raw GNOME interface and monospace font
// descriptions (org.gnome.desktop.interface font-name and
// monospace-font-name) for parseGnomeFontName. An error means "no detectable
// system fonts" — gsettings missing from PATH or the schema absent (KDE, a
// bare WM): both are normal outcomes the RPC reports as unavailable (the
// zero response), never as failures. When the reads succeed, each
// description is parsed independently and an unparsable value empties only
// its own family.
//
// gsettings is exec'd rather than linked via GSettings cgo: it is the
// established pattern for desktop-integration probes in this codebase (see
// config/shell_env.go, session/clipboard_linux.go), keeps the build
// cgo-neutral, and the value is read once per launch so the spawn cost is
// irrelevant.
func readSystemFonts() (systemFontPair, error) {
	if _, err := exec.LookPath(gsettingsBinary); err != nil {
		return systemFontPair{}, fmt.Errorf("%s not found in PATH: %w", gsettingsBinary, err)
	}
	ui, err := readGSettingsKey(gnomeUIFontKey)
	if err != nil {
		return systemFontPair{}, err
	}
	mono, err := readGSettingsKey(gnomeMonoFontKey)
	if err != nil {
		return systemFontPair{}, err
	}
	return systemFontPair{UI: ui, Mono: mono}, nil
}

// readGSettingsKey reads a single font key from gnomeFontSchema under its own
// gnomeFontTimeout bound. An error here means the whole detection is
// unavailable: both keys live in the same schema, so a missing schema or a
// missing binary takes both fonts down together.
func readGSettingsKey(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gnomeFontTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, gsettingsBinary, gsettingsGetCmd, gnomeFontSchema, key).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s %s %s: %w", gsettingsBinary, gsettingsGetCmd, gnomeFontSchema, key, err)
	}
	return string(out), nil
}
