package skills

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// DockerBuilder builds Docker images for skills.
type DockerBuilder struct {
	baseImage string
}

// NewDockerBuilder creates a new DockerBuilder with default settings.
func NewDockerBuilder() *DockerBuilder {
	return &DockerBuilder{
		baseImage: "python:3.11-slim",
	}
}

// dockerfileTemplate is the template for generating Dockerfiles.
const dockerfileTemplate = `FROM {{.BaseImage}}
WORKDIR /skill
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
ENTRYPOINT ["python", "{{.EntryPoint}}"]
`

// dockerfileData holds the data for the Dockerfile template.
type dockerfileData struct {
	BaseImage  string
	EntryPoint string
}

// GenerateDockerfile creates a Dockerfile from the skill manifest.
func (b *DockerBuilder) GenerateDockerfile(manifest *SkillManifest) string {
	tmpl, err := template.New("dockerfile").Parse(dockerfileTemplate)
	if err != nil {
		// Template is hardcoded, so this should never happen
		return ""
	}

	data := dockerfileData{
		BaseImage:  b.baseImage,
		EntryPoint: manifest.EntryPoint,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return ""
	}

	return buf.String()
}

// Build runs docker build and returns the image tag.
// It writes the Dockerfile to the skill directory, builds the image,
// and returns the image tag in the format "agent-skill-{name}:{version}".
func (b *DockerBuilder) Build(ctx context.Context, skillDir string, manifest *SkillManifest) (string, error) {
	// Generate the Dockerfile
	dockerfileContent := b.GenerateDockerfile(manifest)
	if dockerfileContent == "" {
		return "", fmt.Errorf("failed to generate Dockerfile")
	}

	// Write Dockerfile to skill directory
	dockerfilePath := filepath.Join(skillDir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte(dockerfileContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	// Ensure requirements.txt exists (create empty one if not)
	requirementsPath := filepath.Join(skillDir, "requirements.txt")
	if _, err := os.Stat(requirementsPath); os.IsNotExist(err) {
		requirementsContent := strings.Join(manifest.Dependencies, "\n")
		if err := os.WriteFile(requirementsPath, []byte(requirementsContent), 0644); err != nil {
			return "", fmt.Errorf("failed to write requirements.txt: %w", err)
		}
	}

	// Build image tag
	imageTag := fmt.Sprintf("agent-skill-%s:%s", manifest.Name, manifest.Version)

	// Run docker build
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", imageTag, skillDir)
	cmd.Dir = skillDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker build failed: %w\nstderr: %s", err, stderr.String())
	}

	return imageTag, nil
}

// SetBaseImage allows changing the base Docker image.
func (b *DockerBuilder) SetBaseImage(image string) {
	b.baseImage = image
}

// BaseImage returns the current base image.
func (b *DockerBuilder) BaseImage() string {
	return b.baseImage
}
