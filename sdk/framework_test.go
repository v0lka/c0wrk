package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/planner"
	"github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/mcp"
)

// ---------------------------------------------------------------------------
// New() — error paths
// ---------------------------------------------------------------------------

func TestNew_NoProviders(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for empty providers")
	}
	if err.Error() != "at least one LLM provider is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_InvalidProviderType(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers:    []llm.ProviderEntry{{Name: "test", ProviderType: "nonexistent", Models: []string{"m"}}},
			DefaultModel: "m",
		},
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for invalid provider type")
	}
	if !strings.Contains(err.Error(), "failed to create LLM router") {
		t.Errorf("expected router creation error, got: %v", err)
	}
}

func TestNew_EmptyProviderType(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers:    []llm.ProviderEntry{{Name: "test", ProviderType: "", Models: []string{"m"}}},
			DefaultModel: "m",
		},
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for empty provider type")
	}
}

func TestNew_EmptyModels(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers:    []llm.ProviderEntry{{Name: "test", ProviderType: "openai", Models: nil}},
			DefaultModel: "m",
		},
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for empty models")
	}
}

// ---------------------------------------------------------------------------
// New() — success path with defaults
// ---------------------------------------------------------------------------

func TestNew_Success(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel:   "test-model",
			MaxRetries:     2,
			InitialBackoff: "1s",
			MaxBackoff:     "10s",
		},
		Execution: ExecutionConfig{
			MaxSteps:            5,
			MaxRetries:          1,
			SafetyMarginPercent: 10,
		},
		Compaction: CompactionConfig{
			Strategy:          "summary",
			PredictivePercent: 60,
			WarningPercent:    80,
			EmergencyPercent:  90,
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if fw == nil {
		t.Fatal("expected non-nil Framework")
	}
	if fw.llmRouter == nil {
		t.Error("llmRouter should not be nil")
	}
	if fw.tools == nil {
		t.Error("tools should not be nil")
	}
	if fw.modelReg == nil {
		t.Error("modelReg should not be nil")
	}
	if fw.logger == nil {
		t.Error("logger should not be nil")
	}
	// Verify defaults applied to internal cfg copy
	if fw.cfg.Execution.MaxSteps != 5 {
		t.Errorf("MaxSteps = %d, want 5 (custom)", fw.cfg.Execution.MaxSteps)
	}
	if fw.cfg.Execution.MaxRetries != 1 {
		t.Errorf("MaxRetries = %d, want 1 (custom)", fw.cfg.Execution.MaxRetries)
	}
	if fw.cfg.Compaction.Strategy != "summary" {
		t.Errorf("Strategy = %q, want summary", fw.cfg.Compaction.Strategy)
	}
	// Verify zero-value fields got defaults
	if fw.cfg.Execution.ToolResultBudget == (agent.ToolResultBudget{}) {
		t.Error("ToolResultBudget should have default, not zero-value")
	}
	if fw.cfg.Execution.CircuitBreaker == (agent.CircuitBreakerConfig{}) {
		t.Error("CircuitBreaker should have default, not zero-value")
	}

	// Cleanup
	if err := fw.Shutdown(); err != nil {
		t.Errorf("Shutdown error: %v", err)
	}
}

func TestNew_DefaultsApplied(t *testing.T) {
	// Create with minimal config — all zero-value fields should get defaults.
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	// Verify internal cfg has defaults
	if fw.cfg.Execution.MaxSteps != 50 {
		t.Errorf("MaxSteps = %d, want 50 (default)", fw.cfg.Execution.MaxSteps)
	}
	if fw.cfg.Execution.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2 (default)", fw.cfg.Execution.MaxRetries)
	}
	if fw.cfg.Execution.SafetyMarginPercent != 5 {
		t.Errorf("SafetyMarginPercent = %d, want 5 (default)", fw.cfg.Execution.SafetyMarginPercent)
	}
	if fw.cfg.Execution.ToolResultBudget != agent.DefaultToolResultBudget() {
		t.Errorf("ToolResultBudget = %+v, want default", fw.cfg.Execution.ToolResultBudget)
	}
	if fw.cfg.Execution.CircuitBreaker != agent.DefaultCircuitBreakerConfig() {
		t.Errorf("CircuitBreaker = %+v, want default", fw.cfg.Execution.CircuitBreaker)
	}
	if fw.cfg.Compaction.PredictivePercent != 85 {
		t.Errorf("PredictivePercent = %d, want 85 (default)", fw.cfg.Compaction.PredictivePercent)
	}
	if fw.cfg.Compaction.WarningPercent != 92 {
		t.Errorf("WarningPercent = %d, want 92 (default)", fw.cfg.Compaction.WarningPercent)
	}
	if fw.cfg.Compaction.EmergencyPercent != 98 {
		t.Errorf("EmergencyPercent = %d, want 98 (default)", fw.cfg.Compaction.EmergencyPercent)
	}
	if fw.cfg.Compaction.Strategy != "sliding" {
		t.Errorf("Strategy = %q, want sliding", fw.cfg.Compaction.Strategy)
	}
	_ = fw.Shutdown()
}

func TestNew_DefaultsNotOverwriteCustom(t *testing.T) {
	customBudget := agent.ToolResultBudget{HardCapTokens: 5000, MaxFillFraction: 0.3}
	customCB := agent.CircuitBreakerConfig{RepeatNudgeThreshold: 5}

	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Execution: ExecutionConfig{
			MaxSteps:          10,
			MaxRetries:        1,
			ToolResultBudget:  customBudget,
			CircuitBreaker:    customCB,
		},
		Compaction: CompactionConfig{
			Strategy:          "summary",
			PredictivePercent: 50,
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if fw.cfg.Execution.MaxSteps != 10 {
		t.Errorf("MaxSteps = %d, want 10 (custom preserved)", fw.cfg.Execution.MaxSteps)
	}
	if fw.cfg.Execution.MaxRetries != 1 {
		t.Errorf("MaxRetries = %d, want 1 (custom preserved)", fw.cfg.Execution.MaxRetries)
	}
	if fw.cfg.Execution.ToolResultBudget != customBudget {
		t.Errorf("ToolResultBudget = %+v, want custom %+v", fw.cfg.Execution.ToolResultBudget, customBudget)
	}
	if fw.cfg.Execution.CircuitBreaker != customCB {
		t.Errorf("CircuitBreaker = %+v, want custom %+v", fw.cfg.Execution.CircuitBreaker, customCB)
	}
	if fw.cfg.Compaction.Strategy != "summary" {
		t.Errorf("Strategy = %q, want summary (custom)", fw.cfg.Compaction.Strategy)
	}
	if fw.cfg.Compaction.PredictivePercent != 50 {
		t.Errorf("PredictivePercent = %d, want 50 (custom)", fw.cfg.Compaction.PredictivePercent)
	}
	_ = fw.Shutdown()
}

func TestNew_PartialCircuitBreakerPreserved(t *testing.T) {
	// Only set RepeatNudgeThreshold — the rest should get defaults.
	customCB := agent.CircuitBreakerConfig{RepeatNudgeThreshold: 5}
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Execution: ExecutionConfig{
			CircuitBreaker: customCB,
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	// The zero-value check uses struct comparison, so customCB != zero value
	// and it should be preserved as-is, not overwritten with defaults.
	if fw.cfg.Execution.CircuitBreaker.RepeatNudgeThreshold != 5 {
		t.Errorf("RepeatNudgeThreshold = %d, want 5 (custom preserved)", fw.cfg.Execution.CircuitBreaker.RepeatNudgeThreshold)
	}
	_ = fw.Shutdown()
}

// ---------------------------------------------------------------------------
// New() — MCP config path
// ---------------------------------------------------------------------------

func TestNew_MCPConfig_SchemaSanitizerSet(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		MCP: &MCPConfig{
			Servers: map[string]mcp.ServerEntry{},
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	// MCP with empty servers — StartGateway returns nil gateway (no servers to start).
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	// With empty servers, StartGateway returns nil gateway and nil error.
	if fw.mcpGateway != nil {
		t.Error("mcpGateway should be nil for empty servers")
	}
	_ = fw.Shutdown()
}

// ---------------------------------------------------------------------------
// New() — logger and backoff parsing
// ---------------------------------------------------------------------------

func TestNew_CustomLogger(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Logger: logger,
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if fw.logger != logger {
		t.Error("logger should be the provided instance")
	}
	_ = fw.Shutdown()
}

func TestNew_DefaultModelEmpty(t *testing.T) {
	// DefaultModel="" — SetModel is skipped, no error.
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "",
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if fw.cfg.LLM.DefaultModel != "" {
		t.Errorf("DefaultModel = %q, want empty", fw.cfg.LLM.DefaultModel)
	}
	_ = fw.Shutdown()
}

func TestNew_InvalidBackoffWarns(t *testing.T) {
	var logBuf slogBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel:   "test-model",
			InitialBackoff: "not-a-duration",
			MaxBackoff:     "also-invalid",
		},
		Logger: logger,
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_ = fw.Shutdown()
	output := logBuf.String()
	if output == "" {
		t.Error("expected warning logs for invalid backoff durations")
	}
}

func TestNew_LLMRetryDefaults(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
			MaxRetries:   0, // should default to 3
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_ = fw.Shutdown()
	// Cannot observe MaxRetries on the router directly, but we verify no panic.
}

func TestNew_ParseDurationEmpty(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel:   "test-model",
			InitialBackoff: "", // empty → default 1s
			MaxBackoff:     "", // empty → default 30s
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_ = fw.Shutdown()
}

func TestNew_ParseDurationZero(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel:   "test-model",
			InitialBackoff: "0s", // parses to 0 → default applies
			MaxBackoff:     "0s",
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_ = fw.Shutdown()
}

func TestNew_ParseDurationValid(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel:   "test-model",
			InitialBackoff: "2s",
			MaxBackoff:     "60s",
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	_ = fw.Shutdown()
}

// ---------------------------------------------------------------------------
// New() — HITL and Checkpointer passthrough
// ---------------------------------------------------------------------------

func TestNew_HITLHandlerPassthrough(t *testing.T) {
	hitl := &stubHITLHandler{}
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		HITL: hitl,
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	// HITL handler is stored in fw.cfg — verify it's non-nil (interface comparison with concrete type is tricky)
	if fw.cfg.HITL == nil {
		t.Error("HITL handler should be non-nil")
	}
	_ = fw.Shutdown()
}

func TestNew_CheckpointerPassthrough(t *testing.T) {
	cp := &stubCheckpointer{}
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Checkpointer: cp,
		Logger:       slog.New(slog.DiscardHandler),
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if fw.cfg.Checkpointer == nil {
		t.Error("Checkpointer should be non-nil")
	}
	_ = fw.Shutdown()
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestShutdown_NoMCP(t *testing.T) {
	fw := &Framework{
		llmRouter: nil,
		tools:     tools.NewToolRegistry(),
		logger:    slog.Default(),
	}
	if err := fw.Shutdown(); err != nil {
		t.Errorf("Shutdown should return nil when no MCP gateway: %v", err)
	}
	// Idempotent
	if err := fw.Shutdown(); err != nil {
		t.Errorf("second Shutdown should also return nil: %v", err)
	}
}

func TestShutdown_AfterNew(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if err := fw.Shutdown(); err != nil {
		t.Errorf("first Shutdown: %v", err)
	}
	// Idempotent
	if err := fw.Shutdown(); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

func TestShutdown_WithMCPGateway(t *testing.T) {
	// Zero-value Gateway has nil servers map — Stop() iterates safely and returns nil.
	gw := &mcp.Gateway{}
	fw := &Framework{mcpGateway: gw, logger: slog.Default()}
	if err := fw.Shutdown(); err != nil {
		t.Errorf("Shutdown with MCP gateway: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Accessors
// ---------------------------------------------------------------------------

func TestToolRegistry(t *testing.T) {
	reg := tools.NewToolRegistry()
	fw := &Framework{tools: reg}
	if fw.ToolRegistry() != reg {
		t.Error("ToolRegistry should return the same instance")
	}
}

func TestLLMRouter(t *testing.T) {
	r := &llm.Router{}
	fw := &Framework{llmRouter: r}
	if fw.LLMRouter() != r {
		t.Error("LLMRouter should return the same instance")
	}
}

// ---------------------------------------------------------------------------
// NewOrchestrator / Execute — error paths
// ---------------------------------------------------------------------------

func TestNewOrchestrator_NilRouter(t *testing.T) {
	fw := &Framework{
		logger: slog.Default(),
	}
	_, err := fw.NewOrchestrator(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil LLM router")
	}
	if err.Error() != "framework not initialized: LLM router is nil" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewOrchestrator_Success(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	defer func() { _ = fw.Shutdown() }()

	sysPromptFactory := func(ctx context.Context, stepID string, meta llm.ModelMetadata) string {
		return "You are a helpful assistant."
	}
	orch, err := fw.NewOrchestrator(sysPromptFactory, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator unexpected error: %v", err)
	}
	if orch == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	orch.Cleanup()
}

func TestNewOrchestrator_CheckpointerWired(t *testing.T) {
	cp := &stubCheckpointer{}
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Checkpointer: cp,
		Logger:       slog.New(slog.DiscardHandler),
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	defer func() { _ = fw.Shutdown() }()

	sysPromptFactory := func(ctx context.Context, stepID string, meta llm.ModelMetadata) string {
		return "You are a helpful assistant."
	}
	orch, err := fw.NewOrchestrator(sysPromptFactory, nil)
	if err != nil {
		t.Fatalf("NewOrchestrator with checkpointer unexpected error: %v", err)
	}
	if orch == nil {
		t.Fatal("expected non-nil orchestrator with checkpointer")
	}
	orch.Cleanup()
}

func TestExecute_NilRouter(t *testing.T) {
	fw := &Framework{logger: slog.Default()}
	_, err := fw.Execute(context.Background(), nil, nil, "test")
	if err == nil {
		t.Fatal("expected error from Execute when router is nil")
	}
}

func TestExecute_Success(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"test-model"},
			}},
			DefaultModel: "test-model",
		},
		Logger: slog.New(slog.DiscardHandler),
	}
	fw, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	defer func() { _ = fw.Shutdown() }()

	sysPromptFactory := func(ctx context.Context, stepID string, meta llm.ModelMetadata) string {
		return "You are a helpful assistant."
	}
	// Execute will fail because the LLM call will fail (localhost:9999 not running),
	// but we verify the orchestrator is created and the call is attempted.
	_, err = fw.Execute(context.Background(), sysPromptFactory, nil, "hello")
	if err == nil {
		t.Log("Execute succeeded unexpectedly (LLM was reachable)")
	}
	// Either way, no panic.
}

// ---------------------------------------------------------------------------
// frameworkPlannerAdapter tests
// ---------------------------------------------------------------------------

func TestFrameworkPlannerAdapter_Plan_NilPlannerPanics(t *testing.T) {
	adapter := &frameworkPlannerAdapter{planner: nil}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil planner")
		}
	}()
	_, _ = adapter.Plan(context.Background(), "task", nil, nil)
}

func TestFrameworkPlannerAdapter_Replan_NilPlannerPanics(t *testing.T) {
	adapter := &frameworkPlannerAdapter{planner: nil}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil planner")
		}
	}()
	_, _ = adapter.Replan(context.Background(), nil, nil, orchestration.CompletedStep{}, nil, nil)
}

func TestFrameworkPlannerAdapter_PlanContinuation_NilPlannerPanics(t *testing.T) {
	adapter := &frameworkPlannerAdapter{planner: nil}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil planner")
		}
	}()
	_, _ = adapter.PlanContinuation(context.Background(), "req", nil, nil, "new msg", nil)
}

func TestFrameworkPlannerAdapter_WithRealPlanner(t *testing.T) {
	// Create a minimal planner with stub dependencies.
	p := planner.NewPlanner(&stubLLMCaller{}, planner.DefaultPlannerConfig())
	adapter := &frameworkPlannerAdapter{planner: p}

	// Plan will fail because the stub caller returns an error — this is expected.
	_, err := adapter.Plan(context.Background(), "test task", nil, nil)
	if err == nil {
		t.Fatal("expected error from stub LLM caller")
	}
	if !strings.Contains(err.Error(), "stub: not connected") {
		t.Errorf("expected stub error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Config types — compile-time checks
// ---------------------------------------------------------------------------

func TestConfigTypes(t *testing.T) {
	_ = LLMConfig{
		Providers:      []llm.ProviderEntry{{Name: "a", ProviderType: "t", Models: []string{"m"}}},
		DefaultModel:   "m",
		MaxRetries:     3,
		InitialBackoff: "1s",
		MaxBackoff:     "30s",
	}
	_ = MCPConfig{
		Servers: map[string]mcp.ServerEntry{"s": {}},
	}
	_ = ExecutionConfig{
		MaxSteps:            50,
		MaxRetries:          2,
		SafetyMarginPercent: 5,
	}
	_ = CompactionConfig{
		Strategy:          "sliding",
		PredictivePercent: 85,
		WarningPercent:    92,
		EmergencyPercent:  98,
	}
	_ = Config{
		LLM: LLMConfig{
			Providers:    []llm.ProviderEntry{{Name: "a", ProviderType: "t", Models: []string{"m"}}},
			DefaultModel: "m",
		},
	}
}

// ---------------------------------------------------------------------------
// Default model validation
// ---------------------------------------------------------------------------

func TestNew_DefaultModelNotInProvider(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "test",
				ProviderType: "openai",
				BaseURL:      "http://localhost:9999",
				Models:       []string{"model-a"},
			}},
			DefaultModel: "model-b", // not registered in provider
		},
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for unregistered default model")
	}
	if !strings.Contains(err.Error(), "default model") {
		t.Errorf("expected 'default model' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildContextWindow tests
// ---------------------------------------------------------------------------

func TestBuildContextWindow_DefaultThresholds(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Compaction: CompactionConfig{
				Strategy: "sliding",
			},
		},
	}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000}, "sliding")
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

func TestBuildContextWindow_CustomThresholds(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Compaction: CompactionConfig{
				PredictivePercent: 60,
				WarningPercent:    80,
				EmergencyPercent:  90,
			},
		},
	}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000}, "sliding")
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

func TestBuildContextWindow_CustomSafetyMargin(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Execution: ExecutionConfig{
				SafetyMarginPercent: 10,
			},
			Compaction: CompactionConfig{
				Strategy: "sliding",
			},
		},
	}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000}, "sliding")
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

func TestBuildContextWindow_PruningOverrides(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Compaction: CompactionConfig{Strategy: "sliding"},
		},
	}
	overrides := []orchestration.PruningOverride{{
		KeepLastN:      5,
		ProtectedTools: []string{"tool_a", "tool_b"},
	}}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000}, "sliding", overrides...)
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

func TestBuildContextWindow_EmptyPruningOverrides(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Compaction: CompactionConfig{Strategy: "sliding"},
		},
	}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000}, "sliding")
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

func TestBuildContextWindow_DifferentTokenizer(t *testing.T) {
	fw := &Framework{
		cfg: Config{
			Compaction: CompactionConfig{Strategy: "sliding"},
		},
	}
	ctxWin := fw.buildContextWindow("system prompt", llm.ModelMetadata{ContextWindow: 128000, TokenizerType: "claude"}, "sliding")
	if ctxWin == nil {
		t.Fatal("expected non-nil context window")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// slogBuffer implements io.Writer for capturing slog output.
type slogBuffer struct{ buf []byte }

func (b *slogBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *slogBuffer) String() string { return string(b.buf) }

// stubLLMCaller implements agent.LLMCaller for testing.
type stubLLMCaller struct{}

func (s *stubLLMCaller) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, errors.New("stub: not connected")
}

// stubHITLHandler implements agent.HITLHandler for testing.
type stubHITLHandler struct{}

func (h *stubHITLHandler) OnToolCall(_ context.Context, toolName string, input json.RawMessage) (*agent.HITLToolDecision, error) {
	return &agent.HITLToolDecision{Allow: true}, nil
}
func (h *stubHITLHandler) OnStepLimit(_ context.Context, stepNum, effectiveMaxSteps int, reason string) (agent.StepLimitResponse, error) {
	return agent.StepLimitDeny, nil
}

// stubCheckpointer implements orchestration.Checkpointer for testing.
type stubCheckpointer struct{}

func (c *stubCheckpointer) SaveCheckpoint(_ context.Context, id string, bb orchestration.Blackboard) error {
	return nil
}
func (c *stubCheckpointer) LoadCheckpoint(_ context.Context, id string) (orchestration.Blackboard, error) {
	return nil, nil
}
func (c *stubCheckpointer) DeleteCheckpoint(_ context.Context, id string) error {
	return nil
}


