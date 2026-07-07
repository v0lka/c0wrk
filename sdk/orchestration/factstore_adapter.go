package orchestration

import "github.com/v0lka/sp4rk/agent"

// blackboardFactStore adapts a Blackboard to the agent.FactStore interface.
type blackboardFactStore struct {
	bb Blackboard
}

// NewFactStore wraps a Blackboard as an agent.FactStore.
func NewFactStore(bb Blackboard) agent.FactStore {
	return &blackboardFactStore{bb: bb}
}

func (s *blackboardFactStore) StoreFact(keywords []string, content, author string) {
	s.bb.StoreFact(Fact{Keywords: keywords, Content: content, Author: author})
}

func (s *blackboardFactStore) SearchFacts(keywords []string) []agent.FactEntry {
	facts := s.bb.SearchFacts(keywords)
	entries := make([]agent.FactEntry, len(facts))
	for i, f := range facts {
		entries[i] = agent.FactEntry{Keywords: f.Keywords, Content: f.Content, Author: f.Author}
	}
	return entries
}
