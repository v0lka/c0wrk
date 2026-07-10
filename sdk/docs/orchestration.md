# Orchestration

The `orchestration` package provides the primitives that turn a single ReAct agent loop into a multi-step, self-correcting execution engine. It defines the shared state container (the **Blackboard**), the plan data model (a DAG of steps), the single-loop executor (**Conductor**), DAG traversal utilities, and a persistence layer for checkpointing state across restarts.

This document covers every exported type and function in the package. For the planning component that produces DAGs, see [planner.md](planner.md); for the self-correction component, see [reflector.md](reflector.md).

```go
import "github.com/v0lka/sp4rk/orchestration"
```

---

## Table of contents

- [Conductor](#conductor)
  - [ConductorConfig](#conductorconfig)
  - [NewConductor](#newconductor)
  - [Run](#run)
  - [Cleanup](#cleanup)
  - [SetReasoningEffort](#setreasoningeffort)
  - [WithDelegationRegistry](#withdelegationregistry)
- [Interfaces](#interfaces)
  - [Planner](#planner)
  - [Reflector](#reflector)
  - [Events](#events)
  - [SystemPromptFactory](#systempromptfactory)
  - [ContextManagerFactory](#contextmanagerfactory)
  - [PruningOverride](#pruningoverride)
  - [StepScopable and RetryScopable](#stepscopable-and-retryscopable)
- [Blackboard](#blackboard)
  - [Blackboard interface](#blackboard-interface)
  - [MapBlackboard](#mapblackboard)
  - [Fact memory](#fact-memory)
- [Plan data model](#plan-data-model)
  - [Plan](#plan)
  - [PlanStep](#planstep)
  - [CompletedStep](#completedstep)
  - [StepResult](#stepresult)
  - [PlanStepEvent](#planstepevent)
  - [BlackboardEntry](#blackboardentry)
- [Execution outcome](#execution-outcome)
  - [ExecutionStatus](#executionstatus)
  - [ExecutionResult](#executionresult)
  - [ErrExecutionIncomplete](#errexecutionincomplete)
- [Reflection](#reflection)
- [Fact](#fact)
- [DAG utilities](#dag-utilities)
  - [FindReadySteps](#findreadysteps)
  - [BuildCarryForward](#buildcarryforward)
  - [BuildPlanExecutionSteps](#buildplanexecutionsteps)
  - [AggregateOutput](#aggregateoutput)
- [Persistence](#persistence)
  - [Checkpointer](#checkpointer)
  - [CheckpointedBlackboard](#checkpointedblackboard)
  - [RestoreBlackboard](#restoreblackboard)
- [Adapters](#adapters)
- [Complete Plan & Execute example](#complete-plan--execute-example)

---

## Conductor

The `Conductor` is the SDK-level primitive that runs a **single ReAct loop** owning one task end-to-end. It wraps the lower-level `agent.Executor` and wires in the blackboard-backed stores (step outputs, facts, final result) so that built-in tools such as `read_step_output`, `store_fact`, `search_facts`, and `read_final_result` can access shared state during execution.

A Conductor is reusable across steps: the system prompt factory receives the step description at `Run` time, so the same instance adapts to each step it executes.

```go
type Conductor struct {
    cfg ConductorConfig
}
```

### ConductorConfig

`ConductorConfig` holds every dependency a Conductor needs. All fields are populated by the caller before `NewConductor`.

```go
type ConductorConfig struct {
    LLM               agent.LLMCaller
    Tools             agent.ToolExecutor
    ToolRegistry      *tools.ToolRegistry
    TokenCounter      llm.TokenCounter
    Model             string
    ModelRegistry     *llm.ModelRegistry
    ContextFactory    ContextManagerFactory
    SystemPrompt      SystemPromptFactory
    MaxSteps          int
    ToolResultBudget  agent.ToolResultBudget
    CircuitBreaker    agent.CircuitBreakerConfig
    HITLHandler       agent.HITLHandler
    ToolCache         *agent.ToolResultCache
    PerToolTruncation map[string]agent.ToolTruncationConfig
    ReasoningEffort   string
    PreWarningPercent int
    NonCacheableTools []string
    ConversationHistory []llm.Message
}
```

| Field | Purpose |
| --- | --- |
| `LLM` | The LLM caller used by the underlying executor. |
| `Tools` | The tool executor that dispatches tool calls. |
| `ToolRegistry` | Registry of available tool descriptors. |
| `TokenCounter` | Counts tokens for context-window management. |
| `Model` | Active model name; resolved against `ModelRegistry` for metadata. |
| `ModelRegistry` | Resolves model metadata (context window, output limit, tokenizer). When the model is unknown, a usable fallback (`ContextWindow=128000`, `OutputLimit=4096`) is applied so compaction still works. |
| `ContextFactory` | **Required.** Creates a `ContextManager` for the run. |
| `SystemPrompt` | **Required.** Factory that builds the system prompt from the step description and model metadata. |
| `MaxSteps` | Per-run ReAct step budget. Defaults to `80` when zero. |
| `ToolResultBudget` | Limits how much tool-result text is retained in context. |
| `CircuitBreaker` | Detects repetitive/looping tool calls and aborts. |
| `HITLHandler` | Human-in-the-loop handler invoked on tool calls / step-limit extensions. |
| `ToolCache` | Optional cache for tool results, keyed by tool name + arguments. |
| `PerToolTruncation` | Per-tool Stage 1 truncation configuration. |
| `ReasoningEffort` | Reasoning effort passed to reasoning-capable models (e.g. `"low"`, `"medium"`, `"high"`). |
| `PreWarningPercent` | Context-fill percentage that triggers a pre-compaction `store_fact` nudge. `0` disables it. |
| `NonCacheableTools` | Additional tool names whose results must not be cached (e.g. meta-tools whose output is inherently volatile). Extends the SDK-provided defaults. |
| `ConversationHistory` | Prior user/assistant exchanges from the session. When non-empty, the Conductor injects it into the `ContextManager` so the LLM sees the dialogue leading up to the current message. Without this, a follow-up like "implement variant a" has no referent. |

### NewConductor

```go
func NewConductor(cfg ConductorConfig) *Conductor
```

Creates a Conductor from the given config. If `MaxSteps` is zero it defaults to `80`.

```go
conductor := orchestration.NewConductor(orchestration.ConductorConfig{
    LLM:            router,
    Tools:          registry,
    ToolRegistry:   registry,
    TokenCounter:   llm.NewSimpleTokenCounter(),
    Model:          "claude-sonnet-4-5",
    ModelRegistry:  modelRegistry,
    ContextFactory: contextFactory,
    SystemPrompt:   systemPromptFactory,
    MaxSteps:       15,
})
defer conductor.Cleanup()
```

### Run

```go
func (c *Conductor) Run(
    ctx context.Context,
    message string,
    bb Blackboard,
    availableTools []tools.ToolDescriptor,
    events agent.Events,
    compactionStrategy string,
) (*ExecutionResult, error)
```

Launches the Conductor's single ReAct loop for one task.

**Parameters**

| Parameter | Description |
| --- | --- |
| `ctx` | Context. The caller may inject a `PendingDelegations` registry via `WithDelegationRegistry` to enable the finish-join guard. |
| `message` | The task description for this run (typically a plan step's `Description`). |
| `bb` | The blackboard holding shared task state. The Conductor injects `StepOutputStore`, `FactStore`, and `FinalResultStore` adapters derived from it into `ctx`. |
| `availableTools` | Tool descriptors the executor may call. |
| `events` | Event sink for observing the ReAct loop. May be `nil`; a `NoopEvents` instance is used in that case. |
| `compactionStrategy` | Context compaction strategy: `"sliding_window"`, `"summarization"`, or `"hierarchical"`. Empty defaults to `"sliding_window"`. |

**Returns**

An `*ExecutionResult` whose `Status` is `ExecutionStatusSuccess`, `ExecutionStatusPartial` (the loop ended without finishing), or `ExecutionStatusFailed` (an error occurred). The returned `Blackboard` is the same instance passed in, now populated with any reflections recorded during the run.

`Run` returns an error only when the context factory or system prompt factory is missing, or when the underlying executor returns an error. A non-nil error is still accompanied by a non-nil `*ExecutionResult` carrying best-effort output.

```go
result, err := conductor.Run(ctx, step.Description, bb, availableTools, events, "sliding_window")
if err != nil {
    log.Printf("run error: %v (status=%s)", err, result.Status)
}
if result.Status == orchestration.ExecutionStatusSuccess {
    fmt.Println(result.Output)
}
```

### Cleanup

```go
func (c *Conductor) Cleanup()
```

Releases resources held by the Conductor. Currently a no-op; per-step dump cleanup is owned by the session layer. Safe to call via `defer`.

### SetReasoningEffort

```go
func (c *Conductor) SetReasoningEffort(effort string)
```

Updates the reasoning effort applied to subsequent runs. Useful when the same Conductor is reused across phases that benefit from different reasoning depths.

### WithDelegationRegistry

```go
func WithDelegationRegistry(ctx context.Context, reg PendingDelegations) context.Context
```

Injects a `PendingDelegations` implementation into the context. The Conductor's finish-join guard checks it before allowing a `finish` call: if any async delegations are still pending, `finish` is rejected with a nudge listing them. This prevents the Conductor from silently abandoning background work.

`PendingDelegations` is a minimal interface the SDK defines to avoid a circular dependency with higher layers:

```go
type PendingDelegations interface {
    ListPending() []string
}
```

```go
ctx = orchestration.WithDelegationRegistry(ctx, myRegistry)
result, err := conductor.Run(ctx, task, bb, tools, events, "sliding_window")
```

---

## Interfaces

### Planner

```go
type Planner interface {
    Plan(ctx context.Context, task string, availableTools []tools.ToolDescriptor, reflections []Reflection, availableSkills []skills.SkillDescriptor, singleStep bool, conversationHistory []llm.Message) (*Plan, error)
    Replan(ctx context.Context, originalPlan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, sessionReflections []Reflection, availableSkills []skills.SkillDescriptor) (*Plan, error)
    PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor, availableSkills []skills.SkillDescriptor, singleStep bool, conversationHistory []llm.Message, taskComplete bool) (*Plan, error)
}
```

Generates and regenerates DAG execution plans. The reference implementation `planner.Planner` satisfies this interface (verified by a compile-time `var _ orchestration.Planner = (*Planner)(nil)` check in the `planner` package). See [planner.md](planner.md) for the reference implementation.

### Reflector

```go
type Reflector interface {
    Reflect(ctx context.Context, trajectory []agent.Step, plan *Plan, prevReflections []Reflection) (*Reflection, error)
}
```

Analyzes failures and produces corrective insights. See [reflector.md](reflector.md) for the reference implementation.

### Events

`Events` extends `agent.Events` with orchestration-lifecycle hooks. All methods are called by the orchestrator (not the Conductor directly) as it drives the plan.

```go
type Events interface {
    agent.Events
    OnPlanGenerated(stepCount int, steps []PlanStepEvent)
    OnStepStarted(stepID, description, summary string)
    OnStepCompleted(stepID string, success bool, duration time.Duration, errMsg string)
    OnReflected(reflection *Reflection, attempt, maxAttempts int)
    OnRetry(attempt, maxAttempts int)
    OnStepRetry(stepID string, attempt, maxAttempts int)
    OnService(content string)
    OnServiceMeta(content string, meta map[string]any)
    OnReplanFailed(err error)
    OnStepTodoUpdate(stepID string, items []agent.TodoItem)
}
```

`NoopEvents` is a no-op implementation that satisfies the interface; embed it and override only the hooks you care about:

```go
type myEvents struct {
    orchestration.NoopEvents
}

func (e *myEvents) OnStepCompleted(id string, ok bool, d time.Duration, _ string) {
    fmt.Printf("step %s done=%v in %v\n", id, ok, d)
}
```

### SystemPromptFactory

```go
type SystemPromptFactory func(ctx context.Context, stepDescription string, modelMeta llm.ModelMetadata) string
```

Builds the system prompt for a step executor. `ctx` carries the workspace path; `stepDescription` is the step's task text; `modelMeta` provides model capabilities (context window, output limit) so the prompt can adapt.

```go
systemPromptFactory := func(_ context.Context, stepDescription string, _ llm.ModelMetadata) string {
    return fmt.Sprintf(`You are a task execution agent.

## Task
%s

Use the available tools. Verify your work before calling finish.`, stepDescription)
}
```

### ContextManagerFactory

```go
type ContextManagerFactory func(
    systemPrompt string,
    modelMeta llm.ModelMetadata,
    compactionStrategy string,
    pruningOverrides ...PruningOverride,
) agent.ContextManager
```

Creates a `ContextManager` for a new task step. `pruningOverrides`, when provided, override the global pruning configuration with step-specific `KeepLastN` and `ProtectedTools` values.

### Optional ContextManager capabilities

The Conductor type-asserts the `ContextManager` returned by the factory against three named capability interfaces and uses them when implemented (the SDK's `memory.ContextWindow` implements all three):

```go
// Receives the formatted task content (the user message).
type TaskAware interface {
    SetTask(task string)
}

// Receives prior conversation messages (previous user/assistant exchanges)
// rendered before the current task content. Used when
// ConductorConfig.ConversationHistory is set.
type ConversationAware interface {
    SetPriorConversation(msgs []llm.Message)
}

// Exposes the token tracker so API-reported token corrections can be wired
// back into the context window's fill accounting.
type TrackerProvider interface {
    ContextTracker() *llm.ContextTokenTracker
}
```

A custom `ContextManager` that does not implement these interfaces still works — the corresponding features (task content injection, prior-conversation rendering, tracker correction) are simply skipped.

### PruningOverride

```go
type PruningOverride struct {
    KeepLastN      int      // 0 = use global default
    ProtectedTools []string // nil = use global default
}
```

Carries per-step overrides for tool-output pruning. Zero values mean "use the global default".

### StepScopable and RetryScopable

These optional interfaces let an `Events` implementation scope events to a specific plan step or retry attempt:

```go
type StepScopable interface {
    WithStepID(id string) Events
}

type RetryScopable interface {
    WithRetryAttempt(attempt int) Events
}
```

When the orchestrator detects that an events object implements one of these, it wraps it so downstream handlers know which step or retry attempt produced each event.

---

## Blackboard

The Blackboard is the structured, thread-safe container for all shared task state: the original request, the plan, per-step results, reflections, the final result, and keyword-tagged facts. Every step executor reads from and writes to the same blackboard instance, which is how steps communicate.

### Blackboard interface

```go
type Blackboard interface {
    GetOriginalRequest() string
    GetPlan() *Plan
    GetStepResult(stepID string) (StepResult, bool)
    GetStepSummary(stepID string) string
    GetAllStepResults() map[string]StepResult
    GetReflections() []Reflection
    GetFinalResult() string
    SetOriginalRequest(req string)
    SetPlan(plan *Plan)
    SetStepResult(stepID string, output string, err error, steps []agent.Step)
    AddReflection(r Reflection)
    SetFinalResult(result string)
    Search(query string) []BlackboardEntry

    // Fact memory for inter-step communication
    StoreFact(fact Fact)
    SearchFacts(keywords []string) []Fact
    GetFacts() []Fact
}
```

All methods are safe for concurrent use. Read methods return defensive copies, so callers can mutate returned slices without racing with the blackboard.

| Method | Behaviour |
| --- | --- |
| `GetOriginalRequest` / `SetOriginalRequest` | The original user request string. |
| `GetPlan` / `SetPlan` | The current plan. `GetPlan` returns a deep copy or `nil`. |
| `GetStepResult` | Returns the `StepResult` for a step ID and whether it exists. |
| `GetStepSummary` | Returns just the auto-generated summary for a step, or `""`. |
| `GetAllStepResults` | Defensive copy of all step results, keyed by step ID. |
| `GetReflections` | Defensive copy of all reflections, in insertion order. |
| `GetFinalResult` / `SetFinalResult` | The final result string. |
| `SetStepResult` | Records a completed step. Auto-generates a summary from `output` (first paragraph, capped at the configured max length). |
| `AddReflection` | Appends a reflection. |
| `Search` | Case-insensitive substring search across step summaries, full outputs, and reflection summaries. Returns `[]BlackboardEntry`. |
| `StoreFact` / `SearchFacts` / `GetFacts` | Fact memory — see [Fact memory](#fact-memory). |

### MapBlackboard

`MapBlackboard` is the reference thread-safe, map-backed implementation. It is the default blackboard for in-memory tasks.

```go
func NewMapBlackboard(opts ...MapBlackboardOption) *MapBlackboard
```

```go
bb := orchestration.NewMapBlackboard()
bb.SetOriginalRequest("Build a Go project that prints hello")
```

**WithMaxSummaryLen**

```go
func WithMaxSummaryLen(n int) MapBlackboardOption
```

Sets a character-based cap on auto-generated step summaries. A value of `0` uses the default of `500` characters. The summary takes the first paragraph (text up to the first double-newline) or the first `n` characters, whichever is shorter, appending `...` when truncated.

```go
bb := orchestration.NewMapBlackboard(orchestration.WithMaxSummaryLen(800))
```

`MapBlackboard` also exposes two methods used by the persistence layer to hydrate state without regenerating summaries:

- `SetStepResultRaw(stepID string, sr StepResult)` — stores a pre-built `StepResult` directly.
- `SetFacts(facts []Fact)` — replaces the entire facts slice (deep-copied).

### Fact memory

Facts are keyword-tagged pieces of information that steps write for later retrieval by other steps. This is the primary inter-step communication channel beyond explicit step outputs.

- `StoreFact(fact Fact)` appends a fact.
- `SearchFacts(keywords []string) []Fact` returns facts where at least one keyword matches (case-insensitive), **sorted by number of matching keywords descending** — most relevant first.
- `GetFacts() []Fact` returns a defensive copy of all stored facts.

```go
bb.StoreFact(orchestration.Fact{
    Keywords: []string{"auth", "middleware", "jwt"},
    Content:  "Auth middleware lives in internal/auth/middleware.go and validates JWTs.",
    Author:   "step_1",
})

matches := bb.SearchFacts([]string{"auth", "jwt"})
for _, f := range matches {
    fmt.Printf("[%s] %s\n", strings.Join(f.Keywords, ", "), f.Content)
}
```

---

## Plan data model

### Plan

```go
type Plan struct {
    Steps              []PlanStep `json:"steps"`
    ExplorationContext string     `json:"exploration_context,omitempty"`
}
```

A DAG of execution steps. `ExplorationContext` holds a summary of any codebase exploration the planner performed before producing the plan (empty for direct, one-shot planning).

### PlanStep

```go
type PlanStep struct {
    ID             string   `json:"id"`
    Summary        string   `json:"summary"`
    Description    string   `json:"description"`
    DependsOn      []string `json:"depends_on"`
    Parallelizable bool     `json:"parallelizable"`
    EstimatedTools []string `json:"estimated_tools"`
    Profile        any      `json:"profile,omitempty"`
}
```

| Field | Description |
| --- | --- |
| `ID` | Stable step identifier (e.g. `"step_1"`). Used in `DependsOn` and as the blackboard key. |
| `Summary` | Short, human-readable label (5–7 words). |
| `Description` | Full task text passed to the step executor. |
| `DependsOn` | IDs of steps that must complete successfully before this one can start. |
| `Parallelizable` | Hint that this step can run concurrently with its siblings. |
| `EstimatedTools` | Tools the step is expected to use. |
| `Profile` | Optional step-level configuration. During JSON deserialization this is `map[string]any`; consumers convert it to a domain-specific profile (e.g. `*planner.AgentProfile`). The field type is `any`. |

### CompletedStep

```go
type CompletedStep struct {
    StepID string       `json:"step_id"`
    Output string       `json:"output"`
    Error  error        `json:"-"`
    Steps  []agent.Step `json:"steps,omitempty"`
}
```

The result of an executed plan step. `Steps` holds the actual executor trajectory (tool calls + observations) when captured, which reflectors and evaluators use as evidence. `Error` is `nil` on success.

### StepResult

```go
type StepResult struct {
    StepID     string
    Summary    string
    FullOutput string
    Error      error
    Steps      []agent.Step
}
```

The blackboard's representation of a completed step. `Summary` is auto-generated from `FullOutput`; `Steps` is the executor trajectory.

### PlanStepEvent

```go
type PlanStepEvent struct {
    ID          string   `json:"id"`
    Summary     string   `json:"summary"`
    Description string   `json:"description"`
    Status      string   `json:"status"`
    DependsOn   []string `json:"depends_on"`
}
```

A step representation for event emission. `Status` is a string such as `"pending"`, `"running"`, `"completed"`, or `"failed"`.

### BlackboardEntry

```go
type BlackboardEntry struct {
    Type    string // "step_result", "reflection", etc.
    Key     string
    Summary string
}
```

A single search result from `Blackboard.Search`.

---

## Execution outcome

### ExecutionStatus

`ExecutionStatus` classifies the terminal outcome of a plan execution. It is the typed success contract: callers must consult `Status` instead of parsing output suffixes.

```go
type ExecutionStatus string

const (
    ExecutionStatusSuccess   ExecutionStatus = "success"
    ExecutionStatusPartial   ExecutionStatus = "partial"
    ExecutionStatusFailed    ExecutionStatus = "failed"
    ExecutionStatusAborted   ExecutionStatus = "aborted"
    ExecutionStatusCancelled ExecutionStatus = "cancelled"
)
```

| Status | Meaning |
| --- | --- |
| `success` | All plan steps completed without errors. |
| `partial` | Some steps were never attempted (execution incomplete); accompanies `ErrExecutionIncomplete`. |
| `failed` | All steps were attempted, some failed, and the retry budget is exhausted. |
| `aborted` | The reflector recommended aborting after step failures. |
| `cancelled` | The context was cancelled mid-execution. |

### ExecutionResult

```go
type ExecutionResult struct {
    Output       string          `json:"output"`
    Plan         *Plan           `json:"plan,omitempty"`
    Blackboard   Blackboard      `json:"-"`
    AttemptCount int             `json:"attempt_count,omitempty"`
    Reflections  []Reflection    `json:"reflections,omitempty"`
    Status       ExecutionStatus `json:"status,omitempty"`
    FailedSteps  int             `json:"failed_steps,omitempty"`
}
```

| Field | Description |
| --- | --- |
| `Output` | Aggregated or best-effort output text. |
| `Plan` | The plan that was executed (when applicable). |
| `Blackboard` | The blackboard instance (not JSON-serialized). |
| `AttemptCount` | Number of execution attempts. |
| `Reflections` | Reflections recorded during execution. |
| `Status` | Terminal status — see [ExecutionStatus](#executionstatus). |
| `FailedSteps` | Steps that finished with an error in the final attempt. |

### ErrExecutionIncomplete

```go
var ErrExecutionIncomplete = errors.New("plan execution incomplete")
```

Indicates a plan execution ended before all steps completed (e.g. step limit reached, context cancelled). It accompanies a non-nil `*ExecutionResult` carrying best-effort output. Callers should `errors.Is`-check it and use the returned result for partial output.

```go
result, err := orchestrator.Execute(ctx, plan, bb, tools, events)
if errors.Is(err, orchestration.ErrExecutionIncomplete) {
    log.Printf("partial completion: %d/%d steps", len(result.Blackboard.GetAllStepResults()), len(plan.Steps))
}
```

---

## Reflection

```go
type Reflection struct {
    Summary         string    `json:"summary"`
    Hypotheses      []string  `json:"hypotheses"`
    SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
    Reasoning       string    `json:"reasoning"`
    FailureAnalysis string    `json:"failure_analysis"`
    RootCause       string    `json:"root_cause"`
    ActionPlan      string    `json:"action_plan"`
    Timestamp       time.Time `json:"timestamp"`
}
```

The structured result of failure analysis. `SuggestedAction` drives the orchestrator's recovery decision:

| Value | When to use |
| --- | --- |
| `"retry"` | Try the step again with adjustments informed by the reflection. |
| `"replan"` | The plan itself is wrong; generate a new plan via `Planner.Replan`. |
| `"abort"` | The failure is unrecoverable; stop execution. |

See [reflector.md](reflector.md) for how reflections are produced.

---

## Fact

```go
type Fact struct {
    Keywords []string `json:"keywords"` // 3-5 keywords for retrieval
    Content  string   `json:"content"`  // the fact text
    Author   string   `json:"author"`   // step ID that wrote it
}
```

A keyword-tagged piece of information for inter-step communication. Steps store facts via `Blackboard.StoreFact` and retrieve them via `SearchFacts`/`GetFacts`. The `Author` field records which step wrote the fact, aiding traceability. Keywords (3–5 recommended) drive relevance ranking in `SearchFacts`.

---

## DAG utilities

The package provides pure functions for traversing and aggregating plan DAGs. They are the building blocks an orchestrator uses to drive execution.

### FindReadySteps

```go
func FindReadySteps(plan *Plan, completed map[string]CompletedStep) []PlanStep
```

Returns plan steps whose dependencies are **all completed successfully**. Steps that are already completed, or that depend on a failed step, are excluded. Returns `nil` if `plan` is nil.

```go
for {
    ready := orchestration.FindReadySteps(plan, completed)
    if len(ready) == 0 {
        break // all done or blocked by failures
    }
    for _, step := range ready {
        // execute step...
    }
}
```

### BuildCarryForward

```go
func BuildCarryForward(completed []CompletedStep, newPlan *Plan) map[string]CompletedStep
```

Maps previously completed step outputs onto a new (replanned) plan. A step is a carry-forward candidate when its ID appears in the new plan **and** it completed without error. Steps whose dependencies (in the new plan) include a step that is **not** carried forward are transitively excluded — a replanned step invalidates all its downstream dependents. Returns `nil` if no steps can be preserved, signalling that full re-execution is required.

```go
carried := orchestration.BuildCarryForward(completedSteps, newPlan)
// `carried` can be seeded into the `completed` map before executing newPlan
```

### BuildPlanExecutionSteps

```go
func BuildPlanExecutionSteps(completedList []CompletedStep, plan *Plan) []agent.Step
```

Converts completed plan steps into an execution trajectory (`[]agent.Step`) for reflectors and evaluators. When a `CompletedStep` carries the actual executor steps (tool calls + observations), those are used directly so the evaluator sees real evidence. Otherwise a fallback summary step is constructed from the completion output.

### AggregateOutput

```go
func AggregateOutput(completedSteps map[string]CompletedStep, plan *Plan, preCompletedIDs map[string]bool) string
```

Combines outputs from **terminal steps** — steps that no other step depends on. If no terminal outputs exist, all step outputs are collected instead. `preCompletedIDs`, when non-nil, lists step IDs that were pre-completed from a previous turn's blackboard; these are excluded so continuation messages return only newly produced output.

```go
finalOutput := orchestration.AggregateOutput(completed, plan, nil)
```

---

## Persistence

The persistence layer lets you checkpoint blackboard state to an external backend and restore it later — essential for surviving restarts or resuming long-running tasks.

### Checkpointer

```go
type Checkpointer interface {
    SaveCheckpoint(ctx context.Context, id string, bb Blackboard) error
    LoadCheckpoint(ctx context.Context, id string) (Blackboard, error)
    DeleteCheckpoint(ctx context.Context, id string) error
}
```

Provides persistence for blackboard state. Implementations are responsible for the serialization format and backend storage. All methods must be safe for concurrent use. `LoadCheckpoint` returns `nil, nil` when the checkpoint does not exist. When no `Checkpointer` is configured, persistence is simply disabled — the `Framework.RestoreBlackboard` method returns an error if called without one.

### CheckpointedBlackboard

`CheckpointedBlackboard` wraps a `MapBlackboard` and persists every write operation through a `Checkpointer`. Read methods delegate to the embedded `MapBlackboard`; write methods delegate **and** persist.

```go
func NewCheckpointedBlackboard(
    id string,
    cp Checkpointer,
    logger *slog.Logger,
    timeout time.Duration,
    opts ...MapBlackboardOption,
) *CheckpointedBlackboard
```

| Parameter | Description |
| --- | --- |
| `id` | Checkpoint identifier. |
| `cp` | The Checkpointer backend. |
| `logger` | Optional structured logger (`nil`-safe). |
| `timeout` | Max time for a single checkpoint write. `0` uses the default (5s). |
| `opts` | `MapBlackboardOption`s (e.g. `WithMaxSummaryLen`). |

All persistence calls are **best-effort**: errors are logged but do not propagate to callers. Persistence operations run on a single background worker goroutine with a timeout and panic recovery, so a slow or panicking backend cannot hang the agent.

**Lifecycle methods**

- `SetOnChanged(fn func(changeType string))` — optional callback invoked after every successful write. `changeType` is `"plan"`, `"step_result"`, `"fact"`, `"reflection"`, etc.
- `SetPersistContext(ctx context.Context)` — sets the context used for persistence operations (carries cancellation/tracing). Defaults to `context.Background()`.
- `Shutdown()` — closes the persistence channel and waits for the worker to finish. Safe to call multiple times. **Always call this** when the blackboard is no longer needed to prevent goroutine leaks.
- `ID() string` — returns the checkpoint identifier.

```go
cp := myCheckpointer{} // implements orchestration.Checkpointer
bb := orchestration.NewCheckpointedBlackboard("task-42", cp, logger, 5*time.Second)
defer bb.Shutdown()

bb.SetOnChanged(func(change string) {
    fmt.Println("blackboard changed:", change)
})

bb.SetPlan(plan)        // persisted
bb.SetStepResult(...)   // persisted
bb.StoreFact(...)       // persisted
```

### RestoreBlackboard

```go
func RestoreBlackboard(
    ctx context.Context,
    id string,
    cp Checkpointer,
    logger *slog.Logger,
    timeout time.Duration,
    opts ...MapBlackboardOption,
) (*CheckpointedBlackboard, error)
```

Loads a blackboard state from a Checkpointer and hydrates a fresh `CheckpointedBlackboard`. Returns `nil, nil` if the checkpoint does not exist. The restored blackboard is fully writable and will persist subsequent changes under the same `id`.

```go
bb, err := orchestration.RestoreBlackboard(ctx, "task-42", cp, logger, 5*time.Second)
if err != nil {
    return err
}
if bb == nil {
    // no checkpoint found — start fresh
    bb = orchestration.NewCheckpointedBlackboard("task-42", cp, logger, 5*time.Second)
}
defer bb.Shutdown()
```

### StepDumpTracker

```go
func NewStepDumpTracker(dir string) *StepDumpTracker
```

A best-effort manager for **per-step LLM dump files**, used when the conductor records each plan step's LLM traffic to its own file for offline debugging. Pass a directory; `NewStepDumpTracker` creates it (logging a warning, not returning an error, on failure). Pass an empty `dir` to disable the tracker.

```go
type StepDumpTracker struct{ /* unexported */ }

// OpenStepDump returns an io.Writer for the step's dump file, or nil when
// disabled/closed. The file is created on first call and cached — repeated
// calls for the same stepID (including retries) append to the same file.
func (t *StepDumpTracker) OpenStepDump(stepID string) io.Writer

// CloseAll closes every tracked file. Idempotent.
func (t *StepDumpTracker) CloseAll() error
```

Key properties:

- **Path-traversal safe** — the filename is `step_<filepath.Base(stepID)>.jsonl`, so any directory components in `stepID` are stripped and the file always lands inside `dir`.
- **Idempotent** — opening a dump for an already-seen step returns the cached `*os.File`, so retries and sub-steps append rather than overwrite.
- **Graceful degradation** — every failure (directory creation, file open, write) is swallowed with a warning; dumps are debugging aids and never affect execution.

The conductor typically feeds each step's writer into the executor context via [`agent.WithDumpWriter`](agent-executor.md#llm-debugging-callers), so each step produces a self-contained `step_<id>.jsonl` (consumable by [`NewDumpCaller`](agent-executor.md#newdumpcaller--full-jsonl-requestresponse-dumps)).

---

## Adapters

The package provides three small adapters that wrap a `Blackboard` as the `agent.*Store` interfaces consumed by built-in tools. The Conductor injects these into the context automatically, but they are also useful standalone.

```go
func NewStepOutputStore(bb Blackboard) agent.StepOutputStore
func NewFactStore(bb Blackboard) agent.FactStore
func NewFinalResultStore(bb Blackboard) agent.FinalResultStore
```

- `NewStepOutputStore` exposes successful step outputs to the `read_step_output` tool. Only steps that completed without error are visible; outputs are listed in deterministic (step-ID) order.
- `NewFactStore` exposes fact memory to the `store_fact` / `search_facts` tools.
- `NewFinalResultStore` exposes the prior task's final result to the `read_final_result` tool — useful for continuation agents when the conversation history alone is insufficient (e.g. after a restart, or when the result was too large to inject verbatim).

---

## Complete Plan & Execute example

This example shows the full Plan & Execute pattern: a `Planner` generates a DAG, a `Conductor` executes each step, and a `Reflector` provides self-correction on failure. It is adapted from the SDK's example 06.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/planner"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// trajectoryStore captures the executor's step history for reflection.
type trajectoryStore struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (s *trajectoryStore) Sync(steps []agent.Step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = steps
}

func (s *trajectoryStore) Steps() []agent.Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps
}

func run() error {
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		Execution: sp4rk.ExecutionConfig{
			MaxSteps:   15, // per-step ReAct budget
			MaxRetries: 2,  // retries per plan step on failure
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = fw.Shutdown() }()

	// Register tools.
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(agent.NewFinishTool())

	workspaceDir, err := os.MkdirTemp("", "example-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// --- Planner ---
	plannerCfg := planner.DefaultConfig()
	plannerCfg.Prompts = planner.PromptSet{
		BasePrompt: `You are a task planning agent. Break down the user's task into steps.

## Available Tools
AVAILABLE-TOOLS

Create at most MAX-STEPS steps. Use "depends_on" to chain steps.

MODE-PREAMBLE

## Output Format
Return ONLY a valid JSON object:
MODE-JSON-EXAMPLE`,
		PlanPreamble:      "Break the task into logical, sequential steps.",
		MultiStepGuidance: "Prefer fewer, well-defined steps with acceptance criteria.",
	}
	plannerCfg.Model = "claude-sonnet-4-5"

	pl, err := planner.NewPlanner(fw.LLMRouter(), plannerCfg)
	if err != nil {
		return err
	}

	// --- Reflector ---
	rf := reflector.New(fw.LLMRouter(), reflector.Config{
		SystemPrompt: `You are a reflection agent. Analyze the failed execution.
Return JSON with "summary", "root_cause", "suggested_action" (retry/replan/abort), and "action_plan".`,
	})

	// --- Conductor ---
	systemPromptFactory := func(_ context.Context, stepDescription string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a task execution agent.

## Task
%s

Use the available tools. Verify your work before calling finish.`, stepDescription)
	}
	conductor, err := fw.NewConductor(systemPromptFactory)
	if err != nil {
		return err
	}
	defer conductor.Cleanup()

	// --- The task ---
	task := fmt.Sprintf(`Create a small Go project in %s:
1. Create a directory called "myproject"
2. Write a main.go file that prints "Hello from planned agent!"
3. Read the file back to verify it was written correctly`, workspaceDir)

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	availableTools := registry.List()

	// --- Step 1: Plan ---
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(task)

	plan, err := pl.Plan(ctx, task, availableTools, nil, nil, false, nil)
	if err != nil {
		return fmt.Errorf("planning failed: %w", err)
	}
	fmt.Printf("Plan: %d steps\n", len(plan.Steps))

	// --- Step 2: Execute the DAG with retry + reflect ---
	completed := make(map[string]orchestration.CompletedStep)
	var reflections []orchestration.Reflection
	maxRetries := 2

	for {
		readySteps := orchestration.FindReadySteps(plan, completed)
		if len(readySteps) == 0 {
			break
		}

		for _, step := range readySteps {
			success := false
			for attempt := 1; attempt <= maxRetries+1; attempt++ {
				store := &trajectoryStore{}
				stepCtx := agent.WithTrajectoryStore(ctx, store)

				result, runErr := conductor.Run(
					stepCtx, step.Description, bb, availableTools,
					&agent.NoopEvents{}, "sliding_window",
				)
				trajectory := store.Steps()

				if runErr == nil && result.Status == orchestration.ExecutionStatusSuccess {
					completed[step.ID] = orchestration.CompletedStep{
						StepID: step.ID,
						Output: result.Output,
						Steps:  trajectory,
					}
					bb.SetStepResult(step.ID, result.Output, nil, trajectory)
					success = true
					break
				}

				// Failure — reflect and decide.
				if attempt <= maxRetries {
					reflection, reflectErr := rf.Reflect(stepCtx, trajectory, plan, reflections)
					if reflectErr == nil && reflection != nil {
						bb.AddReflection(*reflection)
						reflections = append(reflections, *reflection)
						fmt.Printf("Reflection: %s → %s\n",
							reflection.RootCause, reflection.SuggestedAction)
						if reflection.SuggestedAction == "abort" {
							break
						}
						// For "replan", a full implementation would call
						// pl.Replan(...) and restart with the new plan.
					}
				}
			}
			if !success {
				completed[step.ID] = orchestration.CompletedStep{
					StepID: step.ID,
					Error:  fmt.Errorf("step failed after %d attempts", maxRetries+1),
				}
			}
		}
	}

	// --- Step 3: Aggregate results ---
	finalOutput := orchestration.AggregateOutput(completed, plan, nil)
	fmt.Printf("Completed %d/%d steps, %d reflections\n",
		len(completed), len(plan.Steps), len(reflections))
	fmt.Println(finalOutput)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
```

The pattern is:

1. **Plan** — `Planner.Plan` produces a `*Plan` (a DAG of `PlanStep`s).
2. **Execute** — `FindReadySteps` selects steps whose dependencies are met; `Conductor.Run` executes each step's ReAct loop, recording results on the blackboard.
3. **Reflect** — on failure, `Reflector.Reflect` analyses the trajectory and returns a `Reflection` whose `SuggestedAction` drives retry, replan, or abort.
4. **Aggregate** — `AggregateOutput` combines terminal step outputs into the final result.
