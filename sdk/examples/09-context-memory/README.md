# Example 09 — Context Window & Memory Management

A focused look at the SDK's **memory subsystem**: how the context window fills,
how compaction reclaims space, and how to observe both in real time.

Compaction is configured as a single field in other examples (the capstone sets
`CompactionConfig.Strategy = "sliding_window"` and never triggers it). This
example makes the subsystem the focus: it configures the thresholds, references
all three strategies, and streams the `ContextFill` / `ContextCompaction` events.

This example ships in **two variants**:

| Variant     | File            | Command                 |
|-------------|-----------------|-------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` |
| **Classic** | `main.go`       | `go run .`              |

See [`docs/memory.md`](../../docs/memory.md).

## What you will learn

- `sp4rk.CompactionConfig` — the three trigger thresholds + strategy name
- The three strategies: `sliding_window`, `summarization`, `hierarchical`
- The `ContextFill` event (emitted every step: fill %, tokens used/max, status)
- The `ContextCompaction` event (emitted when space is reclaimed: before → after %)
- Per-task strategy override via `TaskBuilder.Compaction(...)` (fluent variant)

## How it works

```
every ReAct step ──► ContextWindow.CheckFill()
                        │  fill % vs thresholds → status: ok|compact|warning|emergency
                        ▼
                   emitter.ContextFill(percent, used, max, status, stepID)
                        │
            status ∈ {compact,warning,emergency}?
                        │ yes
                        ▼
                   ContextWindow.Compact(ctx)  ──► emitter.ContextCompaction(before, after)
```

The three thresholds (default → demo value):

| Threshold           | Default | Demo | Meaning                                   |
|---------------------|---------|------|-------------------------------------------|
| `PredictivePercent` | 85      | 10   | proactive compaction kicks in             |
| `WarningPercent`    | 92      | 50   | aggressive compaction                     |
| `EmergencyPercent`  | 98      | 70   | last-resort compaction to avoid rejection |

The demo lowers `PredictivePercent` and seeds several files so the fill climbs
quickly and a `ContextCompaction` event can fire on a short run. On a
large-context model at the default 85%, compaction only triggers at scale —
which is exactly why the threshold is configurable.

### The three strategies

| Strategy          | How it reclaims space                            | Needs an LLM summarizer? |
|-------------------|--------------------------------------------------|--------------------------|
| `sliding_window`  | keep first N + last M messages, drop the middle  | No                       |
| `summarization`   | LLM-summarize older blocks into compact summaries | Yes                      |
| `hierarchical`    | tiered resolution: distant/middle/recent detail  | Yes                      |

`sliding_window` works out of the box; the other two require a summarizer
dependency wired into the Framework. Tool-output pruning (keeping only the last
N results, protecting named tools) runs alongside every strategy.

## Run it

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
cd sdk/examples/09-context-memory
go run -tags fluent .   # or: go run .
```

Each step prints a `📊 Context` line; when the fill crosses the predictive
threshold, a `♻️ Compaction` line shows the reclaimed percentage.
