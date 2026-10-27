// Agent quality metrics aggregation: metricsState taps the executor's
// ExecutorDiagnostic stream plus step/token accounting into the "agent_metrics"
// payload (specs/contracts/event-catalog.md, agent_metrics).
package session

import (
	"sync"

	"github.com/v0lka/c0wrk/backend/config"
)

// metricsState accumulates per-session agent quality counters, shared by an
// EventEmitter and all its scoped copies (WithPlanStepID / WithRetryAttempt)
// so delegated subagent activity counts into the session totals.
type metricsState struct {
	mu               sync.Mutex
	parseErrors      int
	invalidToolCalls int
	nudges           AgentMetricsCounters
	aborts           AgentMetricsCounters
	steps            int
	modelProfiles    ModelProfilesMetaInfo
}

// observeDiagnostic maps an executor diagnostic event name onto the counters;
// informational diagnostics (executor_finish_nudge, pre_compaction_nudge,
// checklist_* and friends) are not quality signals and are ignored.
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

// observeToolResult counts "invalid tool call" errors — model-produced calls
// the executor rejected as malformed or structurally invalid, distinct from
// runtime failures and policy refusals
// (specs/contracts/event-catalog.md, agent_metrics).
func (m *metricsState) observeToolResult(isError bool, content string) {
	if !isError || !isInvalidToolCall(content) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidToolCalls++
}

// setModelProfiles snapshots the Model Profiles profile the session runs under.
func (m *metricsState) setModelProfiles(info ModelProfilesMetaInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelProfiles = info
}

// snapshot renders the accumulated counters as the "agent_metrics" payload
// and resets them: one event covers exactly one task run.
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
		ModelProfiles:    modelProfilesInfoSnapshot(m.modelProfiles),
	}
	m.parseErrors = 0
	m.invalidToolCalls = 0
	m.nudges = AgentMetricsCounters{}
	m.aborts = AgentMetricsCounters{}
	m.steps = 0
	return data
}

// modelProfilesInfoSnapshot copies the stored profile, normalizing a nil variant
// list to an empty slice so it serializes as [] rather than null.
func modelProfilesInfoSnapshot(info ModelProfilesMetaInfo) ModelProfilesMetaInfo {
	variants := make([]string, len(info.Variants))
	copy(variants, info.Variants)
	return ModelProfilesMetaInfo{
		Enabled:     info.Enabled,
		Profile:     info.Profile,
		ProfileKind: info.ProfileKind,
		Variants:    variants,
	}
}

// modelProfileFromConfig derives the Model Profiles snapshot a session runs
// under: profile identity is reported regardless of the master toggle, while
// Variants lists only optimizations active under BOTH the toggle and the
// variant's own sub-toggle (specs/domains/model-profiles.md).
func modelProfileFromConfig(cfg config.ModelProfilesConfig, profile config.ModelProfile) ModelProfilesMetaInfo {
	info := ModelProfilesMetaInfo{
		Enabled:     cfg.Enabled,
		Profile:     profile.ID,
		ProfileKind: string(profile.Kind),
		Variants:    []string{},
	}
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
