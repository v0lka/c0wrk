package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// publishMCPModesDuringStartup exercises accepted policy publication without
// dialing a server: reconciliation is canceled while startup is still pending.
func publishMCPModesDuringStartup(t *testing.T, b *OrchestratorBuilder, mode string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &BuilderConfig{MCP: BuilderMCPConfig{Servers: map[string]BuilderMCPServer{
		"live-srv": {Mode: mode},
	}}}
	if err := b.ReconfigureMCP(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconfigureMCP(mode=%q, canceled startup) = %v, want context.Canceled", mode, err)
	}
	// The accepted config must not alias the builder's published map.
	cfg.MCP.Servers["live-srv"] = BuilderMCPServer{Mode: "disabled"}
}

func TestMCPModesLiveSnapshot_ExistingOrchestratorBoundaries(t *testing.T) {
	for _, entry := range []string{"new-task", "resume"} {
		for _, transition := range []struct {
			name, from, to string
		}{
			{name: "auto-to-manual", from: "auto", to: "manual"},
			{name: "manual-to-auto", from: "manual", to: "auto"},
			{name: "manual-to-default", from: "manual", to: ""},
		} {
			t.Run(entry+"/"+transition.name, func(t *testing.T) {
				b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
				publishMCPModesDuringStartup(t, b, transition.from)
				snapshots, calls := 0, 0
				wantMode := transition.from
				caller := &mockLLMCaller{callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
					calls++
					wantGated := wantMode == mcpModeManual
					var catalog []byte
					var err error
					if entry == "new-task" {
						catalog, err = json.Marshal(req.Messages) // E2S embeds the available-tools catalog.
					} else {
						catalog, err = json.Marshal(req.Tools)
					}
					if err != nil {
						t.Errorf("marshal %s catalog = %v, want nil", entry, err)
					}
					if got := strings.Contains(string(catalog), "live_probe"); got == wantGated {
						t.Errorf("%s(mode=%q) catalog contains live_probe=%v, want %v", entry, wantMode, got, !wantGated)
					}
					if got := coretools.GatedMCPServersFromContext(ctx)["live-srv"]; got != wantGated {
						t.Errorf("%s(mode=%q) dispatch gate=%v, want %v", entry, wantMode, got, wantGated)
					}
					if calls == 1 {
						publishMCPModesDuringStartup(t, b, transition.to)
						// Reconfiguration affects the NEXT entry, not this run's context.
						if got := coretools.GatedMCPServersFromContext(ctx)["live-srv"]; got != wantGated {
							t.Errorf("%s in-flight gate after %q save=%v, want unchanged %v", entry, transition.to, got, wantGated)
						}
					}
					if entry == "new-task" {
						return e2sFinishResponse("finish", "done"), nil
					}
					return executorFinishResponse("done"), nil
				}}
				o, _, _, _ := newFunnelOrchestrator(t, caller)
				o.config.MCPServerModes = map[string]string{"live-srv": transition.from} // Intentionally stale session snapshot.
				o.config.MCPServerModesResolver = func() map[string]string {
					snapshots++
					return b.currentMCPServerModes()
				}
				if err := o.toolRegistry.RegisterWithSourceCategory(
					&mockSubagentTool{name: "live_probe", group: sdktools.GroupRemoteMCP},
					"live-srv", sdktools.SourceCategoryMCP); err != nil {
					t.Fatalf("RegisterWithSourceCategory(live_probe) = %v, want nil", err)
				}
				store := &mockTaskStore{}
				bb := newUnitLedgerBB("existing-task", store)
				bb.SetOriginalRequest("work")
				o.SetTaskStore(store)
				if entry == "new-task" {
					o.SetTaskStore(nil) // This case exercises explicit nonpersistent callers.
					o.bbFactory = nil
					o.config.E2S.Enabled = true
					// The `all` preset keeps the MCP probe in the E2S catalog so
					// the assertions target the per-server MODE gate, not the
					// slim default catalog that excludes MCP tools.
					o.config.E2S.Tools = BuilderE2SToolsConfig{Preset: E2SToolsPresetAll}
				}
				for round, mode := range []string{transition.from, transition.to} {
					wantMode = mode
					var result *HandleResult
					var err error
					if entry == "new-task" {
						result, err = o.HandleMessage(context.Background(), "work", "existing-session", HandleOptions{E2S: true})
					} else {
						result, err = o.Resume(context.Background(), bb, nil, "", nil, nil, "")
					}
					if err != nil || result == nil || result.Output != "done" {
						t.Fatalf("%s(round=%d, mode=%q) = (%v, %v), want done and nil error", entry, round, mode, result, err)
					}
					if snapshots != round+1 || calls != round+1 {
						t.Errorf("%s(round=%d) snapshots=%d calls=%d, want %d each", entry, round, snapshots, calls, round+1)
					}
				}
			})
		}
	}
}

func TestMCPModesLiveSnapshot_IndependentMapsAndDirectHelper(t *testing.T) {
	b := &OrchestratorBuilder{mcpDone: make(chan struct{})}
	publishMCPModesDuringStartup(t, b, "manual")
	snapshot := b.currentMCPServerModes()
	snapshot["live-srv"] = "auto"
	if got := b.currentMCPServerModes()["live-srv"]; got != "manual" {
		t.Errorf("currentMCPServerModes(after snapshot mutation) = %q, want manual", got)
	}
	o := &Orchestrator{config: OrchestratorConfig{
		MCPServerModes:         map[string]string{"live-srv": "auto"},
		MCPServerModesResolver: b.currentMCPServerModes,
	}}
	descs := []sdktools.ToolDescriptor{{Name: "live_probe", Source: "live-srv", SourceCategory: sdktools.SourceCategoryMCP}}
	filtered, ctx := o.applyMCPModeGating(context.Background(), descs, nil)
	if len(filtered) != 0 || !coretools.GatedMCPServersFromContext(ctx)["live-srv"] {
		t.Errorf("applyMCPModeGating(live manual, stale auto) = %v, want empty catalog and dispatch gate", filtered)
	}
	publishMCPModesDuringStartup(t, b, "auto")
	filtered, ctx = o.applyMCPModeGating(context.Background(), descs, nil)
	if len(filtered) != 1 || coretools.GatedMCPServersFromContext(ctx)["live-srv"] {
		t.Errorf("applyMCPModeGating(live auto) = %v, want one tool and no dispatch gate", filtered)
	}
}
