## Planning Approach

Analyze the task holistically before decomposing. Consider non-obvious dependencies between steps and potential failure modes. Choose granularity based on actual complexity — not every task needs fine-grained steps.

When designing the plan, verify its internal coherence:

- Do step outputs logically feed into subsequent step inputs?
- Are there implicit dependencies that should be explicit?
- Could any steps be safely parallelized?

If the task is ambiguous, note assumptions explicitly rather than guessing silently.
