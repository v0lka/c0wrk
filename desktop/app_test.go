package desktop

import (
	"strings"
	"testing"
)

// TestPickAndImportThemes_NilContext mirrors the PickDirectory /
// SaveMessageAsMarkdown guards: calling the picker before Startup binds the
// Wails context must fail with a descriptive error instead of panicking on
// a nil context (and never reach the dialog or the import path).
func TestPickAndImportThemes_NilContext(t *testing.T) {
	themes, err := (&App{}).PickAndImportThemes()
	if err == nil {
		t.Fatal("expected an error when the application context is not initialized")
	}
	if !strings.Contains(err.Error(), "context is not initialized") {
		t.Errorf("unexpected error message: %v", err)
	}
	if themes != nil {
		t.Errorf("expected nil themes on error, got %+v", themes)
	}
}
