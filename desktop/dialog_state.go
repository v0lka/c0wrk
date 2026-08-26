package desktop

import (
	"encoding/json"
	"log/slog"
	"os"

	"github.com/v0lka/c0wrk/backend/config"
)

// DialogState is the persisted native-dialog runtime state. It currently holds
// only the last directory the user picked in a directory-chooser dialog, so
// the next dialog opens there instead of at an arbitrary default location.
type DialogState struct {
	LastDirectory string `json:"last_directory"`
}

// LoadDialogState reads the persisted dialog state from agentDir. It is
// defensive, mirroring LoadWindowBounds: a missing file or a malformed JSON
// document yields the zero value so a corrupt state file can never break the
// picker. LastDirectory is additionally required to still exist as a
// directory: Wails' OpenDirectoryDialog validates DefaultDirectory with
// fs.DirExists and returns an error — without ever showing the dialog — when
// it does not exist, so a stale path (deleted checkout, unmounted volume)
// must be dropped here rather than surfaced to the user.
func LoadDialogState(agentDir string) DialogState {
	data, err := os.ReadFile(config.DialogStatePath(agentDir))
	if err != nil {
		// Missing file on first run is expected — return zero value silently.
		return DialogState{}
	}

	var stored DialogState
	if err := json.Unmarshal(data, &stored); err != nil {
		return DialogState{}
	}

	if stored.LastDirectory == "" {
		return DialogState{}
	}
	if info, err := os.Stat(stored.LastDirectory); err != nil || !info.IsDir() {
		return DialogState{}
	}
	return stored
}

// rememberDialogDirectory persists dir as the last picked directory so the
// next native dialog opens there. It is best-effort: a write failure is
// logged and otherwise ignored, never propagated to the caller — remembering
// the location must not fail an otherwise successful pick.
func rememberDialogDirectory(agentDir, dir string, log *slog.Logger) {
	if dir == "" {
		return
	}
	if err := writeDialogState(agentDir, DialogState{LastDirectory: dir}); err != nil {
		log.Warn("failed to persist dialog state", "error", err)
	}
}

// writeDialogState serializes state to dialog_state.json atomically
// (temp + rename), mirroring writeWindowBounds.
func writeDialogState(agentDir string, s DialogState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := config.DialogStatePath(agentDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
