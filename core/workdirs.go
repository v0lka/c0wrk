package core

import "context"

// WorkDirectory is an auxiliary working directory exposed to the agent as an
// additional containment root (Path) with a human-readable Description for the
// system prompt.
//
// This minimal shape is what flows into the execution context + system prompt
// (Phase D consumes it). It carries no ID or timestamps — those live on the
// persistence record in the backend layer (see WorkDirectoryRecord).
type WorkDirectory struct {
	Path        string `json:"path"`
	Description string `json:"description"`
}

// workDirsKey is the context key for the session's auxiliary work directories.
type workDirsKey struct{}

// WithWorkDirectories attaches the session's auxiliary work directories to the
// context. They flow into the system prompt's "Additional Work Directories"
// section. The companion security roots are set separately via
// sdktools.WithAllowedRoots (Phase D injects both at the task-context
// construction points).
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
