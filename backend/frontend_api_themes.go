package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
)

// ListThemes returns descriptors for all user themes installed in the global
// themes directory (~/.c0wrk/themes/*.css), sorted by display name. A missing
// directory yields an empty (non-nil) slice — a fresh install has no themes
// yet and the frontend selector must render an empty list, not an error.
//
// Every file is re-sanitized on read (defense in depth: a hand-edited or
// hand-dropped file that no longer passes the sanitizer is skipped rather
// than shipped to the webview) and must stay within maxThemeCSSSize, the
// same cap the import path enforces.
func (f *FrontendAPI) ListThemes() []ThemeDTO {
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
		// Enforce the documented size cap on this ingestion path too
		// (mirroring importThemeFromPath): a hand-dropped oversized file
		// must not be read into memory, tokenized, and shipped to the
		// webview just because it bypassed the importer.
		info, err := entry.Info()
		if err != nil {
			f.log().Warn("failed to stat theme file", "file", entry.Name(), "error", err)
			continue
		}
		if info.Size() > maxThemeCSSSize {
			f.log().Warn("skipping theme larger than the size cap",
				"file", entry.Name(), "size", info.Size(), "limit", maxThemeCSSSize)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(themesDir, entry.Name()))
		if err != nil {
			f.log().Warn("failed to read theme file", "file", entry.Name(), "error", err)
			continue
		}
		id, err := themeSlug(entry.Name())
		if err != nil {
			// Unusable slugs should not happen for files that were installed
			// through the import flow, but a hand-dropped file with a
			// reserved or unusable name must not break the whole listing.
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

// importThemeFromPath reads a CSS file from a user-chosen path, validates it
// as a theme, and installs it into the global themes directory under the slug
// derived from its file name. Importing a file whose slug already exists
// overwrites the installed copy — this is the update path for a user theme,
// and the theme id stays stable across updates.
//
// What is installed is the CANONICAL SANITIZED CSS, not the raw source: the
// stored theme (and everything the frontend later injects from it) is
// structurally incapable of referencing any resource. The returned DTO
// describes the installed theme and carries the sanitized body.
//
// This is deliberately NOT a method on FrontendAPI: `desktop.App` embeds
// *FrontendAPI, so every exported method is auto-bound to the renderer. A
// path-taking import would be directly callable from compromised renderer
// JS with any filesystem path; the sole entry point is the native picker
// (desktop.App.PickAndImportThemes).
func (f *FrontendAPI) importThemeFromPath(path string) (ThemeDTO, error) {
	if strings.TrimSpace(path) == "" {
		return ThemeDTO{}, errors.New("theme path is empty")
	}
	raw, err := os.ReadFile(path)
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
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		return ThemeDTO{}, fmt.Errorf("failed to create themes directory: %w", err)
	}
	dest := filepath.Join(themesDir, id+".css")
	// Write via a temp file + rename so a failed write never leaves a
	// half-written theme behind (the previous copy, if any, stays intact).
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		_ = os.Remove(tmp)
		return ThemeDTO{}, fmt.Errorf("failed to write theme file: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
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

// importThemesFromPaths imports several CSS files in one call, preserving the
// input order in the results. Each file is validated and installed
// independently: one invalid file never blocks the others — its result carries
// the failure while the rest of the batch still installs. An empty input
// yields an empty (non-nil) slice.
//
// Not a FrontendAPI method for the same reason as importThemeFromPath: the
// renderer must only reach the import flow through the native picker.
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

// ImportThemesFromPaths is the exported bridge used by desktop.App's native
// picker (PickAndImportThemes) to run a batch import with the user-picked
// paths. It must stay unexposed from the FrontendAPI method set so the Wails
// binding generator never publishes a path-taking RPC to the renderer.
func ImportThemesFromPaths(f *FrontendAPI, paths []string) []ThemeImportResult {
	return f.importThemesFromPaths(paths)
}

// DeleteTheme removes an installed user theme by its id (the themeSlug of its
// CSS file name, exactly as ListThemes derives it). The themes directory is
// scanned for the entry whose slug matches, so a hand-dropped file whose
// on-disk name is not in canonical slug form (`My Theme.css` → id
// `my-theme`) is deletable too. Zero matches fail as "does not exist";
// multiple matches (two file names slugifying to the same id) are rejected
// rather than guessing which file to remove.
func (f *FrontendAPI) DeleteTheme(id string) error {
	slug, err := themeSlug(id)
	if err != nil {
		return err
	}
	themesDir := config.ThemesDir(f.agentDir)
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
