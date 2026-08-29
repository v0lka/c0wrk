package terminal

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/version"
)

func envLookup(t *testing.T, env []string, key string) (value string, count int) {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			count++
			value = strings.TrimPrefix(e, prefix)
		}
	}
	return value, count
}

func TestBuildTermEnv_SetsTermProgramConvention(t *testing.T) {
	env := buildTermEnv(nil)

	program, programCount := envLookup(t, env, "TERM_PROGRAM")
	if programCount != 1 {
		t.Fatalf("expected exactly one TERM_PROGRAM entry, got %d (%v)", programCount, env)
	}
	if program != "c0wrk" {
		t.Errorf("TERM_PROGRAM = %q, want %q", program, "c0wrk")
	}

	termVersion, versionCount := envLookup(t, env, "TERM_PROGRAM_VERSION")
	if versionCount != 1 {
		t.Fatalf("expected exactly one TERM_PROGRAM_VERSION entry, got %d (%v)", versionCount, env)
	}
	if termVersion == "" {
		t.Error("TERM_PROGRAM_VERSION must not be empty")
	}
	if termVersion != version.Version {
		t.Errorf("TERM_PROGRAM_VERSION = %q, want build version %q", termVersion, version.Version)
	}
}

func TestBuildTermEnv_OverridesInheritedTermProgram(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "vscode")
	t.Setenv("TERM_PROGRAM_VERSION", "1.99.0")

	env := buildTermEnv(nil)

	program, programCount := envLookup(t, env, "TERM_PROGRAM")
	if programCount != 1 {
		t.Fatalf("inherited TERM_PROGRAM must be replaced, got %d entries (%v)", programCount, env)
	}
	if program != "c0wrk" {
		t.Errorf("TERM_PROGRAM = %q, want inherited %q overridden to %q", program, "vscode", "c0wrk")
	}

	termVersion, versionCount := envLookup(t, env, "TERM_PROGRAM_VERSION")
	if versionCount != 1 {
		t.Fatalf("inherited TERM_PROGRAM_VERSION must be replaced, got %d entries (%v)", versionCount, env)
	}
	if termVersion != version.Version {
		t.Errorf("TERM_PROGRAM_VERSION = %q, want %q", termVersion, version.Version)
	}
}

func TestBuildTermEnv_TermSemanticsPreserved(t *testing.T) {
	// Existing TERM/COLORTERM must be preserved as-is (no override).
	t.Setenv("TERM", "screen-256color")
	t.Setenv("COLORTERM", "24bit")
	env := buildTermEnv(nil)

	if term, count := envLookup(t, env, "TERM"); count != 1 || term != "screen-256color" {
		t.Errorf("TERM = %q (count %d), want inherited screen-256color", term, count)
	}
	if colorterm, count := envLookup(t, env, "COLORTERM"); count != 1 || colorterm != "24bit" {
		t.Errorf("COLORTERM = %q (count %d), want inherited 24bit", colorterm, count)
	}
}

func TestBuildTermEnv_DefaultsTermWhenMissing(t *testing.T) {
	if err := os.Unsetenv("TERM"); err != nil {
		t.Fatalf("failed to unset TERM: %v", err)
	}
	if err := os.Unsetenv("COLORTERM"); err != nil {
		t.Fatalf("failed to unset COLORTERM: %v", err)
	}

	env := buildTermEnv(nil)

	if term, count := envLookup(t, env, "TERM"); count != 1 || term != "xterm-256color" {
		t.Errorf("TERM = %q (count %d), want default xterm-256color", term, count)
	}
	if colorterm, count := envLookup(t, env, "COLORTERM"); count != 1 || colorterm != "truecolor" {
		t.Errorf("COLORTERM = %q (count %d), want default truecolor", colorterm, count)
	}
}

func TestBuildTermEnv_NoDuplicateKeys(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "vscode")
	t.Setenv("TERM", "xterm-256color")

	env := buildTermEnv(nil)

	seen := make(map[string]int)
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		seen[key]++
	}
	for key, count := range seen {
		if count > 1 {
			t.Errorf("duplicate env key %s appears %d times", key, count)
		}
	}
}

func TestBuildTermEnv_UserEnvOverridesDefaultsAndInherited(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "vscode")

	env := buildTermEnv(map[string]string{
		"TERM_PROGRAM": "custom-marker",
		"TMUX_GUARD":   "1",
	})

	if program, count := envLookup(t, env, "TERM_PROGRAM"); count != 1 || program != "custom-marker" {
		t.Errorf("TERM_PROGRAM = %q (count %d), want configured custom-marker", program, count)
	}
	if v, count := envLookup(t, env, "TMUX_GUARD"); count != 1 || v != "1" {
		t.Errorf("TMUX_GUARD = %q (count %d), want configured 1", v, count)
	}
	// Untouched defaults remain.
	if v, count := envLookup(t, env, "TERM_PROGRAM_VERSION"); count != 1 || v != version.Version {
		t.Errorf("TERM_PROGRAM_VERSION = %q (count %d), want %q", v, count, version.Version)
	}
}

func TestBuildTermEnv_UserEnvOverridesBuiltinDefaults(t *testing.T) {
	env := buildTermEnv(map[string]string{"TERM": "c0wrk-custom"})

	if term, count := envLookup(t, env, "TERM"); count != 1 || term != "c0wrk-custom" {
		t.Errorf("TERM = %q (count %d), want configured c0wrk-custom", term, count)
	}
}

func TestBuildTermEnv_NilAndEmptyExtraEquivalent(t *testing.T) {
	fromNil := buildTermEnv(nil)
	fromEmpty := buildTermEnv(map[string]string{})

	if !reflect.DeepEqual(fromNil, fromEmpty) {
		t.Errorf("nil and empty extra env must produce identical results:\nnil:   %v\nempty: %v", fromNil, fromEmpty)
	}
}
