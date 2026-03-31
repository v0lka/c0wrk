package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// SkillContainer manages a Docker container for running a skill.
type SkillContainer struct {
	manifest *SkillManifest
	imageTag string
	builder  *DockerBuilder
	running  bool
}

// NewSkillContainer creates a new SkillContainer for the given manifest.
func NewSkillContainer(manifest *SkillManifest, builder *DockerBuilder) *SkillContainer {
	return &SkillContainer{
		manifest: manifest,
		builder:  builder,
		running:  false,
	}
}

// Build builds the Docker image for this skill.
func (sc *SkillContainer) Build(ctx context.Context, skillDir string) error {
	imageTag, err := sc.builder.Build(ctx, skillDir, sc.manifest)
	if err != nil {
		return fmt.Errorf("failed to build skill image: %w", err)
	}
	sc.imageTag = imageTag
	return nil
}

// Run executes the skill with given JSON input, returns JSON output.
func (sc *SkillContainer) Run(ctx context.Context, input map[string]interface{}) (string, error) {
	if sc.imageTag == "" {
		return "", fmt.Errorf("skill image not built, call Build() first")
	}

	// Marshal input to JSON
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("failed to marshal input: %w", err)
	}

	// Build docker run command with security flags
	args := sc.buildDockerArgs()
	args = append(args, sc.imageTag)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	sc.running = true
	defer func() { sc.running = false }()

	if err := cmd.Run(); err != nil {
		// Check if context was cancelled
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("docker run failed: %w\nstderr: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// buildDockerArgs constructs the docker run arguments based on capabilities.
func (sc *SkillContainer) buildDockerArgs() []string {
	args := []string{"run", "--rm"}

	// Default security options (always applied)
	args = append(args,
		"--security-opt=no-new-privileges",
		"--read-only",
		"--cap-drop=ALL",
		"--user=1000:1000",
		"--memory=512m",
		"--cpus=1",
	)

	// Always provide tmpfs for /tmp (scripts may need temporary file storage)
	args = append(args, "--tmpfs", "/tmp:size=64m")

	// Network isolation based on capabilities
	if !sc.hasCapability("network") {
		args = append(args, "--network=none")
	}

	// Interactive mode for stdin
	args = append(args, "-i")

	return args
}

// hasCapability checks if the manifest includes a specific capability.
func (sc *SkillContainer) hasCapability(cap string) bool {
	for _, c := range sc.manifest.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Stop stops any running container.
// Note: Since we use --rm flag, containers are automatically removed after exit.
// This method is provided for cases where we need to forcefully stop a running container.
func (sc *SkillContainer) Stop() error {
	if !sc.running {
		return nil
	}
	// The container is managed via context cancellation in Run()
	// When context is cancelled, the docker run command is killed
	sc.running = false
	return nil
}

// IsBuilt returns whether the image has been built.
func (sc *SkillContainer) IsBuilt() bool {
	return sc.imageTag != ""
}

// ImageTag returns the Docker image tag for this skill.
func (sc *SkillContainer) ImageTag() string {
	return sc.imageTag
}

// Manifest returns the skill manifest.
func (sc *SkillContainer) Manifest() *SkillManifest {
	return sc.manifest
}

// IsRunning returns whether the container is currently running.
func (sc *SkillContainer) IsRunning() bool {
	return sc.running
}

// SecurityFlags returns the list of security-related Docker flags that will be applied.
// This is useful for testing and debugging.
func (sc *SkillContainer) SecurityFlags() []string {
	flags := []string{
		"--security-opt=no-new-privileges",
		"--read-only",
		"--cap-drop=ALL",
		"--user=1000:1000",
		"--memory=512m",
		"--cpus=1",
		"--tmpfs /tmp:size=64m",
	}

	if !sc.hasCapability("network") {
		flags = append(flags, "--network=none")
	}

	return flags
}
