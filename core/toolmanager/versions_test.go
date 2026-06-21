package toolmanager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadVersions_NoFile(t *testing.T) {
	dir := t.TempDir()
	tv, err := ReadVersions(dir)
	if err != nil {
		t.Fatalf("ReadVersions returned error: %v", err)
	}
	if len(tv) != 0 {
		t.Errorf("expected empty map from non-existent file, got %d entries", len(tv))
	}
}

func TestReadWriteRoundtrip(t *testing.T) {
	dir := t.TempDir()
	orig := ToolVersions{"rg": "14.1.0", "uv": "0.5.0"}

	if err := WriteVersions(dir, orig); err != nil {
		t.Fatalf("WriteVersions failed: %v", err)
	}
	got, err := ReadVersions(dir)
	if err != nil {
		t.Fatalf("ReadVersions failed: %v", err)
	}
	if got["rg"] != "14.1.0" || got["uv"] != "0.5.0" {
		t.Errorf("roundtrip mismatch: got %v", got)
	}
}

func TestWriteVersions_Atomicity(t *testing.T) {
	dir := t.TempDir()
	versions := ToolVersions{"rg": "14.1.0"}
	if err := WriteVersions(dir, versions); err != nil {
		t.Fatalf("WriteVersions failed: %v", err)
	}
	// Verify the tmp file was cleaned up.
	tmpPath := filepath.Join(dir, versionsFileName+".tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temporary file was not cleaned up after atomic write")
	}
	// Verify the real file is valid JSON.
	path := filepath.Join(dir, versionsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written versions file: %v", err)
	}
	var tv ToolVersions
	if err := json.Unmarshal(data, &tv); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
}

func TestReadVersions_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, versionsFileName)
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}
	_, err := ReadVersions(dir)
	if err == nil {
		t.Error("expected error reading corrupt versions file")
	}
}
