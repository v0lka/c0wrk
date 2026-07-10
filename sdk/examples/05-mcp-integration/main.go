//go:build !fluent

// Example 05 — MCP Integration (Classic API)
//
// Demonstrates how to connect external Model Context Protocol (MCP) servers
// to the agent, using the classic sp4rk.Config + MCPConfig API.
// For the concise recommended path, see main_fluent.go (run with `-tags fluent`).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	"github.com/v0lka/sp4rk/tools/mcp"
)

func run() error {
	// Create a directory the MCP filesystem server will expose.
	mcpRoot, err := os.MkdirTemp("", "sp4rk-mcp-root-*")
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
	// The MCP gateway starts during sp4rk.New(), connects to all configured
	// servers, discovers their tools, and registers them in the ToolRegistry.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		MCP: &sp4rk.MCPConfig{
			Servers: map[string]mcp.ServerEntry{
				// A stdio MCP server: sp4rk launches the command, communicates
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
		// MCP tools default to PolicyUserConfirm, and the registry is
		// FAIL-CLOSED: without a ConfirmFunc, they would be denied. The MCP
		// server here is sandboxed to a throwaway temp directory, so we
		// auto-approve. In a real app, prompt the user instead.
		ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
			fmt.Printf("[auto-approving %s]\n", req.ToolName)
			return tools.ConfirmAllowOnce, nil
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
