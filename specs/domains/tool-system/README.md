# Tool System

## Purpose

c0wrk provides tool infrastructure for the agent on top of sp4rk's `Tool`/`ToolRegistry` primitives: a policy-enforcing registry wrapper, built-in tool registration, the c0wrk-specific `ask_user` tool, and tool-manager wiring for external binaries. The `Tool` interface, `ToolPolicy`, `ToolResult`, `BaseTool`, `ToolJudger`, `ConfirmFunc`, and the basic `ToolRegistry` are **sp4rk engine** primitives — see [the sp4rk tool-system spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/README.md) and [the sp4rk tools contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/tools.md).

## Key Files

- `core/tools/registry.go` — core `ToolRegistry` (wraps the sp4rk registry; adds policy resolution, judge, hooks, symlink gate, disabled-tool and bash-blacklist enforcement)
- `core/tools/registry_symlink.go` — symlink detection/traversal integration calling sp4rk `DetectSymlinksInToolInput`
- `core/tools/builtin_registration.go` — `RegisterBuiltinTools` function + `BuiltinToolsConfig`
- `core/tools/askuser.go` / `core/tools/askuser_types.go` — c0wrk-specific `ask_user` tool + AskUser request/response types (moved out of sp4rk per ADR-011)
- `core/toolnames.go` — tool name constants, `NoProjectDisabledTools`, `NoProjectShellBlacklist`
- `core/toolmanager/` — manages external binary dependencies (`rg`, `uv`, `markitdown`), auto-downloaded on first run (see ADR-010)

Engine files (`github.com/v0lka/sp4rk/tools/tool.go`, `safety.go`, `registry.go`, `judge.go`, `github.com/v0lka/sp4rk/security/wrap.go`, `github.com/v0lka/sp4rk/tools/mcp/gateway.go`) are documented in [the sp4rk tool-system spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/README.md).

## Two-Layer Registry

```
┌─────────────────────────────────────────────────────┐
│  core/tools.ToolRegistry                            │
│  (policy enforcement, judge, hooks, filters)        │
│                                                     │
│  ┌─────────────────────────────────────────────┐   │
│  │  sp4rk tools.ToolRegistry (embedded)         │   │
│  │  (basic store: Register, Get, List, Execute) │   │
│  └─────────────────────────────────────────────┘   │
│                                                     │
│  + resolvePolicy()        policy resolution chain     │
│  + confirmAndExecute()    user confirmation flow      │
│  + SetJudge()             LLM safety evaluation        │
│  + SetPreExecuteHook()    pre-execution gate          │
│  + SetToolFilter()         registration filter         │
│  + SetSkillPolicyOverrides()  skill-derived policy layer │
│  + RegisterWithSource()   filtered registration       │
│  + SetDisabledTools()     block tools by name (e.g., No Project) │
│  + DisabledTools()        read disabled-tool set       │
│  + SetExtraShellBlacklist() runtime shell command blacklist      │
└─────────────────────────────────────────────────────┘
```

The embedded sp4rk `ToolRegistry` satisfies `github.com/v0lka/sp4rk/agent.ToolExecutor`. The fail-closed policy pipeline, `ConfirmFunc`, `ToolJudger`, and `ToolPolicy` semantics are engine behavior — see [the sp4rk tools contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/tools.md).

## Flow (c0wrk registry pipeline)

```
core ToolRegistry.Execute(ctx, name, input)
│
├─ 1. Lookup tool by name → not found? return error result
├─ 2. Required-field validation (JSON-Schema "required" params; fail-closed) → missing? return error result
├─ 3. Disabled tool (No Project mode)? → return error result (applies to ALL tools including internal)
├─ 4. Internal tool? → execute immediately (bypass remaining policy/judge/hook checks)
├─ 5. PostExecuteHook deferred (runs on every later return path)
├─ 6. Extra bash blacklist match? → return error result (per-session, e.g., No Project blocks dev commands)
├─ 7. PreExecuteHook (blocking gate, e.g., index ready)
├─ 8. Symlink Gate: detect symlinks in input paths
│      ├─ Symlinks found → force confirmation (unless always_deny)
│      └─ No symlinks → continue
├─ 9. resolvePolicy: per-tool > skill > default > tool's own
└─ 10. Apply policy:
      ├─ AlwaysAllow → execute (unless ToolJudger flags)
      ├─ AlwaysDeny → error result
      └─ UserConfirm → confirmFunc blocks → execute or deny
```

The policy resolution, auto-approval (session roots), and symlink gate are c0wrk's session-security layer — detailed in [../../architecture/security-model.md](../../architecture/security-model.md).

## Invariants

- Tool names are unique within the registry
- Internal tools bypass policy and judge checks. The set (in `core/tools/registry.go` `internalTools`) is: `ask_user`, `delegate`, `cancel_delegation`, `declare_plan`, `execute_plan`, `propose_goal`, `declare_goal_status`, `declare_verification`, `reflect`, `finish`, `list_step_outputs`, `read_step_output`, `read_final_result`, `read_skill_resource`, `read_attachment`, `search_facts`, `semantic_search`, `update_checklist`, `declare_step_complete`, `store_fact`, `tool_result_read`, and `batch` (`sdktools.ToolBatch`). The disabled-tool check (No Project mode) applies to all tools including internal ones, but the extra-bash-blacklist check runs AFTER the internal-tool bypass. `batch` is intercepted at the executor level before reaching the registry's `Execute()` path
- The symlink gate runs before policy resolution for every non-internal tool call
- MCP tools carry source category `mcp` (source tag = the MCP server's name); core built-in tools carry source category `core`
- Disabled tools are blocked at execution time; `SetDisabledTools`/`DisabledTools` deep-copy the map to prevent concurrent mutation
- The registry is thread-safe (sync.RWMutex)
- Untrusted tool output (`IsUntrusted() == true`) is wrapped in `&lt;untrusted-content>` tags before entering the LLM context (engine wrapping; see [../../architecture/security-model.md](../../architecture/security-model.md))

## Configuration

From `config.yaml`:

```yaml
security:
  default_policy: "user_confirm"
  smart_approve: false  # strict OWASP ASI judge for effective user_confirm (opt-in)
  tool_policies:
    bash_exec: { policy: "user_confirm" }  # key matches the active shell tool: bash_exec (Unix) / posh_exec (Windows)
    write_file: { policy: "user_confirm" }

toolLimits:
  readDefaultLines: 2000
  webSearchMaxResults: 5
  perToolTruncation:
    read_file: { maxLines: 2000 }
    read_attachment: { maxLines: 2000 }
    ripgrep: { maxLines: 2000 }
    glob: { maxLines: 2000 }
    list_directory: { maxLines: 2000 }
    web_fetch: { maxBytes: 2097152 }
    bash_exec: { maxLines: 5000 }  # same key as policy: bash_exec (Unix) / posh_exec (Windows)
    posh_exec: { maxLines: 5000 }

executor:
  tool_result_budget:
    cacheTTLSeconds: 300 # MCP tool result cache TTL (seconds)

timeouts:
  bashMaxTimeout: 120 # seconds
  bashWaitDelay: 5 # seconds
  ripgrepTimeout: 60 # seconds
  webFetchTimeout: 30 # seconds
  webSearchTimeout: 30 # seconds
  persistenceTimeout: 5 # seconds
```

Note: `security.*` keys use `snake_case`; `toolLimits.*` and `timeouts.*` keys use `camelCase`. Both conventions match the yaml struct tags in `backend/config/config.go`.

## Extension Points

- `PreExecuteHook` — block until preconditions met (e.g., vector index ready)
- `ToolFilter` — reject tools during registration (e.g., filter MCP tools by server)
- `ToolJudger` interface — per-tool safety evaluation (implement on tool struct)
- New built-in tools: implement the sp4rk `Tool` interface, set `Untrusted: true` on `BaseTool` if output comes from external sources, register in `RegisterBuiltinTools` (c0wrk-specific tools like `ask_user` go in `core/tools/`) — see [builtins.md](builtins.md)
- To disable tools at runtime (e.g., for No Project mode): call `SetDisabledTools(names)` on the core registry; all tools including internal ones are blocked at execution time
- To add runtime shell command restrictions: call `SetExtraShellBlacklist(patterns)` on the core registry; patterns are compiled regexps checked before the shell-exec tool (`bash_exec` on Unix, `posh_exec` on Windows) executes

## Related Specs

- [sp4rk tool-system overview](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/README.md) — canonical `Tool`/`ToolRegistry`/`ToolPolicy`/`ToolJudger`/`ConfirmFunc`
- [sp4rk tools contract](https://github.com/v0lka/sp4rk/blob/main/specs/contracts/tools.md) — interface definitions
- [builtins.md](builtins.md) — catalog of built-in tools and c0wrk registration
- [mcp-gateway.md](mcp-gateway.md) — dynamic MCP tool lifecycle
- [../../architecture/security-model.md](../../architecture/security-model.md) — c0wrk policy/auto-approval/symlink layer
