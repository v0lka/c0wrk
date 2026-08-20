// Agent quality metrics aggregation.
//
// The executor already reports loop-detector activity through
// ExecutorDiagnostic events (same_tool_repeat_nudge, fruitless_abort, …).
// metricsState taps that stream — plus step and token accounting — and
// aggregates it into the "agent_metrics" payload emitted on task finish or
// abort, so the effect of Small-LLM (and any other) profiles can be measured
// against data instead of impressions.
package session

import (
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
)

// metricsState accumulates per-session agent quality counters. A single
// instance is shared by an EventEmitter and all of its scoped copies
// (WithPlanStepID / WithRetryAttempt), so delegated subagent activity counts
// into the session totals — mirroring how tokenState is shared. Safe for
// concurrent use.
type metricsState struct {
	mu               sync.Mutex
	parseErrors      int
	invalidToolCalls int
	nudges           AgentMetricsCounters
	aborts           AgentMetricsCounters
	steps            int
	smallLLM         SmallLLMMetaInfo
}

// observeDiagnostic maps an executor diagnostic event name onto the counters.
// Only loop-detector nudges, hard aborts and parse-quality events are quality
// signals; informational diagnostics (executor_finish_nudge,
// pre_compaction_nudge, checklist_* and friends) are ignored.
func (m *metricsState) observeDiagnostic(event string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch event {
	// Corrective nudges — soft signals that the agent is drifting.
	case "repeated_tool_call_nudge":
		m.nudges.Repeat++
	case "same_tool_repeat_nudge":
		m.nudges.SameTool++
	case "fruitless_nudge":
		m.nudges.Fruitless++
	case "parse_error_nudge", "tool_call_syntax_nudge":
		m.nudges.Parse++
		m.parseErrors++
	// Hard aborts — the loop breaker stopped the run.
	case "repeated_tool_call_abort":
		m.aborts.Repeat++
	case "same_tool_repeat_abort":
		m.aborts.SameTool++
	case "fruitless_abort":
		m.aborts.Fruitless++
	case "parse_error_abort", "tool_call_syntax_abort":
		m.aborts.Parse++
		m.parseErrors++
	case "truncation_abort":
		m.aborts.Truncation++
	}
}

// observeStep counts one executor step (conductor or subagent).
func (m *metricsState) observeStep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps++
}

// observeToolResult classifies a single tool result and counts "invalid tool
// call" errors — model-produced calls the executor rejected as malformed or
// structurally invalid (parse failures, unknown tools, malformed batches).
// Runtime failures (shell errors, missing files) and security/policy/HITL
// refusals are intentionally excluded: they are not invalid *calls*, they are
// invalid (or disallowed) *operations*.
func (m *metricsState) observeToolResult(isError bool, content string) {
	if !isError || !isInvalidToolCall(content) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidToolCalls++
}

// setSmallLLM snapshots the Small-LLM profile the session runs under.
func (m *metricsState) setSmallLLM(info SmallLLMMetaInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.smallLLM = info
}

// snapshot renders the accumulated counters as the "agent_metrics" payload
// and resets them, so the next task run in the same session starts from zero:
// one agent_metrics event covers exactly one task run.
func (m *metricsState) snapshot(finish string, outputTokens int) AgentMetricsData {
	m.mu.Lock()
	defer m.mu.Unlock()
	data := AgentMetricsData{
		Finish:           finish,
		ParseErrors:      m.parseErrors,
		InvalidToolCalls: m.invalidToolCalls,
		Nudges:           m.nudges,
		Aborts:           m.aborts,
		Steps:            m.steps,
		OutputTokens:     outputTokens,
		SmallLLM:         smallLLMInfoSnapshot(m.smallLLM),
	}
	m.parseErrors = 0
	m.invalidToolCalls = 0
	m.nudges = AgentMetricsCounters{}
	m.aborts = AgentMetricsCounters{}
	m.steps = 0
	return data
}

// smallLLMInfoSnapshot copies the stored profile, normalizing a nil variant
// list to an empty slice so it serializes as [] rather than null.
func smallLLMInfoSnapshot(info SmallLLMMetaInfo) SmallLLMMetaInfo {
	variants := make([]string, len(info.Variants))
	copy(variants, info.Variants)
	return SmallLLMMetaInfo{Enabled: info.Enabled, Variants: variants}
}

// smallLLMProfileFromConfig derives the Small-LLM profile snapshot a session
// runs under. Variants lists every optimization that is actually active —
// the master toggle AND the variant's own sub-toggle must both be on,
// mirroring how the builder applies them. Works regardless of whether the
// profile is enabled: metrics collection is a common layer, the profile only
// annotates the payload.
func smallLLMProfileFromConfig(cfg config.SmallLLMConfig) SmallLLMMetaInfo {
	info := SmallLLMMetaInfo{Enabled: cfg.Enabled, Variants: []string{}}
	if !cfg.Enabled {
		return info
	}
	if cfg.EssentialTools.Enabled {
		info.Variants = append(info.Variants, "essential_tools")
	}
	if cfg.SystemPrompt.Lite {
		info.Variants = append(info.Variants, "system_prompt_lite")
		if cfg.SystemPrompt.FewShot {
			info.Variants = append(info.Variants, "system_prompt_few_shot")
		}
		if cfg.SystemPrompt.ReasoningScaffold {
			info.Variants = append(info.Variants, "system_prompt_reasoning_scaffold")
		}
	}
	if cfg.Sampling.Enabled {
		info.Variants = append(info.Variants, "sampling")
	}
	if cfg.LoopHardening.Enabled {
		info.Variants = append(info.Variants, "loop_hardening")
	}
	if cfg.Context.Enabled {
		info.Variants = append(info.Variants, "context")
	}
	return info
}
