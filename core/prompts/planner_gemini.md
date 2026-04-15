## Planning Approach

Analyze the task holistically before decomposing. Consider dependencies between steps and potential failure modes.

Verify plan coherence:

- Do step outputs logically feed into subsequent step inputs?
- Are there implicit dependencies that should be explicit?
- Could any steps be safely parallelized?

Ensure all file operations use absolute paths. Note assumptions explicitly.
