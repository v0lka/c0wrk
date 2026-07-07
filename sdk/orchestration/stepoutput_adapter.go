package orchestration

import (
	"sort"

	"github.com/v0lka/sp4rk/agent"
)

// blackboardStepOutputStore adapts a Blackboard to the agent.StepOutputStore interface.
// Only steps that completed without error are exposed, matching the
// previous behaviour where outputs were stored only on success.
type blackboardStepOutputStore struct {
	bb Blackboard
}

// NewStepOutputStore wraps a Blackboard as an agent.StepOutputStore.
func NewStepOutputStore(bb Blackboard) agent.StepOutputStore {
	return &blackboardStepOutputStore{bb: bb}
}

func (s *blackboardStepOutputStore) GetStepOutput(stepID string) (string, bool) {
	sr, ok := s.bb.GetStepResult(stepID)
	if !ok || sr.Error != nil || sr.FullOutput == "" {
		return "", false
	}
	return sr.FullOutput, true
}

func (s *blackboardStepOutputStore) ListStepOutputs() []agent.StepOutputEntry {
	all := s.bb.GetAllStepResults()
	entries := make([]agent.StepOutputEntry, 0, len(all))
	for stepID, sr := range all {
		if sr.Error != nil || sr.FullOutput == "" {
			continue
		}
		entries = append(entries, agent.StepOutputEntry{
			StepID:     stepID,
			FullOutput: sr.FullOutput,
		})
	}
	// Deterministic order sorted by step ID.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StepID < entries[j].StepID
	})
	return entries
}
