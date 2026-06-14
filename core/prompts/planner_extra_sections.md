## Step Description Format

Each step MUST include two text fields:

- **summary**: A condensed label of 5-7 words STRICTLY. Used only for UI display. Must capture the essence of the step. MUST NOT be empty.
- **description**: A detailed specification well-formatted with Markdown and following the "What-How-Where and Acceptance Criteria" structure:
  - What: What needs to be done in this step.
  - How: The approach, techniques, patterns, or algorithms to use.
  - Where: Specific files, functions, modules, or components involved.
  - Acceptance Criteria: Concrete, verifiable conditions that must be satisfied for this step to be considered complete. Each criterion should be testable.

Example:
  "summary": "Add JWT auth middleware",
  "description": "## Add JWT auth middleware\n### What:\nImplement JWT-based authentication middleware for all protected API endpoints.\n### How:\nCreate a middleware function that extracts and validates JWT tokens from the Authorization header using the existing auth package. Use RS256 signature verification.\n### Where:\nbackend/middleware/auth.go (new file), backend/routes/api.go (wire middleware)\n### Acceptance Criteria:\n- All protected endpoints return 401 for missing or invalid tokens\n- Valid tokens allow request processing with user context\n- Token expiration is properly handled with appropriate error messages"

## Output Expectations

- "researcher" / "tester": Pass all results through the finish tool. Write files ONLY for final deliverables.
- "coder": Write code/config files as needed. Summarize what was done through finish.
- "executor": Files only when the file IS the deliverable.

## Research Task Decomposition

Separate research from writing — never combine reading many files with producing final deliverables in the same step. Use store_fact aggressively during research: earlier tool outputs are pruned from context, so persist findings immediately. The final synthesis step must depend on all research steps and use search_facts to retrieve results.

## Parallelization

Steps are parallelizable when they have NO data dependencies — step B can run in parallel with step A only if B does not need A's output. If B needs A's output, B MUST list A in depends_on.

## Fields

- `estimated_tools`: Informational hint about likely tools. Not a constraint — the executor may use any available tool.
- `profile.allowed_tools`: REQUIRED for multi-step plans. An explicit list of tool names the executor is permitted to use for this step. When building this list:
  * Scan ALL tools in the Available Tools section above — both built-in and MCP
  * Include every tool potentially useful for the step's What/How/Where activities
  * Be generous: tool overlap is beneficial (e.g., include both `semantic_search` and its MCP equivalents)
  * Do NOT include `finish`, `store_fact`, `search_facts`, `ask_user`, `set_step_status`, `read_step_output`, or `tool_result_read` — these critical infrastructure tools are automatically added by the system
  * Omit this field only when the step genuinely needs every available tool (rare; only for unbounded exploration tasks)
Step executors follow MCP-first tool priority: prefer MCP tools over built-in equivalents. Built-in tools are fallback. `bash_exec` is last resort. When writing step descriptions, direct executors to use project-specific MCP tools first, then built-in code exploration, then targeted search, then file operations.
