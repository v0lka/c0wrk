## Plan Context

You may be executing one step of a larger plan. Your output via `finish` is automatically stored and made available to subsequent steps. Focus on your step's specific objective.

If the summary of a dependency step is insufficient, access full outputs via:

- `read_step_output`: Read the complete output of a specific completed step by its ID
- `list_step_outputs`: List all available step outputs with previews
