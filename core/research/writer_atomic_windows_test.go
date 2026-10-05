//go:build windows

package research

// The Windows dialect of writeFilesAtomic's no-partial-outcome guarantee: a
// target held open with no sharing (makeUnreadable) cannot be moved aside, so
// the move-aside phase fails mid-set and the already-moved originals must be
// rolled back byte-for-byte. This is the exact CI regression shape: a runner
// combination that reads THROUGH such a handle (the fail-closed read guard
// then does not fire) still cannot replace the file it holds, so the write
// set's rollback — not the read guard — is what keeps the research artifacts
// consistent.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFilesAtomic_LockedSecondTargetRollsBackSet locks the
// alphabetically-second target and requires the whole write set to abort with
// the first target byte-for-byte unchanged, the locked target's original
// intact, and no temp/backup litter left behind.
func TestWriteFilesAtomic_LockedSecondTargetRollsBackSet(t *testing.T) {
	root, first, second := writeAtomicFixture(t)
	restoreSecond := makeUnreadable(t, second)
	defer restoreSecond()

	err := writeFilesAtomic(root, map[string][]byte{
		first:  []byte("first must not land\n"),
		second: []byte("second must not land\n"),
	})
	if err == nil {
		t.Fatal("writeFilesAtomic succeeded although a target was locked")
	}

	// Restore readability before re-reading for the byte-for-byte comparison.
	restoreSecond()
	if got, readErr := os.ReadFile(first); readErr != nil || string(got) != "first original\n" {
		t.Errorf("first target = %q, %v; want byte-for-byte original after the rollback", got, readErr)
	}
	if got, readErr := os.ReadFile(second); readErr != nil || string(got) != "second original\n" {
		t.Errorf("second target = %q, %v; want byte-for-byte original after the rollback", got, readErr)
	}
	names := dirEntryNames(t, root)
	if len(names) != 2 || names[0] != "a-first.md" || names[1] != "z-second.md" {
		t.Errorf("leftover staging artifacts in %s: %v", root, names)
	}
}

// TestWriteFilesAtomic_LockedSingleTargetAborts pins the degenerate shape
// behind the SetActiveResearch guard: locking the ONLY target of a set must
// abort with the original intact and no litter (nothing was moved aside yet
// when the lock bites, so the rollback is trivially empty).
func TestWriteFilesAtomic_LockedSingleTargetAborts(t *testing.T) {
	root := t.TempDir()
	only := filepath.Join(root, "only.md")
	if err := os.WriteFile(only, []byte("only original\n"), 0o644); err != nil {
		t.Fatalf("writing only target: %v", err)
	}
	restore := makeUnreadable(t, only)
	defer restore()

	if err := writeFilesAtomic(root, map[string][]byte{only: []byte("must not land\n")}); err == nil {
		t.Fatal("writeFilesAtomic succeeded although the only target was locked")
	}

	restore()
	if got, readErr := os.ReadFile(only); readErr != nil || string(got) != "only original\n" {
		t.Errorf("only target = %q, %v; want byte-for-byte original", got, readErr)
	}
	names := dirEntryNames(t, root)
	if len(names) != 1 || names[0] != "only.md" {
		t.Errorf("leftover staging artifacts in %s: %v", root, names)
	}
}
