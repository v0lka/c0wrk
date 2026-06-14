// Package logger provides a thin wrapper around Go's standard log/slog
// for writing structured logs to a session file.
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// baseLogDir is the base directory for log files.
// It can be overridden for testing.
var baseLogDir = filepath.Join(os.Getenv("HOME"), ".c0wrk", "logs")

// SessionLogger wraps a slog.Logger with a session file.
type SessionLogger struct {
	logger *slog.Logger
	file   *os.File
}

// Init creates a new SessionLogger with the specified log level.
// It creates the log directory if it doesn't exist and opens a new session file.
func Init(level string) (*SessionLogger, error) {
	parsedLevel, err := parseLevel(level)
	if err != nil {
		// Log warning about invalid level and use INFO as default
		parsedLevel = slog.LevelInfo
	}

	// Store parse error for later logging (err will be shadowed by file operations)
	parseErr := err

	// Create log directory
	if err := os.MkdirAll(baseLogDir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Generate session file path with timestamp
	timestamp := time.Now().Format("2006-01-02T15-04-05")
	sessionFile := filepath.Join(baseLogDir, fmt.Sprintf("session-%s.log", timestamp))

	// Open log file
	file, err := os.OpenFile(sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
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
	if parseErr != nil {
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
		return slog.LevelInfo, fmt.Errorf("unrecognized log level: %s", level)
	}
	return l, nil
}
