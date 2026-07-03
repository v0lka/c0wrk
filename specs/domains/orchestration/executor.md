# Executor

## Role

Executes individual plan steps via the ReAct loop (Thought → Action → Observation), with circuit breakers for failure detection and context window management for long-running steps.

## Key Files

- `sdk/agent/executor.go` — Executor struct (Run method, ReAct loop, non-cacheable tools set, batch dispatch, `DetectToolCallSyntaxInContent` failure-mode detector)
- `sdk/agent/executor_run.go` — `processSingleToolCall`, `processBatchTool` (batch meta-tool interception), `handleImplicitFinish` (nudge → abort logic)
- `sdk/agent/subagent.go` — `RunSubAgent` (subagent lifecycle, defense-in-depth success check)
- `sdk/orchestration/orchestrator.go` — SDK Orchestrator (drives DAG execution, parallel steps)
- `core/stepconfig.go` — coreStepConfigurator (resolves per-step config)
- `sdk/agent/events.go` — AgentEvents interface (lifecycle hooks)
- `sdk/tools/builtins/batch.go` — batch meta-tool descriptor (intercepted at executor, never directly executed)

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
| ReasoningEffort    | `config.ReasoningEffort` (native string, set via `SetReasoningEffort()`) |
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
│   │      ├─ If batch meta-tool → processBatchTool() (see Batch Tool below)
│   │      ├─ HITL: OnToolCall(ctx, name, input) — consumer may deny or modify input
│   │      │      → Denied: inject rejection observation, skip execution, continue loop
│   │      │      → Modified input: use modified input for execution
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
 │   │      ├─ Mutation gate (if mutationRequired): check if any mutating tool
 │   │      │   (write_file, edit_file, create_directory, delete_file, delete_directory)
 │   │      │   was successfully executed in this step.
 │   │      │   → No mutation + first attempt: inject mutation nudge, continue loop
 │   │      │   → No mutation + second attempt: return Finished=false (triggers reflection/replan)
 │   │      │   → Mutation present or gate disabled: extract output, return success
 │   │      └─ Extract output, return success (when gate passes)
 │   │
 │   ├─ 3a. If no tool calls (implicit finish path — see Implicit Finish & Failure-Mode below):
 │   │      → Failure-mode detector checks for tool-call syntax printed as text
 │   │      → General nudge (up to 2 attempts) before accepting implicit finish
 │   │      → Finish nudge (suppressAssistantEvents mode) requires explicit finish call
 │   │
 │   ├─ 4. Circuit breaker check (see below)
 │   │      → If abort threshold reached: call HITLHandler.OnStepLimit(reason)
 │   │      → AllowOnce: reset + nudge, AllowAlways: disable + nudge, Deny: stop
 │   │
 │   └─ 5. If step limit reached:
 │          → Call HITLHandler.OnStepLimit(reason="")
 │          → AllowOnce: +N steps, AllowAlways: unlimited, Deny: stop
│
└─ Return ExecutorResult {Steps, Output, Finished}
```

### Circuit Breaker

Detects pathological patterns. Each detector has a nudge threshold and an abort threshold:

| Detection   | Trigger                                     | Nudge Action               | Abort Action                   |
| ----------- | ------------------------------------------- | -------------------------- | ------------------------------ |
| Repeat      | Same tool + same args + same error N times  | "try a different approach" | Call HITLHandler.OnStepLimit with reason |
| Truncation  | LLM output truncated (tool call incomplete) | "split into smaller calls" | Call HITLHandler.OnStepLimit with reason |
| Parse error | Invalid tool input N times                  | "simplify your input"      | Call HITLHandler.OnStepLimit with reason |
| Fruitless   | Last N tool results are empty/minimal       | "consider wrapping up"     | Call HITLHandler.OnStepLimit with reason |
| Same tool   | Same tool name N times with similar results | "try a different strategy" | Call HITLHandler.OnStepLimit with reason |

When a circuit breaker abort threshold is reached, the executor calls `HITLHandler.OnStepLimit` with a reason string describing the trigger (same mechanism as the step limit). The user receives the same three options:

- **AllowOnce**: resets the counter, injects a nudge urging the LLM to change approach, continues one more iteration
- **AllowAlways**: resets the counter, disables that specific circuit breaker for the remainder of the execution, injects a permissive nudge
- **Deny**: aborts execution (returns `ExecutorResult{Finished: false}`)

If `HITLHandler` is nil (no UI connected), the executor aborts immediately (preserving headless/test behavior).

Config: `CircuitBreakerConfig` (thresholds per detection type).

### Mutation Gate

When `mutationRequired` is set on the executor (via `SetMutationRequired(true)`), the finish tool call is intercepted before completion. The gate checks whether any mutating tool (`write_file`, `edit_file`, `create_directory`, `delete_file`, `delete_directory`) was **successfully** executed during the current step (scanning `state.allSteps` for `Step.Action.Name` in the mutating set, excluding rejected calls).

Flow:

```
finish called + mutationRequired=true
  │
  ├─ hasMutatingToolExecuted?
  │   ├─ YES → accept finish, return Finished=true
  │   └─ NO  → mutationNudgeAttempted?
  │       ├─ NO  → inject executorMutationNudge, set flag, continue loop (retry)
  │       └─ YES → return Finished=false (triggers orchestrator reflection/replan)
```

Purpose: prevents "false success" on code-modification steps where the agent reads extensively but finishes without making changes. The gate is role-dependent — set by `coreStepConfigurator` only for `profile.Role == "coder" && profile.Domain == "code"`. Researcher/audit/tester steps are not gated.

Rejected tool calls (HITL denial with `Observation` starting `[Tool call rejected`) do **not** count as mutations. `bash_exec` is intentionally excluded from the mutating set — it's ambiguous (could be `pwd && ls` or `go test`), and the gate focuses on structural filesystem mutations.

Source: `sdk/agent/executor.go` `mutatingTools` set, `sdk/agent/executor_run.go` `hasMutatingToolExecuted` + finish branch gate.

### Implicit Finish & Failure-Mode Detection

When the LLM returns no tool calls, `handleImplicitFinish` decides whether to accept an implicit finish, nudge the LLM to use tools, or abort. Two paths:

**General implicit finish** (no tool calls, `end_turn` or other stop reason):

- Up to 2 general nudges (`executorNudge`) are injected before accepting implicit finish. The nudge counter (`implicitFinishNudgeCount`) tracks attempts across the step.
- In `suppressAssistantEvents` mode (plan-step execution), a finish nudge (`executorFinishNudge`) requires an explicit `finish` tool call before accepting completion.
- After the nudge budget is exhausted, the response is accepted as an implicit finish (`Finished: true`).

**Failure-mode: tool-call syntax as text** (`DetectToolCallSyntaxInContent`):

- The detector (`toolCallSyntaxRe` regex) matches a fenced code block at the start of a line whose language tag looks like a c0wrk tool name (`` ```\w+_\w+ `` — e.g. `` ```bash_exec ``, `` ```read_file ``).
- This signals the model is stuck: it "printed" a tool invocation as prose instead of emitting a `tool_use` block. This is NOT a legitimate finish.
- A dedicated nudge (`executorToolCallSyntaxNudge`) is injected up to 3 times, explaining that tools must be called via the `tool_use` mechanism, not typed as text.
- After 3 failed nudges, the executor aborts with `Finished: false` and an "Aborted: model repeatedly printed tool-call syntax as text" output. This triggers reflection/replan or step failure — never a silent success.

**Defense-in-depth (subagent)**:

`RunSubAgent` computes `success = err == nil && result.Finished`. As a backup guard, if `DetectToolCallSyntaxInContent(result.Output)` is true even when `Finished` is true, `success` is forced to false. This catches any escape from the executor's failure-mode detector.

Source: `sdk/agent/executor.go` `DetectToolCallSyntaxInContent` + `executorToolCallSyntaxNudge`, `sdk/agent/executor_run.go` `handleImplicitFinish`, `sdk/agent/subagent.go` `RunSubAgent`.

### Critical Always-Allowed Tools

These tools are always available regardless of step's AllowedTools filter:

- `batch` — execute multiple tool calls sequentially in one turn (intercepted at executor level)
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

- Full result is stored in `ToolResultCache` keyed by SHA256(toolName + "\x00" + content) (null-byte separator prevents collisions)
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
- Non-cacheable tools (`batch`, `tool_result_read`, `finish`, `read_step_output`, `list_step_outputs`, `store_fact`, `search_facts`, `set_step_status`, `ask_user`): excluded from both caching and Stage 1 truncation.

**Cache TTL:** `toolResultBudget.cacheTTLSeconds` (default: 300). Eviction runs on access.

### Batch Tool Interception

The `batch` meta-tool is intercepted at the executor level in `processBatchTool()` before it reaches the tool registry. Flow:

```
processBatchTool(ctx, action, callIdx, toolCalls, resp, thought, state, cw)
│
├─ Parse batch input: { calls: [{ tool, input }, ...] }
├─ For each sub-call (subIdx):
│   │
│   ├─ Emit ToolCall as "<tool_name> (batched)"
│   ├─ Circuit breaker: checkRepeatIdenticalTool, checkFruitlessResult, checkSameToolRepetition
│   ├─ HITL: OnToolCall(ctx, name, input) — consumer may deny or modify input
│   │      → Denied: inject rejection observation, skip to next sub-call
│   │      → Modified input: use modified input for execution
│   ├─ Execute sub-call through full policy pipeline: e.tools.Execute(ctx, name, input)
│   ├─ Set IsUntrusted via e.tools.IsToolUntrusted(name)
│   ├─ Stage 1: Per-tool truncation + ToolResultCache.Store() + fragmentation nudge
│   ├─ Stage 2: Token-budget truncation (preserves Stage 1 nudge)
│   ├─ Emit ToolResult
│   └─ Pre-compaction nudge (on last sub-call only)
│
└─ Return actionNone (continue loop)
```

Key behaviors:
- Errors in individual sub-calls do not abort the batch — the error is captured as the tool result and processing continues
- Sub-calls go through the full policy + truncation + caching pipeline (same as standalone tool calls)
- HITL OnToolCall check runs before each sub-call; denied sub-calls inject a rejection observation and continue to the next sub-call without aborting the batch
- Circuit breakers (repeat, fruitless, same-tool) are checked per sub-call; if triggered, the batch aborts and returns the circuit breaker action
- Each sub-call is emitted to the frontend with the suffix `" (batched)"` (e.g., `read_file (batched)`)
- Only the first sub-call in the first response group carries `Thought` and `ReasoningContent`
- The `batch` tool itself is in the `nonCacheableTools` set — its result is never cached, and the tool never reaches the registry's `Execute()` path (its `Execute()` method returns an error)

## Error Handling

- Step failure (executor returns error): result stored with Error field set
- Context cancelled: propagates immediately, no retry
- Step limit reached without finish: treated as incomplete (not failure)

## Invariants

- `finish` tool is ALWAYS available in every step (never filtered out)
- When `mutationRequired` is set, finish without a prior mutating tool execution is rejected (nudge → `Finished: false`)
- Mutation gate only applies to coder steps with `domain == "code"`; researcher/tester/audit steps are unaffected
- Context window never exceeds model limit (compaction triggers automatically)
- MaxSteps bounds total iterations (HITLHandler.OnStepLimit may extend)
- Each step has its own ContextManager (isolated memory)
- Parallel steps run in separate goroutines with independent contexts
- A step's output is immutable once stored on Blackboard
- Active skill bodies are rendered verbatim in the step system prompt (no truncation)
- Step-local skill narrowing fires whenever `step.Profile.Skills` is non-empty; requested names resolve from the task-scope ActiveSkills pool first, falling back to the SkillManager only for names absent from the pool
- Every `Step` carries `IsUntrusted`; set by executor after tool execution via `tool.IsUntrusted()` or MCP source check
- Untrusted tool output is wrapped in `<untrusted-content>` XML tags in `context.go` `buildStepMessages()` before messages are sent to the LLM
- Every tool result from a cacheable tool is stored in ToolResultCache before truncation; the cache entry key is SHA256(toolName + "\x00" + full content)
- `tool_result_read` validates cache coherence on every read: for file tools, it compares current file mtime+size with the cached signature; for MCP tools, it checks TTL expiry
- `batch` is intercepted in `processBatchTool()` before reaching the registry; its own `Execute()` returns an error. Sub-calls within a batch go through the full policy + truncation + caching pipeline and are emitted as `"<tool_name> (batched)"`
- The `batch` tool is marked in `nonCacheableTools`; its sub-calls are cached individually per normal tool rules
- `batch` sub-call errors do not abort the batch — errors are captured inline and processing continues to the next sub-call
- The general implicit-finish nudge budget is 2 attempts before accepting `Finished: true`
- `DetectToolCallSyntaxInContent` catches failure-mode where the model prints tool-call syntax (`` ```\w+_\w+ ``) as text; 3 dedicated nudges are injected before aborting with `Finished: false`
- Subagent success requires `result.Finished && !DetectToolCallSyntaxInContent(result.Output)` (defense-in-depth: even if the executor returns Finished=true with tool-call syntax in output, the subagent treats it as failure)

## Related Specs

- [README.md](README.md) — orchestration overview
- [planner.md](planner.md) — plan generation
- [../memory/compaction.md](../memory/compaction.md) — compaction strategies
- [../tool-system/README.md](../tool-system/README.md) — tool execution pipeline and trust classification
- [../../architecture/security-model.md](../../architecture/security-model.md) — policy enforcement and injection defense
