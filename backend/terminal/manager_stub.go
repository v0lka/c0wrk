//go:build windows

package terminal

import (
	"context"
	"errors"
	"log/slog"
)

// Manager is a no-op stub on Windows.
type Manager struct{}

// NewManager creates a no-op manager on Windows.
func NewManager(_ context.Context, _ *slog.Logger, _ func(sessionID string, data []byte)) *Manager {
	return &Manager{}
}

// Start returns an error on Windows.
func (*Manager) Start(_, _ string) error { return errors.New("terminal not supported on Windows") }

// Write returns an error on Windows.
func (*Manager) Write(_ string, _ []byte) error {
	return errors.New("terminal not supported on Windows")
}

// Resize returns an error on Windows.
func (*Manager) Resize(_ string, _, _ int) error {
	return errors.New("terminal not supported on Windows")
}

// Stop is a no-op on Windows.
func (*Manager) Stop(_ string) error { return nil }

// StopAll is a no-op on Windows.
func (*Manager) StopAll() {}

// IsActive always returns false on Windows.
func (*Manager) IsActive(_ string) bool { return false }
