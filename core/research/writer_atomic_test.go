package research

// Tests pinning writeFilesAtomic's no-partial-outcome guarantee: a write set
// either lands completely or leaves every target byte-for-byte unchanged.
// The Windows dialect of "an unwritable target" (an exclusive handle) lives
// in writer_atomic_windows_test.go; the fabricated-state rollback test below
// covers the commit-phase mechanics on every platform without needing an OS
// lock.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeAtomicFixture prepares a research root with one existing file and
// returns the root, the existing target's path, and a second (new-file)
// target path that sorts after it.
func writeAtomicFixture(t *testing.T) (root, first, second string) {
	t.Helper()
	root = t.TempDir()
	first = filepath.Join(root, "a-first.md")
	second = filepath.Join(root, "z-second.md")
	if err := os.WriteFile(first, []byte("first original\n"), 0o644); err != nil {
		t.Fatalf("writing first target: %v", err)
	}
	return root, first, second
}

// dirEntryNames lists the base names of every entry in dir (dotfiles
// included) so the tests can assert that no temp (.hyp-*.tmp) or backup
// (.bak-*) litter survives a write.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestWriteFilesAtomic_CommitsNewAndExistingWithoutLitter pins the happy
// path: an existing target is replaced, a new target is created, and neither
// the staged temps nor the moved-aside backups survive the committed write.
func TestWriteFilesAtomic_CommitsNewAndExistingWithoutLitter(t *testing.T) {
	root, first, second := writeAtomicFixture(t)

	if err := writeFilesAtomic(root, map[string][]byte{
		first:  []byte("first replaced\n"),
		second: []byte("second created\n"),
	}); err != nil {
		t.Fatalf("writeFilesAtomic: %v", err)
	}

	if got, err := os.ReadFile(first); err != nil || string(got) != "first replaced\n" {
		t.Errorf("first target = %q, %v; want replaced content", got, err)
	}
	if got, err := os.ReadFile(second); err != nil || string(got) != "second created\n" {
		t.Errorf("second target = %q, %v; want created content", got, err)
	}
	names := dirEntryNames(t, root)
	if len(names) != 2 || names[0] != "a-first.md" || names[1] != "z-second.md" {
		t.Errorf("leftover staging artifacts in %s: %v", root, names)
	}
}

// TestWriteFilesAtomic_NonRegularTargetAbortsUntouched pins the pre-flight
// gate: a target that exists but is not a regular file (a directory here) is
// rejected before ANY file of the set is moved, so the other targets stay
// byte-for-byte unchanged and the directory itself survives.
func TestWriteFilesAtomic_NonRegularTargetAbortsUntouched(t *testing.T) {
	root, first, second := writeAtomicFixture(t)
	// second exists as a directory: renaming a staged temp onto it could never
	// be atomic, so the whole set must abort up front.
	if err := os.Mkdir(second, 0o755); err != nil {
		t.Fatalf("mkdir second target: %v", err)
	}

	err := writeFilesAtomic(root, map[string][]byte{
		first:  []byte("must not be written\n"),
		second: []byte("must not be written\n"),
	})
	if err == nil {
		t.Fatal("writeFilesAtomic accepted a directory target")
	}

	if got, readErr := os.ReadFile(first); readErr != nil || string(got) != "first original\n" {
		t.Errorf("first target = %q, %v; want byte-for-byte original", got, readErr)
	}
	if info, statErr := os.Stat(second); statErr != nil || !info.IsDir() {
		t.Errorf("directory target was disturbed: %v, %v", info, statErr)
	}
	names := dirEntryNames(t, root)
	if len(names) != 2 {
		t.Errorf("leftover staging artifacts in %s: %v", root, names)
	}
}

// TestRollbackStaged_RestoresCommittedSet pins the commit-phase rollback with
// fabricated in-flight state: entry A is fully committed (its new contents
// sit at the target, its original at the backup), entry B's original is
// parked at the backup with no target in place (its commit rename just
// failed), and entry C was a brand-new file whose temp got as far as the
// target. rollbackStaged must restore A and B byte-for-byte, remove C, and
// leave the displaced new contents parked at the temp names for cleanup.
func TestRollbackStaged_RestoresCommittedSet(t *testing.T) {
	root := t.TempDir()
	targetA := filepath.Join(root, "a.md")
	targetB := filepath.Join(root, "b.md")
	targetC := filepath.Join(root, "c.md")
	for _, f := range []struct {
		path    string
		content string
	}{
		{targetA, "a original\n"},
		{targetB, "b original\n"},
	} {
		if err := os.WriteFile(f.path, []byte(f.content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", f.path, err)
		}
	}

	backupA := filepath.Join(root, ".a.md.bak-test")
	backupB := filepath.Join(root, ".b.md.bak-test")
	tmpA := filepath.Join(root, ".hyp-a.tmp")
	tmpC := filepath.Join(root, ".hyp-c.tmp")
	// Commit A: new contents at the target, original moved aside.
	if err := os.WriteFile(backupA, []byte("a original\n"), 0o644); err != nil {
		t.Fatalf("writing backupA: %v", err)
	}
	if err := os.WriteFile(targetA, []byte("a new\n"), 0o644); err != nil {
		t.Fatalf("overwriting targetA: %v", err)
	}
	// B: original parked at the backup, target missing (commit failed).
	if err := os.WriteFile(backupB, []byte("b original\n"), 0o644); err != nil {
		t.Fatalf("writing backupB: %v", err)
	}
	if err := os.Remove(targetB); err != nil {
		t.Fatalf("removing targetB: %v", err)
	}
	// C: brand-new file whose temp reached the target.
	if err := os.WriteFile(targetC, []byte("c new\n"), 0o644); err != nil {
		t.Fatalf("writing targetC: %v", err)
	}

	staged := []stagedFile{
		{target: targetA, tmp: tmpA, backup: backupA, existed: true},
		{target: targetB, tmp: filepath.Join(root, ".hyp-b.tmp"), backup: backupB, existed: true},
		{target: targetC, tmp: tmpC},
	}
	if err := rollbackStaged(staged); err != nil {
		t.Fatalf("rollbackStaged: %v", err)
	}

	for _, f := range []struct {
		path    string
		content string
	}{
		{targetA, "a original\n"},
		{targetB, "b original\n"},
	} {
		if got, err := os.ReadFile(f.path); err != nil || string(got) != f.content {
			t.Errorf("%s = %q, %v; want the original bytes back", f.path, got, err)
		}
	}
	if _, err := os.Lstat(targetC); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("brand-new target C survived the rollback: %v", err)
	}
	// The displaced new contents are parked at the temp names (cleanup's job).
	if got, err := os.ReadFile(tmpA); err != nil || string(got) != "a new\n" {
		t.Errorf("displaced contents of A = %q, %v; want them parked at %s", got, err, tmpA)
	}
}
