package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/safeio"
)

// ListThemes returns descriptors for all user themes installed in the global
// themes directory, sorted by display name; a missing directory yields an
// empty (non-nil) slice. Every file is re-sanitized on read (defense in
// depth) and must stay within maxThemeCSSSize.
// See specs/domains/theming.md.
func (f *FrontendAPI) ListThemes() []ThemeDTO {
	f.seedAcquire()
	themesDir := config.ThemesDir(f.agentDir)
	entries, err := os.ReadDir(themesDir)
	if err != nil {
		return []ThemeDTO{}
	}

	themes := make([]ThemeDTO, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".css") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			f.log().Warn("failed to stat theme file", "file", entry.Name(), "error", err)
			continue
		}
		// A non-regular entry (FIFO/socket/device) named *.css must be
		// skipped: reading it would block this synchronous RPC forever, and
		// entry.Info() decides that without ever opening the file.
		if !info.Mode().IsRegular() {
			f.log().Warn("skipping non-regular theme entry", "file", entry.Name())
			continue
		}
		if info.Size() > maxThemeCSSSize {
			f.log().Warn("skipping theme larger than the size cap",
				"file", entry.Name(), "size", info.Size(), "limit", maxThemeCSSSize)
			continue
		}
		raw, err := safeio.ReadFile(filepath.Join(themesDir, entry.Name()))
		if err != nil {
			f.log().Warn("failed to read theme file", "file", entry.Name(), "error", err)
			continue
		}
		id, err := themeSlug(entry.Name())
		if err != nil {
			// A hand-dropped file with an unusable name must not break the
			// whole listing.
			f.log().Warn("skipping theme with invalid file name", "file", entry.Name(), "error", err)
			continue
		}
		content, err := sanitizeThemeCSS(string(raw))
		if err != nil {
			f.log().Warn("skipping theme that fails sanitization", "file", entry.Name(), "error", err)
			continue
		}
		name, typ := ParseThemeCSS(entry.Name(), content)
		themes = append(themes, ThemeDTO{ID: id, Name: name, Type: typ, CSS: content})
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].Name < themes[j].Name })
	return themes
}

// importThemeFromPath validates a CSS file from a user-chosen path and
// installs it under the slug of its file name; re-import overwrites — the
// update path, with the theme id kept stable. What is installed is the
// CANONICAL SANITIZED CSS, never the raw source.
//
// Deliberately NOT an exported FrontendAPI method: desktop.App embeds
// *FrontendAPI, so exported methods are auto-bound to the renderer, and a
// path-taking import must not be callable from compromised renderer JS —
// the sole entry point is the native picker (desktop.App.PickAndImportThemes).
// See specs/domains/theming.md.
func (f *FrontendAPI) importThemeFromPath(path string) (ThemeDTO, error) {
	f.seedAcquire()
	if strings.TrimSpace(path) == "" {
		return ThemeDTO{}, errors.New("theme path is empty")
	}
	// The size cap is enforced BEFORE reading: the full-file allocation
	// itself is the DoS, so reading first and rejecting afterwards would
	// defeat it.
	info, err := os.Stat(path)
	if err != nil {
		return ThemeDTO{}, fmt.Errorf("failed to stat theme file: %w", err)
	}
	if info.Size() > maxThemeCSSSize {
		return ThemeDTO{}, fmt.Errorf("invalid theme CSS: theme CSS is too large: %d bytes (limit %d)", info.Size(), maxThemeCSSSize)
	}
	// safeio refuses a non-regular file instead of blocking the RPC on an
	// open that waits for a writer forever; the length is re-checked after
	// the read — the file may have grown since the stat.
	raw, err := safeio.ReadFile(path)
	if err != nil {
		return ThemeDTO{}, fmt.Errorf("failed to read theme file: %w", err)
	}
	if len(raw) > maxThemeCSSSize {
		return ThemeDTO{}, fmt.Errorf("invalid theme CSS: theme CSS is too large: %d bytes (limit %d)", len(raw), maxThemeCSSSize)
	}
	content, err := sanitizeThemeCSS(string(raw))
	if err != nil {
		return ThemeDTO{}, fmt.Errorf("invalid theme CSS: %w", err)
	}
	id, err := themeSlug(filepath.Base(path))
	if err != nil {
		return ThemeDTO{}, err
	}

	themesDir := config.ThemesDir(f.agentDir)
	// Created as REAL directories only: a dangling link or a link swapped
	// into a created component fails the import closed. A pre-existing
	// symlinked themes dir resolves as operator intent (macOS /var class);
	// deletion keeps its own strict refusal (see DeleteTheme).
	if err := safeio.MkdirAllReal(themesDir, 0o755); err != nil {
		return ThemeDTO{}, fmt.Errorf("failed to create themes directory: %w", err)
	}
	dest := filepath.Join(themesDir, id+".css")
	// Atomic replace: the data lands in a uniquely named temp file (no
	// plantable fixed "<dest>.tmp") renamed over dest — the rename replaces
	// the entry itself, and a failed write leaves the previous copy intact.
	if err := safeio.WriteFileAtomic(dest, []byte(content), 0o644); err != nil {
		return ThemeDTO{}, fmt.Errorf("failed to install theme file: %w", err)
	}

	name, typ := ParseThemeCSS(filepath.Base(path), content)
	f.log().Info("theme imported", "id", id, "name", name, "type", typ, "source", path)
	return ThemeDTO{ID: id, Name: name, Type: typ, CSS: content}, nil
}

// ThemeImportResult reports the per-file outcome of a batch import. Theme is
// set on success; Error carries the failure reason otherwise (never both).
type ThemeImportResult struct {
	File  string    `json:"file"`
	Theme *ThemeDTO `json:"theme,omitempty"`
	Error string    `json:"error,omitempty"`
}

// importThemesFromPaths imports several CSS files in input order; each file
// is independent — one invalid file never blocks the others. Unexported for
// the same renderer-binding reason as importThemeFromPath.
func (f *FrontendAPI) importThemesFromPaths(paths []string) []ThemeImportResult {
	results := make([]ThemeImportResult, 0, len(paths))
	for _, path := range paths {
		theme, err := f.importThemeFromPath(path)
		if err != nil {
			results = append(results, ThemeImportResult{File: path, Error: err.Error()})
			continue
		}
		results = append(results, ThemeImportResult{File: path, Theme: &theme})
	}
	return results
}

// ImportThemesFromPaths is the exported bridge desktop.App's native picker
// calls; it must stay off the FrontendAPI method set so the Wails binding
// generator never publishes a path-taking RPC to the renderer.
func ImportThemesFromPaths(f *FrontendAPI, paths []string) []ThemeImportResult {
	return f.importThemesFromPaths(paths)
}

// DeleteTheme removes an installed user theme by its id, scanning the themes
// directory for the entry whose slug matches — so a hand-dropped
// non-canonical file name is deletable too. Zero matches fail as "does not
// exist"; multiple matches are rejected rather than guessing which to remove.
func (f *FrontendAPI) DeleteTheme(id string) error {
	f.seedAcquire()
	slug, err := themeSlug(id)
	if err != nil {
		return err
	}
	themesDir := config.ThemesDir(f.agentDir)
	// The themes directory must be a REAL directory before anything is read
	// or unlinked through it: a planted symlink would redirect the scan and
	// the os.Remove below into its target — an out-of-containment unlink.
	// Nothing is created here; a missing directory means the theme is absent.
	fi, err := os.Lstat(themesDir)
	if err != nil {
		return fmt.Errorf("theme %q does not exist", id)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("themes directory %q is not a real directory; refusing to delete themes through it", themesDir)
	}
	entries, err := os.ReadDir(themesDir)
	if err != nil {
		return fmt.Errorf("theme %q does not exist", id)
	}
	var matches []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".css") {
			continue
		}
		candidate, err := themeSlug(entry.Name())
		if err != nil || candidate != slug {
			continue
		}
		matches = append(matches, entry.Name())
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("theme %q does not exist", id)
	case 1:
	default:
		return fmt.Errorf("theme %q is ambiguous: %d theme files slugify to the same id", id, len(matches))
	}
	themePath := filepath.Join(themesDir, matches[0])
	if err := os.Remove(themePath); err != nil {
		return fmt.Errorf("failed to delete theme: %w", err)
	}
	f.log().Info("theme deleted", "id", slug)
	return nil
}
