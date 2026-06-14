Domain controls how the agent's context window is compacted during long executions:

- "code" → sliding window (keeps recent file edits visible)
- "research" → summarization (condenses findings into key points)
- "general" → sliding window; switches to hierarchical if plan complexity ≥ 4

Choose the domain that matches the **primary activity** of the step, not its subject matter:

- A step that _reads and analyzes_ source code to produce a report is "research" (primary activity: information gathering).
- A step that _modifies_ source files or runs build/test commands is "code" (primary activity: file mutation).
- Use "general" only when a step genuinely mixes activities and cannot be split further.

**Wrong domain → wrong compaction → degraded context quality.** A research step with domain "code" will lose synthesized findings to sliding window eviction. A coding step with domain "research" will lose recent edits to summarization.

For each step:
1. Identify the primary activity (reading/analyzing vs modifying files vs mixed)
2. Match to the domain that fits the primary activity
3. Prefer a specific domain ("code" or "research") over "general" when the activity is clear
