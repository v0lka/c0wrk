# Security Model

## Context

c0wrk executes arbitrary tools (filesystem operations, shell commands, web requests) on behalf of an LLM. The security model gates tool execution to prevent unintended destructive actions while keeping the agent productive.

## Policy Resolution

When a tool is about to execute, its effective policy is resolved in this order (first match wins):

```
1. Per-tool config override     (config.yaml Security.ToolPolicies["tool_name"])
2. Skill policy override        (from active skill's policy declarations)
3. Registry default policy      (config.yaml Security.DefaultPolicy)
4. Tool's own default           (Tool.DefaultPolicy() method)
```

Source: `core/tools/registry.go` `resolvePolicy()`

## Tool Policies

| Policy         | Behavior                                                                                               |
| -------------- | ------------------------------------------------------------------------------------------------------ |
| `always_allow` | Execute immediately. No confirmation, no judge (unless tool implements ToolJudger and flags the call). |
| `user_confirm` | Block execution, send confirmation request to frontend. User must allow or deny.                       |
| `always_deny`  | Immediately return error result. Tool is never executed.                                               |

Default if nothing configured: `user_confirm` (safest default).

## Internal Tools

These tools bypass ALL policy checks, judge evaluation, and confirmation flow:

- `ask_user` — prompts the user for information
- `finish` — signals task completion
- `list_step_outputs` — reads completed step metadata
- `read_skill_resource` — reads a resource file from an activated skill
- `read_step_output` — reads a specific step's output
- `search_facts` — searches stored facts by keywords
- `semantic_search` — searches the project codebase by semantic similarity
- `set_step_status` — updates the to-do checklist for the current step
- `store_fact` — stores a fact for later retrieval

Source: `core/tools/registry.go` `internalTools` map

Rationale: these tools are agent-infrastructure, not user-facing operations. Blocking them would break the execution loop.

## Workspace Auto-Approval

Before applying the resolved policy, the registry checks if ALL file paths in the tool input fall within either:

1. The session's temporary directory (`TempDirFrom(ctx)`)
2. The current workspace directory

If yes AND policy is NOT `always_deny`: tool executes without confirmation.

Rationale: operations within the user's project workspace are the normal working mode. Requiring confirmation for every file read/write within the project would be unusable.

Important: `always_deny` is NEVER bypassed by auto-approval.

## Judge System

The `ToolJudge` (`core/tools/judge.go`) provides LLM-based safety evaluation:

- NOT automatic gating — it is invoked on-demand via the frontend "Ask agent" button
- When a tool has `PolicyAlwaysAllow` but implements the `ToolJudger` interface, the tool-specific judge may flag suspicious calls and escalate to user confirmation
- The judge provides reasoning that is displayed to the user in the confirmation dialog

## Confirmation Flow

```
ToolRegistry.Execute()
  → policy = UserConfirm (or judge-escalated)
  │
  ├─ confirmFunc(ctx, ConfirmationRequest{ToolName, Input, JudgeReasoning})
  │   │
  │   ▼
  │ backend: stores in pendingConfirmations sync.Map
  │   │
  │   ▼
  │ frontend: receives tool_confirm event, renders decision UI
  │   │
  │   ▼
  │ user clicks: Allow / Deny / Deny & Stop
  │   │
  │   ▼
  │ frontend emits response → backend resolves channel
  │
  ├─ ConfirmAllowOnce → execute tool
  ├─ ConfirmDeny → return error ToolResult (agent sees denial)
  └─ ConfirmDenyAndStop → return context.Canceled (stops entire task)
```

## Bash Blacklist

The `bash_exec` tool has a regex-based blacklist (`config.yaml Security.BashBlacklist`) that blocks dangerous command patterns (e.g., `rm -rf /`, `chmod 777`, `curl | sh`). Blacklisted commands are denied before policy resolution.

## Invariants

- Internal tools ALWAYS execute, regardless of any policy configuration
- `always_deny` is NEVER bypassed (not by auto-approval, not by judge, not by any mechanism)
- Workspace auto-approval only applies when ALL paths in the input are within workspace/temp
- Confirmation blocks the executor goroutine until the user responds (no timeout)
- A denied tool returns an error ToolResult to the LLM (agent can adapt its strategy)
- `ConfirmDenyAndStop` cancels the entire context (unrecoverable for the current task)

## Configuration

In `config.yaml`:

```yaml
security:
  default_policy: "user_confirm" # default for tools without explicit override
  tool_policies:
    bash_exec:
      policy: "user_confirm"
      blacklist:
        - "rm\\s+-rf\\s+/"
        - "sudo\\s+"
    write_file:
      policy: "user_confirm"
    edit_file:
      policy: "user_confirm"
    create_directory:
      policy: "user_confirm"
    delete_directory:
      policy: "user_confirm"
    delete_file:
      policy: "user_confirm"
    web_search:
      policy: "always_allow"
    web_fetch:
      policy: "always_allow"
```

## Anti-Patterns

- Setting `default_policy: "always_allow"` in production — removes all safety gates
- Adding tools to the `internalTools` set without careful consideration — they bypass everything
- Relying on the judge as a primary safety mechanism — it is advisory, not a gate
- Implementing confirmation timeout — blocking indefinitely is intentional (user may be away)

## Related Specs

- [domains/tool-system/README.md](../domains/tool-system/README.md) - Tool registry details
- [contracts/event-catalog.md](../contracts/event-catalog.md) - tool_confirm event payload
- [architecture/data-flow.md](data-flow.md) - Tool execution flow
