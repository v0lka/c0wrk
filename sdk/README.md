# sp4rk Agent SDK

A standalone Go framework for building AI agent systems with Plan & Execute orchestration, tool integration, and multi-provider LLM support.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/v0lka/sp4rk"
)

func main() {
	// sp4rk.NewF is the fluent entry point; it returns a real *sp4rk.Framework
	// (the same type the classic sp4rk.New constructor returns). The finish tool
	// is auto-registered so the agent can signal completion.
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		Build()
	if err != nil {
		panic(err)
	}
	defer fw.Shutdown()

	// Run a single ReAct loop and return the original *orchestration.ExecutionResult.
	result, err := fw.RunF(context.Background()).
		System("You are a helpful assistant.").
		Ask("Write a hello world in Go")
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Output)
}
```

The fluent builders are part of the **root `sp4rk` package** (no separate import): they return the original SDK types (`*sp4rk.Framework`, `*orchestration.ExecutionResult`) and delegate every call to the underlying API, so you can mix fluent and classic code freely. For the classic `sp4rk.Config` API (full low-level control), see [Getting started](docs/getting-started.md).

> New here? Read the [Fluent API guide](docs/fluent-api.md) for the layer map, before/after comparisons, and when to reach for classic escapes.

## Documentation

Detailed guides live in [`docs/`](docs/):

- [Getting started](docs/getting-started.md) — installation, configuration, first run
- [Architecture](docs/architecture.md) — layered design and package layout
- [Agent executor](docs/agent-executor.md) — the execution loop
- [Orchestration](docs/orchestration.md) — Plan & Execute mode
- [Planner](docs/planner.md) — plan generation
- [Reflector](docs/reflector.md) — self-reflection
- [LLM providers](docs/llm-providers.md) — Anthropic, OpenAI Chat Completions, OpenAI Responses API, and OpenAI-compatible endpoints
- [Tools](docs/tools.md) — built-in tools and the registry
- [MCP integration](docs/mcp-integration.md) — Model Context Protocol gateway
- [Memory](docs/memory.md) — compaction and persistence
- [Embedding & vector search](docs/embedding.md) — semantic search
- [Security](docs/security.md) — tool policies and safety
- [Skills](docs/skills.md) — reusable skill packages
- [Subagents](docs/subagents.md) — delegated execution
- [Human-in-the-loop](docs/hitl.md) — confirmations and ask-user
- [Events](docs/events.md) — streaming event types
- [Prompt building](docs/prompt-building.md) — system prompt assembly
- [Utilities](docs/utilities.md) — path and string helpers

Runnable examples are in [`examples/`](examples/).

## License

[MIT](LICENSE)
