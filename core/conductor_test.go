package core

import "testing"

// TestCompactionStrategyForDomain_CodeNeedsNoProject documents why No Project
// (CHAT) sessions no longer rewrite the routing domain from code to general:
// the code domain maps to sliding_window compaction (the default), which
// assumes nothing about a project workspace or vector index — so code-flavored
// CHAT questions can route honestly. The old rewrite also silently CHANGED
// compaction behavior: general escalates to hierarchical at complexity >= 4.
func TestCompactionStrategyForDomain_CodeNeedsNoProject(t *testing.T) {
	for complexity := 1; complexity <= 5; complexity++ {
		if got := compactionStrategyForDomain("code", complexity); got != "sliding_window" {
			t.Errorf("compactionStrategyForDomain(code, %d) = %q, want sliding_window (no project assumptions)", complexity, got)
		}
	}
	if got := compactionStrategyForDomain("general", 4); got != "hierarchical" {
		t.Errorf("compactionStrategyForDomain(general, 4) = %q, want hierarchical (contrast with code)", got)
	}
	if got := compactionStrategyForDomain("research", 1); got != "summarization" {
		t.Errorf("compactionStrategyForDomain(research, 1) = %q, want summarization", got)
	}
}
