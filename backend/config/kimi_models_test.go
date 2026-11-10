package config

import (
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

func TestKimiModelDefaults_ContainsExpectedModels(t *testing.T) {
	defaults := KimiModelDefaults()
	for _, name := range []string{"k3.1-pro", "k3.1-pro-highspeed", "kimi-for-coding"} {
		if _, ok := defaults[name]; !ok {
			t.Errorf("missing c0wrk default for model %q", name)
		}
	}
}

func TestKimiModelDefaults_Metadata(t *testing.T) {
	defaults := KimiModelDefaults()
	for _, name := range []string{"k3.1-pro", "k3.1-pro-highspeed", "kimi-for-coding"} {
		m := defaults[name]
		if m.ContextWindow != 1_000_000 {
			t.Errorf("%s context window = %d, want 1000000", name, m.ContextWindow)
		}
		if m.OutputLimit != 131_072 {
			t.Errorf("%s output limit = %d, want 131072", name, m.OutputLimit)
		}
		if m.TokenizerType != "approximate" {
			t.Errorf("%s tokenizer type = %q, want approximate", name, m.TokenizerType)
		}
		if m.Family != string(llm.FamilyKimi) {
			t.Errorf("%s family = %q, want %q", name, m.Family, llm.FamilyKimi)
		}
		if m.Protocol != llm.ProtocolChatCompletions {
			t.Errorf("%s protocol = %q, want %q", name, m.Protocol, llm.ProtocolChatCompletions)
		}
		if m.Capabilities == nil {
			t.Fatalf("%s capabilities are nil", name)
		}
		if !m.Capabilities.Attachment || !m.Capabilities.Reasoning || !m.Capabilities.ToolCall {
			t.Errorf("%s capabilities = %+v, want attachment/reasoning/tool_call", name, *m.Capabilities)
		}
	}
}

func TestKimiModelDefault(t *testing.T) {
	if _, ok := KimiModelDefault("k3.1-pro"); !ok {
		t.Error("expected default for k3.1-pro")
	}
	if _, ok := KimiModelDefault("unknown-model"); ok {
		t.Error("expected no default for unknown model")
	}
}

func TestKimiModelDefaults_ReturnsCopy(t *testing.T) {
	a := KimiModelDefaults()
	b := KimiModelDefaults()
	if &a == &b {
		t.Error("KimiModelDefaults should return distinct maps")
	}
	a["k3.1-pro"] = llm.ModelMetadata{}
	if b["k3.1-pro"].ContextWindow == 0 {
		t.Error("mutating one returned map should not affect another")
	}
}
