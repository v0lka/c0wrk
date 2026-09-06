//go:build windows

package sysproc

import (
	"context"
	"testing"
)

// TestGitCmdRawHidesConsole pins that the raw escape hatch still applies
// HideConsole: a trusted repository must not flash a console window on a
// GUI-subsystem app, the same guarantee the hardened path provides.
func TestGitCmdRawHidesConsole(t *testing.T) {
	cmd := GitCmdRaw(context.Background(), "status")
	if cmd.SysProcAttr == nil {
		t.Fatal("GitCmdRaw must configure SysProcAttr (HideConsole) on Windows")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("GitCmdRaw must set CREATE_NO_WINDOW, got CreationFlags=%#x", cmd.SysProcAttr.CreationFlags)
	}
}
