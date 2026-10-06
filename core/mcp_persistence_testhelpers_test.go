package core

import (
	"context"
	"slices"
	"sync"
)

// mockMCPMentions models durable monotonic intent across orchestrator instances.
type mockMCPMentions struct {
	mu    sync.Mutex
	names map[string][]string
}

func (m *mockMCPMentions) PersistMCPMentions(_ context.Context, taskID string, extra []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.names == nil {
		m.names = make(map[string][]string)
	}
	if merged := mergeSortedUniqueNames(m.names[taskID], extra); merged != nil {
		m.names[taskID] = merged
	}
	return nil
}

func (m *mockMCPMentions) LoadMCPMentions(_ context.Context, taskID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.names[taskID]), nil
}
