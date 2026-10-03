package core

import (
	"context"
	"log/slog"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

func TestServiceOutputTokenBudget(t *testing.T) {
	for _, tt := range []struct {
		limit  int
		effort string
		want   int
	}{
		{30, "low", 2048}, {500, "On", 2048}, {2000, "MINIMAL", 2048},
		{2048, "low", 2048}, {4096, "high", 4096}, {0, "low", 0},
		{30, "Off", 30}, {30, "off", 30}, {30, "none", 30}, {30, "NONE", 30}, {30, "", 30},
	} {
		if got := serviceOutputTokenBudget(tt.limit, tt.effort); got != tt.want {
			t.Errorf("serviceOutputTokenBudget(%d, %q) = %d, want %d", tt.limit, tt.effort, got, tt.want)
		}
	}
}

// TestOneshotServices_ModelMatrix exercises the actual service entry points,
// not just the tier helper, across all supported reasoning families.
func TestOneshotServices_ModelMatrix(t *testing.T) {
	models := []struct {
		model string
		off   string
		min   string
	}{
		{"glm-5.3", "low", "low"}, {"Z-ai-API/GLM-5.3", "low", "low"},
		{"zai-org/glm-5.3-flash", "low", "low"}, {"glm-5.2", "none", "high"},
		{"glm-4.7", "Off", "On"}, {"gpt-5", "minimal", "minimal"},
		{"gpt-5.1", "none", "low"}, {"gpt-5.3-codex", "low", "low"},
		{"gpt-5-pro", "high", "high"}, {"gpt-5.2-pro", "medium", "medium"},
		{"qwen3.8-max", "Off", "low"}, {"Bonsai 2 27B", "Off", "low"},
		{"deepseek-v4-pro", "Off", "High"}, {"claude-sonnet-4-5", "Off", "On"},
		{"gemini-2.5-pro", "MINIMAL", "MINIMAL"}, {"kimi-k3", "low", "low"},
		{"kimi-k2", "", ""}, {"totally-unknown-model", "", ""},
	}
	for _, model := range models {
		t.Run(model.model, func(t *testing.T) {
			b := &OrchestratorBuilder{logger: slog.Default(), reasoningEffort: "max"}
			cases := []struct {
				name string
				want string
				run  func(*mockLLMCaller) error
			}{
				{"title", model.off, func(c *mockLLMCaller) error {
					_, err := generateTitleWithCaller(context.Background(), c, nil, model.model, b.log(), "fix a bug", nil)
					return err
				}},
				{"commit", model.off, func(c *mockLLMCaller) error {
					_, err := b.generateCommitMessageWithCaller(context.Background(), c, "test", model.model, "diff")
					return err
				}},
				{"extract", model.min, func(c *mockLLMCaller) error {
					_, err := b.optimizeExtract(context.Background(), c, model.model, "fix a bug")
					return err
				}},
				{"rewrite", model.min, func(c *mockLLMCaller) error {
					_, err := b.optimizeRewrite(context.Background(), c, model.model, "fix a bug")
					return err
				}},
				{"automatic compaction", model.min, func(c *mockLLMCaller) error {
					router, err := llm.NewRouter(context.Background(), llm.RouterConfig{Providers: []llm.ProviderEntry{{
						Name: "test", ProviderType: "openai", APIKey: "test", Models: []string{llm.BareModel(model.model)},
					}}}, nil)
					if err != nil {
						return err
					}
					cfg := &BuilderConfig{}
					cfg.Executor.Compaction.Summarization.KeepLast = 1
					cfg.Executor.Compaction.Summarization.BlockSize = 100
					factory := b.buildContextFactory(llm.NewTrackingCaller(c, llm.NewUsageTracker()), cfg, nil, nil, router)
					cm := factory("system", llm.ModelMetadata{ContextWindow: 8192, OutputLimit: 2048, TokenizerType: "approximate"}, "summarization")
					cm.AddStep(agent.Step{Thought: "old step"})
					cm.AddStep(agent.Step{Thought: "recent step"})
					cm.Compact(context.Background())
					return nil
				}},
				{"manual compaction", model.min, func(c *mockLLMCaller) error {
					o, _ := newCompactionTestOrchestrator(c)
					o.config.Model = model.model
					_, err := o.manualCompactionDeps().Summarize(context.Background(), "history")
					return err
				}},
			}
			for _, service := range cases {
				t.Run(service.name, func(t *testing.T) {
					c := &mockLLMCaller{responses: []*llm.ChatResponse{{Message: llm.Message{Content: "fix: repair bug"}}}}
					if service.name == "extract" {
						c.responses[0].Message.Content = `{"translated":"fix a bug","keywords":["bug"]}`
					}
					if err := service.run(c); err != nil {
						t.Fatalf("%s(%q) error = %v, want nil", service.name, model.model, err)
					}
					if len(c.calls) != 1 {
						t.Fatalf("%s(%q) calls = %d, want 1", service.name, model.model, len(c.calls))
					}
					req := c.calls[0]
					if req.ReasoningEffort != service.want {
						t.Errorf("%s(%q).ReasoningEffort = %q, want %q", service.name, model.model, req.ReasoningEffort, service.want)
					}
					if service.name == "title" && service.want == "low" && req.MaxTokens < 2048 {
						t.Errorf("title(%q).MaxTokens = %d, want >=2048 for mandatory reasoning", model.model, req.MaxTokens)
					}
				})
			}
		})
	}
}
