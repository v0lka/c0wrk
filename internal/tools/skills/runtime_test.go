package skills

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillContainer_New(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()

	sc := NewSkillContainer(manifest, builder)

	if sc == nil {
		t.Fatal("NewSkillContainer returned nil")
	}
	if sc.Manifest() != manifest {
		t.Error("Manifest() should return the provided manifest")
	}
	if sc.IsBuilt() {
		t.Error("IsBuilt() should return false before Build()")
	}
	if sc.IsRunning() {
		t.Error("IsRunning() should return false initially")
	}
	if sc.ImageTag() != "" {
		t.Error("ImageTag() should be empty before Build()")
	}
}

func TestSkillContainer_SecurityFlags(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		wantFlags    []string
		dontWant     []string
	}{
		{
			name:         "no capabilities - maximum security",
			capabilities: nil,
			wantFlags: []string{
				"--security-opt=no-new-privileges",
				"--read-only",
				"--cap-drop=ALL",
				"--user=1000:1000",
				"--memory=512m",
				"--cpus=1",
				"--tmpfs /tmp:size=64m",
				"--network=none",
			},
			dontWant: nil,
		},
		{
			name:         "with network capability",
			capabilities: []string{"network"},
			wantFlags: []string{
				"--security-opt=no-new-privileges",
				"--read-only",
				"--cap-drop=ALL",
				"--user=1000:1000",
				"--memory=512m",
				"--cpus=1",
				"--tmpfs /tmp:size=64m",
			},
			dontWant: []string{"--network=none"},
		},
		{
			name:         "with filesystem capability",
			capabilities: []string{"filesystem"},
			wantFlags: []string{
				"--security-opt=no-new-privileges",
				"--read-only",
				"--cap-drop=ALL",
				"--user=1000:1000",
				"--memory=512m",
				"--cpus=1",
				"--tmpfs /tmp:size=64m",
				"--network=none",
			},
			dontWant: nil,
		},
		{
			name:         "with network and filesystem capabilities",
			capabilities: []string{"network", "filesystem"},
			wantFlags: []string{
				"--security-opt=no-new-privileges",
				"--read-only",
				"--cap-drop=ALL",
				"--user=1000:1000",
				"--memory=512m",
				"--cpus=1",
				"--tmpfs /tmp:size=64m",
			},
			dontWant: []string{"--network=none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &SkillManifest{
				Name:         "test-skill",
				Description:  "A test skill",
				Version:      "1.0.0",
				Language:     "python",
				EntryPoint:   "main.py",
				Capabilities: tt.capabilities,
			}
			builder := NewDockerBuilder()
			sc := NewSkillContainer(manifest, builder)

			flags := sc.SecurityFlags()

			// Check that all wanted flags are present
			for _, wantFlag := range tt.wantFlags {
				found := false
				for _, flag := range flags {
					if flag == wantFlag {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected flag '%s' not found in %v", wantFlag, flags)
				}
			}

			// Check that unwanted flags are not present
			for _, dontWant := range tt.dontWant {
				for _, flag := range flags {
					if flag == dontWant {
						t.Errorf("unexpected flag '%s' found in %v", dontWant, flags)
					}
				}
			}
		})
	}
}

func TestSkillContainer_NetworkIsolation(t *testing.T) {
	tests := []struct {
		name          string
		capabilities  []string
		expectNetwork bool
	}{
		{
			name:          "no capabilities - network isolated",
			capabilities:  nil,
			expectNetwork: false,
		},
		{
			name:          "empty capabilities - network isolated",
			capabilities:  []string{},
			expectNetwork: false,
		},
		{
			name:          "filesystem only - network isolated",
			capabilities:  []string{"filesystem"},
			expectNetwork: false,
		},
		{
			name:          "network capability - has network",
			capabilities:  []string{"network"},
			expectNetwork: true,
		},
		{
			name:          "network and other capabilities - has network",
			capabilities:  []string{"filesystem", "network"},
			expectNetwork: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := &SkillManifest{
				Name:         "test-skill",
				Description:  "A test skill",
				Version:      "1.0.0",
				Language:     "python",
				EntryPoint:   "main.py",
				Capabilities: tt.capabilities,
			}
			builder := NewDockerBuilder()
			sc := NewSkillContainer(manifest, builder)

			flags := sc.SecurityFlags()

			hasNetworkNone := false
			for _, flag := range flags {
				if flag == "--network=none" {
					hasNetworkNone = true
					break
				}
			}

			if tt.expectNetwork && hasNetworkNone {
				t.Error("expected network access, but --network=none is present")
			}
			if !tt.expectNetwork && !hasNetworkNone {
				t.Error("expected network isolation (--network=none), but it's not present")
			}
		})
	}
}

func TestSkillContainer_IsBuilt(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	if sc.IsBuilt() {
		t.Error("IsBuilt() should return false before Build()")
	}
}

func TestSkillContainer_Stop(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	// Stop should not error when not running
	if err := sc.Stop(); err != nil {
		t.Errorf("Stop() should not error when not running: %v", err)
	}
}

func TestSkillContainer_RunWithoutBuild(t *testing.T) {
	manifest := &SkillManifest{
		Name:        "test-skill",
		Description: "A test skill",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	ctx := context.Background()
	_, err := sc.Run(ctx, map[string]interface{}{"test": "value"})
	if err == nil {
		t.Error("Run() should error when image is not built")
	}
}

// Integration tests that require Docker

func TestSkillContainer_Integration_BuildAndRun(t *testing.T) {
	if !isDockerAvailable() {
		t.Skip("Docker is not available, skipping integration test")
	}

	manifest := &SkillManifest{
		Name:        "integration-test-skill",
		Description: "A skill for integration testing",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	// Create temporary skill directory
	tmpDir, err := os.MkdirTemp("", "skill-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create main.py that echoes input
	mainPy := `import json
import sys

input_data = json.load(sys.stdin)
output = {"received": input_data, "status": "ok"}
print(json.dumps(output))
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.py"), []byte(mainPy), 0o644); err != nil {
		t.Fatalf("failed to write main.py: %v", err)
	}

	// Create empty requirements.txt
	if err := os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}

	// Build the skill
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := sc.Build(ctx, tmpDir); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !sc.IsBuilt() {
		t.Error("IsBuilt() should return true after Build()")
	}

	// Run the skill
	input := map[string]interface{}{
		"message": "hello",
		"count":   42,
	}

	output, err := sc.Run(ctx, input)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify output contains expected content
	if output == "" {
		t.Error("Run() returned empty output")
	}

	// Clean up the built image
	_ = exec.CommandContext(context.Background(), "docker", "rmi", sc.ImageTag()).Run()
}

func TestSkillContainer_Integration_ContextCancellation(t *testing.T) {
	if !isDockerAvailable() {
		t.Skip("Docker is not available, skipping integration test")
	}

	manifest := &SkillManifest{
		Name:        "slow-skill",
		Description: "A slow skill for timeout testing",
		Version:     "1.0.0",
		Language:    "python",
		EntryPoint:  "main.py",
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	// Create temporary skill directory
	tmpDir, err := os.MkdirTemp("", "skill-timeout-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create main.py that sleeps for a long time
	mainPy := `import json
import sys
import time

time.sleep(60)
print(json.dumps({"status": "done"}))
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.py"), []byte(mainPy), 0o644); err != nil {
		t.Fatalf("failed to write main.py: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "requirements.txt"), []byte(""), 0o644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}

	// Build with longer timeout
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer buildCancel()

	if err := sc.Build(buildCtx, tmpDir); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Run with short timeout that should be exceeded
	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer runCancel()

	_, err = sc.Run(runCtx, map[string]interface{}{})
	if err == nil {
		t.Error("Run() should error on context cancellation")
	}

	// Clean up
	_ = exec.CommandContext(context.Background(), "docker", "rmi", sc.ImageTag()).Run()
}

func TestSkillContainer_hasCapability(t *testing.T) {
	manifest := &SkillManifest{
		Name:         "test-skill",
		Description:  "A test skill",
		Version:      "1.0.0",
		Language:     "python",
		EntryPoint:   "main.py",
		Capabilities: []string{"network", "filesystem"},
	}
	builder := NewDockerBuilder()
	sc := NewSkillContainer(manifest, builder)

	if !sc.hasCapability("network") {
		t.Error("hasCapability('network') should return true")
	}
	if !sc.hasCapability("filesystem") {
		t.Error("hasCapability('filesystem') should return true")
	}
	if sc.hasCapability("gpu") {
		t.Error("hasCapability('gpu') should return false")
	}
}
