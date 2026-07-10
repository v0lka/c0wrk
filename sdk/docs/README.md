# sp4rk Agent SDK

A Go framework for building AI agent systems with **Plan & Execute orchestration**, tool integration, and multi-provider LLM support.

The SDK gives you a production-grade foundation for agents that reason, act, and self-correct: a ReAct execution loop, a DAG-based planner, a reflector for failure analysis, multi-provider LLM routing, a thread-safe tool registry, Model Context Protocol (MCP) integration, context-window management with automatic compaction, human-in-the-loop hooks, structured event streaming, skill discovery, ONNX-based local embeddings, and prompt-injection defenses.

It is a standalone library with no opinion about your UI, storage, or deployment — wire it into a CLI, a desktop app, a server, or a test harness.

## Features

- **Multi-provider LLM routing** — Anthropic, OpenAI Chat Completions, and OpenAI Responses API behind a single `Router`. Switch models at runtime; composite model IDs (`provider/model`) disambiguate shared names across providers.
- **Custom & built-in tools** — implement the `tools.Tool` interface or use the bundled file, shell, search, web, and vector-search tools. A thread-safe `ToolRegistry` holds them all.
- **MCP integration** — connect to stdio- or HTTP-based Model Context Protocol servers; their tools are discovered and proxied into the same registry as your built-ins.
- **Plan & Execute orchestration with DAG** — a `Planner` generates a directed-acyclic-graph of steps with dependencies and parallelism hints; a `Conductor` executes each step as its own ReAct loop.
- **Reflector for self-correction** — when a step fails, a `Reflector` analyzes the trajectory and produces a structured reflection that drives retry or replan decisions.
- **Context window management with compaction** — a `ContextWindow` tracks fill percentage and triggers predictive, warning, or emergency compaction via sliding-window, summary, or hierarchical strategies. Tool-output pruning and history mutation keep replay cost bounded.
- **Human-in-the-loop** — a `HITLHandler` intercepts tool calls for confirmation or modification and decides what happens when the step budget is reached.
- **Event streaming** — an `Events` interface streams every lifecycle event (thoughts, tool calls, results, context fill, compaction, sub-agent launches) for live observability.
- **Skill discovery** — a `SkillManager` discovers and parses Agent Skills (agentskills.io specification) from priority-ordered directories and exposes lightweight descriptors for routing.
- **ONNX embeddings** — a local `Embedder` runs jina-embeddings-v2-small-en through ONNX Runtime for vector search without external API calls.
- **Prompt injection defense** — untrusted tool output (web, MCP, filesystem) is wrapped in `<untrusted-content>` boundary tags and sanitized before entering LLM context.

- **Tool execution-context intelligence** — a centralized, cached LLM-backed `ToolJudge` decides whether a mutating call may auto-approve; file-coherence tracking prevents cross-session read/write races; an `EnvInfo` snapshot injects host/runtime context into prompts; and symlink detection guards against path-escape traversals. See [Tool Safety & Execution Context](tool-safety.md).

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

func main() {
	// 1. Create the Framework with a single Anthropic provider.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
	})
	if err != nil {
		log.Fatalf("failed to create framework: %v", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// 2. Register the finish tool — the agent signals task completion by
	//    calling it. Without it the loop runs until the step budget is
	//    exhausted and returns a "partial" status.
	fw.ToolRegistry().Register(agent.NewFinishTool())

	// 3. Define a system prompt factory.
	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a helpful assistant. Answer concisely. " +
			"When you have a final answer, call the finish tool with it."
	}

	// 4. Execute a single user message.
	result, err := fw.Execute(
		context.Background(),
		systemPrompt,
		&agent.NoopEvents{}, // no event handling
		"What is the capital of France?",
	)
	if err != nil {
		log.Fatalf("execution failed: %v", err)
	}

	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
}
```

Set an API key and run:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
go run main.go
```

## Table of contents

| Document | Description |
| --- | --- |
| [getting-started.md](getting-started.md) | Installation, configuration reference, and your first agent |
| [architecture.md](architecture.md) | Layered design, package relationships, data flow, and the ReAct loop |
| [fluent-api.md](fluent-api.md) | Fluent API in the root `sp4rk` package — method-chain builders, the `F` postfix, before/after, escape hatches |
| [llm-providers.md](llm-providers.md) | Multi-provider routing, model registry, and provider configuration |
| [tools.md](tools.md) | The `Tool` interface, `ToolRegistry`, built-in tools, and custom tools |
| [mcp-integration.md](mcp-integration.md) | Model Context Protocol servers, the gateway, and external tool discovery |
| [agent-executor.md](agent-executor.md) | The `Executor`, ReAct loop internals, and circuit breakers |
| [events.md](events.md) | The `Events` interface and live execution observability |
| [hitl.md](hitl.md) | Human-in-the-loop: tool-call confirmation and step-limit decisions |
| [subagents.md](subagents.md) | Parallel execution with subagent goroutines and trajectory capture |
| [orchestration.md](orchestration.md) | The `Conductor`, blackboard, and single-task execution |
| [planner.md](planner.md) | DAG plan generation, step profiles, and replanning |
| [reflector.md](reflector.md) | Failure analysis and self-correction reflections |
| [memory.md](memory.md) | Context window management, compaction strategies, and pruning |
| [prompt-building.md](prompt-building.md) | The prompt `Builder`, cache breaks, and substitutions |
| [skills.md](skills.md) | Agent Skill discovery, parsing, and routing |
| [security.md](security.md) | Prompt injection defense and untrusted-content wrapping |
| [tool-safety.md](tool-safety.md) | Tool execution-context intelligence: LLM-backed `ToolJudge`, file coherence, environment info, symlink detection |
| [embedding.md](embedding.md) | ONNX-based local embeddings and vector search |
| [utilities.md](utilities.md) | `pathutil` and `strutil` helper packages |

## Package map

| Package | Purpose |
| --- | --- |
| `github.com/v0lka/sp4rk` | Top-level `Framework`, `Config`, `New`, `Execute`, `NewConductor`, `Shutdown`; fluent builders `NewF`/`RunF`/`TaskF` + option/tool/MCP helpers (recommended entry point) |
| `…/agent` | ReAct `Executor` loop, `Events`, `HITLHandler`, `FinishTool`, `RunSubAgent`, tool-result cache |
| `…/agent/reflector` | `Reflector` for execution failure analysis and self-correction |
| `…/agent/router` | `Router` that classifies requests by domain and complexity |
| `…/llm` | `Router`, `Provider`, `ProviderEntry`, `ModelRegistry`, token counting, Anthropic/OpenAI providers |
| `…/tools` | `Tool` interface, `BaseTool`, thread-safe `ToolRegistry`, `ToolPolicy`, `ToolDescriptor`, LLM-backed `ToolJudge`, file coherence, `EnvInfo`, symlink detection |
| `…/tools/builtins` | Built-in tools: file I/O, shell, glob, ripgrep, web fetch, vector search, facts, checklist; configurable limits/timeouts |
| `…/tools/mcp` | MCP `Gateway`, `Server`, `ServerEntry`, and tool proxying |
| `…/orchestration` | `Conductor`, `Blackboard`, `Plan`, DAG utilities, `Checkpointer`, orchestration interfaces |
| `…/planner` | `Planner`, `Config`, `PromptSet`, `AgentProfile` for DAG plan generation |
| `…/prompt` | Fluent prompt `Builder` with cache-break support and substitutions |
| `…/skills` | `SkillManager` for agentskills.io skill discovery, parsing, and serving |
| `…/memory` | `ContextWindow`, compaction strategies (sliding_window, summarization, hierarchical), pruning |
| `…/security` | Prompt-injection defense: `WrapUntrustedContent`, `StripUntrustedTags` |
| `…/embedding` | ONNX-based `Embedder`, `Tokenizer`, and chunker for local vector embeddings |
| `…/pathutil` | Reusable filesystem-path algorithms: `IsWithinPath`, `SplitPathComponents`, `ResolveExistingPrefix` |
| `…/strutil` | String helpers: `TruncateUTF8` and related utilities |

## Prerequisites

- **Go 1.26+** — the SDK targets the Go version in its `go.mod`.
- **At least one LLM API key** — Anthropic (`ANTHROPIC_API_KEY`) or OpenAI (`OPENAI_API_KEY`). The framework requires at least one configured provider.
- **(Optional) ONNX Runtime** — only if you use the local embedding subsystem for vector search.

## Next steps

- Follow the [getting-started guide](getting-started.md) for a complete walkthrough with custom tools.
- Read the [architecture overview](architecture.md) to understand how the layers fit together.
- Browse the `examples/` directory for eleven self-contained programs that progress from a minimal agent through focused subsystem deep-dives to a full-stack system exercising every subsystem.
