package config

import (
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

func TestValidateSearchBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		raw      string
		wantErr  string
	}{
		{name: "non-searxng provider ignores the value", provider: "tavily", raw: "anything at all"},
		{name: "searxng with https URL", provider: "searxng", raw: "https://searx.internal.example.com"},
		{name: "searxng with http URL and path", provider: "searxng", raw: "http://127.0.0.1:8888/searxng"},
		{name: "searxng with env placeholder", provider: "searxng", raw: "${SEARXNG_URL}"},
		{name: "searxng with default instance", provider: "searxng", raw: websearch.DefaultSearxngBaseURL},
		{name: "searxng empty", provider: "searxng", raw: "", wantErr: "requires search.base_url"},
		{name: "searxng whitespace", provider: "searxng", raw: "   ", wantErr: "requires search.base_url"},
		{name: "searxng not a URL", provider: "searxng", raw: "not a url", wantErr: "must be an absolute http(s) URL"},
		{name: "searxng wrong scheme", provider: "searxng", raw: "ftp://127.0.0.1:8888", wantErr: "must be an absolute http(s) URL"},
		{name: "searxng no host", provider: "searxng", raw: "http://", wantErr: "must be an absolute http(s) URL"},
		{name: "searxng relative path", provider: "searxng", raw: "/searxng", wantErr: "must be an absolute http(s) URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSearchBaseURL(tc.provider, tc.raw)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateSearchBaseURL(%q, %q) = %v, want nil", tc.provider, tc.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateSearchBaseURL(%q, %q) = nil, want it to contain %q", tc.provider, tc.raw, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestApplyDefaults_SearxngBaseURLSeeded verifies the sensible default: an
// operator who only sets provider: searxng gets the local-instance default
// URL, an explicit value always wins, and non-searxng configs are untouched.
func TestApplyDefaults_SearxngBaseURLSeeded(t *testing.T) {
	cfg := &Config{Search: SearchConfig{Provider: "searxng"}}
	ApplyDefaults(cfg)
	if cfg.Search.BaseURL != websearch.DefaultSearxngBaseURL {
		t.Errorf("base_url = %q, want the seeded default %q", cfg.Search.BaseURL, websearch.DefaultSearxngBaseURL)
	}

	cfg = &Config{Search: SearchConfig{Provider: "searxng", BaseURL: "http://10.0.0.5:8888"}}
	ApplyDefaults(cfg)
	if cfg.Search.BaseURL != "http://10.0.0.5:8888" {
		t.Errorf("explicit base_url = %q, want it preserved", cfg.Search.BaseURL)
	}

	cfg = &Config{Search: SearchConfig{Provider: "tavily"}}
	ApplyDefaults(cfg)
	if cfg.Search.BaseURL != "" {
		t.Errorf("non-searxng base_url = %q, want it untouched", cfg.Search.BaseURL)
	}
}

// TestLoad_SearxngBaseURL pins the load-time behavior: provider searxng with
// no base_url loads cleanly (ApplyDefaults seeds the default before
// validate), while an invalid raw base_url fails the load with an explicit
// error naming the key.
func TestLoad_SearxngBaseURL(t *testing.T) {
	t.Run("default seeded", func(t *testing.T) {
		configPath := writeTestConfig(t, `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
search:
  provider: searxng
`)
		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if cfg.Search.Provider != "searxng" || cfg.Search.BaseURL != websearch.DefaultSearxngBaseURL {
			t.Errorf("search = %+v, want searxng with the default base_url", cfg.Search)
		}
	})

	t.Run("invalid base_url fails the load", func(t *testing.T) {
		configPath := writeTestConfig(t, `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
search:
  provider: searxng
  base_url: "not a url"
`)
		_, err := Load(configPath)
		if err == nil {
			t.Fatal("Load() succeeded, want an explicit error for the invalid search.base_url")
		}
		if !strings.Contains(err.Error(), "search.base_url") {
			t.Errorf("error = %q, want it to name search.base_url", err.Error())
		}
	})
}
