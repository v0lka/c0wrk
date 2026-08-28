package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// judgeFakeProvider answers every judge request with a strict-judge ALLOW and
// records the model it was asked for, so tests can prove WHICH judge instance
// evaluated a request (session-pinned vs shared-registry fallback).
type judgeFakeProvider struct {
	name     string
	gotModel string
}

func (p *judgeFakeProvider) ChatCompletion(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.gotModel = req.Model
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: "VERDICT: ALLOW\nREASON: benign test command"},
		StopReason: "end_turn",
	}, nil
}

func (p *judgeFakeProvider) Name() string { return p.name }

// TestEvaluateJudgeForSession verifies the session-pinning path of the manual
// judge evaluation (ADR-028): a pending-confirmation evaluation for a known
// session runs on the SESSION registry's judge — the one bound to the
// session's own router — and an unknown session falls back to the shared
// registry's judge path (EvaluateJudge).
func TestEvaluateJudgeForSession(t *testing.T) {
	prov := &judgeFakeProvider{name: "sessionProv"}
	judge := sdktools.NewToolJudgeFromConfig(sdktools.JudgeConfig{
		Model:        "session-model",
		DefaultModel: "session-model",
		Provider:     prov,
		MaxCacheSize: 8,
	}, nil)
	if judge == nil {
		t.Fatal("failed to build session judge from fake provider")
	}

	factory := session.OrchestratorFactory(func(_ core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		reg := coretools.NewToolRegistry()
		reg.SetJudge(judge)
		return core.NewOrchestrator(core.OrchestratorConfig{Model: "session-model"}, core.OrchestratorDeps{CoreToolRegistry: reg}), nil
	})
	manager := session.NewManager(factory, func(session.Event) {}, t.TempDir())
	t.Cleanup(manager.Shutdown)

	// Zero-value builder: its WaitReady blocks until ctx.Done, so any request
	// that REACHES the shared-registry fallback fails with "judge not
	// available" — a deterministic signal that the fallback branch ran.
	app := &Application{manager: manager, builder: &core.OrchestratorBuilder{}}

	info, err := manager.CreateSession("proj", t.TempDir())
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	t.Run("known session evaluates on the session judge", func(t *testing.T) {
		verdict, reasoning, err := app.EvaluateJudgeForSession(context.Background(), info.ID, "bash_exec", json.RawMessage(`{"command":"ls"}`), "test task context")
		if err != nil {
			t.Fatalf("EvaluateJudgeForSession(session %s) error = %v, want nil", info.ID, err)
		}
		if verdict != sdktools.VerdictAllow {
			t.Errorf("EvaluateJudgeForSession verdict = %v, want VerdictAllow", verdict)
		}
		if !strings.HasPrefix(reasoning, "SAFE: ") {
			t.Errorf("EvaluateJudgeForSession reasoning = %q, want \"SAFE: \" prefix", reasoning)
		}
		if prov.gotModel != "session-model" {
			t.Errorf("session judge provider got model %q, want \"session-model\" (the session-pinned judge, not a fallback)", prov.gotModel)
		}
	})

	t.Run("unknown session falls back to the shared judge", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, _, err := app.EvaluateJudgeForSession(ctx, "no-such-session", "bash_exec", json.RawMessage(`{"command":"ls"}`), "test task context")
		if err == nil || !strings.Contains(err.Error(), "judge not available") {
			t.Errorf("EvaluateJudgeForSession(unknown session) error = %v, want \"judge not available\" (shared-registry fallback reached)", err)
		}
	})
}
