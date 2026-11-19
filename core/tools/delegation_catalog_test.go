package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// catalogProbeTool is a minimal GroupSystem + AlwaysAllow probe: Gate 3
// bypasses every policy check for it, so only the delegation-catalog gate
// (Gate 2c) can stop a call outside a delegated subagent's catalog — exactly
// the smuggled-call channel the gate exists for.
type catalogProbeTool struct {
	*sdktools.BaseTool
}

func newCatalogProbeTool(name string) *catalogProbeTool {
	return &catalogProbeTool{BaseTool: &sdktools.BaseTool{
		ToolGroup:       sdktools.GroupSystem,
		ToolName:        name,
		ToolDescription: "catalog gate probe",
		Schema:          json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Policy:          sdktools.PolicyAlwaysAllow,
	}}
}

func (t *catalogProbeTool) Execute(ctx context.Context, raw json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{Content: t.Name() + " executed"}, nil
}

// TestRegistryDelegationCatalogGate_BlocksOutsideCatalog pins Gate 2c: a call
// under a delegated catalog may only dispatch names the catalog carries —
// GroupSystem tools included, since the system-group bypass below the gate
// would otherwise make conductor-only/infra tools dispatchable by bare name.
// The block's security WARN is intercepted and asserted here (severity,
// payload, single emission — specs/domains/testing.md).
func TestRegistryDelegationCatalogGate_BlocksOutsideCatalog(t *testing.T) {
	var logs bytes.Buffer
	r := NewToolRegistry()
	r.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	r.RegisterWithSource(newCatalogProbeTool("allowed_tool"), "core")
	r.RegisterWithSource(newCatalogProbeTool("secret_tool"), "core")

	catalog := DelegationCatalog{Names: map[string]struct{}{"allowed_tool": {}}}
	ctx := WithDelegationCatalog(context.Background(), catalog)

	res, err := r.Execute(ctx, "secret_tool", nil)
	if err != nil {
		t.Fatalf("Execute(secret_tool) returned a hard error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Execute(secret_tool) = %+v, want a blocked (IsError) result", res)
	}
	if !strings.Contains(res.Content, "not part of this delegated subagent's tool catalog") {
		t.Errorf("blocked content = %q, want the catalog refusal", res.Content)
	}

	logText := logs.String()
	for _, field := range []string{
		`"msg":"security: tool blocked outside the delegated subagent's catalog"`,
		`"tool":"secret_tool"`,
		`"reason":"not_in_delegation_catalog"`,
	} {
		if !strings.Contains(logText, field) {
			t.Errorf("security log missing %s: %s", field, logText)
		}
	}
	if n := strings.Count(logText, `"level":"WARN"`); n != 1 {
		t.Errorf("security WARN emissions = %d, want exactly 1 (allowed_tool must stay silent): %s", n, logText)
	}

	// Inside the catalog: dispatches.
	res, err = r.Execute(ctx, "allowed_tool", nil)
	if err != nil || res.IsError {
		t.Fatalf("Execute(allowed_tool) = (%q, %v), want success", res.Content, err)
	}
	if !strings.Contains(res.Content, "allowed_tool executed") {
		t.Errorf("allowed content = %q", res.Content)
	}
}

// TestRegistryDelegationCatalogGate_EmptyCatalogBlocksEverything pins the
// fail-closed direction: a carried-but-empty catalog (a delegation that
// resolved to no tools) must not silently disable the gate.
func TestRegistryDelegationCatalogGate_EmptyCatalogBlocksEverything(t *testing.T) {
	var logs bytes.Buffer
	r := NewToolRegistry()
	r.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	r.RegisterWithSource(newCatalogProbeTool("probe"), "core")

	ctx := WithDelegationCatalog(context.Background(), DelegationCatalog{Names: nil})
	res, err := r.Execute(ctx, "probe", nil)
	if err != nil {
		t.Fatalf("Execute returned a hard error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Execute with an empty catalog = %+v, want blocked", res)
	}
	if !strings.Contains(logs.String(), `"tool":"probe"`) {
		t.Errorf("security log missing the blocked tool: %s", logs.String())
	}
}

// TestRegistryDelegationCatalogGate_NoCatalogUnrestricted pins the scope of
// the gate: a context WITHOUT a catalog is not under a delegated subagent —
// the Conductor's own dispatches, the E2S loop, and verifier executors stay
// unrestricted (each has its own grant/validation mechanism).
func TestRegistryDelegationCatalogGate_NoCatalogUnrestricted(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterWithSource(newCatalogProbeTool("probe"), "core")

	res, err := r.Execute(context.Background(), "probe", nil)
	if err != nil || res.IsError {
		t.Fatalf("Execute without a catalog = (%q, %v), want success (gate must not engage)", res.Content, err)
	}
}
