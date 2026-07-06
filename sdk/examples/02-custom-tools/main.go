// Example 02 — Custom Tools
//
// Demonstrates how to implement a custom tool and register built-in tools
// alongside it. The agent uses a custom "calculator" tool to evaluate
// arithmetic expressions and a built-in "write_file" tool to persist the
// result to disk.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/sdk"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
)

// CalculatorTool evaluates simple arithmetic expressions.
// It embeds tools.BaseTool which provides default implementations of
// Name, Description, InputSchema, DefaultPolicy, and IsUntrusted.
type CalculatorTool struct {
	*tools.BaseTool
}

// NewCalculatorTool creates a new CalculatorTool.
func NewCalculatorTool() *CalculatorTool {
	return &CalculatorTool{BaseTool: &tools.BaseTool{
		ToolName:        "calculator",
		ToolDescription: "Evaluate an arithmetic expression (supports +, -, *, /, parentheses). Example: calculator(expression=\"15 * 37 + 4\")",
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "The arithmetic expression to evaluate, e.g. \"2 + 3 * 4\""
				}
			},
			"required": ["expression"]
		}`),
		Policy: tools.PolicyAlwaysAllow,
	}}
}

// Execute parses the expression and returns the numeric result.
func (t *CalculatorTool) Execute(_ context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}
	if params.Expression == "" {
		return tools.ToolResult{Content: "validation error: expression is required", IsError: true}, nil
	}

	result, err := evaluate(params.Expression)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("evaluation error: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: fmt.Sprintf("%s = %g", params.Expression, result)}, nil
}

func main() {
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
			DefaultModel: "claude-sonnet-4-5",
		},
	})
	if err != nil {
		log.Fatalf("failed to create framework: %v", err)
	}
	defer fw.Shutdown()

	// Register tools — the agent can only use tools that are in the registry.
	registry := fw.ToolRegistry()

	// Built-in tools from sdk/tools/builtins
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewListDirectoryTool())

	// The finish tool (required for task completion)
	registry.Register(agent.NewFinishTool())

	// Our custom calculator tool
	registry.Register(NewCalculatorTool())

	// Set up a workspace directory so file tools know where to read/write.
	workspaceDir, err := os.MkdirTemp("", "c0wrk-example-02-*")
	if err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	fmt.Println("Workspace:", workspaceDir)

	// Inject the workspace path into the context. Built-in file tools
	// retrieve it via tools.WorkspacePathFrom(ctx).
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a coding assistant working in the directory %s.
You have a calculator tool for arithmetic and file tools for reading/writing files.
When you have completed the task, call the finish tool with a summary.`, workspaceDir)
	}

	task := fmt.Sprintf(
		"Use the calculator tool to compute 17 * 23 + 100, "+
			"then write the result to a file called 'result.txt' in the workspace (%s). "+
			"Finally, read the file back to verify its contents.",
		workspaceDir,
	)

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		log.Fatalf("execution failed: %v", err)
	}

	fmt.Println("\nStatus:", result.Status)
	fmt.Println("Output:", result.Output)

	// Verify the file was created
	resultPath := filepath.Join(workspaceDir, "result.txt")
	if content, err := os.ReadFile(resultPath); err == nil {
		fmt.Printf("\nFile %s contains:\n%s\n", resultPath, string(content))
	} else {
		fmt.Printf("\nFile %s was not created: %v\n", resultPath, err)
	}
}
