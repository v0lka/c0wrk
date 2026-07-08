# sp4rk Agent SDK

A standalone Go framework for building AI agent systems with Plan & Execute orchestration, tool integration, and multi-provider LLM support.

## Quick start

```go
package main

import (
	"context"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

func main() {
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
	})
	if err != nil {
		panic(err)
	}
	defer fw.Shutdown()

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a helpful assistant."
	}
	result, err := fw.Execute(context.Background(), systemPrompt, &agent.NoopEvents{}, "Write a hello world in Go")
	if err != nil {
		panic(err)
	}
	_ = result
}
```

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
