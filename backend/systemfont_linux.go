//go:build linux

package backend

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// gnomeFontTimeout bounds one gsettings child per key (a wedged settings daemon must
// never hang an RPC); the read happens once per app launch. See specs/domains/frontend/fonts.md.
const gnomeFontTimeout = 3 * time.Second

// Compile-time command constants: nothing user- or model-controlled reaches the argv.
const (
	gsettingsBinary  = "gsettings"
	gsettingsGetCmd  = "get"
	gnomeFontSchema  = "org.gnome.desktop.interface"
	gnomeUIFontKey   = "font-name"
	gnomeMonoFontKey = "monospace-font-name"
)

// readSystemFonts returns the raw GNOME interface and monospace font descriptions for
// parseGnomeFontName; an error means "no detectable system fonts" (no gsettings, schema
// absent on KDE/bare WM) — normal, reported as unavailable, never a failure. See
// specs/domains/frontend/fonts.md.
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

// readGSettingsKey reads a single font key under its own gnomeFontTimeout bound;
// an error takes the whole detection down (both keys share the schema).
func readGSettingsKey(key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gnomeFontTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, gsettingsBinary, gsettingsGetCmd, gnomeFontSchema, key).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s %s %s: %w", gsettingsBinary, gsettingsGetCmd, gnomeFontSchema, key, err)
	}
	return string(out), nil
}
