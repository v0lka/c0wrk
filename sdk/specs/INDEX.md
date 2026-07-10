# Specs Index

## Navigation by Task

| If your task involves...                                       | Read these specs                                                                |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| sp4rk package layers, import rules                            | [architecture/layers.md](architecture/layers.md)                                |
| Request lifecycle inside the engine                           | [architecture/data-flow.md](architecture/data-flow.md)                          |
| Tool policies, confirmations, judge                           | [architecture/security-model.md](architecture/security-model.md)                |
| Prompt-injection defense, untrusted-content wrapping          | [architecture/security-model.md](architecture/security-model.md)                |
| Orchestration overview (Conductor pipeline)                   | [domains/orchestration/README.md](domains/orchestration/README.md)              |
| Conductor (runs a task end-to-end)                            | [domains/orchestration/conductor.md](domains/orchestration/conductor.md)        |
| ReAct loop, circuit breakers, step limits                     | [domains/orchestration/executor.md](domains/orchestration/executor.md)          |
| Request classification, domain/complexity                     | [domains/orchestration/router.md](domains/orchestration/router.md)              |
| Plan generation, DAG, replan                                  | [domains/orchestration/planner.md](domains/orchestration/planner.md)            |
| Failure analysis, retry/replan/abort                          | [domains/orchestration/reflector.md](domains/orchestration/reflector.md)        |
| Subagent delegation, parallel steps                           | [domains/orchestration/subagents.md](domains/orchestration/subagents.md)        |
| Tool registry, execution pipeline                             | [domains/tool-system/README.md](domains/tool-system/README.md)                  |
| Adding/modifying built-in tools                               | [domains/tool-system/builtins.md](domains/tool-system/builtins.md)              |
| MCP servers, dynamic tools                                    | [domains/tool-system/mcp-gateway.md](domains/tool-system/mcp-gateway.md)        |
| Context window, compaction strategies                         | [domains/memory/compaction.md](domains/memory/compaction.md)                    |
| Blackboard, shared state, facts                               | [domains/memory/blackboard.md](domains/memory/blackboard.md)                    |
| LLM providers, router, model registry, tokens                 | [domains/llm-providers.md](domains/llm-providers.md)                            |
| Skill system, activation, resources                           | [domains/skills.md](domains/skills.md)                                          |
| Prompt builder, system-prompt composition                     | [domains/prompt-building.md](domains/prompt-building.md)                        |
| Embeddings, chunking, ONNX                                    | [domains/embedding.md](domains/embedding.md)                                    |
| What an embedding application must provide (events, confirm)  | [contracts/agent-execution.md](contracts/agent-execution.md)                    |
| LLM provider contract                                         | [contracts/llm-providers.md](contracts/llm-providers.md)                        |
| Tool interface contract                                       | [contracts/tools.md](contracts/tools.md)                                        |
| "Why was X designed this way?"                                | [decisions/](decisions/)                                                        |

## Package Dependency Graph

sp4rk is a single Go module (`github.com/v0lka/sp4rk`). Arrows show import direction (`A → B` means package A imports package B). Import direction flows downward — upper orchestration layers depend on lower primitive layers, never the reverse.

```
                       github.com/v0lka/sp4rk  (root: Framework, Config, Execute)
                          │  owns shared infrastructure (LLM router, tool registry,
                          │  MCP gateway, tool cache) and creates per-task conductors
            ┌─────────────┼──────────────┬──────────────┐
            ▼             ▼              ▼              ▼
       orchestration    planner        memory        tools/mcp
            │             │              │              │
            │             │              │              │
   ┌────────┼──────┐      │      ┌───────┴────┐         │
   ▼        ▼      ▼      │      ▼            ▼         │
 agent   llm  skills ◀────┴──  security    strutil      │
   │      ▲                                ▲            │
   │      │                                │            │
   ▼      │                                │            │
 tools ◀──┘                                │            │
   │                                       │            │
   ├──→ pathutil  ─────────────────────────┘            │
   ├──→ strutil                                         │
   └──→ tools/internal/judge_prompts ◀──────────────────┘
            ▲
            │
   prompt ◀─┘   (prompt imports nothing engine-internal)

   agent/reflector → {agent, llm, orchestration, prompt, tools}
   agent/router    → {agent, llm, prompt, tools}
   tools/builtins  → tools
   tools/mcp       → tools
   skills          → {pathutil, tools}
```

Supporting packages — `prompt`, `skills`, `security`, `embedding`, `pathutil`, `strutil` — are consumed across layers as needed and have no upward dependencies.

Import rule: every arrow is one-way. `tools` and `llm` are near-leaf dependencies; they never import `agent`, `orchestration`, `planner`, or the root package. This keeps the primitive layers free of higher-level concerns and prevents import cycles.

## Spec Workflow and Format Reference

See [META.md](META.md) for document templates, naming rules, and update protocol.

## Directory Listing

### architecture/

- [layers.md](architecture/layers.md) - sp4rk package layers, import rules, responsibilities
- [data-flow.md](architecture/data-flow.md) - Request lifecycle, ReAct loop, Plan&Execute, event flow
- [security-model.md](architecture/security-model.md) - Tool policies, judge, confirmations, prompt-injection defense

### domains/orchestration/

- [README.md](domains/orchestration/README.md) - Orchestration domain overview (Conductor pipeline)
- [conductor.md](domains/orchestration/conductor.md) - Conductor that runs a single task end-to-end
- [executor.md](domains/orchestration/executor.md) - Executor ReAct loop primitive (shared by Conductor and subagents)
- [router.md](domains/orchestration/router.md) - Request classification and skill matching
- [planner.md](domains/orchestration/planner.md) - Plan generation, DAG, replan
- [reflector.md](domains/orchestration/reflector.md) - Failure analysis and retry/replan/abort
- [subagents.md](domains/orchestration/subagents.md) - Subagent delegation and parallel step execution

### domains/tool-system/

- [README.md](domains/tool-system/README.md) - Tool registry and execution pipeline
- [builtins.md](domains/tool-system/builtins.md) - Built-in tool catalog and extension guide
- [mcp-gateway.md](domains/tool-system/mcp-gateway.md) - MCP server lifecycle and dynamic tools

### domains/memory/

- [README.md](domains/memory/README.md) - Context management overview
- [compaction.md](domains/memory/compaction.md) - Compaction strategies and thresholds
- [blackboard.md](domains/memory/blackboard.md) - Shared task state, facts, checkpoints

### domains/ (single-file)

- [llm-providers.md](domains/llm-providers.md) - Provider abstraction, router, model registry, token counting
- [skills.md](domains/skills.md) - Skill system, activation, resource access
- [prompt-building.md](domains/prompt-building.md) - Prompt builder and system-prompt composition
- [embedding.md](domains/embedding.md) - Embeddings, chunking, ONNX runtime

### contracts/

- [agent-execution.md](contracts/agent-execution.md) - What an embedding application provides (events, confirmations, HITL)
- [llm-providers.md](contracts/llm-providers.md) - LLM provider interface contract
- [tools.md](contracts/tools.md) - Tool and ToolJudger interface contract

### decisions/

- [_template.md](decisions/_template.md) - ADR template
- sp4rk-native Architecture Decision Records (`00N-slug.md`)
