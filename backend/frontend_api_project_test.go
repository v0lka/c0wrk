package backend

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/user/agent/backend/mcp"
)

func TestTriggerCodebaseIndexing_NotInstalled(t *testing.T) {
	// Override checkCodebaseMemoryFunc to simulate not installed
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	var execCalled bool
	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: false}
	}
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "true")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}

	// Verify indexingDone is nil before call
	f.indexingMu.Lock()
	if f.indexingDone != nil {
		t.Error("expected indexingDone to be nil before triggerCodebaseIndexing")
	}
	f.indexingMu.Unlock()

	f.triggerCodebaseIndexing("/tmp/test-workspace")

	// Verify gate channel is closed and cleared even on early return (not installed)
	f.indexingMu.Lock()
	if f.indexingDone != nil {
		t.Error("expected indexingDone to be nil after triggerCodebaseIndexing completes (not installed path)")
	}
	f.indexingMu.Unlock()

	if execCalled {
		t.Error("exec should not have been called when codebase-memory-mcp is not installed")
	}
}

func TestTriggerCodebaseIndexing_Installed(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	var mu sync.Mutex
	var capturedName string
	var capturedArgs []string

	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		// Only capture the first call (index_repository)
		if capturedName == "" {
			capturedName = name
			capturedArgs = arg
		}
		mu.Unlock()
		// Return a no-op command that succeeds
		return exec.CommandContext(ctx, "true")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}

	// Verify indexingDone is nil before call
	f.indexingMu.Lock()
	if f.indexingDone != nil {
		t.Error("expected indexingDone to be nil before triggerCodebaseIndexing")
	}
	f.indexingMu.Unlock()

	f.triggerCodebaseIndexing("/tmp/test-workspace")

	// Verify indexingDone is nil after completion (channel closed and cleared)
	f.indexingMu.Lock()
	if f.indexingDone != nil {
		t.Error("expected indexingDone to be nil after triggerCodebaseIndexing completes")
	}
	f.indexingMu.Unlock()

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

	expectedJSON := `{"workspace_path":"/tmp/test-workspace"}`
	if capturedArgs[2] != expectedJSON {
		t.Errorf("expected JSON arg %q, got %q", expectedJSON, capturedArgs[2])
	}
}

func TestTriggerCodebaseIndexing_SkipsWhenAlreadyRunning(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	var execCalled bool
	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "true")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}

	// Simulate an already-running indexing operation.
	existingCh := make(chan struct{})
	f.indexingMu.Lock()
	f.indexingDone = existingCh
	f.indexingMu.Unlock()

	f.triggerCodebaseIndexing("/tmp/test-workspace")

	if execCalled {
		t.Error("exec should not have been called when indexing is already in progress")
	}

	// Verify the original channel is still set (not closed or replaced).
	f.indexingMu.Lock()
	if f.indexingDone != existingCh {
		t.Error("expected indexingDone to still reference the original channel")
	}
	f.indexingMu.Unlock()

	// Verify the original channel is still open (not closed).
	select {
	case <-existingCh:
		t.Error("expected the original channel to still be open")
	default:
		// OK — channel is still open.
	}
}

func TestTriggerCodebaseIndexing_CommandFailure(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		// Return a command that will fail
		return exec.CommandContext(ctx, "false")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}
	// Should not panic even when the command fails
	f.triggerCodebaseIndexing("/tmp/test-workspace")
}

func TestResolveCodebaseProjectName_MatchFound(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}

	mcpJSON := `{"content":[{"type":"text","text":"{\"projects\":[{\"name\":\"Test-Project\",\"root_path\":\"/tmp/test-workspace\"},{\"name\":\"Other-Project\",\"root_path\":\"/other/path\"}]}"}]}`

	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", mcpJSON)
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.resolveCodebaseProjectName("/tmp/test-workspace")

	f.activeProjectMu.RLock()
	got := f.codebaseProjectName
	f.activeProjectMu.RUnlock()

	if got != "Test-Project" {
		t.Errorf("expected codebaseProjectName %q, got %q", "Test-Project", got)
	}
}

func TestResolveCodebaseProjectName_NoMatch(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}

	mcpJSON := `{"content":[{"type":"text","text":"{\"projects\":[{\"name\":\"Test-Project\",\"root_path\":\"/tmp/test-workspace\"}]}"}]}`

	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", mcpJSON)
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.resolveCodebaseProjectName("/no/match/path")

	f.activeProjectMu.RLock()
	got := f.codebaseProjectName
	f.activeProjectMu.RUnlock()

	if got != "" {
		t.Errorf("expected empty codebaseProjectName, got %q", got)
	}
}

func TestResolveCodebaseProjectName_NotInstalled(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	var execCalled bool
	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: false}
	}
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		execCalled = true
		return exec.CommandContext(ctx, "true")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.resolveCodebaseProjectName("/tmp/test-workspace")

	f.activeProjectMu.RLock()
	got := f.codebaseProjectName
	f.activeProjectMu.RUnlock()

	if got != "" {
		t.Errorf("expected empty codebaseProjectName, got %q", got)
	}
	if execCalled {
		t.Error("exec should not have been called when codebase-memory-mcp is not installed")
	}
}

func TestTriggerCodebaseIndexing_ResolvesProjectName(t *testing.T) {
	origCheck := checkCodebaseMemoryFunc
	origExec := execCommandFunc
	t.Cleanup(func() {
		checkCodebaseMemoryFunc = origCheck
		execCommandFunc = origExec
	})

	checkCodebaseMemoryFunc = func() mcp.CodeMemoryStatus {
		return mcp.CodeMemoryStatus{Installed: true, Path: "/usr/local/bin/codebase-memory-mcp"}
	}

	mcpJSON := `{"content":[{"type":"text","text":"{\"projects\":[{\"name\":\"Indexed-Project\",\"root_path\":\"/tmp/test-workspace\"}]}"}]}`

	var callCount int
	var mu sync.Mutex
	execCommandFunc = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if len(arg) >= 2 && arg[1] == "list_projects" {
			// list_projects call — return MCP JSON
			return exec.CommandContext(ctx, "echo", mcpJSON)
		}
		// index_repository call — succeed silently
		return exec.CommandContext(ctx, "true")
	}

	f := &FrontendAPI{
		appCtx: context.Background,
	}
	f.triggerCodebaseIndexing("/tmp/test-workspace")

	f.activeProjectMu.RLock()
	got := f.codebaseProjectName
	f.activeProjectMu.RUnlock()

	if got != "Indexed-Project" {
		t.Errorf("expected codebaseProjectName %q, got %q", "Indexed-Project", got)
	}

	mu.Lock()
	defer mu.Unlock()
	// Should have been called at least twice: once for index_repository, once for list_projects
	// checkCodebaseMemoryFunc is called twice too (once per method), but execCommandFunc is what we count
	if callCount < 2 {
		t.Errorf("expected at least 2 exec calls (index + list_projects), got %d", callCount)
	}
}
