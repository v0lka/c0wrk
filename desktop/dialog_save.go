package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

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
	// dialog; pickSaveDialogDir only returns verified-existing paths.
	if dir := pickSaveDialogDir(a.ActiveProjectDir(), LoadDialogState(a.agentDir()).LastDirectory); dir != "" {
		options.DefaultDirectory = dir
	}

	path, err := wailsRuntime.SaveFileDialog(a.wailsCtx(), options)
	if err != nil {
		return "", fmt.Errorf("save dialog failed: %w", err)
	}
	if path == "" {
		return "", nil
	}

	path, err = resolveWritePath(path)
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
	rememberDialogDirectory(a.agentDir(), filepath.Dir(path), a.log())
	return path, nil
}

// pickSaveDialogDir chooses the save dialog's default directory: the active
// project directory when set and still existing, else the last used dialog
// directory (already validated by LoadDialogState). Both inputs may be "".
func pickSaveDialogDir(projectDir, lastDir string) string {
	if projectDir != "" {
		if info, err := os.Stat(projectDir); err == nil && info.IsDir() {
			return projectDir
		}
	}
	return lastDir
}

// ensureMarkdownExtension appends .md when the chosen path does not already
// end with it (case-insensitive) — platform save dialogs do not reliably
// append the filter extension, so the .md guarantee lives here.
func ensureMarkdownExtension(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		return path
	}
	return path + ".md"
}

// resolveWritePath guards the one case the dialog's overwrite confirmation
// cannot cover: when .md normalization CHANGES the name, the user confirmed
// the literal name, so an existing normalized file is never overwritten
// silently — the write fails and the user re-picks. A verbatim-confirmed name
// (including dialogs that appended .md themselves) goes through, as does a
// normalized name that does not exist yet.
func resolveWritePath(chosen string) (string, error) {
	normalized := ensureMarkdownExtension(chosen)
	if normalized == chosen {
		return normalized, nil
	}
	if _, err := os.Stat(normalized); err == nil {
		return "", fmt.Errorf("refusing to overwrite %q: the .md-normalized name differs from the one confirmed in the save dialog — pick the .md name explicitly or choose another name", normalized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to inspect save target: %w", err)
	}
	return normalized, nil
}
