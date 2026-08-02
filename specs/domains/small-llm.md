# Small-LLM Profile

## Purpose

The Small-LLM profile is a set of optimizations applied when running the Conductor against a "small" (low-capacity / cheaper) LLM. Small models are disproportionately penalized by large tool schemas, verbose system prompts, and loose sampling, so the profile narrows the visible tool set, simplifies the system prompt, tightens the circuit breakers, and overrides sampling parameters — each as an independently gated variant under a single master toggle. The master toggle is manual only: there is no auto-detection; the operator decides when to enable the profile.

## Key Files

- `backend/config/config.go` — `SmallLLMConfig` and its sub-configs (`EssentialToolsConfig`, `SystemPromptConfig`, `SmallLLMSamplingConfig`, `LoopHardeningConfig`)
- `backend/config/defaults.go` — `defaultSmallLLMAlwaysPresent` and the zero-value defaults for every threshold/value
- `backend/configadapter.go` — `ToBuilderConfig` copies `SmallLLMConfig` into `core.BuilderSmallLLMConfig` (core never imports `backend/config`)
- `core/builderconfig.go` — `BuilderSmallLLMConfig` + sub-structs (the core-layer mirror)
- `core/builder.go` — `applySmallLLMPresets` (seeds builder-level reasoning effort), `applyLoopHardening` (overrides circuit-breaker thresholds), `resolveSamplingFunc` (overrides router sampling temperature)
- `core/smallllm/tools_filter.go` — pure, deterministic `SelectTools` + `ProtectedToolNames` (tool-set assembly + budget enforcement)
- `core/orchestrator.go` — `SmallLLMSettings` mirror on `OrchestratorConfig`; `applySmallLLMToolFilter` (the single call site in `HandleMessage`)
- `core/orchestrator_handle.go` — `prepareRequestContext` carries the SystemPrompt sub-toggle flags into context
- `core/orchestrator_goal.go` — the essential-tools filter is intentionally NOT applied in goal mode
- `core/systemprompt.go` — `buildSystemPromptWith` swaps the OrchestratorSystem directive for OrchestratorSystemLite and appends the scaffold / few-shot blocks
- `core/prompts/orchestrator_lite.md` — compact core directive (the Lite swap)
- `core/prompts/orchestrator_lite_scaffold.md` — three-step reasoning scaffold
- `core/prompts/orchestrator_lite_fewshot.md` — curated worked-example ReAct cycles
- `core/router_adapter.go` / `core/builder.go` — `coreRouter.SetToolMatching(...)` enables semantic tool selection in the router
- `backend/frontend_api_config.go` — `GetSmallLLMConfig` / `UpdateSmallLLMConfig` (RPC surface) + `validateSmallLLMConfig`
- `backend/session/events.go` — `ToolsAssignedData`; `backend/session/emitter.go` — `ToolsAssigned`; `backend/session/event_persister.go` — `tools_assigned` → role `status`
- `frontend/src/components/settings/SmallLLMSettings.tsx` / `SmallLLMControls.tsx` / `SmallLLMSections.tsx` — settings UI

## Core Types

```go
// Master profile, config layer (backend/config/config.go). The master
// Enabled toggle gates every variant: when false, no variant activates
// regardless of its sub-toggle.
type SmallLLMConfig struct {
    Enabled        bool                   `yaml:"enabled"`
    EssentialTools EssentialToolsConfig   `yaml:"essential_tools"`
    SystemPrompt   SystemPromptConfig      `yaml:"system_prompt"`
    Sampling       SmallLLMSamplingConfig  `yaml:"sampling"`
    LoopHardening  LoopHardeningConfig     `yaml:"loop_hardening"`
}

// Runtime mirror carried to the orchestrator via OrchestratorConfig.
type SmallLLMSettings struct {
    Enabled        bool
    EssentialTools SmallLLMEssentialSettings
    SystemPrompt   SmallLLMSystemPromptSettings
}
```

## Variants

Every variant is gated by BOTH the master `SmallLLM.Enabled` toggle AND its own sub-toggle (defense-in-depth). When the master toggle is off, the whole profile is inert — behavior is identical to the un-profiled baseline.

### Essential Tools (narrowing)

Narrows the Conductor's advertised tool set once per task (before the ReAct loop, in `HandleMessage`) to reduce per-prompt JSON-schema/token overhead. Selection is purely semantic — there is no domain-specific allow-list. `smallllm.SelectTools` unions four sources:

1. **router-matched tools** — `routing.MatchedTools` (the router classifies which tools are relevant to the task; semantic tool selection must be enabled in the router via `coreRouter.SetToolMatching`, itself gated on the master toggle + the EssentialTools variant).
2. **always-present tools** — the operator's pinned list (`essential_tools.always_present`).
3. **protected orchestration tools** — `finish`, `store_fact`, `search_facts`, `ask_user`, `update_checklist` (never dropped).
4. **every MCP-sourced tool** — user-installed, outside the orchestration-noise problem.

When `max_tools > 0` and the union exceeds the budget, `trimToBudget` trims non-protected tools (in input order) while guaranteeing protected and MCP tools always survive. The curated set is surfaced as a `tools_assigned` event (a `status` card mirroring `skills_activated`).

**Goal mode is never narrowed.** The only filter call site is `HandleMessage`'s non-goal path, which runs AFTER the goal-mode early return. Goal mode deliberately keeps the full tool set (the goal-loop tools, including the verifier-required `declare_verification`, would otherwise be dropped by `SelectTools`).

### System Prompt Simplification

Shrinks the system prompt injected for a small model. Applied in `buildSystemPromptWith` (gated on the context-carried profile from `prepareRequestContext`):

- **Lite** — swaps the verbose `OrchestratorSystem` core directive for the compact `OrchestratorSystemLite` directive. The lite directive drops verbose operational docs (truncation internals, fact-memory mechanics, checklist/table mechanics, progress-tracking internals) that an SLM cannot hold. It carries NO injection-defense content — that section is injected separately and unchanged (strict constraint).
- **ReasoningScaffold** — appends a three-step thought template (goal → tool choice + rationale → exact args). Only honored when Lite is on.
- **FewShot** — appends curated worked-example ReAct cycles (correct tool-call format, tool choice, error recovery, finish). Only honored when Lite is on.

Specialized runs (e.g. goal derivation) carry their own core directive and are never swapped to the lite orchestrator directive. The shared sections (family overlay, verification mandate, injection defense, workspace, env, AGENTS.md, skills) are appended UNCHANGED in both modes.

### Sampling Overrides

Overrides LLM sampling parameters for more deterministic, lower-effort generation. Applied in `resolveSamplingFunc` at router construction:

- **Temperature** — replaces the per-family `DefaultSampling` default with a constant SmallLLM temperature. This is the active lever.
- **ReasoningEffort** — when non-empty (`off`/`low`/`medium`), seeds the builder-level default via `applySmallLLMPresets`. Per-request overrides (`HandleOptions.ReasoningEffort`) still take precedence.
- **TopP** — carried for forward compatibility but NOT applied: the sp4rk `llm.ChatRequest` exposes no `TopP` field and the router consumes only temperature from `SamplingFunc`, so TopP cannot reach the API without a sp4rk change. The value is carried in the profile for when sp4rk adds support.

### Loop Hardening

Tightens the executor circuit-breaker thresholds so a small model that repeats itself or makes no progress is nudged/aborted sooner, conserving the token budget. Applied in `applyLoopHardening` at builder construction. Only the thresholds present in the profile are overridden; all others (RepeatAbortThreshold, TruncationAbortThreshold, etc.) keep their baseline:

- `repeat_nudge_threshold`
- `parse_error_abort_threshold`
- `fruitless_nudge_threshold`
- `fruitless_abort_threshold`
- `same_tool_repeat_nudge_threshold`

These are tighter than the baseline `executor.circuitBreaker` values.

## Flow

```
config.yaml small_llm:
       │
       ▼
backend/configadapter.go: ToBuilderConfig
  → core.BuilderSmallLLMConfig
       │
       ▼
core/builder.go: NewOrchestratorBuilder
  ├─ applySmallLLMPresets  → builder reasoning-effort default
  ├─ buildRouter           → resolveSamplingFunc (temperature override)
  ├─ buildCoreAgents       → coreRouter.SetToolMatching (router tool selection)
  └─ Build                 → applyLoopHardening (circuit-breaker thresholds)
                              + OrchestratorConfig.SmallLLMSettings
       │
       ▼
per-session Orchestrator.HandleMessage (non-goal path):
  ├─ applySmallLLMToolFilter (ONCE) → smallllm.SelectTools
  │     → emit tools_assigned event
  └─ prepareRequestContext → withSmallLLMPromptProfile (ctx flags)
       → buildSystemPromptWith → Lite swap + scaffold + few-shot
```

## Invariants

- The master `SmallLLM.Enabled` toggle gates every variant; when it is off, behavior is identical to the un-profiled baseline (zero behavior change at every variant's call site).
- Each variant is independently gated by BOTH the master toggle and its own sub-toggle (defense-in-depth).
- The essential-tools filter runs exactly once per task, before the non-goal ReAct loop starts; it is never applied in goal mode.
- `finish` and the fact-memory / human-interaction tools are always preserved regardless of the matched set, always-present list, or `max_tools` budget; every MCP-sourced tool is always kept.
- The injection-defense section is never removed or altered by the Lite swap (strict constraint); the lite directive carries no injection-defense content because it is injected separately and unchanged.
- FewShot and ReasoningScaffold are only honored when Lite is active (both are tailored to the lite directive's style).
- Specialized runs (goal derivation) are never swapped to the lite orchestrator directive.
- TopP is carried in the profile but never applied (no sp4rk plumbing); temperature is the active sampling lever.
- `validateSmallLLMConfig` runs before any mutation, so an invalid payload produces no partial write to config or config.yaml.
- `UpdateSmallLLMConfig` rebuilds the LLM router on success so the new profile takes effect for new sessions without an app restart.

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig). The authoritative reference for every tunable is `config.example.yaml`.

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `small_llm.enabled` | false | Master toggle. Manual only — no auto-detection. |
| `small_llm.essential_tools.enabled` | false | Gates the essential-tools variant. |
| `small_llm.essential_tools.always_present` | `defaultSmallLLMAlwaysPresent` (read_file, write_file, edit_file, list_directory, glob, ripgrep, bash_exec, semantic_search, store_fact, search_facts, ask_user, finish) | Tools always kept regardless of router selection. May be empty (protected + MCP tools are always kept implicitly). |
| `small_llm.essential_tools.max_tools` | 12 | Cap on total tools exposed after selection. 0 = unlimited. Protected and MCP tools always survive. |
| `small_llm.system_prompt.lite` | false | Swap the verbose core directive for the compact lite directive. |
| `small_llm.system_prompt.few_shot` | false | Append worked-example ReAct cycles (requires Lite). |
| `small_llm.system_prompt.reasoning_scaffold` | false | Append three-step thought template (requires Lite). |
| `small_llm.sampling.enabled` | false | Gates the sampling variant. |
| `small_llm.sampling.temperature` | 0.1 | Generation temperature (must be > 0 when sampling is on). |
| `small_llm.sampling.top_p` | 0.9 | Nucleus-sampling mass (carried, not yet applied; range (0, 1]). |
| `small_llm.sampling.reasoning_effort` | "" (inherit) | `off` \| `low` \| `medium` (seeds builder default; per-request overrides win). |
| `small_llm.loop_hardening.enabled` | false | Gates the loop-hardening variant. |
| `small_llm.loop_hardening.repeat_nudge_threshold` | 2 | Consecutive identical tool calls before a nudge. |
| `small_llm.loop_hardening.parse_error_abort_threshold` | 3 | Consecutive parse errors before abort. |
| `small_llm.loop_hardening.fruitless_nudge_threshold` | 3 | Consecutive minimal-result calls before a nudge. |
| `small_llm.loop_hardening.fruitless_abort_threshold` | 5 | Consecutive minimal-result calls before abort. |
| `small_llm.loop_hardening.same_tool_repeat_nudge_threshold` | 4 | Same-tool (varied args) calls before a nudge. |

## RPC Surface

The small-LLM profile is editable at runtime via the settings UI. See [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) (`GetSmallLLMConfig` / `UpdateSmallLLMConfig`).

## Related Specs

- [orchestration/README.md](orchestration/README.md) — HandleMessage flow where the essential-tools filter applies
- [orchestration/conductor.md](orchestration/conductor.md) — Conductor system prompt (the Lite swap target)
- [orchestration/router.md](orchestration/router.md) — semantic tool matching gated on this profile
- [orchestration/executor.md](orchestration/executor.md) — circuit breakers (the loop-hardening target)
- [llm-providers.md](llm-providers.md) — LLM router / sampling (the sampling override target)
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `GetSmallLLMConfig` / `UpdateSmallLLMConfig` RPC
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `tools_assigned` event
- [../decisions/022-small-llm-profile.md](../decisions/022-small-llm-profile.md) — rationale for the variant-and-master-toggle design
