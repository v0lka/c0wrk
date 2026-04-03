package logger

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantLevel slog.Level
		wantErr   bool
	}{
		{
			name:      "DEBUG uppercase",
			level:     "DEBUG",
			wantLevel: slog.LevelDebug,
			wantErr:   false,
		},
		{
			name:      "INFO uppercase",
			level:     "INFO",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "WARN uppercase",
			level:     "WARN",
			wantLevel: slog.LevelWarn,
			wantErr:   false,
		},
		{
			name:      "ERROR uppercase",
			level:     "ERROR",
			wantLevel: slog.LevelError,
			wantErr:   false,
		},
		{
			name:      "debug lowercase",
			level:     "debug",
			wantLevel: slog.LevelDebug,
			wantErr:   false,
		},
		{
			name:      "info mixed case",
			level:     "Info",
			wantLevel: slog.LevelInfo,
			wantErr:   false,
		},
		{
			name:      "invalid level defaults to INFO with error",
			level:     "INVALID",
			wantLevel: slog.LevelInfo,
			wantErr:   true,
		},
		{
			name:      "empty level defaults to INFO with error",
			level:     "",
			wantLevel: slog.LevelInfo,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLevel, err := parseLevel(tt.level)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLevel(%q) error = %v, wantErr %v", tt.level, err, tt.wantErr)
				return
			}
			if gotLevel != tt.wantLevel {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.level, gotLevel, tt.wantLevel)
			}
		})
	}
}

func TestInit_CreatesDirectoryAndFile(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	logger, err := Init("INFO")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Check that the log directory was created
	if _, err := os.Stat(testDir); os.IsNotExist(err) {
		t.Errorf("log directory was not created")
	}

	// Check that a session file was created
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	foundSessionFile := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			foundSessionFile = true
			break
		}
	}

	if !foundSessionFile {
		t.Errorf("session log file was not created")
	}
}

func TestInit_InvalidLevelDefaultsToInfo(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	logger, err := Init("INVALID_LEVEL")
	if err != nil {
		t.Fatalf("Init() with invalid level should not return error, got: %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Logger should be created successfully with INFO level
	if logger.Logger() == nil {
		t.Errorf("Logger() returned nil")
	}
}

func TestSessionLogger_Logger(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	logger, err := Init("DEBUG")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Logger() should return non-nil *slog.Logger
	if logger.Logger() == nil {
		t.Errorf("Logger() returned nil")
	}

	// Verify it's a proper slog.Logger by calling a method
	logger.Logger().Info("test message", "key", "value")
}

func TestSessionLogger_Close(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	logger, err := Init("INFO")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// Close should succeed
	if err := logger.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Double close - the file handle is already closed, which may return an error
	// This is acceptable behavior; we just verify it doesn't panic
	_ = logger.Close()
}

func TestSessionLogger_LevelFiltering(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	// Create logger with WARN level
	logger, err := Init("WARN")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Log messages at different levels
	logger.Logger().Debug("debug message")
	logger.Logger().Info("info message")
	logger.Logger().Warn("warn message")
	logger.Logger().Error("error message")

	// Find and read the log file
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	var logFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			logFile = filepath.Join(testDir, entry.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatalf("log file not found")
	}

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	contentStr := string(content)

	// Should contain WARN and ERROR
	if !strings.Contains(contentStr, "warn message") {
		t.Errorf("log should contain warn message")
	}
	if !strings.Contains(contentStr, "error message") {
		t.Errorf("log should contain error message")
	}
}

func TestSessionLogger_JSONFormat(t *testing.T) {
	// Override baseLogDir for testing
	testDir := t.TempDir()
	oldBaseLogDir := baseLogDir
	baseLogDir = testDir
	defer func() { baseLogDir = oldBaseLogDir }()

	logger, err := Init("INFO")
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer func() { _ = logger.Close() }()

	// Log a structured message
	logger.Logger().Info("test message", "key1", "value1", "key2", 42)

	// Find and read the log file
	entries, err := os.ReadDir(testDir)
	if err != nil {
		t.Fatalf("failed to read test directory: %v", err)
	}

	var logFile string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "session-") && strings.HasSuffix(entry.Name(), ".log") {
			logFile = filepath.Join(testDir, entry.Name())
			break
		}
	}

	if logFile == "" {
		t.Fatalf("log file not found")
	}

	content, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	contentStr := string(content)

	// Should be valid JSON format
	if !strings.Contains(contentStr, `"msg":"test message"`) {
		t.Errorf("log should contain JSON-formatted message")
	}
	if !strings.Contains(contentStr, `"key1":"value1"`) {
		t.Errorf("log should contain key1 field")
	}
	if !strings.Contains(contentStr, `"key2":42`) {
		t.Errorf("log should contain key2 field")
	}
}
