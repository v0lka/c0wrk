package desktop

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/user/agent/backend/mcp"
)

func TestTriggerCodebaseIndexing_NotInstalled(t *testing.T) {
	// Override checkCodebaseMemoryFn to simulate not installed
	origCheck := checkCodebaseMemoryFn
	origExec := execCommandFn
	t.Cleanup(func() {
		checkCodebaseMemoryFn = origCheck
		execCommandFn = origExec
	})

	var execCalled bool
	checkCodebaseMemoryFn = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: false}
	}
	execCommandFn = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "true")
	}

	app := &App{}
	app.triggerCodebaseIndexing("/tmp/test-workspace")

	if execCalled {
		t.Error("exec should not have been called when codebase-memory-mcp is not installed")
	}
}

func TestTriggerCodebaseIndexing_Installed(t *testing.T) {
	origCheck := checkCodebaseMemoryFn
	origExec := execCommandFn
	t.Cleanup(func() {
		checkCodebaseMemoryFn = origCheck
		execCommandFn = origExec
	})

	var mu sync.Mutex
	var capturedName string
	var capturedArgs []string

	checkCodebaseMemoryFn = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}
	execCommandFn = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		capturedName = name
		capturedArgs = arg
		mu.Unlock()
		// Return a no-op command that succeeds
		return exec.CommandContext(ctx, "true")
	}

	app := &App{}
	app.triggerCodebaseIndexing("/tmp/test-workspace")

	mu.Lock()
	defer mu.Unlock()

	if capturedName != "/usr/local/bin/codebase-memory-mcp" {
		t.Errorf("expected binary path %q, got %q", "/usr/local/bin/codebase-memory-mcp", capturedName)
	}
	if len(capturedArgs) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(capturedArgs), capturedArgs)
	}
	if capturedArgs[0] != "cli" {
		t.Errorf("expected first arg %q, got %q", "cli", capturedArgs[0])
	}
	if capturedArgs[1] != "index_repository" {
		t.Errorf("expected second arg %q, got %q", "index_repository", capturedArgs[1])
	}

	expectedJSON := `{"workspace_path": "/tmp/test-workspace"}`
	if capturedArgs[2] != expectedJSON {
		t.Errorf("expected JSON arg %q, got %q", expectedJSON, capturedArgs[2])
	}
}

func TestTriggerCodebaseIndexing_CommandFailure(t *testing.T) {
	origCheck := checkCodebaseMemoryFn
	origExec := execCommandFn
	t.Cleanup(func() {
		checkCodebaseMemoryFn = origCheck
		execCommandFn = origExec
	})

	checkCodebaseMemoryFn = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}
	execCommandFn = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		// Return a command that will fail
		return exec.CommandContext(ctx, "false")
	}

	app := &App{}
	// Should not panic even when the command fails
	app.triggerCodebaseIndexing("/tmp/test-workspace")
}
