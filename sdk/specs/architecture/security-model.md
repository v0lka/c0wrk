# Security Model

## Context

sp4rk executes arbitrary tools (filesystem operations, shell commands, web requests) on behalf of an LLM, and ingests external data into the model's context. The engine provides two complementary security primitives: a **tool-execution policy layer** that gates whether and how a tool runs, and a **prompt-injection defense** that delimits untrusted external content before it reaches the model. This spec describes the engine-level primitives only — a host application layers additional wiring (workspace/temp auto-approval, command blacklists, symlink-forced confirmation) on top of these.

## Scope Boundary

sp4rk ships the *primitives*; the host application assembles the *policy*. The SDK provides:

- the `ToolPolicy` enum and per-tool policy resolution,
- the `ToolJudger` interface and the fail-closed confirmation flow (`ConfirmFunc`/`ConfirmationRequest`/`ConfirmationResponse`),
- the prompt-injection defense (`security.WrapUntrustedContent`/`StripUntrustedTags`), and
- the MCP tool-shadowing guard.

The host application owns, and this spec deliberately does **not** cover:

- workspace/temp-directory auto-approval (path-locality heuristics that bypass confirmation),
- command blacklists (e.g. dangerous-shell patterns),
- symlink-forced confirmation gating.

Those decisions depend on host-specific concepts (session roots, command grammars, OS conventions) and are wired by the embedding application around the SDK registry.

## Tool Policies

Every tool carries a `ToolPolicy` (`github.com/v0lka/sp4rk/tools`). It is an integer enum whose string mapping is stable:

```go
type ToolPolicy int

const (
    PolicyAlwaysAllow ToolPolicy = iota // execute without confirmation or judge
    PolicyAlwaysDeny                     // block; tool never executes
    PolicyUserConfirm                    // require user confirmation before executing
)
```

`ParseToolPolicy` maps the configuration strings `"always_allow"`, `"always_deny"`, and `"user_confirm"`; any unrecognized value (including the empty string) falls back to `PolicyUserConfirm` — the safest default.

| Policy string    | Behavior                                                                                                |
| ---------------- | ------------------------------------------------------------------------------------------------------- |
| `always_allow`   | Execute immediately. No confirmation. A `ToolJudger` may still escalate a flagged call (see below).     |
| `user_confirm`   | Block execution and call `ConfirmFunc`. The host must allow, deny, or deny-and-stop. **Fail-closed** when no `ConfirmFunc` is configured. |
| `always_deny`    | Immediately return an error `ToolResult`. The tool is never executed.                                   |

## Policy Resolution

When a tool is about to execute, its effective policy is resolved (first match wins):

```
1. Per-tool override   (registry.SetPolicyOverride(name, policy))
2. Registry default    (registry default policy)
3. Tool's own default  (Tool.DefaultPolicy())
```

Source: `github.com/v0lka/sp4rk/tools` registry `Execute`.

The engine itself has no concept of "skill policy" or a configuration file; those are host-side layers that call `SetPolicyOverride` on the registry. The engine only resolves between the per-tool override, the registry default, and the tool's own `DefaultPolicy()`.

## The ToolJudger Interface

Tools may optionally implement `ToolJudger` to provide tool-specific safety heuristics:

```go
type ToolJudger interface {
    Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string)
}
```

For a tool whose effective policy is `PolicyAlwaysAllow`, the registry calls `Judge` **before** execution. If the judge returns `allow=false` **with non-empty reasoning**, the call is escalated to user confirmation via `ConfirmFunc`. A return of `allow=false` with empty reasoning is treated as "no concern to report" and the tool proceeds.

This is the SDK hook a host uses to implement its own safety checks (for example, a shell tool whose `Judge` matches a blacklist of dangerous commands, or a file tool whose `Judge` flags paths outside permitted roots). The engine provides the interface and the escalation wiring; the heuristics themselves are tool/host-specific.

The separate `ToolJudge` (`github.com/v0lka/sp4rk/tools` `judge.go`) is an LLM-backed **advisory** safety evaluation invoked on demand by the host — it is not an automatic gate.

## Confirmation Flow

`PolicyUserConfirm` tools (and judge-escalated `PolicyAlwaysAllow` tools) go through the confirmation flow:

```go
type ConfirmationRequest struct {
    ToolName       string          `json:"tool_name"`
    Input          json.RawMessage `json:"input"`
    JudgeReasoning string          `json:"judge_reasoning,omitempty"`
}

type ConfirmationResponse int

const (
    ConfirmAllowOnce    ConfirmationResponse = iota // allow this single execution
    ConfirmDeny                                     // deny this execution
    ConfirmDenyAndStop                              // deny and cancel the entire task
)

type ConfirmFunc func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error)
```

```
registry.Execute()
  → effective policy == PolicyUserConfirm   (or judge-escalated PolicyAlwaysAllow)
  │
  ├─ no ConfirmFunc configured? → FAIL-CLOSED: return error ToolResult
  │                                ("no ConfirmFunc is configured")
  ├─ ConfirmFunc(ctx, ConfirmationRequest{ToolName, Input, JudgeReasoning})
  │      │
  │      ▼
  │   host responds (Allow / Deny / Deny & Stop)
  │
  ├─ ConfirmAllowOnce     → execute tool
  ├─ ConfirmDeny          → return error ToolResult (agent sees the denial)
  └─ ConfirmDenyAndStop   → cancel the task context
```

The host wires `ConfirmFunc` via `sp4rk.Config.ConfirmFunc` (framework) or `registry.SetConfirmFunc` (direct registry use). `WithAutoApprove` installs a callback that approves every `PolicyUserConfirm` call — intended for throwaway/sandboxed workspaces.

**Fail-closed is the default.** Without a `ConfirmFunc`, a `PolicyUserConfirm` tool is denied rather than executing silently, so an injected instruction cannot trigger a mutation in a default-configured engine. To relax individual tools, the host calls `registry.SetPolicyOverride(name, PolicyAlwaysAllow)`.

## MCP Tool-Shadowing Protection

MCP servers are untrusted; a malicious or compromised server could advertise a tool named `read_file` or `bash_exec` to intercept calls intended for built-in tools. The registry stores each tool's source category explicitly at registration (`RegisterWithSourceCategory`) and **rejects any MCP-categorized registration that would overwrite an existing non-MCP tool**. Built-in tools can always replace MCP tools, and an MCP server may re-register its own tools on reconnect.

## Indirect Prompt Injection Defense

sp4rk protects the LLM context from untrusted tool output that could contain hidden instructions (prompt injection). The defense lives in `github.com/v0lka/sp4rk/security` and is applied by the memory layer's `ContextWindow`.

### Content Delimiting (Spotlighting)

Tool output from untrusted sources is wrapped in `<untrusted-content>` XML tags before it enters the LLM context:

```xml
<untrusted-content source="web_fetch">
...external data here...
</untrusted-content>
```

```go
const UntrustedTag = "untrusted-content"

func WrapUntrustedContent(content, source string, metadata map[string]string) string
func StripUntrustedTags(content string) string
```

`WrapUntrustedContent` wraps content in `<untrusted-content>` tags with a `source` attribute (the producing tool's name) and optional metadata attributes. The content is **first sanitized** via `StripUntrustedTags` to prevent tag-breakout attacks.

The wrapping is applied by `memory.ContextWindow.BuildPrompt` (when constructed with `InjectionDefenseEnabled: true`) — the last point before content reaches the LLM API. It runs **after** history mutation and pruning, so the defense always wraps the final content the model sees.

### Trust Classification

A tool marks its output as untrusted via the `IsUntrusted() bool` method on the `Tool` interface (set through `BaseTool.Untrusted`). The executor sets `Step.IsUntrusted` after tool execution; the context builder reads that flag to decide whether to wrap.

Typical untrusted tools:

- Web fetch / search tools — return arbitrary internet content.
- MCP-backed tools — return data from external servers. MCP tools are always considered untrusted regardless of their `IsUntrusted()` value.
- Filesystem tools reading untrusted paths — return file contents that may contain injected instructions.

Tools that return only internally generated, trusted data (e.g. the `finish` tool) do not set the flag, so their output is not wrapped.

### Tag-Breakout Protection

`StripUntrustedTags` escapes literal `<untrusted-content` and `</untrusted-content` patterns to prevent attackers from closing the wrapper early. Only the **leading `<`** of a matching tag is replaced with `&lt;`; the rest of the tag text is preserved as-is. Matching is case-insensitive and tolerates whitespace.

This operates on **literal character sequences only**. HTML-entity-encoded variants (e.g. `&#60;/untrusted-content>`) are **not** escaped — intentionally. LLMs process raw text tokens; they do not decode HTML entities when interpreting context boundaries, so escaping entity-encoded variants would add noise without improving security.

### No LLM-Based Output Judging

The defense does **not** include LLM-based output-content judging for injection detection. Judging whether content constitutes an attack is delegated to external firewall/proxy defenses. This keeps latency predictable and avoids token waste on detection tasks.

### No Domain Gate

All untrusted tools (including web fetchers) receive the same wrapping treatment. There is no domain allowlist or content-type gate before wrapping — the wrapping is unconditional for any tool marked as untrusted.

## Configuration (Engine-Level)

The engine exposes policy as Go values, not a configuration file. A host configures policies programmatically:

```go
// Per-tool overrides (host calls these on the registry)
registry.SetPolicyOverride("write_file", tools.PolicyUserConfirm)
registry.SetPolicyOverride("bash_exec",  tools.PolicyUserConfirm)
registry.SetPolicyOverride("web_search", tools.PolicyAlwaysAllow)
registry.SetPolicyOverride("web_fetch",  tools.PolicyAlwaysAllow)

// The confirmation channel (host supplies this)
framework := sp4rk.New(sp4rk.Config{
    ConfirmFunc: func(ctx context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
        // prompt the user; return ConfirmAllowOnce / ConfirmDeny / ConfirmDenyAndStop
    },
})

// Prompt-injection defense is enabled on the context window
cw := memory.NewContextWindow(memory.ContextWindowConfig{
    InjectionDefenseEnabled: true, // wrap untrusted tool outputs
    // ...system prompt, model meta, thresholds, strategy...
})
```

File-based defaults, session roots, and blacklist regexes are host-application configuration; the engine only consumes the resulting `ToolPolicy` values and `ConfirmFunc`.

## Invariants

- A `PolicyUserConfirm` tool with no `ConfirmFunc` configured is **always denied** (fail-closed), never executed silently.
- `PolicyAlwaysDeny` is never bypassed — not by a judge, not by a confirmation, not by an override that does not change the policy.
- A `ToolJudger` escalation requires `allow=false` **with non-empty reasoning**; `allow=false` with empty reasoning proceeds.
- Untrusted tool output is wrapped in `<untrusted-content>` before it reaches the model, unconditionally for any tool whose `IsUntrusted()` is true (or that is MCP-sourced).
- `WrapUntrustedContent` always sanitizes its content with `StripUntrustedTags` first.
- An MCP tool registration can never overwrite an existing non-MCP tool.

## Anti-Patterns

- Treating the `ToolJudge` as a primary safety mechanism — it is advisory, not a gate. The hard boundary is the policy layer.
- Relying on path-locality auto-approval as a security control — that is host wiring layered over these primitives, not an engine guarantee.
- Calling `tool.Execute` directly instead of `registry.Execute` — bypasses policy resolution, the judge gate, the confirmation flow, and MCP shadowing protection.
- Constructing a `ContextWindow` with `InjectionDefenseEnabled: false` in production — disables untrusted-content wrapping for all tool output.
- Expecting the engine to enforce a command blacklist — blacklists are host/tool-specific `ToolJudger` heuristics, not engine behavior.

## Related Specs

- [layers.md](layers.md) - Where the security primitives live in the package hierarchy
- [data-flow.md](data-flow.md) - Tool execution flow and the fail-closed gate
- [../contracts/tools.md](../contracts/tools.md) - The Tool and ToolJudger interface contract
