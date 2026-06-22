package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
)

// WriteFile writes content to a file within the session's workspace.
func (f *FrontendAPI) WriteFile(sessionID, path, content string) error {
	absPath, _, err := f.resolveWorkspacePath(path)
	if err != nil {
		return err
	}

	// Validate containment within the session's workspace directory.
	// For No Project this enforces <sessionID>/workspace/ isolation.
	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if err := config.ValidateWithinSessionWorkspace(f.agentDir, projectID, sessionID, absPath); err != nil {
		return fmt.Errorf("path %q is outside session workspace: %w", path, err)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(absPath, []byte(content), 0o644)
}

// ApprovePlan approves a plan for execution after user review.
func (f *FrontendAPI) ApprovePlan(sessionID, planPath string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().ApprovePlan(f.ctx(), sessionID, planPath)
}

// RejectPlan rejects a plan, optionally with feedback for replanning.
func (f *FrontendAPI) RejectPlan(sessionID, feedback string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().RejectPlan(f.ctx(), sessionID, feedback)
}
