//go:build fluent

// Example 05 — MCP Integration (Fluent API)
//
// The same MCP integration as main.go, expressed through the fluent façade.
// The stdio MCP server is registered inline with
// [sp4rk.FrameworkBuilder.MCPStdio]; [sp4rk.FrameworkBuilder.AutoApprove]
// satisfies the fail-closed registry for the MCP-discovered tools.
//
// You need Node.js (npx) to run the filesystem MCP server. If it is
// unavailable, the agent still works with the built-in tools.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/tools"
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

	// sp4rk.NewF: provider + MCP stdio server + auto-approve + built-in tools.
	// MCPStdio registers the mcp.ServerEntry inline (no tuple to unpack). The
	// gateway starts during New(), discovers the server's tools, and adds them
	// to the registry alongside the built-ins.
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", mcpRoot).
		MCPWorkDir(mcpRoot).
		AutoApprove().
		FileTools(). // read_file, list_directory, …
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// List all available tools so we can see what MCP contributed.
	fmt.Println("\nAvailable tools:")
	for _, td := range fw.ToolRegistry().List() {
		fmt.Printf("  [%s] %s — %s\n", td.Source, td.Name, truncate(td.Description, 60))
	}
	fmt.Println()

	ctx := tools.WithWorkspacePath(context.Background(), mcpRoot)
	task := "Read the file greeting.txt in the workspace and tell me its contents."

	result, err := fw.RunF(ctx).
		System("You are a file exploration assistant with access to both " +
			"built-in tools and MCP-provided tools. " +
			"Use any available tool to accomplish the task. " +
			"Call finish when done.").
		Ask(task)
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
