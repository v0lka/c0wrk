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

	// MkdirAllRealWithin (bounded by the agent dir): a link planted at
	// ~/.c0wrk/logs is refused and startup logging fails closed instead of
	// redirecting session logs outside ~/.c0wrk (review finding #77); a
	// symlinked agent directory itself remains operator intent.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
	if err := safeio.MkdirAllRealWithin(filepath.Dir(logDir), logDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	timestamp := time.Now().Format("2006-01-02T15-04-05")
	sessionFile := filepath.Join(logDir, fmt.Sprintf("session-%s.log", timestamp))

	// O_NOFOLLOW: a symlink planted at the predictable session-<ts>.log name
	// must not redirect the write (plain safeio.OpenFile's post-open fstat
	// would see the regular link target). On Windows a final symlink is
	// still resolved (safeio parity note), tempered by symlink creation
	// requiring elevated/dev-mode rights there.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
	file, err := safeio.OpenFileNoFollow(sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	opts := &slog.HandlerOptions{
		Level: parsedLevel,
	}
	handler := slog.NewJSONHandler(file, opts)
	logger := slog.New(handler)

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

// Close flushes and closes the log file.
func (s *SessionLogger) Close() error {
	if s.file != nil {
		f := s.file
		s.file = nil
		return f.Close()
	}
	return nil
}

// parseLevel converts a level name ("DEBUG"…"ERROR", case-insensitive, or
// numeric) to slog.Level via slog.Level.UnmarshalText.
func parseLevel(level string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return 0, fmt.Errorf("unrecognized log level: %s", level)
	}
	return l, nil
}
