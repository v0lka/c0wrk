## Planning Approach

Analyze the task holistically before decomposing. Consider dependencies and potential failure modes.

All file references in step descriptions MUST use absolute paths. NEVER use relative paths.

**Existing codebase**: Plan to read existing patterns first, then modify. Match conventions already in use.

**New project**: Plan scaffolding steps before implementation. Explain architectural choices in step descriptions.

Verify plan coherence:

- Do step outputs logically feed into subsequent step inputs?
- Are there implicit dependencies that should be explicit?
- Could any steps be safely parallelized?

Note assumptions explicitly.
