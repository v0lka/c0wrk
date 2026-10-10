package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/e2s"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

type durableMCPProbe struct {
	sdktools.BaseTool
	calls int
}

func (p *durableMCPProbe) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	p.calls++
	return sdktools.ToolResult{Content: "ok"}, nil
}

type failingMCPStore struct {
	mockTaskStore
	readErr, writeErr error
}

func (s *failingMCPStore) LoadMCPMentions(ctx context.Context, id string) ([]string, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return s.mockTaskStore.LoadMCPMentions(ctx, id)
}
func (s *failingMCPStore) PersistMCPMentions(ctx context.Context, id string, names []string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.mockTaskStore.PersistMCPMentions(ctx, id, names)
}

func TestPrepareTaskMCP_DurableUnionIsolationAndRecovery(t *testing.T) {
	store := &failingMCPStore{}
	bb := newDelegSpecBB("actual-task", store)
	bb.SetOriginalRequest("work")
	o := &Orchestrator{taskStore: store, config: OrchestratorConfig{MCPServerModes: map[string]string{"a": "manual", "b": "manual"}}}
	stale := coretools.WithGatedMCPServers(WithUserMCPServers(context.Background(), []string{"foreign"}), map[string]bool{"foreign": true})
	for _, extra := range [][]string{{"b", "a", "b"}, nil, {"c"}} {
		ctx, err := o.prepareTaskMCP(stale, bb, extra)
		if err != nil {
			t.Fatalf("prepareTaskMCP(%v) = %v, want nil", extra, err)
		}
		if coretools.GatedMCPServersFromContext(ctx)["foreign"] {
			t.Error("prepareTaskMCP retained foreign gate")
		}
	}
	// Restart-equivalent: a new orchestrator has no local mention cache.
	restarted := &Orchestrator{taskStore: store, config: o.config}
	ctx, err := restarted.prepareTaskMCP(stale, bb, nil)
	if err != nil || !reflect.DeepEqual(UserMCPServersFromContext(ctx), []string{"a", "b", "c"}) {
		t.Fatalf("prepareTaskMCP(restart) = (%v, %v), want durable a,b,c", ctx, err)
	}
	fresh := newDelegSpecBB("fresh-task", store)
	freshCtx, err := restarted.prepareTaskMCP(ctx, fresh, nil)
	if err != nil || len(UserMCPServersFromContext(freshCtx)) != 0 || !coretools.GatedMCPServersFromContext(freshCtx)["a"] {
		t.Fatalf("prepareTaskMCP(fresh) = (%v, %v), want empty names and gated a", freshCtx, err)
	}
	store.writeErr = errors.New("write unavailable")
	if _, err := restarted.prepareTaskMCP(stale, bb, []string{"d"}); !errors.Is(err, ErrMCPAuthorizationState) {
		t.Errorf("prepareTaskMCP(write failure) = %v, want authorization error", err)
	}
	store.writeErr = nil
	store.readErr = errors.New("read unavailable")
	if _, err := restarted.prepareTaskMCP(stale, bb, nil); !errors.Is(err, ErrMCPAuthorizationState) {
		t.Errorf("prepareTaskMCP(read failure) = %v, want authorization error", err)
	}
	store.readErr = nil
	ctx, err = restarted.prepareTaskMCP(stale, bb, nil)
	if err != nil || !reflect.DeepEqual(UserMCPServersFromContext(ctx), []string{"a", "b", "c"}) {
		t.Fatalf("prepareTaskMCP(recovered) = (%v,%v), want no failed write", ctx, err)
	}
}

func TestResumeMCP_PreparationFailurePreventsWaveAndLLM(t *testing.T) {
	calls := 0
	caller := &mockLLMCaller{callFn: func(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
		calls++
		return executorFinishResponse("done"), nil
	}}
	o, emitter, _, _ := newFunnelOrchestrator(t, caller)
	store := &failingMCPStore{readErr: errors.New("database unavailable")}
	o.SetTaskStore(store)
	bb := newUnitLedgerBB("task", store)
	bb.SetOriginalRequest("work")
	seedLedgerUnit(t, bb, "", "del_1", units.UnitKindSubagent, units.UnitStatusInterrupted, "", 0, coretools.DelegationTask{ID: "del_1", Task: "work"}, nil)
	if _, err := o.Resume(context.Background(), bb, nil, t.TempDir(), nil, nil, ""); !errors.Is(err, ErrMCPAuthorizationState) {
		t.Fatalf("Resume(read failure) = %v, want authorization error", err)
	}
	if calls != 0 || emitter.launchCount("del_1") != 0 {
		t.Fatalf("Resume(read failure) calls=%d launches=%d, want 0,0", calls, emitter.launchCount("del_1"))
	}
	store.readErr = nil
	if _, err := o.Resume(context.Background(), bb, nil, t.TempDir(), nil, nil, ""); err != nil {
		t.Fatalf("Resume(recovered) = %v, want nil", err)
	}
	if calls != 2 || emitter.launchCount("del_1") != 1 {
		t.Errorf("Resume(recovered) calls=%d launches=%d, want 2,1", calls, emitter.launchCount("del_1"))
	}
}

func TestResumeMCP_WaveCatalogDispatchAndCurrentSnapshot(t *testing.T) {
	for _, kind := range []string{"plan", "delegate-all", "delegate-group", "e2s-delegate"} {
		t.Run(kind, func(t *testing.T) {
			store := newE2SCheckpointStore()
			if err := store.PersistMCPMentions(context.Background(), "task", []string{"selected", "disabled"}); err != nil {
				t.Fatal(err)
			}
			modes := map[string]string{"selected": "manual", "unmentioned": "manual", "disabled": "disabled"}
			snapshots, calls := 0, 0
			var o *Orchestrator
			caller := &mockLLMCaller{callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
				calls++
				catalog := make(map[string]bool)
				for _, tool := range req.Tools {
					catalog[tool.Name] = true
				}
				if kind == "e2s-delegate" && calls == 4 {
					for _, message := range req.Messages {
						for _, name := range []string{"selected_probe", "unmentioned_probe", "disabled_probe"} {
							if strings.Contains(message.Content, name) {
								catalog[name] = true
							}
						}
					}
				}
				for _, name := range []string{"unmentioned_probe", "disabled_probe"} {
					if catalog[name] {
						t.Errorf("Resume(%s) catalog contains gated %s", kind, name)
					}
				}
				if !catalog["selected_probe"] {
					t.Errorf("Resume(%s) catalog lacks durable selected_probe", kind)
				}
				if calls == 1 {
					modes = map[string]string{"selected": "disabled"}
					return assistantToolCall("stale1", "unmentioned_probe", `{}`), nil
				}
				if calls == 2 {
					return assistantToolCall("stale2", "disabled_probe", `{}`), nil
				}

				if kind == "e2s-delegate" && calls == 4 {
					return e2sFinishResponse("finish", "done"), nil
				}
				return executorFinishResponse("done"), nil
			}}
			var emitter *launchRecorder
			o, emitter, _, _ = newFunnelOrchestrator(t, caller)
			o.SetTaskStore(store)
			r := coretools.NewToolRegistry()
			r.Register(coretools.NewDeclarePlanTool(nil))
			r.Register(coretools.NewExecutePlanTool())
			r.Register(coretools.NewDelegateTool())
			o.toolRegistry, o.toolExec, o.coreToolRegistry = r.ToolRegistry, r, r
			o.config.MCPServerModesResolver = func() map[string]string { snapshots++; return modes }
			logger := expectedCoreLogger(t,
				expectedCoreDiagnostic{level: "WARN", message: "security: tool blocked by MCP server mode gating", fields: map[string]string{"tool": "unmentioned_probe", "server": "unmentioned"}},
				expectedCoreDiagnostic{level: "WARN", message: "security: tool blocked by MCP server mode gating", fields: map[string]string{"tool": "disabled_probe", "server": "disabled"}})
			r.SetLogger(logger)
			r.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{sdktools.GroupRemoteMCP: sdktools.PolicyAlwaysAllow})
			probes := make([]*durableMCPProbe, 0, 3)
			for _, server := range []string{"selected", "unmentioned", "disabled"} {
				probe := &durableMCPProbe{BaseTool: sdktools.BaseTool{ToolName: server + "_probe", Schema: json.RawMessage(`{"type":"object"}`), ToolGroup: sdktools.GroupRemoteMCP}}
				if err := o.toolRegistry.RegisterWithSourceCategory(probe, server, sdktools.SourceCategoryMCP); err != nil {
					t.Fatal(err)
				}
				probes = append(probes, probe)
			}
			bb := newUnitLedgerBB("task", store)
			bb.SetOriginalRequest("work")
			if kind == "e2s-delegate" {
				// The `all` preset puts the MCP probes in the E2S catalog so the
				// assertions target the per-server MODE gate, not the slim
				// default catalog that excludes MCP tools.
				o.config.E2S.Tools = BuilderE2SToolsConfig{Preset: E2SToolsPresetAll}
				es := e2s.NewE2SState("work", time.Now())
				if err := store.PersistE2SState("task", &es); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "plan" {
				bb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Summary: "work", Description: "work"}}})
			} else {
				var grant []any
				if kind == "delegate-group" {
					grant = []any{"remote-mcp"}
				}
				seedLedgerUnit(t, bb, "", "s1", units.UnitKindSubagent, units.UnitStatusInterrupted, "", 0, coretools.DelegationTask{ID: "s1", Summary: "work", Task: "work", Tools: grant}, nil)
			}
			if _, err := o.Resume(context.Background(), bb, nil, t.TempDir(), nil, nil, ""); err != nil {
				t.Fatalf("Resume(%s) = %v, want nil", kind, err)
			}
			if calls != 4 || snapshots != 1 {
				t.Errorf("Resume(%s) calls=%d snapshots=%d, want 4,1", kind, calls, snapshots)
			}
			if kind != "plan" && emitter.launchCount("s1") != 1 {
				t.Errorf("Resume(%s) launches=%d, want 1", kind, emitter.launchCount("s1"))
			}
			if sr, ok := bb.GetStepResult("s1"); !ok || sr.Error != nil || sr.FullOutput != "done" {
				t.Errorf("Resume(%s) s1=%+v (ok=%v), want done", kind, sr, ok)
			}

			for _, probe := range probes {
				if probe.calls != 0 {
					t.Errorf("Resume(%s) %s side effects=%d, want 0", kind, probe.Name(), probe.calls)
				}
			}
			next, err := o.prepareTaskMCP(context.Background(), bb, nil)
			if err != nil || !coretools.GatedMCPServersFromContext(next)["selected"] {
				t.Errorf("prepareTaskMCP(next entry) = (%v,%v), want selected disabled", next, err)
			}
		})
	}
}

func TestResumeMCP_E2SDurableDirective(t *testing.T) {
	store := newE2SCheckpointStore()
	if err := store.PersistMCPMentions(context.Background(), "task", []string{"selected"}); err != nil {
		t.Fatal(err)
	}
	bb := newDelegSpecBB("task", store)
	bb.SetOriginalRequest("work")
	calls := 0
	caller := &mockLLMCaller{callFn: func(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
		calls++
		var prompt strings.Builder
		for _, message := range req.Messages {
			prompt.WriteString(message.Content)
		}
		if strings.Contains(prompt.String(), "Requested MCP Servers") {
			t.Errorf("E2S Resume prompt renders a server directive — E2S renders no server sections; the durable mention gates the catalog instead")
		}
		return e2sFinishResponse("finish", "done"), nil
	}}
	o, _, _, _ := newFunnelOrchestrator(t, caller)
	o.SetTaskStore(store)
	o.config.MCPServerModesResolver = func() map[string]string { return map[string]string{"selected": "manual"} }
	es := e2s.NewE2SState("work", time.Now())
	if err := store.PersistE2SState("task", &es); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Resume(context.Background(), bb, nil, t.TempDir(), nil, nil, ""); err != nil {
		t.Fatalf("E2S Resume = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("E2S Resume calls=%d, want 1", calls)
	}
	// The durable mention survives the restart: the common task preparation
	// re-derives the context from the persisted union, so the mentioned manual
	// server is ALLOWED for the resumed run (it joins the user-server set —
	// only unmentioned manual servers land in the gated set) even though no
	// directive renders.
	next, err := o.prepareTaskMCP(context.Background(), bb, nil)
	if err != nil {
		t.Fatalf("prepareTaskMCP(after E2S Resume) = %v, want nil", err)
	}
	if got := UserMCPServersFromContext(next); !slices.Contains(got, "selected") {
		t.Errorf("UserMCPServersFromContext(after E2S Resume) = %v, want the durable selected mention", got)
	}
}
