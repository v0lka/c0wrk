package config

import (
	"fmt"
	"github.com/v0lka/sp4rk/pathutil"
	"path/filepath"
	"testing"
)

func TestWorktreesRootConfigRoundTrip(t *testing.T) {
	base := `llm:
  default_model: anthropic/model
  anthropic:
    models: [model]
`
	for _, root := range []string{"", t.TempDir()} {
		path := writeTestConfig(t, base+fmt.Sprintf("git:\n  worktrees_root: %q\n", root))
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Git.WorktreesRoot != root {
			t.Errorf("Load(%q).WorktreesRoot = %q, want %q", root, cfg.Git.WorktreesRoot, root)
		}
		cfg.LogLevel = "debug" // An unrelated settings write must preserve the key.
		if err := Save(cfg, path); err != nil {
			t.Fatal(err)
		}
		again, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if again.Git.WorktreesRoot != root {
			t.Errorf("Load(Save(%q)).WorktreesRoot = %q, want %q", root, again.Git.WorktreesRoot, root)
		}
	}
	for _, root := range []string{"relative", "~/worktrees", "${HOME}/worktrees", filepath.Join(t.TempDir(), "a") + string(filepath.Separator) + ".."} {
		path := writeTestConfig(t, base+fmt.Sprintf("git:\n  worktrees_root: %q\n", root))
		if _, err := Load(path); err == nil {
			t.Errorf("Load(worktrees_root=%q) = nil error, want invalid absolute clean path", root)
		}
	}
}

func TestNewManagedWorktreesDir(t *testing.T) {
	agentDir := t.TempDir()
	for _, override := range []string{"", t.TempDir()} {
		got, err := NewManagedWorktreesDir(agentDir, "project-id", override)
		if err != nil {
			t.Fatal(err)
		}
		base := override
		if base == "" {
			base = filepath.Join(agentDir, "projects")
		}
		base = pathutil.ResolveExistingPrefix(base)
		want := filepath.Join(base, "project-id", "worktrees")
		if got != want {
			t.Errorf("NewManagedWorktreesDir(%q).path = %q, want %q", override, got, want)
		}
	}
	for _, id := range []string{"", "..", "../escape", "a/b", "NUL"} {
		if _, err := NewManagedWorktreesDir(agentDir, id, ""); err == nil {
			t.Errorf("NewManagedWorktreesDir(project=%q) = nil error, want rejection", id)
		}
	}
}

// TestManagedWorktreeNameFromPathProject pins durable-path recognition: a
// project-scoped container path is recognized by its layout even without a
// repository marker, while a foreign project id or an arbitrary path is
// refused. Legacy derivation is purely lexical (no container inspection).
func TestManagedWorktreeNameFromPathProject(t *testing.T) {
	repo := t.TempDir()
	tree := "s-abcdef01"
	legacy := filepath.Join(ManagedWorktreesDir(repo), tree)

	scoped := filepath.Join(t.TempDir(), "proj-1", "worktrees", tree)
	if got, err := ManagedWorktreeNameFromPathProject(repo, "proj-1", scoped); err != nil || got != tree {
		t.Fatalf("project-scoped = %q, %v; want %q", got, err, tree)
	}
	// The strict recognizer refuses the same marker-less path.
	if _, err := ManagedWorktreeNameFromPath(repo, scoped); err == nil {
		t.Error("strict recognizer accepted a marker-less path")
	}
	if got, err := ManagedWorktreeNameFromPathProject(repo, "proj-1", legacy); err != nil || got != tree {
		t.Fatalf("legacy = %q, %v; want %q", got, err, tree)
	}
	if _, err := ManagedWorktreeNameFromPathProject(repo, "other", scoped); err == nil {
		t.Error("accepted a foreign project-scoped container")
	}
	if _, err := ManagedWorktreeNameFromPathProject(repo, "proj-1", repo); err == nil {
		t.Error("accepted the repository root")
	}
	if got, err := ManagedWorktreeLegacyPath(repo, tree); err != nil || got != legacy {
		t.Fatalf("ManagedWorktreeLegacyPath = %q, %v; want %q", got, err, legacy)
	}
}
