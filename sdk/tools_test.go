package sp4rk

import (
	"testing"

	"github.com/v0lka/sp4rk/tools"
)

// toolNames extracts the Name() of each tool for assertion.
func toolNames(ts []tools.Tool) []string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.Name()
	}
	return names
}

func TestFileTools(t *testing.T) {
	got := toolNames(FileTools())
	want := []string{"read_file", "write_file", "edit_file", "list_directory", "glob", "create_directory"}
	assertSameSet(t, "FileTools", got, want)
}

func TestMemoryTools(t *testing.T) {
	got := toolNames(MemoryTools())
	want := []string{"store_fact", "search_facts"}
	assertSameSet(t, "MemoryTools", got, want)
}

func TestCodeTools(t *testing.T) {
	got := toolNames(CodeTools())
	want := []string{
		"read_file", "write_file", "edit_file", "list_directory", "glob", "create_directory",
		"ripgrep", "delete_file", "delete_directory",
	}
	assertSameSet(t, "CodeTools", got, want)
}

func TestFinishTool(t *testing.T) {
	got := FinishTool()
	if len(got) != 1 {
		t.Fatalf("FinishTool len = %d, want 1", len(got))
	}
	if got[0].Name() != "finish" {
		t.Errorf("FinishTool name = %q, want %q", got[0].Name(), "finish")
	}
}

func TestAllBuiltinTools(t *testing.T) {
	got := AllBuiltinTools()
	if len(got) < 10 {
		t.Errorf("AllBuiltinTools len = %d, want at least 10", len(got))
	}
}

func TestToolsPassthrough(t *testing.T) {
	bundle := FileTools()
	combined := Tools(bundle...)
	if len(combined) != len(bundle) {
		t.Fatalf("Tools passthrough len = %d, want %d", len(combined), len(bundle))
	}
}

// assertSameSet checks that got and want contain the same elements, ignoring order.
func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s len = %d (%v), want %d (%v)", label, len(got), got, len(want), want)
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("%s contains unexpected tool %q", label, g)
		}
	}
}
