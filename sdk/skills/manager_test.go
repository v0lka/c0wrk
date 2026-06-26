package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantName string
		wantDesc string
		wantBody string
		wantErr  bool
	}{
		{
			name:     "minimal valid",
			content:  "---\nname: pdf-processing\ndescription: Extract PDF text and tables.\n---\n\nStep 1: Read the PDF.",
			wantName: "pdf-processing",
			wantDesc: "Extract PDF text and tables.",
			wantBody: "Step 1: Read the PDF.",
		},
		{
			name:     "with optional fields",
			content:  "---\nname: data-analysis\ndescription: Analyze datasets.\nlicense: Apache-2.0\ncompatibility: Requires Python 3.14+\nallowed-tools: Read Bash(jq:*)\nmetadata:\n  author: example-org\n  version: \"1.0\"\n---\n\nAnalyze the data.",
			wantName: "data-analysis",
			wantDesc: "Analyze datasets.",
			wantBody: "Analyze the data.",
		},
		{
			name:    "empty content",
			content: "",
			wantErr: true,
		},
		{
			name:    "missing frontmatter",
			content: "Just some markdown without frontmatter.",
			wantErr: true,
		},
		{
			name:    "missing closing delimiter",
			content: "---\nname: test-skill\ndescription: A test.\n",
			wantErr: true,
		},
		{
			name:    "missing name",
			content: "---\ndescription: No name provided.\n---\nBody.",
			wantErr: true,
		},
		{
			name:    "missing description",
			content: "---\nname: no-desc\n---\nBody.",
			wantErr: true,
		},
		{
			name:    "uppercase name",
			content: "---\nname: PDF-Processing\ndescription: Bad name.\n---\nBody.",
			wantErr: true,
		},
		{
			name:    "name starts with hyphen",
			content: "---\nname: -bad-name\ndescription: Bad name.\n---\nBody.",
			wantErr: true,
		},
		{
			name:    "name too long",
			content: "---\nname: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ndescription: Too long name.\n---\nBody.", // 66 chars
			wantErr: true,
		},
		{
			name:    "description too long",
			content: "---\nname: long-desc\ndescription: " + string(make([]byte, 1025)) + "\n---\nBody.",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a temp directory named after the expected skill name
			dirName := tt.wantName
			if dirName == "" {
				dirName = "fallback-dir"
			}
			tmpDir := t.TempDir()
			skillDir := filepath.Join(tmpDir, dirName)
			if err := os.MkdirAll(skillDir, 0o755); err != nil {
				t.Fatal(err)
			}
			skillMDPath := filepath.Join(skillDir, "SKILL.md")
			if err := os.WriteFile(skillMDPath, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			skill, err := ParseSkill(skillMDPath, skillDir)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if skill.Metadata.Name != tt.wantName {
				t.Errorf("name = %q, want %q", skill.Metadata.Name, tt.wantName)
			}
			if skill.Metadata.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", skill.Metadata.Description, tt.wantDesc)
			}
			if skill.Body != tt.wantBody {
				t.Errorf("body = %q, want %q", skill.Body, tt.wantBody)
			}
		})
	}
}

func TestAllowedToolList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tools string
		want  []string
	}{
		{name: "empty", tools: "", want: nil},
		{name: "single", tools: "Read", want: []string{"Read"}},
		{name: "multiple", tools: "Read Write Bash(git:*)", want: []string{"Read", "Write", "Bash(git:*)"}},
		{name: "extra spaces", tools: "  Read   Write  ", want: []string{"Read", "Write"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := SkillMetadata{AllowedTools: tt.tools}
			got := m.AllowedToolList()
			if len(got) != len(tt.want) {
				t.Fatalf("AllowedToolList() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("AllowedToolList()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSkillManagerScan(t *testing.T) {
	t.Parallel()

	// Create temp directory structure with two skill dirs (different priority)
	tmpDir := t.TempDir()

	// High priority dir: contains pdf-processing
	highDir := filepath.Join(tmpDir, "high")
	lowDir := filepath.Join(tmpDir, "low")
	if err := os.MkdirAll(filepath.Join(highDir, "pdf-processing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(lowDir, "data-analysis"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Override: same skill in both dirs
	if err := os.MkdirAll(filepath.Join(lowDir, "pdf-processing"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write SKILL.md files
	writeSkillMD(t, filepath.Join(highDir, "pdf-processing", "SKILL.md"),
		"pdf-processing", "High priority PDF skill.", "High priority body.")
	writeSkillMD(t, filepath.Join(lowDir, "data-analysis", "SKILL.md"),
		"data-analysis", "Analyze data.", "Analyze body.")
	writeSkillMD(t, filepath.Join(lowDir, "pdf-processing", "SKILL.md"),
		"pdf-processing", "Low priority PDF skill.", "Low priority body.")

	mgr := NewSkillManager([]string{highDir, lowDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}

	// Should find 2 skills
	list := mgr.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}

	// High-priority pdf-processing should win
	skill, ok := mgr.Get("pdf-processing")
	if !ok {
		t.Fatal("pdf-processing skill not found")
	}
	if skill.Metadata.Description != "High priority PDF skill." {
		t.Errorf("got low-priority description: %q", skill.Metadata.Description)
	}

	// data-analysis from low dir should be available
	skill2, ok := mgr.Get("data-analysis")
	if !ok {
		t.Fatal("data-analysis skill not found")
	}
	if skill2.Metadata.Description != "Analyze data." {
		t.Errorf("unexpected description: %q", skill2.Metadata.Description)
	}
}

func TestSkillManagerSymlink(t *testing.T) {
	t.Parallel()

	// Create a real skill directory outside the scan root.
	tmpDir := t.TempDir()
	realSkillDir := filepath.Join(tmpDir, "real-skills", "my-skill")
	if err := os.MkdirAll(realSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkillMD(t, filepath.Join(realSkillDir, "SKILL.md"),
		"my-skill", "Symlinked skill.", "Body via symlink.")

	// Create a scan root with a symlink pointing to the real skill dir.
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realSkillDir, filepath.Join(scanDir, "my-skill")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}

	skill, ok := mgr.Get("my-skill")
	if !ok {
		t.Fatal("expected symlinked skill to be discovered")
	}
	if skill.Metadata.Description != "Symlinked skill." {
		t.Errorf("unexpected description: %q", skill.Metadata.Description)
	}
	if skill.DirPath != filepath.Join(scanDir, "my-skill") {
		t.Errorf("DirPath = %q, want symlink path %q", skill.DirPath, filepath.Join(scanDir, "my-skill"))
	}
}

func TestSkillManagerSymlinkToFile(t *testing.T) {
	t.Parallel()

	// Symlinks to files should be ignored (not treated as skill dirs).
	tmpDir := t.TempDir()
	scanDir := filepath.Join(tmpDir, "scan")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a regular file and symlink to it in the scan dir.
	regularFile := filepath.Join(tmpDir, "some-file.txt")
	if err := os.WriteFile(regularFile, []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regularFile, filepath.Join(scanDir, "not-a-dir")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	mgr := NewSkillManager([]string{scanDir}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatal(err)
	}
	if len(mgr.List()) != 0 {
		t.Error("expected no skills from symlink to file")
	}
}

func TestSkillManagerNonexistentDir(t *testing.T) {
	t.Parallel()

	mgr := NewSkillManager([]string{"/nonexistent/path"}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatalf("Scan should not error on nonexistent dirs: %v", err)
	}
	if len(mgr.List()) != 0 {
		t.Error("expected no skills from nonexistent dir")
	}
}

// writeSkillMD is a test helper that creates a minimal SKILL.md file.
func writeSkillMD(t *testing.T, path, name, description, body string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
