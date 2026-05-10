# Blackboard

## Role

Provides shared state for the Plan&Execute loop: stores the plan, step results, reflections, facts, and file changes. Enables inter-step communication and task persistence.

## Key Files

- `sdk/orchestration/blackboard.go` — MapBlackboard (in-memory implementation)
- `sdk/orchestration/interfaces.go` — Blackboard interface definition
- `core/persistent_blackboard.go` — PersistentBlackboard (SQLite-backed)

## Behavior

### Blackboard Interface

```go
type Blackboard interface {
    // Request & Plan
    GetOriginalRequest() string
    SetOriginalRequest(req string)
    GetPlan() *Plan
    SetPlan(plan *Plan)

    // Step Results
    GetStepResult(stepID string) (StepResult, bool)
    GetStepSummary(stepID string) string
    GetAllStepResults() map[string]StepResult
    SetStepResult(stepID, output string, err error, steps []agent.Step)

    // Reflections
    GetReflections() []Reflection
    AddReflection(r Reflection)

    // Final Result
    GetFinalResult() string
    SetFinalResult(result string)

    // File Changes
    SetStepFileChanges(stepID string, changes []FileChange)
    GetStepFileChanges(stepID string) []FileChange
    GetAllFileChanges() map[string][]FileChange
    GetSessionFileChanges() []FileChange  // unique paths across all steps

    // Fact Memory (inter-step communication)
    StoreFact(fact Fact)
    SearchFacts(keywords []string) []Fact

    // Full-text search
    Search(query string) []BlackboardEntry
}
```

### Fact Memory

Facts are the primary inter-step communication mechanism:

```
Step A (researcher):
  → Discovers important insight
  → Calls store_fact tool
  → Fact stored on blackboard

Step B (coder, depends on A):
  → Calls search_facts tool
  → Retrieves facts from A
  → Uses insights to implement
```

Fact structure:

```go
type Fact struct {
    Keywords []string
    Content  string
    Author   string  // step ID that stored it
}
```

### PersistentBlackboard

Wraps `MapBlackboard` with SQLite persistence:

- Automatically saves state on `SetStepResult`, `StoreFact`, `SetFinalResult`
- Supports `RestoreBlackboard()` for task resumption
- Tracks task lifecycle: `ReactivateTask()`, `CompleteTask()`, `FailTask()`
- Emits warnings via `Emitter` if persistence fails (non-fatal)

### Step Results

```go
type StepResult struct {
    FullOutput string       // finish tool output
    Summary    string       // auto-generated summary (first N chars or LLM-generated)
    Error      error        // nil on success
    Steps      []agent.Step // full ReAct trajectory
}
```

Step results enable:

1. Dependency context injection (downstream steps read upstream outputs)
2. Replan (Planner sees what already succeeded)
3. File rollback (steps track which files were modified)

## Error Handling

- Persistence failure → logged + emitted as service warning, execution continues
- Missing step result for dependency → empty context (not an error)
- Fact search with no matches → empty slice returned

## Invariants

- Blackboard is created once per task (not shared across tasks)
- All Blackboard methods are safe for concurrent use (sync.RWMutex in MapBlackboard)
- Step results are immutable once written (no overwrite)
- Facts accumulate monotonically (no deletion during task)
- PersistentBlackboard persists synchronously on each write
- RestoreBlackboard recreates full in-memory state from SQLite

## Related Specs

- [README.md](README.md) — memory overview
- [../orchestration/executor.md](../orchestration/executor.md) — how executor writes results
- [../orchestration/planner.md](../orchestration/planner.md) — how planner reads state for replan
- [../../architecture/data-flow.md](../../architecture/data-flow.md) — blackboard flow diagram
