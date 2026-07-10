# ADR-002: Skills and MCP Gateway Live in sp4rk

## Status

Accepted

## Context

sp4rk ships two integration subsystems that an embedding application may or may not use: a **SkillManager** that discovers portable markdown skill documents from the filesystem, and an **MCP gateway** that connects to external Model Context Protocol servers and exposes their tools. Both raise the same architectural question: do they belong in the reusable framework, or are they host-application concerns?

Skills depend on:

- Filesystem access (reading skill directories, resolving paths).
- Per-session activation state (which skill is active for a given run).
- The tool registry (registering skill-derived tools at runtime).

The MCP gateway depends on:

- The tool registry (registering MCP tools with source/policy metadata).
- Environment-variable expansion for server configuration.
- Schema sanitization (stripping auto-injected parameters so the LLM never sees them).

A earlier position held that these were application-level integrations that should live above the framework, because skills needed orchestration-level context and the gateway needed policy enforcement the framework "did not model." That position was abandoned once the coupling was resolved through interface indirection rather than relocation: skills use context values for per-session activation, and the MCP gateway registers through the standard sp4rk tool registry.

## Decision

The SkillManager and the MCP gateway live **inside sp4rk**:

- `github.com/v0lka/sp4rk/skills` — `SkillManager` discovers skills from a prioritized list of directories (`NewSkillManager(dirs []string, logger)`), parses each `SKILL.md`, and exposes `List`/`Get`/`SkillPath`. Skills with the same name in a higher-priority directory override lower-priority ones.
- `github.com/v0lka/sp4rk/tools/mcp` — the `Gateway` manages connections to multiple MCP servers. `Gateway.RegisterTools(registry *tools.ToolRegistry)` registers every discovered MCP tool (wrapped to implement `tools.Tool`) into the standard tool registry with `SourceCategoryMCP`.

The framework owns the discovery, parsing, and registration mechanics. The **host application** only *wires* them into the orchestration cycle: it constructs the `SkillManager`, activates skills per session (via context values), constructs and starts the `Gateway`, and calls `RegisterTools` so MCP tools become available to the executor. Policy enforcement, per-session activation, and lifecycle are the host's responsibility; the registry's fail-closed policy enforcement applies to MCP tools exactly as it does to built-in tools.

## Consequences

**Positive:**

- sp4rk is self-contained for these integrations: a consumer that wants skills or MCP gets them without writing an adapter layer.
- The MCP gateway registers through the same `ToolRegistry` the executor consumes (`tools.ToolRegistry` satisfies `agent.ToolExecutor`), so MCP tools are first-class — they appear in tool listings, honor policy overrides, and are classified as untrusted for prompt-injection defense.
- Skills remain portable: the `SkillManager` reads plain markdown with no agent-specific metadata; any consuming agent can use the same skill directory.
- The framework stays independently testable — skills and MCP are exercised without a host.

**Negative:**

- sp4rk carries the `mcp-go` dependency (and the shell-parser dependency in the tools package, see [003-shell-parser-symlink-detection.md](003-shell-parser-symlink-detection.md)). Consumers that never use MCP still pull it into their module graph.
- Adding a new MCP feature or skill capability touches framework packages, which are now part of a separately versioned module.
- The host must still implement per-session activation and lifecycle wiring; the framework provides the building blocks, not the orchestration policy.

## Alternatives Considered

**Keep skills and MCP only in the host application.** Rejected: it forces every embedding application that wants these capabilities to reimplement discovery, parsing, schema sanitization, and registration. The coupling concerns were resolved by interface indirection (context values for skill activation, the standard registry for MCP registration), so relocation to the framework lost none of the original safety properties.

**Add orchestration-lifecycle hooks into sp4rk to fully own skill activation.** Rejected: it would pull orchestration policy (when to activate/deactivate a skill, how to match skills to tasks) down into the framework, violating the principle that sp4rk is a reusable engine, not an opinionated host. Per-session activation via context values keeps the policy in the host.

## Related

- [001-separate-module.md](001-separate-module.md) — these packages are part of the `github.com/v0lka/sp4rk` module boundary.
- [004-application-concept-extraction.md](004-application-concept-extraction.md) — frames which concerns belong in sp4rk versus the host.
- [../contracts/tools.md](../contracts/tools.md) — the `ToolRegistry` the MCP gateway registers into, and the policy/untrusted classification that applies to MCP tools.
