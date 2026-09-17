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

// judgePromptCaptureProvider snapshots every LLM request so tests can assert
// what the ADVISORY judge actually saw in its prompt (the user prompt is the
// templated markdown; the shell-analysis digest renders as its
// "## Static Analysis Report" block).
type judgePromptCaptureProvider struct {
	name     string
	response string
	requests []llm.ChatRequest
}

func (p *judgePromptCaptureProvider) ChatCompletion(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p.requests = append(p.requests, req)
	return &llm.ChatResponse{
		Message:    llm.Message{Role: "assistant", Content: p.response},
		StopReason: "end_turn",
	}, nil
}

func (p *judgePromptCaptureProvider) Name() string { return p.name }

func (p *judgePromptCaptureProvider) promptText() string {
	var b strings.Builder
	for _, req := range p.requests {
		for _, msg := range req.Messages {
			b.WriteString(msg.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestEvaluateJudgeWith_AdvisoryPathIncludesShellDigest proves the Ask-Agent
// advisory path (evaluateJudgeWith) attaches the deterministic flowsh digest
// for shell tools: the advisory judge's user prompt carries the
// "## Static Analysis Report" block with the digest document — even for a
// benign command with no fired criterion — behind the shell_analysis
// untrusted-content boundary. Non-shell tools must not grow the block.
func TestEvaluateJudgeWith_AdvisoryPathIncludesShellDigest(t *testing.T) {
	newJudge := func() (*sdktools.ToolJudge, *judgePromptCaptureProvider) {
		prov := &judgePromptCaptureProvider{name: "captureProv", response: "VERDICT: ALLOW\nREASON: benign"}
		judge := sdktools.NewToolJudgeFromConfig(sdktools.JudgeConfig{
			Model:        "judge-model",
			DefaultModel: "judge-model",
			Provider:     prov,
			MaxCacheSize: 8,
		}, nil)
		if judge == nil {
			t.Fatal("failed to build judge from capture provider")
		}
		return judge, prov
	}
	ctx := sdktools.WithWorkspacePath(context.Background(), t.TempDir())

	t.Run("bash_exec advisory prompt includes the digest", func(t *testing.T) {
		judge, prov := newJudge()
		verdict, _, err := evaluateJudgeWith(ctx, judge, "bash_exec", json.RawMessage(`{"command":"git status"}`), "check repo state")
		if err != nil {
			t.Fatalf("evaluateJudgeWith error = %v, want nil", err)
		}
		if verdict != sdktools.VerdictAllow {
			t.Errorf("verdict = %v, want VerdictAllow (scripted)", verdict)
		}
		if len(prov.requests) != 1 {
			t.Fatalf("judge LLM calls = %d, want 1", len(prov.requests))
		}
		prompt := prov.promptText()
		for _, marker := range []string{
			"## Static Analysis Report",
			`"schemaVersion":"sp4rk-shell-analysis/v1"`,
			"shell_analysis",
		} {
			if !strings.Contains(prompt, marker) {
				t.Errorf("advisory prompt missing %q (the flowsh digest must reach the Ask-Agent judge)", marker)
			}
		}
	})

	t.Run("non-shell tool advisory prompt has no digest block", func(t *testing.T) {
		judge, prov := newJudge()
		if _, _, err := evaluateJudgeWith(ctx, judge, "write_file", json.RawMessage(`{"path":"/tmp/x.md","content":"hi"}`), "write a note"); err != nil {
			t.Fatalf("evaluateJudgeWith error = %v, want nil", err)
		}
		// The judge SYSTEM prompt teaches the static-analysis section, so the
		// header phrase alone is not proof of a digest; the digest document's
		// schemaVersion signature is.
		if prompt := prov.promptText(); strings.Contains(prompt, "sp4rk-shell-analysis/v1") {
			t.Error("non-shell tool must not grow a shell-analysis digest in the advisory prompt")
		}
	})
}

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
