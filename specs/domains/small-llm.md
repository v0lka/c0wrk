# Small-LLM Profile

## Purpose

The Small-LLM profile is a set of optimizations applied when running the Conductor against a "small" (low-capacity / cheaper) LLM. Small models are disproportionately penalized by large tool schemas, verbose system prompts, loose sampling, runaway loops, and overflowing context windows, so the profile narrows the visible tool set, simplifies the system prompt, tightens the circuit breakers, overrides sampling parameters, and aggressively manages the context — each as an independently gated variant under a single master toggle. The master toggle is manual only: there is no auto-detection; the operator decides when to enable the profile.

## Key Files

- `backend/config/config.go` — `SmallLLMConfig` and its sub-configs (`EssentialToolsConfig`, `SystemPromptConfig`, `SmallLLMSamplingConfig`, `LoopHardeningConfig`, `SmallLLMContextConfig`)
- `backend/config/defaults.go` — `defaultSmallLLMAlwaysPresent` and the zero-value defaults for every threshold/value
- `backend/configadapter.go` — `ToBuilderConfig` copies `SmallLLMConfig` into `core.BuilderSmallLLMConfig` (core never imports `backend/config`)
- `core/builderconfig.go` — `BuilderSmallLLMConfig` + sub-structs (the core-layer mirror)
- `core/builder.go` — `applySmallLLMPresets` (seeds builder-level reasoning effort), `applyLoopHardening` (overrides circuit-breaker thresholds), `applyContextManagement` (overrides compaction/pruning/reserve on the executor config), `resolveSamplingFunc` (overrides router sampling temperature)
- `core/smallllm/tools_filter.go` — pure, deterministic `SelectTools` (with turn-scoped `extraGuaranteed` names) + `ProtectedToolNames` (static tool-set assembly)
- `core/orchestrator.go` — `SmallLLMSettings` mirror on `OrchestratorConfig`; `applySmallLLMToolFilter` (the single call site in `HandleMessage`) and `smallLLMAgentGuaranteedTools` (turn-scoped delegate guarantee)
- `core/orchestrator_handle.go` — `prepareRequestContext` carries the SystemPrompt sub-toggle flags into context
- `core/orchestrator_goal.go` — the essential-tools filter is intentionally NOT applied in goal mode
- `core/systemprompt.go` — `buildSystemPromptWith` swaps the OrchestratorSystem directive for OrchestratorSystemLite and appends the scaffold / few-shot blocks
- `core/prompts/orchestrator_lite.md` — compact core directive (the Lite swap)
- `core/prompts/orchestrator_lite_scaffold.md` — three-step reasoning scaffold
- `core/prompts/orchestrator_lite_fewshot.md` — curated worked-example ReAct cycles
- `backend/frontend_api_config.go` — `GetSmallLLMConfig` / `UpdateSmallLLMConfig` (RPC surface) + `validateSmallLLMConfig`
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

Narrows the Conductor's advertised tool set once per task (before the ReAct loop, in `HandleMessage`) to reduce per-prompt JSON-schema/token overhead. Selection is purely static — there is no router matching, no quantitative budget, and no domain-specific allow-list; the operator's selection IS the narrowing. `smallllm.SelectTools` unions four sources:

1. **always-present tools** — the operator's pinned list (`essential_tools.always_present`).
2. **protected orchestration tools** — `finish`, `store_fact`, `search_facts`, `ask_user`, `update_checklist` (never dropped).
3. **every MCP-sourced tool** — user-installed, always included whole (a server's tools are never partially dropped).
4. **turn-scoped extra guarantees** — e.g. `delegate` when the task's directives require it (see below).

The assigned set is exactly this union — nothing more, nothing less — emitted in registry order. Every tool in it is guaranteed and never trimmed: the pins are explicit operator choices, MCP tools are user-installed integrations, and the protected set carries the completion channel; dropping any of it would silently break a pinned workflow, a user-installed MCP server, or the conductor loop's ability to terminate. There is no slot budget and no over-budget concept: the set cannot be "too large" for the mechanism itself (the operator controls its size through the selection and the installed MCP servers; the 10–20-tool selection-accuracy safe zone from `docs/small-llm-defaults-research.md` remains operator-side guidance, not an enforced guard).

**Turn-scoped delegate guarantee.** When the task context carries requested subagents — an explicit `#agent-name` mention, surfaced to the model as the `## Requested Subagents` directive that mandates delegation via `delegate(agent: ...)` — the orchestrator passes `["delegate"]` into `smallllm.SelectTools` as the turn-scoped `extraGuaranteed` set (`smallLLMAgentGuaranteedTools` in `core/orchestrator.go`). The name joins the set for that task alone, and unknown names are silently ignored. This closes the cross-feature gap where an active `#agent` mention could meet a narrowed tool set that lacks the `delegate` tool the directive requires.

**Compact descriptions.** With `compact_descriptions` on, every known builtin's full rubric description (purpose/when-to-use/inputs/outputs/example/anti-example, 480-1100 chars) is replaced by a one-line compact variant; unknown tools (e.g. MCP) keep their original descriptions.

**No chat surfaced events.** The narrowing is a silent, deterministic background step: it emits no `tools_assigned` card and no budget diagnostics into the chat (the former `tools_assigned` event and `small_llm_tool_budget_overflow` diagnostic were removed alongside the `max_tools` budget — see [../decisions/035-remove-small-llm-tool-budget.md](../decisions/035-remove-small-llm-tool-budget.md)).

**Goal mode is never narrowed.** The only filter call site is `HandleMessage`'s non-goal path, which runs AFTER the goal-mode early return. Goal mode deliberately keeps the full tool set (the goal-loop tools, including the verifier-required `declare_verification`, would otherwise be dropped by `SelectTools`).

### System Prompt Simplification

Shrinks the system prompt injected for a small model. Applied in `buildSystemPromptWith` (gated on the context-carried profile from `prepareRequestContext`):

- **Lite** — swaps the verbose `OrchestratorSystem` core directive for the compact `OrchestratorSystemLite` directive. The lite directive drops verbose operational docs (checklist/table mechanics, progress-tracking internals) that an SLM cannot hold, but keeps compact versions of the behavioral guards that must never be lost: a short **Git Policy** (no state-modifying git commands unless explicitly requested — the behavioral layer above the `execute` group's `user_confirm` policy) and the **Efficiency Hints** micro-hints (truncated-output mechanics via `tool_result_read`, fact-memory discipline — `store_fact` early/often, `search_facts` before a new sub-step — and `[MCP]`-tool priority, plus files-are-deliverables and minimal shell output). The `Edit → Verify Cycle` section appears exactly once in the lite directive and exactly once in the full directive. It carries NO injection-defense content — that section is injected separately and unchanged (strict constraint).
- **ReasoningScaffold** — appends a three-step thought template (goal → tool choice + rationale → exact args). Only honored when Lite is on.
- **FewShot** — appends curated worked-example ReAct cycles (correct tool-call format, tool choice, error recovery, finish). Only honored when Lite is on.

Specialized runs (e.g. goal derivation) carry their own core directive and are never swapped to the lite orchestrator directive. The shared sections (family overlay, verification mandate, injection defense, workspace, env, AGENTS.md, skills) are appended UNCHANGED in both modes.

### Sampling Overrides

Overrides LLM sampling parameters for more deterministic, lower-effort generation. Applied in `resolveSamplingFunc` at router construction.

**Inherit-by-default semantics (post-regression fix):** the per-family vendor preset (`prompt.DefaultSampling`) is always the base for the numeric parameters. Only parameters the user set explicitly (non-zero) override the preset; every unset parameter inherits the vendor value — the earlier behavior (a constant temperature, seeded to 0.1 by `ApplyDefaults`, clobbering every family) broke vendor-tuned presets and is the suspected cause of the 27-30B model regression. The seeding of `temperature: 0.1` / `top_p: 0.9` in `backend/config/defaults.go` was removed accordingly: zero means "inherit the vendor preset" end-to-end (config → adapter → builder → router). `reasoning_effort` is the deliberate exception: `ApplyDefaults` seeds an unset value to `medium` (rationale below), so enabling the variant with no explicit values lowers the reasoning depth from the vendor default instead of being a pure no-op.

- **Temperature** — when set (must be > 0), overrides the per-family default. Unset (0) inherits.
- **TopP** — when set (must be in (0, 1]), overrides the per-family default; plumbed to all providers via the router's `SamplingFunc` (sp4rk `llm.ChatRequest.TopP`). Unset (0) inherits.
- **TopK** — when set (must be >= 1), overrides the per-family default; sent only to providers that support it (Anthropic, Google, LM Studio/vLLM-style OpenAI-compatible endpoints). Unset (0) inherits.
- **RepetitionPenalty** — when set (must be in [1, 2]), overrides the per-family default; sent only to OpenAI-compatible endpoints with a custom base URL (LM Studio/vLLM; strict api.openai.com rejects unknown fields). Unset (0) inherits. The Qwen card keeps this at the vendor 1.0 and prescribes `presence_penalty` as the sanctioned anti-repetition lever instead.
- **PresencePenalty** — when set (must be in [0, 2]), overrides the per-family default. Part of the official OpenAI Chat Completions schema (unlike `repetition_penalty`/`top_k`), so it is sent to every OpenAI-compatible endpoint when set, including strict api.openai.com; the Anthropic and Google providers do not serialize it. Qwen card: 0–2, instruct-mode default 1.5; higher values increase language mixing. Unset (0) inherits — no family preset sets it, so the field is simply not sent.
- **ReasoningEffort** — the deliberate exception to inherit-by-default: `ApplyDefaults` seeds an unset (`""`) value to `medium`. The vendor inherit is `xhigh` on qwen thinking models — measured overthinking on trivial tasks (22,276 reasoning tokens / 21 min for a simple SVG vs 3,715 tokens / 137 s with thinking off) — while `medium` is the model's native pre-training regime (no effort instruction injected, unlike `low`, which shortens traces but risks retries in multi-turn agentic tasks) and cuts thinking-token spend 60–90%. The resolved value seeds the builder-level default via `applySmallLLMPresets`; sp4rk's qwen mapping sends the native per-request `reasoning_effort` parameter. Per-request overrides (`HandleOptions.ReasoningEffort`) still take precedence; an explicit non-empty YAML value (`off`/`low`/`medium`) is never overwritten, and the vendor `xhigh` default remains reachable by disabling the variant. The resolved effort is also threaded into every compaction Summarize call (`buildContextFactory` sets `ReasoningEffort` on the deterministic `CallPurposeCompaction` request alongside the pinned temperature), so compaction runs at the same reasoning depth as the main loop — a deliberate trade-off: summaries benefit from the same depth at the cost of thinking tokens/latency per compaction on small models. Rationale: `docs/small-llm-defaults-research.md` (R1/R3).

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
- `output_token_reserve` → `OutputTokenReserve` — overrides the global `executor.output_token_reserve` while the variant is on. The value reaches the router as `llm.RouterConfig.OutputTokenReserve`, where it is consulted only as the output reserve in pre-submission context-window validation (`validateContextWindow`) for models whose resolved metadata carries no `OutputLimit`; the registry resolves every model to a non-zero `OutputLimit` (built-in catalog, probe cache, or the 32768 static fallback), so the fallback tier is effectively unreachable. The knob does not change the per-request MaxTokens generation ceiling: that ceiling is the model's resolved `ModelMetadata.OutputLimit` — per-model `llm.models.<model>.output_limit` > per-provider `llm.<provider>.output_token_reserve` > catalog/probe tiers (see [llm-providers.md](llm-providers.md)). For a thinking-capable small model whose catalog output limit truncates reasoning + answer (thinking tokens are spent first, measured ~3.7K–22.3K per turn), raise `llm.models.<model>.output_limit` or the per-provider reserve. The default (16384, vs the general executor 8192) keeps the fallback tier thinking-aware; the knob exists to override the reserve per-profile without touching the global executor setting.

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
  ├─ buildCoreAgents       → applyContextManagement (subagent context factory)
  └─ Build                 → applyLoopHardening (circuit-breaker thresholds)
                           + applyContextManagement (orchestrator executor)
                              + OrchestratorConfig.SmallLLMSettings
       │
       ▼
per-session Orchestrator.HandleMessage (non-goal path):
  ├─ applySmallLLMToolFilter (ONCE) → smallllm.SelectTools (static union,
  │     silent — no events emitted)
  └─ prepareRequestContext → withSmallLLMPromptProfile (ctx flags)
       → buildSystemPromptWith → Lite swap + scaffold + few-shot
```

## Invariants

- The master `SmallLLM.Enabled` toggle gates every variant; when it is off, behavior is identical to the un-profiled baseline (zero behavior change at every variant's call site).
- The experimental-features master switch (`experimental.enabled`) gates the whole profile at the `ToBuilderConfig` boundary: when off, the builder sees `SmallLLM.Enabled = false` regardless of the stored `small_llm.enabled`, so the profile is inert for every session. The stored value is preserved so re-enabling experimental features restores the prior profile.
- Each variant is independently gated by BOTH the master toggle and its own sub-toggle (defense-in-depth).
- The essential-tools filter runs exactly once per task, before the non-goal ReAct loop starts; it is never applied in goal mode.
- **The assigned set is exactly always-present ∪ protected ∪ MCP ∪ turn-scoped guarantees, in registry order.** There is no slot budget and no router matching: nothing in the assigned set is ever trimmed, and the filter emits no events.
- A task whose context carries requested subagents (an explicit `#agent` mention) always has `delegate` in its curated tool set, even though it is neither pinned nor MCP-sourced.
- The lite directive retains the compact Git Policy and the Efficiency Hints micro-hints (truncated-output mechanics, fact-memory discipline, MCP priority); the `Edit → Verify Cycle` section appears exactly once in each of the lite and full directives.
- `finish` and the fact-memory / human-interaction tools are always preserved regardless of the always-present list; every MCP-sourced tool is always kept whole.
- Tool narrowing emits nothing into the chat: no `tools_assigned` card, no budget diagnostics (removed with the `max_tools` budget — see [../decisions/035-remove-small-llm-tool-budget.md](../decisions/035-remove-small-llm-tool-budget.md)).
- The injection-defense section is never removed or altered by the Lite swap (strict constraint); the lite directive carries no injection-defense content because it is injected separately and unchanged.
- FewShot and ReasoningScaffold are only honored when Lite is active (both are tailored to the lite directive's style).
- Specialized runs (goal derivation) are never swapped to the lite orchestrator directive.
- Sampling overrides are inherit-by-default: only explicitly set (non-zero) parameters override the vendor preset; unset parameters inherit it. All set parameters (temperature, top_p, top_k, repetition_penalty, presence_penalty) reach the providers that support them through the sp4rk router plumbing (per-parameter provider notes above).
- Context-management overrides are applied identically at every executor-config materialization site (`Build`, `buildRouter`, `buildContextFactory`), so the orchestrator executor, the router fallback, and the subagent context factory never disagree when the variant is on.
- When the master toggle or the `context` variant toggle is off, the executor config is returned byte-for-byte unchanged; every override knob is independent (`> 0` per-field gate), and general compaction/pruning defaults are never modified.
- `validateSmallLLMConfig` runs before any mutation, so an invalid payload produces no partial write to config or config.yaml.
- `UpdateSmallLLMConfig` rebuilds the LLM router on success so the new profile takes effect for new sessions without an app restart.

## Configuration

From `config.yaml` (via BuilderConfig → OrchestratorConfig). The authoritative reference for every tunable is `config.example.yaml`. Every `small_llm.*` value below takes effect only when the top-level `experimental.enabled` switch is on; with it off the profile is inert regardless of these values.

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `small_llm.enabled` | false | Master toggle. Manual only — no auto-detection. |
| `small_llm.essential_tools.enabled` | false | Gates the essential-tools variant. |
| `small_llm.essential_tools.always_present` | `defaultSmallLLMAlwaysPresent` (read_file, write_file, edit_file, list_directory, glob, ripgrep, bash_exec, semantic_search, store_fact, search_facts, ask_user, finish) | Tools always kept. May be empty (protected + MCP tools are always kept implicitly). The assigned set is this list ∪ protected ∪ every MCP tool. |
| `small_llm.essential_tools.compact_descriptions` | false | Replace every known builtin's full description (480-1100-char rubric) with a one-line compact variant while the variant is active; unknown tools (e.g. MCP) keep their original descriptions. |
| `small_llm.system_prompt.lite` | false | Swap the verbose core directive for the compact lite directive. |
| `small_llm.system_prompt.few_shot` | false | Append worked-example ReAct cycles (requires Lite). |
| `small_llm.system_prompt.reasoning_scaffold` | false | Append three-step thought template (requires Lite). |
| `small_llm.sampling.enabled` | false | Gates the sampling variant. |
| `small_llm.sampling.temperature` | 0 (inherit) | Generation temperature; 0 inherits the per-family vendor preset (must be > 0 when set). |
| `small_llm.sampling.top_p` | 0 (inherit) | Nucleus-sampling mass, applied via the router sampling func (must be in (0, 1] when set). |
| `small_llm.sampling.top_k` | 0 (inherit) | Top-k sampling; only sent to providers that support it (must be ≥ 1 when set). |
| `small_llm.sampling.repetition_penalty` | 0 (inherit) | Repetition penalty; only sent to providers that support it (must be in [1, 2] when set). |
| `small_llm.sampling.presence_penalty` | 0 (inherit) | Presence penalty — the OpenAI-schema anti-repetition lever (Qwen card: 0–2, instruct default 1.5; higher values increase language mixing). Part of the official OpenAI Chat Completions schema, so sent to every OpenAI-compatible endpoint when set, incl. api.openai.com; the Anthropic and Google providers do not serialize it (must be in [0, 2] when set). |
| `small_llm.sampling.reasoning_effort` | medium (seeded when unset) | `off` \| `low` \| `medium` (seeds builder default; per-request overrides win). Unset/`""` resolves to the seeded default `medium` — the vendor inherit (xhigh on qwen thinking models) is reachable by disabling the variant. Rationale: `docs/small-llm-defaults-research.md` (R3). |
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
| `small_llm.context.output_token_reserve` | 16384 | Overrides `executor.output_token_reserve`; feeds only the router's context-window validation fallback for models whose metadata carries no `OutputLimit` (effectively unreachable — the registry always resolves one). Does not change the MaxTokens ceiling: raise `llm.models.<model>.output_limit` / per-provider `output_token_reserve` for that. Must be ≥ 1 when enabled. |

## RPC Surface

The small-LLM profile is editable at runtime via the settings UI. See [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) (`GetSmallLLMConfig` / `UpdateSmallLLMConfig`).

## Extension Points

- **New variant** — add a sub-config to `SmallLLMConfig` (`backend/config/config.go`), mirror it in `BuilderSmallLLMConfig` (`core/builderconfig.go`), copy it in `configadapter.ToBuilderConfig`, apply it in a dedicated `apply*` helper in `core/builder.go`, gate it on BOTH the master toggle and its own sub-toggle, validate it in `validateSmallLLMConfig`, and document it in `config.example.yaml` and the Configuration table above.
- **New sampling knob** — extend `SmallLLMSamplingConfig` and `BuilderSmallLLMSampling` with inherit-by-default semantics (zero = vendor preset), wire it through `resolveSamplingFunc` into the corresponding sp4rk `ChatRequest` field, and range-check it in `validateSmallLLMConfig`.
- **New protected tool** — add it to `protectedToolNames` in `core/smallllm/tools_filter.go`. The protected core grows with it; the locked chips in the settings UI follow automatically (the UI renders `ProtectedToolNames` from the RPC payload).

## Related Specs

- [orchestration/README.md](orchestration/README.md) — HandleMessage flow where the essential-tools filter applies
- [orchestration/conductor.md](orchestration/conductor.md) — Conductor system prompt (the Lite swap target)
- [orchestration/router.md](orchestration/router.md) — routing (domain/complexity/skills); router tool matching is NOT used by the narrowing
- [orchestration/executor.md](orchestration/executor.md) — circuit breakers (the loop-hardening target)
- [memory/compaction.md](memory/compaction.md) — compaction semantics (the context-management override target)
- [llm-providers.md](llm-providers.md) — LLM router / sampling (the sampling override target)
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `GetSmallLLMConfig` / `UpdateSmallLLMConfig` RPC
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — session event catalog (`tools_assigned` was removed)
- [../decisions/022-small-llm-profile.md](../decisions/022-small-llm-profile.md) — rationale for the variant-and-master-toggle design
- [../decisions/035-remove-small-llm-tool-budget.md](../decisions/035-remove-small-llm-tool-budget.md) — removal of the `max_tools` budget, router tool matching, and the tool chat cards
- [../../docs/small-llm-defaults-research.md](../../docs/small-llm-defaults-research.md) — external-evidence review behind every profile default; the evidence base for the `medium` reasoning-effort default, the 16384 output-token reserve, `presence_penalty`, and the 10–20-tool selection-accuracy guidance
