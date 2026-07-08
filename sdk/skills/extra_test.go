package skills

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/pathutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ---------- SkillManager.log ----------

func TestSkillManagerLogWithLogger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mgr := NewSkillManager([]string{dir}, slog.Default())
	// The log method returns slog.Default() when logger is nil,
	// but we want to exercise the branch where logger is non-nil.
	// We can't easily assert the return value, but we ensure no panic.
	l := mgr.log()
	if l == nil {
		t.Error("log() returned nil")
	}
}

// ---------- SkillManager.Scan / List / Get / SkillPath ----------

func TestSkillManagerGetNotFound(t *testing.T) {
	t.Parallel()
	mgr := NewSkillManager(nil, nil)
	skill, ok := mgr.Get("nonexistent")
	if ok {
		t.Errorf("expected ok=false, got ok=true, skill=%v", skill)
	}
	if skill != nil {
		t.Error("expected nil skill")
	}
}

func TestSkillManagerSkillPathFound(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "test-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(skillDir, "SKILL.md"), "test-skill", "A test skill.", "Body.")

	mgr := NewSkillManager([]string{tmpDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}

	path, ok := mgr.SkillPath("test-skill")
	if !ok {
		t.Error("expected skill to be found")
	}
	if path != skillDir {
		t.Errorf("SkillPath = %q, want %q", path, skillDir)
	}
}

func TestSkillManagerSkillPathNotFound(t *testing.T) {
	t.Parallel()
	mgr := NewSkillManager(nil, nil)
	path, ok := mgr.SkillPath("nonexistent")
	if ok {
		t.Error("expected ok=false")
	}
	if path != "" {
		t.Errorf("expected empty path, got %q", path)
	}
}

func TestSkillManagerListEmpty(t *testing.T) {
	t.Parallel()
	mgr := NewSkillManager(nil, nil)
	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}
}

// ---------- Skill.Descriptor ----------

func TestSkillDescriptor(t *testing.T) {
	t.Parallel()
	s := &Skill{
		Metadata: SkillMetadata{
			Name:        "my-skill",
			Description: "Does something useful.",
		},
		Body:    "Step 1: Do it.",
		DirPath: "/some/dir",
	}
	d := s.Descriptor()
	if d.Name != "my-skill" {
		t.Errorf("Descriptor.Name = %q, want %q", d.Name, "my-skill")
	}
	if d.Description != "Does something useful." {
		t.Errorf("Descriptor.Description = %q, want %q", d.Description, "Does something useful.")
	}
}

// ---------- ParseError ----------

func TestParseError(t *testing.T) {
	t.Parallel()
	e := &ParseError{Path: "/some/SKILL.md", Message: "bad name"}
	if e.Error() != "skill parse error (/some/SKILL.md): bad name" {
		t.Errorf("Error() = %q, want %q", e.Error(), "skill parse error (/some/SKILL.md): bad name")
	}
}

// ---------- ParseSkill edge cases ----------

func TestParseSkillFileNotFound(t *testing.T) {
	t.Parallel()
	_, err := ParseSkill("/nonexistent/SKILL.md", "/nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseSkillInvalidYAML(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Malformed YAML (tab instead of space after colon)
	content := "---\nname:\tbad-tabs\ndescription: Has tabs.\n---\nBody."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseSkillNameMismatchDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	// Directory name doesn't match skill name
	skillDir := filepath.Join(tmpDir, "different-dir")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: my-skill\ndescription: Name mismatch.\n---\nBody."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err == nil {
		t.Error("expected error for name/directory mismatch")
	}
}

func TestParseSkillWithOptionalFields(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "full-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: full-skill\ndescription: Full featured.\nlicense: MIT\ncompatibility: all\nallowed-tools: Read Write\nmetadata:\n  author: test\n---\n\nBody content here."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	skill, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if skill.Metadata.License != "MIT" {
		t.Errorf("License = %q, want %q", skill.Metadata.License, "MIT")
	}
	if skill.Metadata.Compatibility != "all" {
		t.Errorf("Compatibility = %q, want %q", skill.Metadata.Compatibility, "all")
	}
	if skill.Metadata.AllowedTools != "Read Write" {
		t.Errorf("AllowedTools = %q, want %q", skill.Metadata.AllowedTools, "Read Write")
	}
	if skill.Body != "Body content here." {
		t.Errorf("Body = %q, want %q", skill.Body, "Body content here.")
	}
}

// ---------- ValidateSkill edge cases ----------

func TestValidateSkillCompatibilityTooLong(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "compat-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	longCompat := "---\nname: compat-skill\ndescription: Compat test.\ncompatibility: " + strings.Repeat("x", 501) + "\n---\nBody."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(longCompat), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err == nil {
		t.Error("expected error for compatibility > 500 chars")
	}
}

func TestValidateSkillNameWithConsecutiveHyphens(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	// The regex ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ does NOT reject consecutive hyphens,
	// so "bad--name" is valid. Test a truly invalid name: leading digit + special char.
	skillDir := filepath.Join(tmpDir, "1bad+name")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: 1bad+name\ndescription: Bad name with special char.\n---\nBody."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err == nil {
		t.Error("expected error for name with special character")
	}
}

func TestValidateSkillNameEndsWithHyphen(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "bad-name-")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: bad-name-\ndescription: Bad name.\n---\nBody."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseSkill(filepath.Join(skillDir, "SKILL.md"), skillDir)
	if err == nil {
		t.Error("expected error for trailing hyphen in name")
	}
}

// ---------- SafeResolvePath ----------

func TestSafeResolvePathValid(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file inside
	if err := os.WriteFile(filepath.Join(subDir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := SafeResolvePath(tmpDir, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// On macOS /var is a symlink to /private/var, so EvalSymlinks may change the prefix.
	// Verify the resolved path is within the expected directory.
	expected := filepath.Join(tmpDir, "sub", "file.txt")
	if path != expected {
		// Accept symlink-resolved version (e.g., /private/var vs /var on macOS).
		resolved, _ := filepath.EvalSymlinks(expected)
		if path != resolved {
			t.Errorf("path = %q, want %q (or %q)", path, expected, resolved)
		}
	}
}

func TestSafeResolvePathDot(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	path, err := SafeResolvePath(tmpDir, ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != tmpDir {
		// Accept symlink-resolved version (e.g., /private/var vs /var on macOS).
		resolved, _ := filepath.EvalSymlinks(tmpDir)
		if path != resolved {
			t.Errorf("path = %q, want %q (or %q)", path, tmpDir, resolved)
		}
	}
}

func TestSafeResolvePathEmptyRelPath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	path, err := SafeResolvePath(tmpDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// On macOS /var is a symlink to /private/var — accept either.
	cleanDir := filepath.Clean(tmpDir)
	if path != cleanDir {
		resolved, _ := filepath.EvalSymlinks(cleanDir)
		if path != resolved {
			t.Errorf("path = %q, want %q (or %q)", path, cleanDir, resolved)
		}
	}
}

func TestSafeResolvePathTraversal(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file outside base
	outsideFile := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := SafeResolvePath(baseDir, "../outside.txt")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestSafeResolvePathSymlinkEscapes(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file outside the base
	outsideFile := filepath.Join(tmpDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink inside base pointing outside
	linkPath := filepath.Join(baseDir, "escape-link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, err := SafeResolvePath(baseDir, "escape-link")
	if err == nil {
		t.Error("expected error for symlink escaping base directory")
	}
}

func TestSafeResolvePathBrokenSymlink(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "base")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a broken symlink (pointing to nonexistent file within base)
	brokenLink := filepath.Join(baseDir, "broken")
	if err := os.Symlink(filepath.Join(baseDir, "nonexistent-target"), brokenLink); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	// Broken symlink within base should still resolve; the longest existing
	// prefix is symlink-resolved (e.g. /var → /private/var on macOS) and the
	// non-existent suffix is joined back.
	path, err := SafeResolvePath(baseDir, "broken")
	if err != nil {
		t.Fatalf("unexpected error for broken symlink within base: %v", err)
	}
	want := filepath.Clean(pathutil.ResolveExistingPrefix(brokenLink))
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestSafeResolvePathSymlinkToDirInsideBase(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	baseDir := filepath.Join(tmpDir, "base")
	targetDir := filepath.Join(baseDir, "target-dir")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "data.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink inside base to another dir inside base
	linkPath := filepath.Join(baseDir, "link-dir")
	if err := os.Symlink(targetDir, linkPath); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	path, err := SafeResolvePath(baseDir, "link-dir/data.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The resolved path should be inside the target directory.
	// On macOS /var → /private/var so EvalSymlinks changes path prefix.
	expected := filepath.Join(targetDir, "data.txt")
	if path != expected {
		resolved, _ := filepath.EvalSymlinks(expected)
		if path != resolved {
			t.Errorf("path = %q, want %q (or %q)", path, expected, resolved)
		}
	}
}

// ---------- scanDir edge cases ----------

func TestScanDirRegularFiles(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Add a regular file (not a symlink, not a dir)
	if err := os.WriteFile(filepath.Join(scanDir, "README.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Add a valid skill
	skillDir := filepath.Join(scanDir, "a-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(skillDir, "SKILL.md"), "a-skill", "A real skill.", "Body.")

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	list := mgr.List()
	if len(list) != 1 {
		t.Errorf("expected 1 skill, got %d", len(list))
	}
}

func TestScanDirInvalidSkill(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a directory with no SKILL.md
	skillDir := filepath.Join(scanDir, "not-a-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Also create a valid skill
	validDir := filepath.Join(scanDir, "valid-skill")
	if err := os.MkdirAll(validDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(validDir, "SKILL.md"), "valid-skill", "Valid.", "Body.")

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	list := mgr.List()
	if len(list) != 1 {
		t.Errorf("expected 1 skill (invalid one skipped), got %d", len(list))
	}
}

func TestScanDirInvalidSkillMD(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a dir with an invalid SKILL.md (missing description)
	badDir := filepath.Join(scanDir, "bad-skill")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte("---\nname: bad-skill\n---\nBody."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a valid skill
	goodDir := filepath.Join(scanDir, "good-skill")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(goodDir, "SKILL.md"), "good-skill", "Good.", "Body.")

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	list := mgr.List()
	if len(list) != 1 {
		t.Errorf("expected 1 skill, got %d", len(list))
	}
}

func TestScanDirEmpty(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	emptyDir := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := NewSkillManager([]string{emptyDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 0 {
		t.Error("expected no skills from empty dir")
	}
}

// ---------- ReadSkillResourceTool.Execute ----------

func TestReadSkillResourceToolExecuteSuccess(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resourceContent := "Some reference material."
	if err := os.WriteFile(filepath.Join(skillDir, "reference.md"), []byte(resourceContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Resolver that returns the skill dir
	resolver := func(_ context.Context, name string) (string, bool) {
		if name == "my-skill" {
			return skillDir, true
		}
		return "", false
	}
	tool := NewReadSkillResourceTool(resolver)

	input := json.RawMessage(`{"skill": "my-skill", "path": "reference.md"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != resourceContent {
		t.Errorf("Content = %q, want %q", result.Content, resourceContent)
	}
}

func TestReadSkillResourceToolExecuteSkillNotFound(t *testing.T) {
	t.Parallel()
	resolver := func(_ context.Context, name string) (string, bool) {
		return "", false
	}
	tool := NewReadSkillResourceTool(resolver)

	input := json.RawMessage(`{"skill": "nonexistent", "path": "file.txt"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing skill")
	}
	if !strings.Contains(result.Content, "not found") {
		t.Errorf("error should mention 'not found', got: %v", result.Content)
	}
}

func TestReadSkillResourceToolExecutePathTraversal(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := func(_ context.Context, name string) (string, bool) {
		if name == "my-skill" {
			return skillDir, true
		}
		return "", false
	}
	tool := NewReadSkillResourceTool(resolver)

	input := json.RawMessage(`{"skill": "my-skill", "path": "../outside.txt"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for path traversal")
	}
}

func TestReadSkillResourceToolExecuteResourceNotFound(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	resolver := func(_ context.Context, name string) (string, bool) {
		if name == "my-skill" {
			return skillDir, true
		}
		return "", false
	}
	tool := NewReadSkillResourceTool(resolver)

	input := json.RawMessage(`{"skill": "my-skill", "path": "nonexistent.txt"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing resource")
	}
}

func TestReadSkillResourceToolExecuteEmptySkill(t *testing.T) {
	t.Parallel()
	tool := NewReadSkillResourceTool(nil)
	input := json.RawMessage(`{"skill": "", "path": "file.txt"}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty skill")
	}
}

func TestReadSkillResourceToolExecuteEmptyPath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := func(_ context.Context, name string) (string, bool) {
		if name == "my-skill" {
			return skillDir, true
		}
		return "", false
	}
	tool := NewReadSkillResourceTool(resolver)
	input := json.RawMessage(`{"skill": "my-skill", "path": ""}`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty path")
	}
}

func TestReadSkillResourceToolExecuteInvalidJSON(t *testing.T) {
	t.Parallel()
	tool := NewReadSkillResourceTool(nil)
	input := json.RawMessage(`not json`)
	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ParseInputError wraps parse errors into ToolResult
	if !result.IsError {
		t.Error("expected error result for invalid JSON")
	}
}

// ---------- SkillManager with non-nil logger ----------

func TestSkillManagerScanWithLogger(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "logged-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(skillDir, "SKILL.md"), "logged-skill", "With logger.", "Body.")

	// Use a non-nil logger
	mgr := NewSkillManager([]string{tmpDir}, slog.Default())
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	skill, ok := mgr.Get("logged-skill")
	if !ok {
		t.Error("expected skill to be found")
	}
	if skill.Metadata.Name != "logged-skill" {
		t.Errorf("name = %q", skill.Metadata.Name)
	}
}

// ---------- scanDir with broken symlink ----------

func TestScanDirBrokenSymlink(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a broken symlink to a nonexistent dir
	brokenLink := filepath.Join(scanDir, "broken-link")
	if err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), brokenLink); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	// Also create a valid skill
	validDir := filepath.Join(scanDir, "valid-skill")
	if err := os.MkdirAll(validDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(validDir, "SKILL.md"), "valid-skill", "Valid.", "Body.")

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	list := mgr.List()
	if len(list) != 1 {
		t.Errorf("expected 1 skill (broken symlink skipped), got %d", len(list))
	}
}

// ---------- Re-scan (clear existing, then reload) ----------

func TestSkillManagerRescan(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "first-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(skillDir, "SKILL.md"), "first-skill", "First.", "Body.")

	mgr := NewSkillManager([]string{tmpDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 1 {
		t.Fatalf("expected 1 skill after first scan, got %d", len(mgr.List()))
	}

	// Remove the skill dir
	if err := os.RemoveAll(skillDir); err != nil {
		t.Fatal(err)
	}

	// Re-scan — should clear existing skills and find nothing
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 0 {
		t.Errorf("expected 0 skills after re-scan, got %d", len(mgr.List()))
	}
}

// ---------- NewReadSkillResourceTool schema validation ----------

func TestReadSkillResourceToolSchema(t *testing.T) {
	t.Parallel()
	tool := NewReadSkillResourceTool(nil)
	if tool.ToolName != "read_skill_resource" {
		t.Errorf("ToolName = %q", tool.ToolName)
	}
	if tool.ToolDescription != toolReadSkillResourceDesc {
		t.Errorf("ToolDescription mismatch")
	}
	if len(tool.Schema) == 0 {
		t.Error("Schema is empty")
	}
	// Verify the schema is valid JSON
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(tool.Schema, &schemaMap); err != nil {
		t.Errorf("Schema is not valid JSON: %v", err)
	}
	if schemaMap["type"] != "object" {
		t.Errorf("schema type = %v, want object", schemaMap["type"])
	}
	props, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["skill"]; !ok {
		t.Error("schema missing 'skill' property")
	}
	if _, ok := props["path"]; !ok {
		t.Error("schema missing 'path' property")
	}
}

// ---------- SkillPathResolver type ----------

func TestSkillPathResolverType(t *testing.T) {
	t.Parallel()
	var resolver SkillPathResolver = func(_ context.Context, name string) (string, bool) {
		return "/skills/" + name, true
	}
	path, ok := resolver(context.Background(), "pdf")
	if !ok {
		t.Error("expected ok=true")
	}
	if path != "/skills/pdf" {
		t.Errorf("path = %q", path)
	}
}

// ---------- Tool Execute with BaseTool field ----------

func TestReadSkillResourceToolPolicyAlwaysAllow(t *testing.T) {
	t.Parallel()
	tool := NewReadSkillResourceTool(nil)
	if tool.Policy != sdktools.PolicyAlwaysAllow {
		t.Errorf("Policy = %v, want PolicyAlwaysAllow", tool.Policy)
	}
}
