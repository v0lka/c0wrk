package orchestration

import "github.com/v0lka/sp4rk/agent"

// blackboardFinalResultStore adapts a Blackboard to the
// agent.FinalResultStore interface, exposing the prior task's final result
// to the read_final_result tool.
type blackboardFinalResultStore struct {
	bb Blackboard
}

// NewFinalResultStore wraps a Blackboard as an agent.FinalResultStore.
func NewFinalResultStore(bb Blackboard) agent.FinalResultStore {
	return &blackboardFinalResultStore{bb: bb}
}

func (s *blackboardFinalResultStore) GetFinalResult() (string, bool) {
	r := s.bb.GetFinalResult()
	if r == "" {
		return "", false
	}
	return r, true
}
