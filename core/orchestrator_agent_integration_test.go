package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agents"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// writeAgentProfileToDisk writes an AGENT.md with the given frontmatter+body
// into a `.agents/agents/<name>/` directory under parent and returns the
// `.agents/agents` directory path. This mirrors the on-disk layout the runtime
// AgentManager scans (AgentsRelativePath = ".agents/agents").
func writeAgentProfileToDisk(t *testing.T, parent, name, content string) string {
	t.Helper()
	agentsDir := filepath.Join(parent, AgentsRelativePath)
	dir := filepath.Join(agentsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write AGENT.md: %v", err)
	}
	return agentsDir
}

// TestEnrichAgentContext_DiscoversAgentsFromDisk is the integration test for the
// subagent roster wiring: a temp `.agents/agents` directory holds a real
// AGENT.md profile; an AgentManager scans it; enrichAgentContext attaches the
// discovered catalog (and explicit #mentions) to the context; buildSystemPrompt
// then renders BOTH the "Available Subagents" (discovery) and "Requested
// Subagents" (#mention directive) sections from the real on-disk profile.
//
// This closes the unit-test gap: systemprompt_test.go stubs the catalog via
// WithAvailableAgents, while here the catalog flows from disk through the
// manager to the prompt exactly as it does at runtime.
func TestEnrichAgentContext_DiscoversAgentsFromDisk(t *testing.T) {
	ws := t.TempDir()
	// A visible agent and a hidden agent on disk.
	writeAgentProfileToDisk(t, ws, "code-reviewer",
		"---\nname: code-reviewer\ndescription: Reviews Go code for style and correctness.\ntools: read-only\n---\nYou are a meticulous reviewer.\n")
	writeAgentProfileToDisk(t, ws, "internal-helper",
		"---\nname: internal-helper\ndescription: Hidden infrastructure agent.\nhidden: true\n---\nPlumbing only.\n")

	mgr := agents.NewAgentManager([]string{filepath.Join(ws, AgentsRelativePath)}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	o := &Orchestrator{agentManager: mgr}

	// enrichAgentContext is the HandleMessage seam (orchestrator_handle.go):
	// it attaches the discovered catalog + explicit #mentions to the context.
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	ctx = o.enrichAgentContext(ctx, []string{"code-reviewer"})

	// The discovered catalog must include both agents (hidden filtering happens
	// at prompt-render time, not discovery time).
	descriptors := AvailableAgentsFromContext(ctx)
	if len(descriptors) != 2 {
		t.Fatalf("expected 2 discovered agents (incl. hidden), got %d: %+v", len(descriptors), descriptors)
	}
	// The explicit #mention must thread through.
	if got := UserAgentsFromContext(ctx); len(got) != 1 || got[0] != "code-reviewer" {
		t.Fatalf("UserAgents = %v, want [code-reviewer]", got)
	}

	// End-to-end: the assembled Conductor prompt renders both roster sections
	// from the real on-disk profile.
	result := buildSystemPrompt(ctx, "review my PR #code-reviewer", llmModelMetaForTests())

	if !strings.Contains(result, "## Available Subagents") {
		t.Error("prompt should contain the Available Subagents section derived from the on-disk catalog")
	}
	if !strings.Contains(result, "code-reviewer") {
		t.Error("prompt should advertise the discovered code-reviewer agent")
	}
	if !strings.Contains(result, "Reviews Go code for style and correctness.") {
		t.Error("prompt should carry the discovered description from disk")
	}
	// Hidden agent must NOT appear in the public roster.
	if strings.Contains(result, "internal-helper") {
		t.Error("hidden agent must not appear in the public Available Subagents roster")
	}
	// The #mention must drive the directive section.
	if !strings.Contains(result, "## Requested Subagents") {
		t.Error("prompt should contain the Requested Subagents directive for the #mention")
	}
}

// TestEnrichAgentContext_NilManagerIsNoRegression verifies that an orchestrator
// with no agentManager (profiles unavailable) leaves the context untouched —
// the path taken by projects and CHAT mode where no .agents/agents exists.
func TestEnrichAgentContext_NilManagerIsNoRegression(t *testing.T) {
	o := &Orchestrator{} // agentManager == nil

	ctx := o.enrichAgentContext(context.Background(), nil)

	if got := AvailableAgentsFromContext(ctx); got != nil {
		t.Errorf("nil agentManager must leave no available agents, got %v", got)
	}
	if got := UserAgentsFromContext(ctx); got != nil {
		t.Errorf("no #mentions must leave no requested agents, got %v", got)
	}
}

// TestEnrichAgentContext_RequestOnlyStillListsRequest verifies that with a
// discovered catalog but NO #mentions, only the Available (discovery) section
// renders — not the Requested (directive) section. This guards the inverse of
// the main integration test and the no-regression contract for the directive.
func TestEnrichAgentContext_RequestOnlyStillListsRequest(t *testing.T) {
	ws := t.TempDir()
	writeAgentProfileToDisk(t, ws, "code-reviewer",
		"---\nname: code-reviewer\ndescription: Reviews Go code.\n---\nReview.\n")

	mgr := agents.NewAgentManager([]string{filepath.Join(ws, AgentsRelativePath)}, nil)
	if err := mgr.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	o := &Orchestrator{agentManager: mgr}
	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	// No explicit #mentions.
	ctx = o.enrichAgentContext(ctx, nil)

	result := buildSystemPrompt(ctx, "just review this", llmModelMetaForTests())

	if !strings.Contains(result, "## Available Subagents") {
		t.Error("discovered catalog should still render the Available section")
	}
	if strings.Contains(result, "## Requested Subagents") {
		t.Error("no #mentions must NOT render the Requested Subagents section")
	}
}
