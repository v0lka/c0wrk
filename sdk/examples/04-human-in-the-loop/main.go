// Example 04 — Human-in-the-Loop
//
// Demonstrates how to intercept tool calls for user confirmation before
// destructive operations execute. A custom HITLHandler prompts on stdin
// whenever the agent tries to use a "dangerous" tool (write_file, delete_file,
// bash_exec) and decides whether to allow, deny, or modify the call.
//
// This example is interactive — it reads y/n from stdin.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// ConfirmingHITL implements agent.HITLHandler. It allows all tool calls
// by default but prompts for confirmation on a configurable denylist of
// "dangerous" tool names.
type ConfirmingHITL struct {
	// DangerousTools lists tool names that require explicit confirmation.
	DangerousTools map[string]bool

	// reader is used for stdin prompts.
	reader *bufio.Reader
}

// NewConfirmingHITL creates a HITL handler that confirms calls to the
// given dangerous tool names.
func NewConfirmingHITL(dangerousTools []string) *ConfirmingHITL {
	dangerous := make(map[string]bool, len(dangerousTools))
	for _, name := range dangerousTools {
		dangerous[name] = true
	}
	return &ConfirmingHITL{
		DangerousTools: dangerous,
		reader:         bufio.NewReader(os.Stdin),
	}
}

// OnToolCall is invoked before every tool execution. It can:
//   - Allow the call as-is (Allow=true, ModifiedInput=nil)
//   - Deny the call (Allow=false)
//   - Modify the input (Allow=true, ModifiedInput=non-nil)
func (h *ConfirmingHITL) OnToolCall(_ context.Context, toolName string, input json.RawMessage) (*agent.HITLToolDecision, error) {
	// Non-dangerous tools are allowed immediately.
	if !h.DangerousTools[toolName] {
		return &agent.HITLToolDecision{Allow: true}, nil
	}

	// Pretty-print the tool call for the user.
	fmt.Printf("\n⚠️  APPROVAL REQUIRED\n")
	fmt.Printf("   Tool: %s\n", toolName)
	fmt.Printf("   Input: %s\n", formatJSON(input))
	fmt.Printf("   Allow? [y/N]: ")

	line, _ := h.reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))

	if line == "y" || line == "yes" {
		fmt.Printf("   ✅ Allowed\n")
		return &agent.HITLToolDecision{Allow: true, Reason: "user approved"}, nil
	}

	fmt.Printf("   ❌ Denied\n")
	return &agent.HITLToolDecision{
		Allow:  false,
		Reason: "user denied this tool call",
	}, nil
}

// OnStepLimit is invoked when the agent exhausts its step budget or a
// circuit breaker fires. The handler decides whether to grant more steps
// or terminate execution.
func (h *ConfirmingHITL) OnStepLimit(_ context.Context, currentStep, maxSteps int, reason string) (agent.StepLimitResponse, error) {
	fmt.Printf("\n⏰ STEP LIMIT REACHED (step %d/%d", currentStep, maxSteps)
	if reason != "" {
		fmt.Printf(", reason: %s", reason)
	}
	fmt.Printf(")\n   Grant one more step? [y/N]: ")

	line, _ := h.reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))

	if line == "y" || line == "yes" {
		fmt.Printf("   ✅ One more step granted\n")
		return agent.StepLimitAllowOnce, nil
	}

	fmt.Printf("   🛑 Execution stopped\n")
	return agent.StepLimitDeny, nil
}

// formatJSON pretty-prints a JSON RawMessage for display.
func formatJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	pretty, err := json.MarshalIndent(v, "   ", "  ")
	if err != nil {
		return string(raw)
	}
	return string(pretty)
}

func run() error {
	// Create the Framework with our custom HITL handler.
	// The handler is passed via Config.HITL.
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		Execution: sdk.ExecutionConfig{
			MaxSteps: 10, // low limit to demonstrate OnStepLimit
		},
		// HITL is the human-in-the-loop handler. Nil means defaults
		// (allow all tool calls, deny step extensions).
		HITL: NewConfirmingHITL([]string{
			"write_file",
			"delete_file",
			"create_directory",
			"bash_exec",
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Register tools — including dangerous ones that will trigger confirmation.
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewDeleteFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(agent.NewFinishTool())

	// In this example, confirmation is handled by the HITL handler at the
	// executor level (OnToolCall above). The registry itself is FAIL-CLOSED
	// for PolicyUserConfirm tools, so we explicitly relax the registry-level
	// policy for the tools our HITL handler already gates — otherwise the
	// user would be asked twice (or the registry would deny the call).
	for _, name := range []string{"write_file", "delete_file", "create_directory"} {
		registry.SetPolicyOverride(name, tools.PolicyAlwaysAllow)
	}

	// Set up workspace
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-04-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	fmt.Printf("Workspace: %s\n", workspaceDir)
	fmt.Println("This example is INTERACTIVE — you will be asked to approve tool calls.")
	fmt.Println("Press y + Enter to allow, or just Enter to deny.")
	fmt.Println()

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a file management assistant working in %s.
Create a file called "notes.txt" with some content, then delete it.
Use the available file tools. Call finish when done.`, workspaceDir)
	}

	task := "Create a file called notes.txt with the text 'Hello HITL!', then delete it."

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("\n═══════════════════════════════════════════")
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
