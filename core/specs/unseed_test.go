package specs

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/seedpack"
)

// TestUnseedSkills_RemovesSeededCopiesAndReseeds pins the Specs-gate cycle:
// seed → unseed removes exactly the seeded pack copies (never a non-pack
// directory), a repeated unseed is a no-op, and a following seed
// materializes the pack again — the disable/enable round-trip.
func TestUnseedSkills_RemovesSeededCopiesAndReseeds(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	// A non-pack directory must survive: the unseed touches pack entries only.
	userDir := filepath.Join(dest, "my-own-skill")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir non-pack dir: %v", err)
	}

	res, err := UnseedSkills(dest, nil)
	if err != nil {
		t.Fatalf("UnseedSkills: %v", err)
	}
	if len(res.Removed) != len(expectedSkillNames) {
		t.Fatalf("Removed: got %d entries, want %d: %v", len(res.Removed), len(expectedSkillNames), res.Removed)
	}
	for _, name := range expectedSkillNames {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Errorf("seeded skill %s still exists after unseed (stat err=%v)", name, err)
		}
	}
	if _, err := os.Stat(userDir); err != nil {
		t.Errorf("non-pack directory was removed by the unseed: %v", err)
	}

	res2, err := UnseedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second UnseedSkills: %v", err)
	}
	if len(res2.Removed) != 0 || len(res2.PreservedUser) != 0 || len(res2.PreservedModified) != 0 {
		t.Errorf("second unseed reported activity: %+v", res2)
	}

	// Re-enabling reseeds the pack.
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	for _, name := range expectedSkillNames {
		if _, err := os.Stat(filepath.Join(dest, name, "SKILL.md")); err != nil {
			t.Errorf("%s/SKILL.md missing after reseed: %v", name, err)
		}
	}
}

// TestUnseedSkills_PreservesUserOwnedAndModified pins the non-destructive
// half of the unseed contract: a marker-less user-authored copy and a
// pack-marked locally edited copy survive the unseed untouched, and the
// edited copy is named in a visible warning (the preserve_warn contract).
// The warning is behavior under test here — intercepted with a test-local
// logger and asserted at the source.
func TestUnseedSkills_PreservesUserOwnedAndModified(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}

	// A user-authored copy: a pack skill's name, no marker, diverging.
	userSkill := filepath.Join(dest, "spec-init")
	if err := os.RemoveAll(userSkill); err != nil {
		t.Fatalf("clear seeded spec-init: %v", err)
	}
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatalf("mkdir user skill dir: %v", err)
	}
	userBody := []byte("---\nname: spec-init\ndescription: mine\n---\n# my own copy\n")
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), userBody, 0o644); err != nil {
		t.Fatalf("write user SKILL.md: %v", err)
	}

	// A locally edited seeded copy: the marker stays, the content diverges.
	const localEdit = "\n<!-- local edit -->\n"
	editedPath := filepath.Join(dest, "spec-consult", "SKILL.md")
	orig, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("read seeded spec-consult: %v", err)
	}
	editedBody := make([]byte, 0, len(orig)+len(localEdit))
	editedBody = append(editedBody, orig...)
	editedBody = append(editedBody, localEdit...)
	if err := os.WriteFile(editedPath, editedBody, 0o644); err != nil {
		t.Fatalf("edit seeded spec-consult: %v", err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	res, err := UnseedSkills(dest, logger)
	if err != nil {
		t.Fatalf("UnseedSkills: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(userSkill, "SKILL.md"))
	if err != nil {
		t.Fatalf("user copy vanished: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Error("user-authored spec-init was modified by the unseed")
	}
	if _, err := os.Stat(filepath.Join(userSkill, seedpack.VersionFile)); !os.IsNotExist(err) {
		t.Errorf("user-authored spec-init gained a pack marker (stat err=%v)", err)
	}

	gotEdited, err := os.ReadFile(editedPath)
	if err != nil {
		t.Fatalf("edited copy vanished: %v", err)
	}
	if !bytes.Equal(gotEdited, editedBody) {
		t.Error("locally edited spec-consult content was changed by the unseed")
	}
	if _, err := os.Stat(filepath.Join(dest, "spec-consult", seedpack.VersionFile)); err != nil {
		t.Errorf("edited copy lost its pack marker: %v", err)
	}

	if len(res.PreservedModified) != 1 || res.PreservedModified[0] != "spec-consult" {
		t.Errorf("PreservedModified = %v, want [spec-consult]", res.PreservedModified)
	}
	if len(res.PreservedUser) != 1 || res.PreservedUser[0] != "spec-init" {
		t.Errorf("PreservedUser = %v, want [spec-init]", res.PreservedUser)
	}

	warn := logBuf.String()
	if !strings.Contains(warn, "kept on disk") || !strings.Contains(warn, "spec-consult") {
		t.Errorf("unseed log must warn naming the preserved modified copy, got:\n%s", warn)
	}

	// Every other pack entry (neither user-owned nor edited) is gone.
	for _, name := range expectedSkillNames {
		if name == "spec-init" || name == "spec-consult" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Errorf("seeded skill %s should have been removed (stat err=%v)", name, err)
		}
	}
}

// TestUnseedAgents_RemovesSeededProfileAndPreservesUserOwned pins the agent
// pack's unseed: the seeded specs profile is removed, and a marker-less
// same-named user-authored profile survives untouched.
func TestUnseedAgents_RemovesSeededProfileAndPreservesUserOwned(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedAgents(dest, nil); err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}

	// Replace the seeded profile with a user-authored one (no marker).
	profileDir := filepath.Join(dest, expectedAgentNames[0])
	if err := os.RemoveAll(profileDir); err != nil {
		t.Fatalf("clear seeded profile: %v", err)
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir user profile dir: %v", err)
	}
	userBody := []byte("---\nname: specs\ndescription: my own steward\n---\n# my own specs profile\n")
	if err := os.WriteFile(filepath.Join(profileDir, "AGENT.md"), userBody, 0o644); err != nil {
		t.Fatalf("write user AGENT.md: %v", err)
	}

	res, err := UnseedAgents(dest, nil)
	if err != nil {
		t.Fatalf("UnseedAgents: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %v, want none (the on-disk copy is user-owned)", res.Removed)
	}
	if len(res.PreservedUser) != 1 || res.PreservedUser[0] != expectedAgentNames[0] {
		t.Errorf("PreservedUser = %v, want [%s]", res.PreservedUser, expectedAgentNames[0])
	}
	got, err := os.ReadFile(filepath.Join(profileDir, "AGENT.md"))
	if err != nil {
		t.Fatalf("user profile vanished: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Error("user-authored specs profile was modified by the unseed")
	}

	// A freshly seeded profile is removed by a following unseed.
	if _, err := SeedAgents(dest, nil); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	// SeedAgents preserved the user-owned copy (no marker, diverged), so
	// clear it first to get a genuine pack copy back.
	if err := os.RemoveAll(profileDir); err != nil {
		t.Fatalf("clear user profile: %v", err)
	}
	if _, err := SeedAgents(dest, nil); err != nil {
		t.Fatalf("seed pack copy: %v", err)
	}
	res2, err := UnseedAgents(dest, nil)
	if err != nil {
		t.Fatalf("second UnseedAgents: %v", err)
	}
	if len(res2.Removed) != 1 || res2.Removed[0] != expectedAgentNames[0] {
		t.Errorf("Removed = %v, want [%s]", res2.Removed, expectedAgentNames[0])
	}
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Errorf("seeded profile still exists after unseed (stat err=%v)", err)
	}
}
