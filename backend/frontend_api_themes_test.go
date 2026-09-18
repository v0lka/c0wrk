package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newThemesTestAPI builds a FrontendAPI whose agentDir points at a temporary
// directory, so theme installs land in <tmp>/themes/.
func newThemesTestAPI(t *testing.T) (api *FrontendAPI, agentDir string) {
	t.Helper()
	agentDir = t.TempDir()
	return &FrontendAPI{agentDir: agentDir}, agentDir
}

// writeThemeFile writes content to a source file outside the themes dir.
func writeThemeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func TestFrontendAPI_Themes_ImportListDeleteLifecycle(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)

	// No themes directory yet — ListThemes must return an empty, non-nil slice.
	if got := f.ListThemes(); got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil list for missing dir, got %#v", got)
	}

	// Import a valid theme.
	src := writeThemeFile(t, filepath.Join(agentDir, "downloads"), "nord.css",
		"/* c0wrk-theme: Nord | dark */\n:root { --color-background: #2e3440; --color-foreground: #d8dee9; }\n")
	dto, err := f.importThemeFromPath(src)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if dto.ID != "nord" || dto.Name != "Nord" || dto.Type != "dark" {
		t.Fatalf("unexpected DTO: %+v", dto)
	}
	// The import result must carry the theme body — the frontend activates
	// the freshly imported theme immediately via themeStore.setTheme(id, css).
	if dto.CSS == "" {
		t.Fatalf("import result must carry the theme CSS body, got %q", dto.CSS)
	}

	// File must land at <agentDir>/themes/nord.css (ThemesDir contract).
	installed := filepath.Join(agentDir, "themes", "nord.css")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed theme file missing: %v", err)
	}

	list := f.ListThemes()
	if len(list) != 1 {
		t.Fatalf("expected 1 theme, got %+v", list)
	}
	// List entries carry the theme body too (ListThemes reads every file
	// anyway) — selecting a custom theme from the list can activate it
	// without a second round-trip.
	if list[0].ID != dto.ID || list[0].Name != dto.Name || list[0].Type != dto.Type {
		t.Fatalf("expected [%+v], got %+v", dto, list)
	}
	if list[0].CSS != dto.CSS {
		t.Fatalf("list entry must carry the same CSS body as the import, got %q", list[0].CSS)
	}

	// Delete, then the list is empty again.
	if err := f.DeleteTheme("nord"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := f.ListThemes(); len(got) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", got)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("expected theme file removed, stat err=%v", err)
	}
}

func TestFrontendAPI_Themes_ReimportOverwritesNoDuplicate(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	srcDir := filepath.Join(agentDir, "src")

	v1 := writeThemeFile(t, srcDir, "nord.css",
		"/* c0wrk-theme: Nord | dark */\n:root { --color-background: #111; --color-foreground: #eee; }\n")
	if _, err := f.importThemeFromPath(v1); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// Simulate a v2 download (same file name, new content) in the same dir.
	v2 := writeThemeFile(t, srcDir, "nord.css",
		"/* c0wrk-theme: Nord v2 | dark */\n:root { --color-background: #222; --color-foreground: #ddd; }\n")
	dto, err := f.importThemeFromPath(v2)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if dto.ID != "nord" {
		t.Fatalf("id must stay stable across updates, got %q", dto.ID)
	}

	list := f.ListThemes()
	if len(list) != 1 {
		t.Fatalf("re-import must not duplicate: got %+v", list)
	}
	if list[0].Name != "Nord v2" {
		t.Fatalf("expected updated metadata, got %+v", list[0])
	}

	// No temp leftovers from the atomic write.
	entries, err := os.ReadDir(filepath.Join(agentDir, "themes"))
	if err != nil {
		t.Fatalf("read themes dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "nord.css" {
		t.Fatalf("expected exactly nord.css, got %d entries", len(entries))
	}
}

func TestFrontendAPI_Themes_ImportValidationErrors(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	srcDir := filepath.Join(agentDir, "src")

	cases := map[string]string{
		"import-rule":    "@import url('https://evil.example/x.css');\n:root { --color-background: #fff; --color-foreground: #000; }",
		"external-url":   ":root { --color-background: url(https://evil.example/bg.png); --color-foreground: #000; }",
		"missing-tokens": ":root { --accent: #528bff; }",
	}
	for name, css := range cases {
		src := writeThemeFile(t, srcDir, name+".css", css)
		if _, err := f.importThemeFromPath(src); err == nil {
			t.Errorf("%s: expected import rejection, got nil", name)
		}
	}
	// Nothing may have been installed.
	if got := f.ListThemes(); len(got) != 0 {
		t.Fatalf("rejected themes must not install, got %+v", got)
	}
	// Reserved slug needs a valid CSS body to prove rejection comes from the
	// slug rule, not the CSS validation.
	reserved := writeThemeFile(t, srcDir, "Default-Dark.css", validThemeCSS)
	if _, err := f.importThemeFromPath(reserved); err == nil {
		t.Fatal("expected reserved-id rejection")
	}

	if _, err := f.importThemeFromPath(filepath.Join(srcDir, "does-not-exist.css")); err == nil {
		t.Fatal("expected error for missing source file")
	}
	if _, err := f.importThemeFromPath("   "); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestFrontendAPI_Themes_ImportBatchIndependentResults(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	srcDir := filepath.Join(agentDir, "src")

	nord := writeThemeFile(t, srcDir, "nord.css",
		"/* c0wrk-theme: Nord | dark */\n:root { --color-background: #2e3440; --color-foreground: #d8dee9; }\n")
	paper := writeThemeFile(t, srcDir, "paper.css",
		"/* c0wrk-theme: Paper | light */\n:root { --color-background: #faf8f2; --color-foreground: #3a3a38; }\n")
	broken := writeThemeFile(t, srcDir, "broken.css", ":root { --accent: #528bff; }")
	missing := filepath.Join(srcDir, "does-not-exist.css")

	results := f.importThemesFromPaths([]string{nord, broken, paper, missing})
	if len(results) != 4 {
		t.Fatalf("expected one result per input path, got %d: %+v", len(results), results)
	}
	// Input order is preserved across successes and failures alike.
	if results[0].File != nord || results[1].File != broken || results[2].File != paper || results[3].File != missing {
		t.Fatalf("results must preserve input order, got %+v", results)
	}

	// Success entries carry the theme and no error.
	if results[0].Theme == nil || results[0].Theme.ID != "nord" || results[0].Error != "" {
		t.Fatalf("unexpected nord result: %+v", results[0])
	}
	if results[2].Theme == nil || results[2].Theme.ID != "paper" || results[2].Error != "" {
		t.Fatalf("unexpected paper result: %+v", results[2])
	}
	// The broken and missing files failed — with an error, never a theme.
	if results[1].Theme != nil || results[1].Error == "" {
		t.Fatalf("expected failure for broken.css, got %+v", results[1])
	}
	if results[3].Theme != nil || results[3].Error == "" {
		t.Fatalf("expected failure for missing file, got %+v", results[3])
	}

	// One invalid file must not block the rest of the batch: both valid
	// themes are installed.
	list := f.ListThemes()
	if len(list) != 2 {
		t.Fatalf("expected 2 installed themes despite 2 failed files, got %+v", list)
	}

	// An empty batch yields an empty non-nil slice.
	if got := f.importThemesFromPaths(nil); got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil result for empty input, got %#v", got)
	}
}

func TestFrontendAPI_Themes_DeleteMissing(t *testing.T) {
	f, _ := newThemesTestAPI(t)
	if err := f.DeleteTheme("never-installed"); err == nil {
		t.Fatal("expected error for missing theme")
	}
	if err := f.DeleteTheme("default-light"); err == nil {
		t.Fatal("expected error for reserved id (never installable)")
	}
}

func TestFrontendAPI_Themes_ListSortsByNameAndSkipsNonCSS(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")

	mustWrite := func(name, content string) {
		t.Helper()
		writeThemeFile(t, themesDir, name, content)
	}
	mustWrite("zeta.css", "/* c0wrk-theme: Zeta | dark */\n:root { --color-background: #000; --color-foreground: #fff; }")
	mustWrite("alpha.css", "/* c0wrk-theme: Alpha | light */\n:root { --color-background: #fff; --color-foreground: #000; }")
	mustWrite("notes.txt", "not a theme")
	if err := os.MkdirAll(filepath.Join(themesDir, "sub"), 0o755); err != nil {
		t.Fatalf("MkdirAll sub: %v", err)
	}

	list := f.ListThemes()
	if len(list) != 2 {
		t.Fatalf("expected 2 themes (txt skipped), got %+v", list)
	}
	if list[0].Name != "Alpha" || list[1].Name != "Zeta" {
		t.Fatalf("expected sort by name, got %+v", list)
	}
}

func TestFrontendAPI_Themes_ListSkipsInvalidSlug(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")

	// Hand-dropped reserved id — listed never, but must not break the scan.
	writeThemeFile(t, themesDir, "default-dark.css",
		":root { --color-background: #282c34; --color-foreground: #abb2bf; }")
	writeThemeFile(t, themesDir, "ok.css",
		":root { --color-background: #282c34; --color-foreground: #abb2bf; }")

	list := f.ListThemes()
	if len(list) != 1 || list[0].ID != "ok" {
		t.Fatalf("expected only ok.css, got %+v", list)
	}
}

// TestFrontendAPI_Themes_ImportStoresSanitizedCSS pins the storage contract:
// the installed file and every DTO/list entry carry the canonical sanitized
// CSS, never the raw source (comments dropped, whitespace normalized) — the
// webview only ever injects what the sanitizer emitted.
func TestFrontendAPI_Themes_ImportStoresSanitizedCSS(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	src := writeThemeFile(t, filepath.Join(agentDir, "downloads"), "nord.css",
		"/* c0wrk-theme: Nord | dark */\n:root {\n  /* comment */\n  --color-background:   #2e3440 ;\n  --color-foreground: #d8dee9;\n}\n")

	dto, err := f.importThemeFromPath(src)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := "/* c0wrk-theme: Nord | dark */\n:root {\n  --color-background: #2e3440;\n  --color-foreground: #d8dee9;\n}\n"
	if dto.CSS != want {
		t.Fatalf("import DTO CSS must be canonical:\ngot:\n%s\nwant:\n%s", dto.CSS, want)
	}
	installed, err := os.ReadFile(filepath.Join(agentDir, "themes", "nord.css"))
	if err != nil {
		t.Fatalf("read installed theme: %v", err)
	}
	if string(installed) != want {
		t.Fatalf("installed file must store the sanitized CSS:\ngot:\n%s", installed)
	}
	if list := f.ListThemes(); len(list) != 1 || list[0].CSS != want {
		t.Fatalf("list entry must carry the sanitized CSS: %+v", f.ListThemes())
	}
}

// TestFrontendAPI_Themes_ListSkipsUnsanitizableFiles guards the defense in
// depth: a theme file that was hand-edited (or hand-dropped) past validation
// is skipped on listing instead of reaching the webview.
func TestFrontendAPI_Themes_ListSkipsUnsanitizableFiles(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")
	writeThemeFile(t, themesDir, "ok.css",
		"/* c0wrk-theme: Ok | dark */\n:root { --color-background: #282c34; --color-foreground: #abb2bf; }\n")
	writeThemeFile(t, themesDir, "hostile.css",
		":root { --color-background: #fff; --color-foreground: #000; background-image: image-set(\"https://evil.example/b.png\" 1x); }\n")

	list := f.ListThemes()
	if len(list) != 1 || list[0].ID != "ok" {
		t.Fatalf("expected only the sanitizable theme, got %+v", list)
	}
}

// TestFrontendAPI_Themes_ListSkipsOversizedFiles pins the size cap on the
// listing path: a hand-dropped .css above maxThemeCSSSize (the same cap the
// import path enforces) is skipped rather than read, tokenized, and shipped
// to the webview.
func TestFrontendAPI_Themes_ListSkipsOversizedFiles(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")
	writeThemeFile(t, themesDir, "ok.css",
		"/* c0wrk-theme: Ok | dark */\n:root { --color-background: #282c34; --color-foreground: #abb2bf; }\n")
	oversized := validThemeCSS + "\n/* " + strings.Repeat("x", maxThemeCSSSize) + " */"
	writeThemeFile(t, themesDir, "big.css", oversized)

	list := f.ListThemes()
	if len(list) != 1 || list[0].ID != "ok" {
		t.Fatalf("expected only the in-cap theme, got %+v", list)
	}
}

// TestFrontendAPI_Themes_DeleteHandDroppedNonCanonicalFile pins the
// ListThemes/DeleteTheme id symmetry: a hand-dropped file whose on-disk name
// is not in canonical slug form (`My Theme.css`) is listed under the slug id
// (`my-theme`) and must be deletable by that id — previously DeleteTheme
// reconstructed `<id>.css` and could never remove such a file.
func TestFrontendAPI_Themes_DeleteHandDroppedNonCanonicalFile(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")
	dropped := writeThemeFile(t, themesDir, "My Theme.css",
		":root { --color-background: #282c34; --color-foreground: #abb2bf; }\n")

	list := f.ListThemes()
	if len(list) != 1 || list[0].ID != "my-theme" {
		t.Fatalf("expected the hand-dropped theme listed as my-theme, got %+v", list)
	}
	if err := f.DeleteTheme("my-theme"); err != nil {
		t.Fatalf("delete by the listed id must succeed: %v", err)
	}
	if _, err := os.Stat(dropped); !os.IsNotExist(err) {
		t.Fatalf("expected My Theme.css removed, stat err=%v", err)
	}
	if got := f.ListThemes(); len(got) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", got)
	}
}

// TestFrontendAPI_Themes_DeleteAmbiguousSlugRejected: two theme files that
// slugify to the same id (canonical + hand-dropped variant) are an ambiguous
// delete target — the call must fail rather than guess which file to remove.
func TestFrontendAPI_Themes_DeleteAmbiguousSlugRejected(t *testing.T) {
	f, agentDir := newThemesTestAPI(t)
	themesDir := filepath.Join(agentDir, "themes")
	css := ":root { --color-background: #282c34; --color-foreground: #abb2bf; }\n"
	writeThemeFile(t, themesDir, "my-theme.css", css)
	writeThemeFile(t, themesDir, "My Theme.css", css)

	if err := f.DeleteTheme("my-theme"); err == nil {
		t.Fatal("expected an ambiguity error, got nil")
	}
	// Neither file may have been removed by the rejected call.
	for _, name := range []string{"my-theme.css", "My Theme.css"} {
		if _, err := os.Stat(filepath.Join(themesDir, name)); err != nil {
			t.Fatalf("rejected delete must not remove %s: %v", name, err)
		}
	}
}
