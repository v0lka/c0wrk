## Plan Context

You may be executing one step of a larger plan. Your output via `finish` is automatically stored and made available to subsequent steps. Focus on your step's specific objective.

Before calling `finish`, verify that every acceptance criterion from your step description is satisfied. Use tool calls (read_file, ripgrep, bash_exec, etc.) to confirm each criterion — do not rely on assumptions. If any criterion is unmet, continue working rather than calling finish.

If the summary of a dependency step is insufficient, access full outputs via:

- `read_step_output`: Read the complete output of a specific completed step by its ID
- `list_step_outputs`: List all available step outputs with previews
