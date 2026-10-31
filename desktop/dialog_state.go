package desktop

import (
	"encoding/json"
	"log/slog"
	"os"

	"github.com/v0lka/c0wrk/backend/config"

	"github.com/v0lka/sp4rk/safeio"
)

// DialogState is the persisted native-dialog runtime state: the last
// directory the user picked, so the next dialog opens there.
type DialogState struct {
	LastDirectory string `json:"last_directory"`
}

// LoadDialogState reads the persisted dialog state defensively (mirroring
// LoadWindowBounds): corruption yields the zero value. LastDirectory must
// still exist as a directory — Wails' OpenDirectoryDialog errors out without
// ever showing the dialog otherwise, so stale paths drop here.
func LoadDialogState(agentDir string) DialogState {
	data, err := safeio.ReadFile(config.DialogStatePath(agentDir))
	if err != nil {
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

// rememberDialogDirectory persists dir as the last picked directory,
// best-effort: a write failure is logged, never fails the pick.
func rememberDialogDirectory(agentDir, dir string, log *slog.Logger) {
	if dir == "" {
		return
	}
	if err := writeDialogState(agentDir, DialogState{LastDirectory: dir}); err != nil {
		log.Warn("failed to persist dialog state", "error", err)
	}
}

// writeDialogState writes dialog_state.json atomically (mirroring
// writeWindowBounds): staged through a random-suffix sibling, and the rename
// replaces a symlink at the final path itself instead of writing through it.
func writeDialogState(agentDir string, s DialogState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return safeio.WriteFileAtomic(config.DialogStatePath(agentDir), data, 0o600)
}
