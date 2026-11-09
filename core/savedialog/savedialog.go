// Package savedialog holds the pure filesystem decisions behind the native
// save-file dialog: the default-directory precedence, the .md normalization
// guarantee, and the overwrite guard for a name the .md normalization
// changed. The Wails binding (desktop SaveMessageAsMarkdown) stays a thin
// adapter that calls into this package.
package savedialog

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/v0lka/c0wrk/core/markitdown"
)

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

// PickSaveDialogDir forwards to pickSaveDialogDir for the desktop dialog
// adapter.
func PickSaveDialogDir(projectDir, lastDir string) string {
	return pickSaveDialogDir(projectDir, lastDir)
}

// attachmentFilterPattern builds the Wails FileFilter pattern string for the
// markitdown supported extensions, using the conventional "*.ext" form (e.g.
// "*.pdf;*.docx;*.pptx"). This is the cross-platform Wails convention — on
// macOS the backend strips the "*." prefix internally and matches by extension.
func attachmentFilterPattern() string {
	exts := markitdown.SupportedExtensions()
	cleaned := make([]string, 0, len(exts))
	for _, ext := range exts {
		if e := strings.TrimPrefix(ext, "."); e != "" {
			cleaned = append(cleaned, "*."+e)
		}
	}
	return strings.Join(cleaned, ";")
}

// AttachmentFilterPattern forwards to attachmentFilterPattern for the desktop
// dialog adapter.
func AttachmentFilterPattern() string {
	return attachmentFilterPattern()
}

// ResolveWritePath forwards to resolveWritePath for the desktop dialog
// adapter.
func ResolveWritePath(chosen string) (string, error) {
	return resolveWritePath(chosen)
}
