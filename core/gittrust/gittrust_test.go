package gittrust

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestRegistryEmptyByDefault pins the fail-closed starting state: a fresh
// process trusts nothing, so the hardened spawn path stays in force until
// backend populates the registry from config.
func TestRegistryEmptyByDefault(t *testing.T) {
	Clear() // defensive: no test may leak a root into this assertion
	if got := Snapshot(); len(got) != 0 {
		t.Fatalf("registry should start empty, got %v", got)
	}
	if IsTrusted(filepath.Join(t.TempDir(), "repo")) {
		t.Fatal("registry reported a never-trusted root as trusted")
	}
}

func TestTrustUntrustIsTrusted(t *testing.T) {
	Clear()
	t.Cleanup(Clear)

	root := filepath.Join(t.TempDir(), "repo")
	if IsTrusted(root) {
		t.Fatal("fresh root must not be trusted")
	}

	Trust(root)
	if !IsTrusted(root) {
		t.Fatal("Trust did not mark the root trusted")
	}
	Trust(root) // idempotent: no panic, no duplicate

	Untrust(root)
	if IsTrusted(root) {
		t.Fatal("Untrust did not remove the root")
	}
	Untrust(root) // idempotent: removing an absent root is a no-op
}

func TestReplaceMirrorsConfig(t *testing.T) {
	Clear()
	t.Cleanup(Clear)

	a := filepath.Join(t.TempDir(), "repo-a")
	b := filepath.Join(t.TempDir(), "repo-b")

	Replace([]string{a, b, ""}) // empty entries dropped
	if !IsTrusted(a) || !IsTrusted(b) {
		t.Fatalf("Replace did not install both roots: %v", Snapshot())
	}

	// Replace is a full swap, not an append: a dropped root must vanish.
	Replace([]string{b})
	if IsTrusted(a) {
		t.Fatal("Replace left a removed root behind")
	}
	if !IsTrusted(b) {
		t.Fatal("Replace dropped a still-listed root")
	}

	// Nil clears (fail-closed).
	Replace(nil)
	if len(Snapshot()) != 0 {
		t.Fatalf("Replace(nil) should clear the registry, got %v", Snapshot())
	}
}

func TestIsTrustedCanonicalizes(t *testing.T) {
	Clear()
	t.Cleanup(Clear)

	root := filepath.Join(t.TempDir(), "repo")
	Trust(root + string(filepath.Separator)) // trailing separator cleans away
	if !IsTrusted(root) {
		t.Fatal("Trust/IsTrusted do not agree on filepath.Clean canonicalization")
	}

	if IsTrusted("") {
		t.Fatal(`IsTrusted("") must be false (empty is never a stored root)`)
	}
}

func TestSnapshotIsDefensiveCopy(t *testing.T) {
	Clear()
	t.Cleanup(Clear)

	root := filepath.Join(t.TempDir(), "repo")
	Trust(root)

	snap := Snapshot()
	snap[0] = "/corrupted" // mutating the copy must not touch the registry
	if !IsTrusted(root) {
		t.Fatal("mutating Snapshot's return mutated the live registry")
	}
	if !slices.Contains(Snapshot(), root) {
		t.Fatal("registry lost its root")
	}
}
