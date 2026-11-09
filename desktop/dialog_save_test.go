package desktop

import (
	"strings"
	"testing"
)

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
