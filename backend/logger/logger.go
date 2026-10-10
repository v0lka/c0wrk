// Package logger provides a thin wrapper around Go's standard log/slog
// for writing structured logs to a session file.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/v0lka/sp4rk/safeio"
)

// SessionLogger wraps a slog.Logger with a session file.
type SessionLogger struct {
	logger *slog.Logger
	file   *os.File
}

// Init creates a new SessionLogger with the specified log level and log directory.
// It creates the log directory if it doesn't exist and opens a new session file.
func Init(level, logDir string) (*SessionLogger, error) {
	parsedLevel, levelErr := parseLevel(level)

	// Create the log directory as a chain of REAL directories under the
	// agent directory (the log dir's parent) as the containment boundary: a
	// link planted at ~/.c0wrk/logs → <outside> is REFUSED and startup
	// logging fails closed instead of redirecting every session log outside
	// ~/.c0wrk (review finding #77). A symlinked agent directory itself —
	// and any link resolving inside it — remains operator intent; a
	// dangling or swapped-in link still fails as before.
	if err := safeio.MkdirAllRealWithin(filepath.Dir(logDir), logDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Generate session file path with timestamp
	timestamp := time.Now().Format("2006-01-02T15-04-05")
	sessionFile := filepath.Join(logDir, fmt.Sprintf("session-%s.log", timestamp))

	// Open log file with O_NOFOLLOW (unix): a symlink planted at the
	// predictable session-<ts>.log name must not redirect the write (the
	// plain safeio.OpenFile fstat guard runs after the open and would see
	// the regular link target). On Windows the safeio parity note applies —
	// the final symlink is still resolved there — tempered by Windows
	// requiring elevated/dev-mode rights to create symlinks.
	file, err := safeio.OpenFileNoFollow(sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Create JSON handler with the parsed level
	opts := &slog.HandlerOptions{
		Level: parsedLevel,
	}
	handler := slog.NewJSONHandler(file, opts)
	logger := slog.New(handler)

	// Log warning if level was invalid
	if levelErr != nil {
		logger.Warn("invalid log level specified, defaulting to INFO", "level", level)
	}

	return &SessionLogger{
		logger: logger,
		file:   file,
	}, nil
}

// Logger returns the underlying slog.Logger.
func (s *SessionLogger) Logger() *slog.Logger {
	return s.logger
}

// Close flushes and closes the log file. Safe to call multiple times.
func (s *SessionLogger) Close() error {
	if s.file != nil {
		f := s.file
		s.file = nil
		return f.Close()
	}
	return nil
}

// parseLevel converts a string level to slog.Level.
// Delegates to slog.Level.UnmarshalText which accepts "DEBUG", "INFO",
// "WARN", "ERROR" (case-insensitive) and numeric levels.
// Returns an error if the level is unrecognized.
func parseLevel(level string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return 0, fmt.Errorf("unrecognized log level: %s", level)
	}
	return l, nil
}
