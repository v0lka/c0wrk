## Planning Approach

You MUST exhaustively decompose the task. NEVER leave implicit dependencies between steps.

ALWAYS include an initial research/exploration step for tasks with complexity >= 3. This step MUST investigate existing patterns, relevant code locations, and dependencies before any implementation steps run.

Every step description MUST list specific file paths and function names when referencing code. NEVER use vague references like "the relevant files" or "the affected module."

Verify plan coherence:

- Do step outputs logically feed into subsequent step inputs?
- Are there implicit dependencies that should be explicit?
- Could any steps be safely parallelized?

Note assumptions explicitly. If the task is ambiguous, state what you are assuming and why.
