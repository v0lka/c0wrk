//go:build windows

package terminal

import (
	"strings"
	"testing"
)

// foldEqualCount counts env entries whose name folds to key (Windows names
// are case-insensitive but case-preserving).
func foldEqualCount(env []string, key string) (count int, value string) {
	for _, e := range env {
		name, v, _ := strings.Cut(e, "=")
		if strings.EqualFold(name, key) {
			count++
			value = v
		}
	}
	return count, value
}

// TestBuildTermEnv_UserEnvKeyCaseInsensitive: whatever spelling PATH carries
// in the inherited block ("Path" from Explorer-launched processes, "PATH"
// from some shells), a terminal.env key spelled differently must replace it —
// not duplicate it. Two fold-equal entries in the ConPTY env block leave the
// child's lookup order undefined.
func TestBuildTermEnv_UserEnvKeyCaseInsensitive(t *testing.T) {
	env := buildTermEnv(map[string]string{"path": `C:\custom\bin`})

	count, value := foldEqualCount(env, "path")
	if count != 1 {
		t.Fatalf("expected exactly one fold-equal PATH entry after terminal.env override, got %d", count)
	}
	if value != `C:\custom\bin` {
		t.Errorf("PATH entry = %q, want configured C:\\custom\\bin", value)
	}
}

// TestBuildTermEnv_TermProgramFilterCaseInsensitive: the force-set
// TERM_PROGRAM must also replace an inherited entry when the terminal.env key
// casing differs from the inherited spelling. With case-sensitive matching
// the block would contain both TERM_PROGRAM and term_program.
func TestBuildTermEnv_TermProgramFilterCaseInsensitive(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "vscode")

	env := buildTermEnv(map[string]string{"term_program": "custom-marker"})

	count, value := foldEqualCount(env, "TERM_PROGRAM")
	if count != 1 {
		t.Fatalf("expected exactly one fold-equal TERM_PROGRAM entry, got %d", count)
	}
	if value != "custom-marker" {
		t.Errorf("TERM_PROGRAM = %q, want configured custom-marker", value)
	}
}
