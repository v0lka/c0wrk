# Small-LLM Profile

## Purpose

The Small-LLM profile is a set of optimizations applied when running the Conductor against a "small" (low-capacity / cheaper) LLM. Small models are disproportionately penalized by large tool schemas, verbose system prompts, loose sampling, runaway loops, and overflowing context windows, so the profile narrows the visible tool set, simplifies the system prompt, tightens the circuit breakers, overrides sampling parameters, and aggressively manages the context — each as an independently gated variant under a single master toggle. The master toggle is manual only: there is no auto-detection; the operator decides when to enable the profile.

## Key Files

- `backend/config/config.go` — `SmallLLMConfig` and its sub-configs (`EssentialToolsConfig`, `SystemPromptConfig`, `SmallLLMSamplingConfig`, `LoopHardeningConfig`, `SmallLLMContextConfig`)
- `backend/config/defaults.go` — `defaultSmallLLMAlwaysPresent` and the zero-value defaults for every threshold/value
- `backend/configadapter.go` — `ToBuilderConfig` copies `SmallLLMConfig` into `core.BuilderSmallLLMConfig` (core never imports `backend/config`)
- `core/builderconfig.go` — `BuilderSmallLLMConfig` + sub-structs (the core-layer mirror)
- `core/builder.go` — `applySmallLLMPresets` (seeds builder-level reasoning effort), `applyLoopHardening` (overrides circuit-breaker thresholds), `applyContextManagement` (overrides compaction/pruning/reserve on the executor config), `resolveSamplingFunc` (overrides router sampling temperature)
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
    Context        SmallLLMContextConfig   `yaml:"context"`
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

The tool population is split into two classes with different budget semantics:

- **guaranteed** — always-present ∪ protected ∪ MCP-sourced. This set is NEVER trimmed: it reflects explicit user/operator choices (pins, user-installed integrations) and the completion channel, so dropping any of it would silently break a pinned workflow.
- **router-matched** — fills the free slots left after the guaranteed set: at most `max_tools − len(guaranteed)` of them are kept, in registry order. When `max_tools = 0` the cap is unlimited.

Because guaranteed tools are never trimmed, the result can legitimately exceed `max_tools` when the guaranteed set alone is larger than the budget. `validateSmallLLMConfig` rejects such profiles up front with an actionable message (`max_tools` must be ≥ the guaranteed count, which is `always_present ∪ protected` — MCP tools join at runtime and cannot be validated ahead of time); `SelectTools` itself is the runtime defense in depth and never trims a guaranteed tool. The curated set is surfaced as a `tools_assigned` event (a `status` card mirroring `skills_activated`).

**Compact descriptions.** With `compact_descriptions` on, every known builtin's full rubric description (purpose/when-to-use/inputs/outputs/example/anti-example, 480-1100 chars) is replaced by a one-line compact variant; unknown tools (e.g. MCP) keep their original descriptions. Compaction is applied to the curated set AND to the selection-fallback set — the degradation guard that keeps the full, unfiltered tool list when semantic selection fails — because the fallback ships the largest descriptor payload and benefits most from one-liners.

**Goal mode is never narrowed.** The only filter call site is `HandleMessage`'s non-goal path, which runs AFTER the goal-mode early return. Goal mode deliberately keeps the full tool set (the goal-loop tools, including the verifier-required `declare_verification`, would otherwise be dropped by `SelectTools`).

### System Prompt Simplification

Shrinks the system prompt injected for a small model. Applied in `buildSystemPromptWith` (gated on the context-carried profile from `prepareRequestContext`):

- **Lite** — swaps the verbose `OrchestratorSystem` core directive for the compact `OrchestratorSystemLite` directive. The lite directive drops verbose operational docs (truncation internals, fact-memory mechanics, checklist/table mechanics, progress-tracking internals) that an SLM cannot hold. It carries NO injection-defense content — that section is injected separately and unchanged (strict constraint).
- **ReasoningScaffold** — appends a three-step thought template (goal → tool choice + rationale → exact args). Only honored when Lite is on.
- **FewShot** — appends curated worked-example ReAct cycles (correct tool-call format, tool choice, error recovery, finish). Only honored when Lite is on.

Specialized runs (e.g. goal derivation) carry their own core directive and are never swapped to the lite orchestrator directive. The shared sections (family overlay, verification mandate, injection defense, workspace, env, AGENTS.md, skills) are appended UNCHANGED in both modes.

### Sampling Overrides

Overrides LLM sampling parameters for more deterministic, lower-effort generation. Applied in `resolveSamplingFunc` at router construction.

**Inherit-by-default semantics (post-regression fix):** the per-family vendor preset (`prompt.DefaultSampling`) is always the base. Only parameters the user set explicitly (non-zero) override the preset; every unset parameter inherits the vendor value. Enabling the variant with no explicit values is therefore a behavioral no-op — the earlier behavior (a constant temperature, seeded to 0.1 by `ApplyDefaults`, clobbering every family) broke vendor-tuned presets and is the suspected cause of the 27-30B model regression. The seeding of `temperature: 0.1` / `top_p: 0.9` in `backend/config/defaults.go` was removed accordingly: zero means "inherit the vendor preset" end-to-end (config → adapter → builder → router).

- **Temperature** — when set (must be > 0), overrides the per-family default. Unset (0) inherits.
- **TopP** — when set (must be in (0, 1]), overrides the per-family default; plumbed to all providers via the router's `SamplingFunc` (sp4rk `llm.ChatRequest.TopP`). Unset (0) inherits.
- **TopK** — when set (must be >= 1), overrides the per-family default; sent only to providers that support it (Anthropic, Google, LM Studio/vLLM-style OpenAI-compatible endpoints). Unset (0) inherits.
- **RepetitionPenalty** — when set (must be in [1, 2]), overrides the per-family default; sent only to OpenAI-compatible endpoints with a custom base URL (LM Studio/vLLM; strict api.openai.com rejects unknown fields). Unset (0) inherits.
- **ReasoningEffort** — when non-empty (`off`/`low`/`medium`), seeds the builder-level default via `applySmallLLMPresets`. Per-request overrides (`HandleOptions.ReasoningEffort`) still take precedence.

Range validation lives in `backend/frontend_api_config.go` `validateSmallLLMConfig` and runs whenever a value is set (regardless of the toggle), so a stored out-of-range value cannot go live the moment the variant is switched on. `config.example.yaml` documents the same fields and the inherit semantics.

### Loop Hardening

Tightens the executor circuit-breaker thresholds so a small model that repeats itself or makes no progress is nudged/aborted sooner, conserving the token budget. Applied in `applyLoopHardening` at builder construction. Only the thresholds present in the profile are overridden; all others (RepeatAbortThreshold, TruncationAbortThreshold, etc.) keep their baseline:

- `repeat_nudge_threshold`
- `parse_error_abort_threshold`
- `fruitless_nudge_threshold`
- `fruitless_abort_threshold`
- `same_tool_repeat_nudge_threshold`

These are tighter than, or equal to, the baseline `executor.circuitBreaker` values. Four are strictly tighter; `parse_error_abort_threshold` matches the baseline (`3`), since small models do not parse-fail more often than the baseline abort point intends.

### Context Management

Aggressive context management for small context windows. Applied in `applyContextManagement` — a pure helper invoked at every place an executor config is materialized (`Build`, `buildRouter`, `buildContextFactory`), so the overrides hold for the orchestrator executor, the router fallback executor, and the subagent context factory alike. When the master or variant toggle is off it returns the executor config byte-for-byte unchanged; when on, each knob is overridden independently (a zero value keeps the baseline for that knob):

- `compaction.keep_last` → `ExecutorConfig.Compaction.SlidingWindow.KeepLast` — messages kept verbatim at the conversation tail (default 6 vs the general 10).
- `compaction.block_size` → `Compaction.Summarization.BlockSize` — batch size for pruning per compaction round (default 5 vs the general 7).
- `compaction.trigger_percent` → `Compaction.Thresholds.PredictivePercent` — percentage of the context window at which compaction triggers (default 80 vs the general 85).
- `tool_output_keep_last_n` → `ToolOutputPruning.KeepLastN` — only the N most recent tool outputs are kept verbatim (default 2 vs the general 3); stricter than the conversation-wide compaction.
- `output_token_reserve` → `OutputTokenReserve` — tokens reserved from the context window for the model's own answer, so compaction fires early enough to leave room (default 8192, equal to the general executor default — the knob exists to override the reserve per-profile without touching the global executor setting).

`validateSmallLLMConfig` range-checks the variant when enabled: `keep_last ≥ 2`, `block_size ≥ 2`, `1 ≤ trigger_percent < 100`, `tool_output_keep_last_n ≥ 1`, `output_token_reserve ≥ 1`.

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
  │                        + applyContextManagement (router fallback executor)
  ├─ buildCoreAgents       → coreRouter.SetToolMatching (router tool selection)
  │                        + applyContextManagement (subagent context factory)
  └─ Build                 → applyLoopHardening (circuit-breaker thresholds)
                           + applyContextManagement (orchestrator executor)
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
- **The guaranteed set (always-present ∪ protected ∪ MCP) is never trimmed.** `max_tools` is a slot budget for router-matched tools only: at most `max_tools − len(guaranteed)` matched tools are kept, filled deterministically in registry order. The result may exceed `max_tools` when the guaranteed set alone is larger than the budget.
- `finish` and the fact-memory / human-interaction tools are always preserved regardless of the matched set, always-present list, or `max_tools` budget; every MCP-sourced tool is always kept.
- `validateSmallLLMConfig` rejects an enabled profile whose guaranteed count (`always_present ∪ protected`) exceeds `max_tools` (unless `max_tools = 0`), so a guaranteed tool can never be silently lost — neither by trimming nor by a budget that is unsatisfiable by construction.
- The injection-defense section is never removed or altered by the Lite swap (strict constraint); the lite directive carries no injection-defense content because it is injected separately and unchanged.
- FewShot and ReasoningScaffold are only honored when Lite is active (both are tailored to the lite directive's style).
- Specialized runs (goal derivation) are never swapped to the lite orchestrator directive.
- Sampling overrides are inherit-by-default: only explicitly set (non-zero) parameters override the vendor preset; unset parameters inherit it. All set parameters (temperature, top_p, top_k, repetition_penalty) reach the providers through the sp4rk router plumbing.
- Context-management overrides are applied identically at every executor-config materialization site (`Build`, `buildRouter`, `buildContextFactory`), so the orchestrator executor, the router fallback, and the subagent context factory never disagree when the variant is on.
- When the master toggle or the `context` variant toggle is off, the executor config is returned byte-for-byte unchanged; every override knob is independent (`> 0` per-field gate), and general compaction/pruning defaults are never modified.
- `validateSmallLLMConfig` runs before any mutation, so an invalid payload produces no partial write to config or config.yaml.
- `UpdateSmallLLMConfig` rebuilds the LLM router on success so the new profile takes effect for new sessions without an app restart.

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig). The authoritative reference for every tunable is `config.example.yaml`.

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `small_llm.enabled` | false | Master toggle. Manual only — no auto-detection. |
| `small_llm.essential_tools.enabled` | false | Gates the essential-tools variant. |
| `small_llm.essential_tools.always_present` | `defaultSmallLLMAlwaysPresent` (read_file, write_file, edit_file, list_directory, glob, ripgrep, bash_exec, semantic_search, store_fact, search_facts, ask_user, finish) | Tools always kept regardless of router selection. May be empty (protected + MCP tools are always kept implicitly). |
| `small_llm.essential_tools.max_tools` | 16 | Slot budget for router-matched tools: at most `max_tools − len(guaranteed)` matched tools are kept (registry order). The guaranteed set (always-present ∪ protected ∪ MCP) is never trimmed; validation rejects a cap below its size. 0 = unlimited. |
| `small_llm.essential_tools.compact_descriptions` | false | Replace every known builtin's full description (480-1100-char rubric) with a one-line compact variant while the variant is active; unknown tools (e.g. MCP) keep their original descriptions. Applies to the curated set and to the selection-fallback set alike. |
| `small_llm.system_prompt.lite` | false | Swap the verbose core directive for the compact lite directive. |
| `small_llm.system_prompt.few_shot` | false | Append worked-example ReAct cycles (requires Lite). |
| `small_llm.system_prompt.reasoning_scaffold` | false | Append three-step thought template (requires Lite). |
| `small_llm.sampling.enabled` | false | Gates the sampling variant. |
| `small_llm.sampling.temperature` | 0 (inherit) | Generation temperature; 0 inherits the per-family vendor preset (must be > 0 when set). |
| `small_llm.sampling.top_p` | 0 (inherit) | Nucleus-sampling mass, applied via the router sampling func (must be in (0, 1] when set). |
| `small_llm.sampling.top_k` | 0 (inherit) | Top-k sampling; only sent to providers that support it (must be ≥ 1 when set). |
| `small_llm.sampling.repetition_penalty` | 0 (inherit) | Repetition penalty; only sent to providers that support it (must be in [1, 2] when set). |
| `small_llm.sampling.reasoning_effort` | "" (inherit) | `off` \| `low` \| `medium` (seeds builder default; per-request overrides win). |
| `small_llm.loop_hardening.enabled` | false | Gates the loop-hardening variant. |
| `small_llm.loop_hardening.repeat_nudge_threshold` | 2 | Consecutive identical tool calls before a nudge. |
| `small_llm.loop_hardening.parse_error_abort_threshold` | 3 | Consecutive parse errors before abort. |
| `small_llm.loop_hardening.fruitless_nudge_threshold` | 3 | Consecutive minimal-result calls before a nudge. |
| `small_llm.loop_hardening.fruitless_abort_threshold` | 5 | Consecutive minimal-result calls before abort. |
| `small_llm.loop_hardening.same_tool_repeat_nudge_threshold` | 4 | Same-tool (varied args) calls before a nudge. |
| `small_llm.context.enabled` | false | Gates the context-management variant. |
| `small_llm.context.compaction.keep_last` | 6 | Messages kept verbatim at the tail during compaction (general: 10). Must be ≥ 2 when enabled. |
| `small_llm.context.compaction.block_size` | 5 | Pruning batch size per compaction round (general: 7). Must be ≥ 2 when enabled. |
| `small_llm.context.compaction.trigger_percent` | 80 | Context-window percentage that triggers compaction (general: 85). Range [1, 100). |
| `small_llm.context.tool_output_keep_last_n` | 2 | Most-recent tool outputs kept verbatim (general: 3). Must be ≥ 1 when enabled. |
| `small_llm.context.output_token_reserve` | 8192 | Tokens reserved for the model's answer (general: 8192 — equal; the knob allows per-profile override). Must be ≥ 1 when enabled. |

## RPC Surface

The small-LLM profile is editable at runtime via the settings UI. See [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) (`GetSmallLLMConfig` / `UpdateSmallLLMConfig`).

## Related Specs

- [orchestration/README.md](orchestration/README.md) — HandleMessage flow where the essential-tools filter applies
- [orchestration/conductor.md](orchestration/conductor.md) — Conductor system prompt (the Lite swap target)
- [orchestration/router.md](orchestration/router.md) — semantic tool matching gated on this profile
- [orchestration/executor.md](orchestration/executor.md) — circuit breakers (the loop-hardening target)
- [memory/compaction.md](memory/compaction.md) — compaction semantics (the context-management override target)
- [llm-providers.md](llm-providers.md) — LLM router / sampling (the sampling override target)
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `GetSmallLLMConfig` / `UpdateSmallLLMConfig` RPC
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `tools_assigned` event
- [../decisions/022-small-llm-profile.md](../decisions/022-small-llm-profile.md) — rationale for the variant-and-master-toggle design
