package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewDockerBuilder(t *testing.T) {
	builder := NewDockerBuilder()
	if builder == nil {
		t.Fatal("NewDockerBuilder returned nil")
	}
	if builder.BaseImage() != "python:3.11-slim" {
		t.Errorf("expected base image 'python:3.11-slim', got '%s'", builder.BaseImage())
	}
}

func TestDockerBuilder_SetBaseImage(t *testing.T) {
	builder := NewDockerBuilder()
	builder.SetBaseImage("python:3.12-slim")
	if builder.BaseImage() != "python:3.12-slim" {
		t.Errorf("expected base image 'python:3.12-slim', got '%s'", builder.BaseImage())
	}
}

func TestGenerateDockerfile_Basic(t *testing.T) {
	builder := NewDockerBuilder()
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}

	dockerfile := builder.GenerateDockerfile(manifest)

	// Verify Dockerfile content
	expectedLines := []string{
		"FROM python:3.11-slim",
		"WORKDIR /skill",
		"COPY requirements.txt .",
		"RUN pip install --no-cache-dir -r requirements.txt",
		"COPY . .",
		`ENTRYPOINT ["python", "main.py"]`,
	}

	for _, line := range expectedLines {
		if !strings.Contains(dockerfile, line) {
			t.Errorf("Dockerfile missing expected line: %s\nGot:\n%s", line, dockerfile)
		}
	}
}

func TestGenerateDockerfile_WithDependencies(t *testing.T) {
	builder := NewDockerBuilder()
	manifest := &SkillManifest{
		Name:         "data-processor",
		Description:  "Processes data files",
		Version:      "2.0.0",
		Language:     "python",
		EntryPoint:   "process.py",
		Dependencies: []string{"pandas", "numpy", "requests"},
	}

	dockerfile := builder.GenerateDockerfile(manifest)

	// The Dockerfile should reference requirements.txt
	// Dependencies are written to requirements.txt in Build(), not in GenerateDockerfile()
	if !strings.Contains(dockerfile, "COPY requirements.txt .") {
		t.Error("Dockerfile should copy requirements.txt")
	}
	if !strings.Contains(dockerfile, "pip install --no-cache-dir -r requirements.txt") {
		t.Error("Dockerfile should install from requirements.txt")
	}
	if !strings.Contains(dockerfile, `ENTRYPOINT ["python", "process.py"]`) {
		t.Errorf("Dockerfile should have correct entrypoint, got:\n%s", dockerfile)
	}
}

func TestGenerateDockerfile_CustomBaseImage(t *testing.T) {
	builder := NewDockerBuilder()
	builder.SetBaseImage("python:3.12-alpine")

	manifest := &SkillManifest{
		Name:        "alpine-skill",
		Description: "A skill using alpine",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "run.py",
	}

	dockerfile := builder.GenerateDockerfile(manifest)

	if !strings.Contains(dockerfile, "FROM python:3.12-alpine") {
		t.Errorf("Dockerfile should use custom base image, got:\n%s", dockerfile)
	}
}

func TestDockerBuilder_BuildCommand(t *testing.T) {
	// This test verifies the docker build command construction
	// by checking that the correct image tag format is generated
	builder := NewDockerBuilder()
	manifest := &SkillManifest{
		Name:        "my-skill",
		Description: "Test skill",
		Version:     "1.2.3",
		Language:    "python",
		EntryPoint:  "main.py",
	}

	// Expected image tag format: agent-skill-{name}:{version}
	expectedTag := "agent-skill-my-skill:1.2.3"

	// Create a temporary directory for the skill
	tmpDir, err := os.MkdirTemp("", "skill-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a minimal main.py
	mainPy := `import json, sys
input_data = json.load(sys.stdin)
print(json.dumps({"result": "ok"}))
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.py"), []byte(mainPy), 0644); err != nil {
		t.Fatalf("failed to write main.py: %v", err)
	}

	// Create requirements.txt
	if err := os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}

	// Skip if Docker is not available
	if !isDockerAvailable() {
		t.Skip("Docker is not available, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	imageTag, err := builder.Build(ctx, tmpDir, manifest)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if imageTag != expectedTag {
		t.Errorf("expected image tag '%s', got '%s'", expectedTag, imageTag)
	}

	// Verify Dockerfile was written
	dockerfilePath := filepath.Join(tmpDir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		t.Error("Dockerfile was not written to skill directory")
	}

	// Clean up the built image
	_ = exec.Command("docker", "rmi", imageTag).Run()
}

func TestDockerBuilder_BuildCreatesRequirements(t *testing.T) {
	// Test that Build creates requirements.txt from dependencies if it doesn't exist
	builder := NewDockerBuilder()
	manifest := &SkillManifest{
		Name:         "dep-skill",
		Description:  "Skill with dependencies",
		Version:      "1.0.0",
		Language:     "python",
		EntryPoint:   "main.py",
		Dependencies: []string{"requests", "pyyaml"},
	}

	// Create a temporary directory without requirements.txt
	tmpDir, err := os.MkdirTemp("", "skill-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a minimal main.py
	mainPy := `import json, sys
print(json.dumps({"result": "ok"}))
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.py"), []byte(mainPy), 0644); err != nil {
		t.Fatalf("failed to write main.py: %v", err)
	}

	// Skip if Docker is not available
	if !isDockerAvailable() {
		t.Skip("Docker is not available, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	imageTag, err := builder.Build(ctx, tmpDir, manifest)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Verify requirements.txt was created
	requirementsPath := filepath.Join(tmpDir, "requirements.txt")
	content, err := os.ReadFile(requirementsPath)
	if err != nil {
		t.Fatalf("failed to read requirements.txt: %v", err)
	}

	expectedContent := "requests\npyyaml"
	if string(content) != expectedContent {
		t.Errorf("expected requirements.txt content '%s', got '%s'", expectedContent, string(content))
	}

	// Clean up the built image
	_ = exec.Command("docker", "rmi", imageTag).Run()
}

// isDockerAvailable checks if Docker daemon is running and accessible.
func isDockerAvailable() bool {
	cmd := exec.Command("docker", "version")
	return cmd.Run() == nil
}
