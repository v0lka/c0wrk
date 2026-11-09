package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/v0lka/c0wrk/backend"
	"github.com/v0lka/c0wrk/core/savedialog"
	"github.com/v0lka/sp4rk/safeio"
)

// SaveMessageAsMarkdown opens a native save-file dialog and writes content as
// a Markdown document: the dialog defaults to the active project directory
// (else the last used location) with a .md filename pre-filled, and the chosen
// path is normalized to end in .md — a normalization that changed the
// confirmed name never overwrites an existing file silently (the dialog's
// overwrite prompt covered only the literal name; see resolveWritePath).
// Returns the written path, or "" on cancel (PickDirectory's contract). This
// must remain on App (it requires the Wails context); the native dialog is
// itself the user's consent for the write, so no extra confirmation gate
// applies.
func (a *App) SaveMessageAsMarkdown(content string) (string, error) {
	if a.wailsCtx() == nil {
		return "", errors.New("SaveMessageAsMarkdown: application context is not initialized")
	}

	options := wailsRuntime.SaveDialogOptions{
		Title:           "Save Message as Markdown",
		DefaultFilename: "message.md",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Markdown (*.md)", Pattern: "*.md"},
		},
	}
	// DefaultDirectory must exist or the runtime errors without showing any
	// dialog; savedialog.PickSaveDialogDir only returns verified-existing paths.
	if dir := savedialog.PickSaveDialogDir(a.ActiveProjectDir(), backend.LoadDialogState(a.agentDir()).LastDirectory); dir != "" {
		options.DefaultDirectory = dir
	}

	path, err := wailsRuntime.SaveFileDialog(a.wailsCtx(), options)
	if err != nil {
		return "", fmt.Errorf("save dialog failed: %w", err)
	}
	if path == "" {
		return "", nil
	}

	path, err = savedialog.ResolveWritePath(path)
	if err != nil {
		return "", err
	}
	// Guarded write (safeio.OpenFile): the open is non-blocking and fstat-
	// checked for regularity, so a FIFO planted at the chosen path is refused
	// instead of blocking this Wails RPC goroutine inside open(2). A symlink
	// keeps its historical follow-through: the picker is the user's explicit
	// choice of target.
	f, err := safeio.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("failed to write markdown file: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("failed to write markdown file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("failed to write markdown file: %w", err)
	}

	// Remember the directory so the next native dialog opens where the user
	// last worked (the same UX memory PickDirectory maintains); best-effort.
	backend.RememberDialogDirectory(a.agentDir(), filepath.Dir(path), a.log())
	return path, nil
}
