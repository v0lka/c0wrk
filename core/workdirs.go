package core

import "context"

// WorkDirectory is an auxiliary working directory: an additional containment
// root (Path) plus the Description rendered in the system prompt. The minimal
// context/prompt shape — ID and timestamps live on the backend persistence
// record (WorkDirectoryRecord); see specs/architecture/security-model.md.
type WorkDirectory struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// workDirsKey is the context key for the session's auxiliary work directories.
type workDirsKey struct{}

// WithWorkDirectories attaches the auxiliary work directories for the system
// prompt's "Additional Work Directories" section; the companion containment
// roots ride sdktools.WithAllowedRoots separately — specs/architecture/security-model.md.
func WithWorkDirectories(ctx context.Context, dirs []WorkDirectory) context.Context {
	return context.WithValue(ctx, workDirsKey{}, dirs)
}

// WorkDirectoriesFrom extracts the auxiliary work directories from the context.
// Returns nil when none are set.
func WorkDirectoriesFrom(ctx context.Context) []WorkDirectory {
	if v, ok := ctx.Value(workDirsKey{}).([]WorkDirectory); ok {
		return v
	}
	return nil
}
