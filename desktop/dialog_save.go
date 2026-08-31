package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveMessageAsMarkdown opens a native save-file dialog and writes content to
// the chosen path as a Markdown document. The dialog defaults to the active
// project's directory (falling back to the last used dialog location) and
// pre-fills a .md filename; the chosen path is normalized to end in .md so
// the saved file is always a Markdown document. When that normalization
// changes the confirmed name, an existing normalized file is never
// overwritten silently (the dialog's overwrite prompt covered the literal
// name) — the write fails and the user re-picks (see resolveWritePath).
//
// Returns the absolute path of the written file, or "" when the user cancels
// the dialog — mirroring PickDirectory's ("", nil) cancel contract.
//
// This must remain on App (not FrontendAPI) because it requires the Wails
// context. The native save dialog is itself the user's explicit consent for
// the write (including overwrite confirmation), so no additional confirmation
// gate applies; the content is chat text the user already sees on screen.
func (a *App) SaveMessageAsMarkdown(content string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("SaveMessageAsMarkdown: application context is not initialized")
	}

	options := wailsRuntime.SaveDialogOptions{
		Title:           "Save Message as Markdown",
		DefaultFilename: "message.md",
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Markdown (*.md)", Pattern: "*.md"},
		},
	}
	// DefaultDirectory must reference an existing directory or the runtime
	// returns an error without showing any dialog (fs.DirExists check inside
	// SaveFileDialog). pickSaveDialogDir only returns paths verified to exist.
	if dir := pickSaveDialogDir(a.ActiveProjectDir(), LoadDialogState(a.agentDir()).LastDirectory); dir != "" {
		options.DefaultDirectory = dir
	}

	path, err := wailsRuntime.SaveFileDialog(a.ctx, options)
	if err != nil {
		return "", fmt.Errorf("save dialog failed: %w", err)
	}
	// On cancel SaveFileDialog returns ("", nil).
	if path == "" {
		return "", nil
	}

	path, err = resolveWritePath(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write markdown file: %w", err)
	}

	// Remember the directory so the next native dialog opens where the user
	// last worked — the same UX memory PickDirectory maintains. Best-effort.
	rememberDialogDirectory(a.agentDir(), filepath.Dir(path), a.log())
	return path, nil
}

// pickSaveDialogDir chooses the default directory for the save dialog: the
// active project directory when it is set and still exists on disk, otherwise
// the last used dialog directory (already validated by LoadDialogState). Both
// inputs may be "".
func pickSaveDialogDir(projectDir, lastDir string) string {
	if projectDir != "" {
		if info, err := os.Stat(projectDir); err == nil && info.IsDir() {
			return projectDir
		}
	}
	return lastDir
}

// ensureMarkdownExtension appends the .md extension when the chosen path does
// not already end with it (case-insensitive). Platform save dialogs do not
// reliably append the filter extension, so the .md guarantee lives here.
func ensureMarkdownExtension(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		return path
	}
	return path + ".md"
}

// resolveWritePath normalizes the dialog-chosen path to a Markdown filename
// and guards the one case the native dialog's overwrite confirmation cannot
// cover: when the .md normalization changes the name, the user confirmed the
// literal name, so an already-existing normalized file must never be
// overwritten silently — the write fails instead and the user re-picks. A
// name confirmed verbatim (including dialogs that appended .md themselves)
// goes through, as does a normalized name that does not exist yet.
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
