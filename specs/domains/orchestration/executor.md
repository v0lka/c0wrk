# Executor

## Role

Executes individual plan steps via the ReAct loop (Thought → Action → Observation), with circuit breakers for failure detection and context window management for long-running steps.

## Key Files

- `sdk/agent/executor.go` — Executor struct (Run method, ReAct loop)
- `sdk/orchestration/orchestrator.go` — SDK Orchestrator (drives DAG execution, parallel steps)
- `core/stepconfig.go` — coreStepConfigurator (resolves per-step config)
- `sdk/agent/events.go` — AgentEvents interface (lifecycle hooks)

## Behavior

### SDK Orchestration Engine (DAG Driver)

The SDK `orchestration.Orchestrator` drives the plan:

```
engine.Resume(ctx, bb)
│
├─ Read plan from Blackboard
├─ Loop until all steps complete or max retries exhausted:
│   │
│   ├─ FindReadySteps() — steps with all dependencies satisfied
│   ├─ For each ready step (parallel goroutines):
│   │   ├─ StepConfigurator resolves StepConfig
│   │   ├─ Build step task (description + dependency context)
│   │   ├─ Create ContextManager for step
│   │   ├─ Run Executor.Run(ctx, task, tools, systemPrompt)
│   │   └─ Store result on Blackboard
│   │
│   ├─ If step failed:
│   │   ├─ Reflector.Reflect() → Reflection
│   │   ├─ Planner.Replan() → new Plan
│   │   └─ Retry from step 1
│   │
│   └─ If all steps succeeded: done
│
└─ Return ExecutionResult {Output, Plan, Blackboard, AttemptCount, Reflections}
```

### Per-Step Configuration (StepConfigurator)

`coreStepConfigurator` resolves runtime config for each step:

| Field              | Resolution                                                      |
| ------------------ | --------------------------------------------------------------- |
| MaxSteps           | step.Profile.MaxSteps > 0 ? use it : config.MaxSteps            |
| SystemPrompt       | buildSystemPrompt(ctx) with step-local skill narrowing          |
| CompactionStrategy | step.Profile.Domain mapping (code→sliding, research→summary)    |
| AgentRole          | step.Profile.Role (affects prompt + pruning)                    |
| AllowedTools       | step.Profile.AllowedTools (nil = all)                           |
| ReasoningEffort    | ResolveAgentReasoningMode(role, base, overrides)                |
| KeepLastN          | step.Profile.KeepLastN > 0 ? use it : rolePruningDefaults[role] |
| ProtectedTools     | step.Profile.ProtectedTools ?? rolePruningDefaults[role]        |

Role-based pruning defaults:

- `researcher`: KeepLastN=10
- `coder`, `tester`, `executor`: KeepLastN=5

### Dependency Context Injection

Before a step runs, outputs from its dependencies are injected into the task description:

```
Step task = step.Description + "\n\n## Context from previous steps\n"
  + For each dep in step.DependsOn:
      "### {dep.Summary}\n{truncate(dep.Output, budget)}\n"
```

Budget: `MaxDependencyContextChars` (default: 8000) divided among dependencies.

### ReAct Loop (Executor.Run)

```
Executor.Run(ctx, task, tools, systemPrompt)
│
├─ Initialize: ContextManager, messages, step counter
│
├─ Loop (max MaxSteps iterations):
│   │
│   ├─ 1. Call LLM with current messages
│   │      → Response may contain: text, tool_calls, or finish
│   │
│   │   ├─ 2. If tool_call:
│   │      ├─ Inject ToolResultCache into context (for fragmentation reader)
│   │      ├─ Execute tool via ToolExecutor.Execute(ctx, name, input)
│   │      ├─ Set Step.IsUntrusted ← tool.IsUntrusted() || source starts with "mcp"
│   │      ├─ Stage 1: Per-tool line/byte truncation (configurable, cached)
│   │      │   → Store full result in ToolResultCache keyed by SHA256 hash
│   │      │   → Truncate to per-tool limits, append fragmentation nudge with hash
│   │      ├─ Stage 2: Apply ToolResultBudget (token-based truncation)
│   │      ├─ Add observation to messages (context.go wraps untrusted output in <untrusted-content>)
│   │      └─ Check context fill → compact if needed
│   │
│   ├─ 3. If finish tool called:
│   │      → Extract output, return success
│   │
│   ├─ 4. Circuit breaker check (see below)
│   │      → If abort threshold reached: call StepLimitFunc(reason)
│   │      → AllowOnce: reset + nudge, AllowAlways: disable + nudge, Deny: stop
│   │
│   └─ 5. If step limit reached:
│          → Call StepLimitFunc(reason="")
│          → AllowOnce: +N steps, AllowAlways: unlimited, Deny: stop
│
└─ Return ExecutorResult {Steps, Output, Finished}
```

### Circuit Breaker

Detects pathological patterns. Each detector has a nudge threshold and an abort threshold:

| Detection   | Trigger                                     | Nudge Action               | Abort Action                   |
| ----------- | ------------------------------------------- | -------------------------- | ------------------------------ |
| Repeat      | Same tool + same args + same error N times  | "try a different approach" | Call StepLimitFunc with reason |
| Truncation  | LLM output truncated (tool call incomplete) | "split into smaller calls" | Call StepLimitFunc with reason |
| Parse error | Invalid tool input N times                  | "simplify your input"      | Call StepLimitFunc with reason |
| Fruitless   | Last N tool results are empty/minimal       | "consider wrapping up"     | Call StepLimitFunc with reason |
| Same tool   | Same tool name N times with similar results | "try a different strategy" | Call StepLimitFunc with reason |

When a circuit breaker abort threshold is reached, the executor calls `StepLimitFunc` with a reason string describing the trigger (same mechanism as the step limit). The user receives the same three options:

- **AllowOnce**: resets the counter, injects a nudge urging the LLM to change approach, continues one more iteration
- **AllowAlways**: resets the counter, disables that specific circuit breaker for the remainder of the execution, injects a permissive nudge
- **Deny**: aborts execution (returns `ExecutorResult{Finished: false}`)

If `StepLimitFunc` is nil (no UI connected), the executor aborts immediately (preserving headless/test behavior).

Config: `CircuitBreakerConfig` (thresholds per detection type).

### Critical Always-Allowed Tools

These tools are always available regardless of step's AllowedTools filter:

- `finish` — end step execution
- `store_fact` — save findings to blackboard
- `search_facts` — retrieve stored facts
- `ask_user` — prompt user for information
- `set_step_status` — update step status/checklist
- `read_step_output` — read a specific step's output
- `tool_result_read` — read cached tool result fragments by hash

The set is enforced in `core/stepconfig.go` `criticalAlwaysAllowedTools` and unioned into the filtered list whenever `AllowedTools` is non-empty.

### Tool Result Caching & Two-Stage Truncation

Every non-infrastructure tool result is cached and truncated in two stages:

**Stage 1 — Per-tool line/byte truncation (configurable per tool):**

- Full result is stored in `ToolResultCache` keyed by SHA256(content)
- Result is truncated to per-tool `MaxLines` / `MaxBytes` (from config `toolLimits.perToolTruncation`)
- A fragmentation nudge is appended: `[This output was truncated. Read the rest with tool_result_read(hash="sha256...", start_line=N+1, num_lines=M)]`
- Cache entries carry metadata for coherence checking (file tools: mtime+size; MCP tools: TTL)

**Stage 2 — Token-based budget (existing, unchanged):**

- `HardCapTokens` — absolute maximum tokens for a single result
- `FillFraction` — max percentage of available context for tool results
- Floor: 256 tokens minimum
- Truncation notice appended when result exceeds budget

**Cache coherence:**

- File tools (`read_file`, `write_file`, `edit_file`, `delete_file`): cache entries tagged with file path + mtime + size. On `tool_result_read`, the executor checks current file metadata against cached signature. If changed, returns an error instructing the LLM to re-read.
- MCP tools: cache entries carry a TTL. Expired entries are evicted on access.
- Non-cacheable tools (`tool_result_read`, `finish`, `read_step_output`, `list_step_outputs`, `store_fact`, `search_facts`, `set_step_status`, `ask_user`): excluded from both caching and Stage 1 truncation.

**Cache TTL:** `toolResultBudget.cacheTTLSeconds` (default: 300). Eviction runs on access.

## Error Handling

- Step failure (executor returns error): result stored with Error field set
- Context cancelled: propagates immediately, no retry
- Step limit reached without finish: treated as incomplete (not failure)

## Invariants

- `finish` tool is ALWAYS available in every step (never filtered out)
- Context window never exceeds model limit (compaction triggers automatically)
- MaxSteps bounds total iterations (StepLimitFunc may extend)
- Each step has its own ContextManager (isolated memory)
- Parallel steps run in separate goroutines with independent contexts
- A step's output is immutable once stored on Blackboard
- Active skill bodies are rendered verbatim in the step system prompt (no truncation)
- Step-local skill narrowing fires whenever `step.Profile.Skills` is non-empty; requested names resolve from the task-scope ActiveSkills pool first, falling back to the SkillManager only for names absent from the pool
- Every `Step` carries `IsUntrusted`; set by executor after tool execution via `tool.IsUntrusted()` or MCP source check
- Untrusted tool output is wrapped in `<untrusted-content>` XML tags in `context.go` `buildStepMessages()` before messages are sent to the LLM
- Every tool result from a cacheable tool is stored in ToolResultCache before truncation; the cache entry key is SHA256(full content)
- `tool_result_read` validates cache coherence on every read: for file tools, it compares current file mtime+size with the cached signature; for MCP tools, it checks TTL expiry

## Related Specs

- [README.md](README.md) — orchestration overview
- [planner.md](planner.md) — plan generation
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../tool-system/README.md](../tool-system/README.md) — tool execution pipeline and trust classification
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and injection defense
