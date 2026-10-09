package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The tests in this file pin the resolution-safety contract of
// ResolveAndLoad: the agent-data root is created as a chain of REAL
// directories (a dangling link, a non-directory component, or a link swapped
// into a created component fails the load loudly), pre-existing
// operator-symlinked trees resolve as intent, and the process working
// directory is never consulted for a config.yaml.

// TestResolveAndLoad_ResolvesSymlinkedAgentDir pins the revised root
// contract: a PRE-EXISTING symlinked ~/.c0wrk resolves as operator intent
// (the macOS /var → /private/var class — refusing it broke every symlinked
// agent setup), and the derived tree — the config with plaintext provider
// keys, the database, the projects/logs/themes trees — is created inside the
// link's REAL target. A dangling link or a non-directory component still
// fails the load with the loud refusal below.
func TestResolveAndLoad_ResolvesSymlinkedAgentDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics are unix-specific")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(tmpHome, DefaultAgentDir)); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	resolved := ResolveAndLoad(newDiscardLogger())

	for _, loadErr := range resolved.LoadErrors {
		if strings.Contains(loadErr, "not a real directory") {
			t.Fatalf("load error %q refuses an operator-symlinked agent dir", loadErr)
		}
	}

	// The derived tree was created inside the link's resolved target.
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("read link target: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected the agent-data tree inside the symlink's resolved target")
	}
}

// TestResolveAndLoad_NeverAdoptsCwdConfigYaml pins the removal of the
// process-CWD fallback: a directory-planted ./config.yaml must not become the
// live configuration when the primary ~/.c0wrk/config.yaml is absent — the
// document controls spawned MCP commands, security policies and provider
// endpoints, so a silent adoption is a takeover vector. The default config is
// created at the primary path instead.
func TestResolveAndLoad_NeverAdoptsCwdConfigYaml(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// Plant a parseable config.yaml in the process working directory.
	tmpWd := t.TempDir()
	planted := filepath.Join(tmpWd, "config.yaml")
	if err := os.WriteFile(planted, []byte("llm:\n  default_model: planted-model\n"), 0o644); err != nil {
		t.Fatalf("plant CWD config: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	resolved := ResolveAndLoad(newDiscardLogger())

	// The primary path was adopted: a default config was created there and no
	// load error was produced (with the fallback in place, the planted file
	// was loaded instead — its validation failure surfaced as a load error and
	// no default was created).
	primary := ConfigPath(filepath.Join(tmpHome, DefaultAgentDir))
	if resolved.ConfigPath != primary {
		t.Errorf("resolved config path = %q, want the primary %q (the CWD file must never be adopted)", resolved.ConfigPath, primary)
	}
	if len(resolved.LoadErrors) != 0 {
		t.Errorf("load errors = %v, want none (the planted CWD file must be ignored, not loaded)", resolved.LoadErrors)
	}
	if _, err := os.Stat(primary); err != nil {
		t.Errorf("default config was not created at the primary path: %v", err)
	}
	// The planted file itself is untouched.
	if b, err := os.ReadFile(planted); err != nil || !strings.Contains(string(b), "planted-model") {
		t.Errorf("planted CWD config was modified: %q (%v)", b, err)
	}
}
