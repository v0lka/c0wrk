// events.go is compiled for BOTH the classic and fluent variants (no build
// tag). The sink embeds orchestration.NoopEvents, which itself embeds
// agent.NoopEvents — so printingEvents satisfies BOTH the agent.Events
// interface (classic fw.Execute) and the orchestration.Events interface
// (fluent TaskBuilder.Events).

package main

import (
	"fmt"

	"github.com/v0lka/sp4rk/orchestration"
)

// printingEvents observes the context-window lifecycle: how full the window is
// after each step (ContextFill) and when compaction reclaims space
// (ContextCompaction). These are the two events the memory subsystem emits.
type printingEvents struct {
	orchestration.NoopEvents
}

func (e *printingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	fmt.Printf("│ 📊 Context: %5.1f%% (%d/%d tokens) — %s\n", fillPercent, usedTokens, maxTokens, status)
}

func (e *printingEvents) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
	fmt.Printf("│ ♻️  Compaction: %.1f%% → %.1f%% (reclaimed)\n", beforePercent, afterPercent)
}
