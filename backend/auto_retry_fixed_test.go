package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

func fixedRetryRequest(provider string, seconds *int) LLMFullConfigRequest {
	req := LLMFullConfigRequest{}
	p := &ProviderConfigRequest{AutoRetrySeconds: seconds}
	switch provider {
	case "anthropic":
		req.Anthropic = p
	case "chatgpt":
		req.ChatGPT = p
	case "kimi":
		req.Kimi = p
	}
	return req
}

func TestFixedAutoRetryConfigLifecycle(t *testing.T) {
	for _, provider := range []string{"anthropic", "chatgpt", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			f, _, path, app := newTestAPIWithApp(t)
			for _, tc := range []struct {
				name    string
				seconds *int
				want    int
			}{
				{"enable", intPtr(30), 30}, {"nil preserves", nil, 30}, {"boundary", intPtr(config.MaxAutoRetrySeconds()), config.MaxAutoRetrySeconds()}, {"zero disables", intPtr(0), 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := f.UpdateLLMConfig(fixedRetryRequest(provider, tc.seconds)); err != nil {
						t.Fatalf("UpdateLLMConfig(%s): %v", provider, err)
					}
					resp := f.buildLLMResponse()
					got := map[string]int{"anthropic": resp.Anthropic.AutoRetrySeconds, "chatgpt": resp.ChatGPT.AutoRetrySeconds, "kimi": resp.Kimi.AutoRetrySeconds}[provider]
					if got != tc.want {
						t.Errorf("GetConfig(%s) interval = %d, want %d", provider, got, tc.want)
					}
					if got := app.autoRetryIntervalSnapshot()[provider]; got != tc.want {
						t.Errorf("snapshot[%s] = %d, want %d", provider, got, tc.want)
					}
					loaded, err := config.Load(path)
					if err != nil {
						t.Fatal(err)
					}
					if got := buildAutoRetrySnapshot(loaded).providerIntervals[provider]; got != tc.want {
						t.Errorf("Load(%s) interval = %d, want %d", provider, got, tc.want)
					}
				})
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := app.autoRetryIntervals.Load()
			for _, seconds := range []int{-1, config.MaxAutoRetrySeconds() + 1} {
				if err := f.UpdateLLMConfig(fixedRetryRequest(provider, intPtr(seconds))); err == nil {
					t.Errorf("UpdateLLMConfig(%s, %d) succeeded, want rejection", provider, seconds)
				}
				if app.autoRetryIntervals.Load() != snapshot {
					t.Error("rejected update replaced snapshot")
				}
				if got := buildAutoRetrySnapshot(f.config).providerIntervals[provider]; got != 0 {
					t.Errorf("rejected update interval = %d, want 0", got)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, before) {
					t.Error("rejected update changed disk")
				}
			}
			// A file used as the parent guarantees persist failure on every platform.
			blocker := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
				t.Fatal(err)
			}
			f.configPath = filepath.Join(blocker, "config.yaml")
			if err := f.UpdateLLMConfig(fixedRetryRequest(provider, intPtr(60))); err == nil {
				t.Fatal("UpdateLLMConfig(blocked path) succeeded, want persist error")
			}
			if got := app.autoRetryIntervalSnapshot()[provider]; got != 0 {
				t.Errorf("rollback snapshot[%s] = %d, want 0", provider, got)
			}
			if got := buildAutoRetrySnapshot(f.config).providerIntervals[provider]; got != 0 {
				t.Errorf("rollback config[%s] = %d, want 0", provider, got)
			}
		})
	}
}

func TestBuildAutoRetrySnapshotLegacyKimi(t *testing.T) {
	for _, transport := range []string{"openai", "anthropic"} {
		t.Run(transport, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.LLM.Anthropic.AutoRetrySeconds = 5
			cfg.LLM.ChatGPT.AutoRetrySeconds = 10
			cfg.LLM.Kimi.AutoRetrySeconds = 300
			if transport == "openai" {
				cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{"kimi": {AutoRetrySeconds: 30}}
			} else {
				cfg.LLM.AnthropicCompatible = map[string]config.AnthropicCompatibleConfig{"kimi": {AutoRetrySeconds: 30}}
			}
			got := buildAutoRetrySnapshot(cfg).providerIntervals
			for provider, want := range map[string]int{"anthropic": 5, "chatgpt": 10, "kimi": 30, "unknown": 0} {
				if got[provider] != want {
					t.Errorf("buildAutoRetrySnapshot(%s).providerIntervals[%s] = %d, want %d", transport, provider, got[provider], want)
				}
			}
		})
	}
	if got := buildAutoRetrySnapshot(nil).providerIntervals["unknown"]; got != 0 {
		t.Errorf("buildAutoRetrySnapshot(nil).providerIntervals[unknown] = %d, want 0", got)
	}
}

func TestFixedAutoRetryInvalidHiddenKimi(t *testing.T) {
	f, _, path, app := newTestAPIWithApp(t)
	f.config.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{"kimi": {Models: []string{"legacy"}, AutoRetrySeconds: 30}}
	app.publishAutoRetryIntervals(f.config)
	if err := f.UpdateLLMConfig(fixedRetryRequest("kimi", intPtr(-1))); err == nil {
		t.Fatal("UpdateLLMConfig(hidden kimi, -1) succeeded, want rejection")
	}
	// The compatible entry owns the snapshot under legacy precedence, so a
	// snapshot assertion cannot fail here for any fixed-field value; check
	// the hidden fixed interval itself.
	if got := f.config.LLM.Kimi.AutoRetrySeconds; got != 0 {
		t.Errorf("rejected update left hidden fixed kimi interval = %d, want 0", got)
	}
	if got := app.autoRetryIntervalSnapshot()["kimi"]; got != 30 {
		t.Errorf("snapshot[kimi] = %d, want 30 (compatible entry keeps precedence)", got)
	}

	// Accept path: a valid hidden fixed interval is stored and persists even
	// though the compatible entry keeps snapshot precedence (ADR-127).
	if err := f.UpdateLLMConfig(fixedRetryRequest("kimi", intPtr(300))); err != nil {
		t.Fatalf("UpdateLLMConfig(hidden kimi, 300): %v", err)
	}
	if got := f.config.LLM.Kimi.AutoRetrySeconds; got != 300 {
		t.Errorf("hidden fixed kimi interval = %d, want 300", got)
	}
	if got := app.autoRetryIntervalSnapshot()["kimi"]; got != 30 {
		t.Errorf("snapshot[kimi] = %d, want 30 (compatible precedence over the hidden fixed entry)", got)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.LLM.Kimi.AutoRetrySeconds; got != 300 {
		t.Errorf("loaded hidden fixed kimi interval = %d, want 300", got)
	}
	if got := buildAutoRetrySnapshot(loaded).providerIntervals["kimi"]; got != 30 {
		t.Errorf("loaded snapshot[kimi] = %d, want 30 (compatible precedence)", got)
	}
}
