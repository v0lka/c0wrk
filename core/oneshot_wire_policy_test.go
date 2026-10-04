package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// TestOneshotGLM53_StrictEndpoint reproduces Z.ai's thinking-locked contract
// through the real service -> Router -> OpenAI-compatible JSON transport.
func TestOneshotGLM53_StrictEndpoint(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		t.Run(model, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var req struct {
					Model           string `json:"model"`
					ReasoningEffort string `json:"reasoning_effort"`
					Thinking        struct {
						Type string `json:"type"`
					} `json:"thinking"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("Decode request = %v, want nil", err)
					http.Error(w, "bad JSON", http.StatusBadRequest)
					return
				}
				if req.Model != model || req.Thinking.Type != "enabled" || req.ReasoningEffort != "low" {
					t.Errorf("GLM service request = %+v, want model %q, thinking enabled, effort low", req, model)
					http.Error(w, "This model always engages in thinking and cannot be disabled; please use low, high, or max", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if _, err := io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"fix: repair service gate"},"finish_reason":"stop"}]}`); err != nil {
					t.Errorf("Write response = %v, want nil", err)
				}
			}))
			t.Cleanup(srv.Close)
			router, err := llm.NewRouter(context.Background(), llm.RouterConfig{MaxRetries: -1, HTTPClient: srv.Client(), Providers: []llm.ProviderEntry{{
				Name: "test", ProviderType: "openai", APIKey: "test", BaseURL: srv.URL, Models: []string{model},
			}}}, llm.NewModelRegistry(nil))
			if err != nil {
				t.Fatalf("NewRouter(%q) = %v, want nil", model, err)
			}
			b := &OrchestratorBuilder{logger: slog.Default()}
			got, err := b.generateCommitMessageWithCaller(context.Background(), router, "test", model, "diff")
			if err != nil || got != "fix: repair service gate" {
				t.Errorf("generateCommitMessageWithCaller(%q) = %q, %v, want valid commit, nil", model, got, err)
			}
			if requests != 1 {
				t.Errorf("generateCommitMessageWithCaller(%q) HTTP requests = %d, want 1", model, requests)
			}
		})
	}
}
