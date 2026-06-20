package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteFile writes content to a file within the session's workspace.
// For No Project, validates containment within the session-specific workspace
// (~/.c0wrk/projects/__no_project__/<sessionID>/workspace/).
// For concrete projects, validates against the project workspace directory.
func (f *FrontendAPI) WriteFile(sessionID, path, content string) error {
	absPath, _, err := f.resolveWorkspacePath(path)
	if err != nil {
		return err
	}

	// For No Project, tighten containment to the session-specific workspace.
	if f.app != nil && f.app.Manager() != nil {
		sessionWS, ok := f.app.Manager().GetSessionWorkspacePath(sessionID)
		if ok {
			sessionWS, err = filepath.Abs(sessionWS)
			if err != nil {
				return fmt.Errorf("invalid session workspace path: %w", err)
			}
			// Validate that absPath is within the session workspace.
			rel, relErr := filepath.Rel(sessionWS, absPath)
			if relErr != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("path %q is outside session workspace %q", path, sessionWS)
			}
		}
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
