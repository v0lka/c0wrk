package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureMarkdownExtension verifies that the .md guarantee holds for every
// filename shape the native dialog can return.
func TestEnsureMarkdownExtension(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "already .md", in: "/tmp/notes.md", want: "/tmp/notes.md"},
		{name: "uppercase .MD preserved", in: "/tmp/notes.MD", want: "/tmp/notes.MD"},
		{name: "no extension gets .md", in: "/tmp/notes", want: "/tmp/notes.md"},
		{name: "other extension gets .md appended", in: "/tmp/notes.txt", want: "/tmp/notes.txt.md"},
		{name: "trailing dot gets .md", in: "/tmp/notes.", want: "/tmp/notes..md"},
		{name: "directory-like name", in: "/tmp/dir/file", want: "/tmp/dir/file.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ensureMarkdownExtension(tt.in); got != tt.want {
				t.Errorf("ensureMarkdownExtension(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestPickSaveDialogDir verifies the default-directory precedence: an existing
// project directory wins, otherwise the remembered last dialog directory is
// used. A stale (deleted) project directory must fall through to the memory —
// Wails validates DefaultDirectory and refuses to show the dialog when it
// does not exist.
func TestPickSaveDialogDir(t *testing.T) {
	existing := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	lastDir := t.TempDir()

	tests := []struct {
		name       string
		projectDir string
		lastDir    string
		want       string
	}{
		{name: "existing project dir wins", projectDir: existing, lastDir: lastDir, want: existing},
		{name: "missing project dir falls back to last dir", projectDir: missing, lastDir: lastDir, want: lastDir},
		{name: "empty project dir falls back to last dir", projectDir: "", lastDir: lastDir, want: lastDir},
		{name: "file instead of dir falls back", projectDir: filepath.Join(existing, "f"), lastDir: lastDir, want: lastDir},
		{name: "both empty yields empty", projectDir: "", lastDir: "", want: ""},
	}

	// Create the regular file used by the "file instead of dir" case.
	if err := os.WriteFile(filepath.Join(existing, "f"), []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickSaveDialogDir(tt.projectDir, tt.lastDir); got != tt.want {
				t.Errorf("pickSaveDialogDir(%q, %q) = %q, want %q", tt.projectDir, tt.lastDir, got, tt.want)
			}
		})
	}
}

// TestSaveMessageAsMarkdown_NilContext ensures the binding fails loudly
// instead of panicking when called before Startup wires the Wails context
// (mirrors the PickDirectory guard).
func TestSaveMessageAsMarkdown_NilContext(t *testing.T) {
	path, err := (&App{}).SaveMessageAsMarkdown("content")
	if err == nil {
		t.Fatal("expected an error when the application context is not initialized")
	}
	if path != "" {
		t.Errorf("expected empty path on error, got %q", path)
	}
	if !strings.Contains(err.Error(), "context is not initialized") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestResolveWritePath verifies the .md normalization plus the overwrite
// guard: the native dialog's overwrite confirmation covers only the literal
// name the user typed, so when normalization changes that name, an existing
// normalized file must fail closed instead of being silently overwritten.
func TestResolveWritePath(t *testing.T) {
	dir := t.TempDir()

	existingMD := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(existingMD, []byte("original"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	existingTxtMD := filepath.Join(dir, "notes.txt.md")
	if err := os.WriteFile(existingTxtMD, []byte("original"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	t.Run("verbatim .md name passes through even when it exists", func(t *testing.T) {
		// The dialog's own overwrite prompt covered this exact name.
		got, err := resolveWritePath(existingMD)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != existingMD {
			t.Errorf("got %q, want %q", got, existingMD)
		}
	})

	t.Run("extensionless name normalizes to a free .md path", func(t *testing.T) {
		want := filepath.Join(dir, "fresh.md")
		got, err := resolveWritePath(filepath.Join(dir, "fresh"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("existing normalized target fails closed and stays untouched", func(t *testing.T) {
		if _, err := resolveWritePath(filepath.Join(dir, "notes")); err == nil {
			t.Fatal("expected an error when the .md-normalized target exists")
		}
		data, err := os.ReadFile(existingMD)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(data) != "original" {
			t.Errorf("existing file was modified: %q", data)
		}
	})

	t.Run("other extension with existing normalized target fails closed", func(t *testing.T) {
		if _, err := resolveWritePath(filepath.Join(dir, "notes.txt")); err == nil {
			t.Fatal("expected an error when the .md-normalized target exists")
		}
	})
}
