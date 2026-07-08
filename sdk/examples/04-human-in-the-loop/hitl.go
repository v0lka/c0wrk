package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/v0lka/sp4rk/agent"
)

// ConfirmingHITL implements agent.HITLHandler. It allows all tool calls
// by default but prompts for confirmation on a configurable denylist of
// "dangerous" tool names.
//
// This type lives in a tagless file so that both the classic (main.go) and
// fluent (main_fluent.go) example variants can use it.
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
