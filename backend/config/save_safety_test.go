package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The tests in this file pin the FIFO/symlink safety of the atomic config
// writers (backend/config Save + SaveCustomModelProfiles): the old
// write-to-fixed-"<path>.tmp"-then-rename pattern followed a symlink planted
// at the deterministic temp name (truncating an arbitrary user file with the
// YAML) and blocked forever on a FIFO planted there (open with O_WRONLY
// waits for a reader). The randomized temp name + rename-over of safeio
// removes both mechanisms: there is no plantable fixed temp path, and the
// final rename replaces a planted symlink AT the target itself instead of
// writing through it.

// TestSaveDoesNotFollowPlantedTmpSymlink pins the symlink half of the atomic
// config write: the fixed ".tmp" sibling must not exist anymore, and a link
// planted at the old deterministic name must be left untouched.
func TestSaveDoesNotFollowPlantedTmpSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics are unix-specific")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	if err := os.Symlink(victim, cfgPath+".tmp"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Save(cfg, cfgPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The config was written as a regular file.
	fi, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Fatalf("config.yaml is not a regular file after Save: mode=%v", fi.Mode())
	}
	if data, err := os.ReadFile(cfgPath); err != nil || len(data) == 0 {
		t.Fatalf("config.yaml empty or unreadable after Save: %v", err)
	}

	// The planted link and its target are untouched: the old implementation
	// truncated the target with the YAML contents.
	if b, err := os.ReadFile(victim); err != nil || string(b) != "SECRET" {
		t.Fatalf("victim file was modified through the planted symlink: %q (%v)", b, err)
	}
}

// TestSaveReplacesPlantedSymlinkAtTarget pins the rename semantics: a symlink
// planted at config.yaml itself is replaced AS AN ENTRY by the atomic rename —
// the link's target is never written through.
func TestSaveReplacesPlantedSymlinkAtTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics are unix-specific")
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	if err := os.Symlink(victim, cfgPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Save(cfg, cfgPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if b, err := os.ReadFile(victim); err != nil || string(b) != "SECRET" {
		t.Fatalf("victim file was written through the planted symlink: %q (%v)", b, err)
	}
	fi, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the symlink at config.yaml survived Save; want it replaced by a regular file")
	}
}

// TestSaveCustomModelProfilesDoesNotFollowPlantedTmpSymlink mirrors the config
// Save contract for the model-profiles store writer.
func TestSaveCustomModelProfilesDoesNotFollowPlantedTmpSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics are unix-specific")
	}
	dir := t.TempDir()
	storePath := filepath.Join(dir, "model-profiles.yaml")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	if err := os.Symlink(victim, storePath+".tmp"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	// Saving an empty set is legal and writes the version marker.
	if err := SaveCustomModelProfiles(storePath, nil); err != nil {
		t.Fatalf("SaveCustomModelProfiles: %v", err)
	}

	if b, err := os.ReadFile(victim); err != nil || string(b) != "SECRET" {
		t.Fatalf("victim file was modified through the planted symlink: %q (%v)", b, err)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Fatalf("store file missing after save: %v", err)
	}
}

// TestSavePublishesOwnerOnlyMode pins the published permission bits of both
// atomic YAML writers. WriteFileAtomic publishes the staging file with an
// exact fchmod (no umask filtering — unlike the os.WriteFile staging writes
// these files had before, which produced perm &^ umask), so the writer
// itself must carry the final mode. config.yaml stores provider API keys,
// so both stores publish 0o600 — owner-only regardless of the ambient umask
// and regardless of any pre-existing file's wider mode (the rename replaces
// the whole entry).
func TestSavePublishesOwnerOnlyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are unix-specific")
	}

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &Config{}
	ApplyDefaults(cfg)
	if err := Save(cfg, cfgPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("config.yaml mode = %04o, want 0600 (owner-only, exact fchmod)", got)
	}

	// A pre-existing wider mode must not survive the rename: the published
	// entry carries the writer's own owner-only bits.
	if err := os.Chmod(cfgPath, 0o666); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	if err := Save(cfg, cfgPath); err != nil {
		t.Fatalf("Save (re-save): %v", err)
	}
	fi, err = os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config after re-save: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("config.yaml mode after re-save = %04o, want 0600 (wider pre-existing mode must be replaced)", got)
	}

	storePath := filepath.Join(t.TempDir(), "model-profiles.yaml")
	if err := SaveCustomModelProfiles(storePath, nil); err != nil {
		t.Fatalf("SaveCustomModelProfiles: %v", err)
	}
	fi, err = os.Stat(storePath)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("model-profiles.yaml mode = %04o, want 0600 (owner-only, exact fchmod)", got)
	}
}
