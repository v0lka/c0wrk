// Example 05 — MCP Integration
//
// Demonstrates how to connect external Model Context Protocol (MCP) servers
// to the agent. MCP tools are discovered at startup and registered alongside
// built-in tools, giving the agent access to arbitrary external capabilities
// (databases, APIs, file systems, etc.) without writing custom Go code.
//
// This example configures a stdio MCP server. You need Node.js installed
// (npx) to run the filesystem MCP server. If the server is unavailable,
// the agent still works with the built-in tools — MCP failures are logged
// as warnings, not fatal errors.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/c0wrk/sdk"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	"github.com/v0lka/c0wrk/sdk/tools/mcp"
)

func run() error {
	// Create a directory the MCP filesystem server will expose.
	mcpRoot, err := os.MkdirTemp("", "c0wrk-mcp-root-*")
	if err != nil {
		return fmt.Errorf("failed to create MCP root: %w", err)
	}
	defer func() { _ = os.RemoveAll(mcpRoot) }()

	// Seed it with a sample file so the agent has something to read.
	seedPath := mcpRoot + "/greeting.txt"
	if err := os.WriteFile(seedPath, []byte("Hello from MCP filesystem server!\n"), 0o644); err != nil {
		return fmt.Errorf("failed to seed MCP root: %w", err)
	}

	fmt.Println("MCP filesystem root:", mcpRoot)

	// Create the Framework with an MCP server configuration.
	// The MCP gateway starts during sdk.New(), connects to all configured
	// servers, discovers their tools, and registers them in the ToolRegistry.
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		MCP: &sdk.MCPConfig{
			Servers: map[string]mcp.ServerEntry{
				// A stdio MCP server: the SDK launches the command, communicates
				// over stdin/stdout, and discovers tools via the MCP protocol.
				"filesystem": {
					Transport: "stdio",
					Command:   "npx",
					Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", mcpRoot},
				},
				// An HTTP MCP server (commented out — uncomment if you have one):
				// "api": {
				//     Transport: "http",
				//     URL:       "http://localhost:3001/mcp",
				//     Headers:   map[string]string{"Authorization": "Bearer ${MCP_TOKEN}"},
				// },
			},
			// DefaultWorkDir is the fallback working directory for stdio servers
			// that don't specify their own. Not needed here because the filesystem
			// server takes its root as a command-line argument.
			DefaultWorkDir: mcpRoot,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Register built-in tools alongside the MCP-discovered tools.
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(agent.NewFinishTool())

	// List all available tools so we can see what MCP contributed.
	fmt.Println("\nAvailable tools:")
	for _, td := range registry.List() {
		fmt.Printf("  [%s] %s — %s\n", td.Source, td.Name, truncate(td.Description, 60))
	}
	fmt.Println()

	// Use the MCP root as the workspace for built-in tools too.
	ctx := tools.WithWorkspacePath(context.Background(), mcpRoot)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a file exploration assistant with access to both " +
			"built-in tools and MCP-provided tools. " +
			"Use any available tool to accomplish the task. " +
			"Call finish when done."
	}

	task := "Read the file greeting.txt in the workspace and tell me its contents."

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
	fmt.Println("═══════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}
