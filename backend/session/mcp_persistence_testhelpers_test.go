package session

import (
	"context"
	"slices"
	"sync"
)

type mockMCPMentions struct {
	mu    sync.Mutex
	names map[string][]string
}

func (m *mockMCPMentions) SaveMCPMentions(_ context.Context, taskID string, extra []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.names == nil {
		m.names = make(map[string][]string)
	}
	names := append(slices.Clone(m.names[taskID]), extra...)
	slices.Sort(names)
	m.names[taskID] = slices.Compact(names)
	return nil
}

func (m *mockMCPMentions) PersistMCPMentions(ctx context.Context, taskID string, extra []string) error {
	return m.SaveMCPMentions(ctx, taskID, extra)
}

func (m *mockMCPMentions) LoadMCPMentions(_ context.Context, taskID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.names[taskID]), nil
}
