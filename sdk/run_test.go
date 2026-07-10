package sp4rk

import (
	"context"
	"errors"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

func TestRunAskWithoutSystem(t *testing.T) {
	fw := testFramework(t)

	_, err := fw.RunF(context.Background()).Ask("hello")
	if !errors.Is(err, errNoSystemPrompt) {
		t.Errorf("Ask without system: err = %v, want errNoSystemPrompt", err)
	}
}

func TestRunBuilderChaining(t *testing.T) {
	fw := testFramework(t)

	events := &agent.NoopEvents{}
	factory := func(_ context.Context, _ string, _ llm.ModelMetadata) string { return "dynamic" }

	b := fw.RunF(context.Background())
	// Each setter must return the same builder instance for chaining.
	if b.System("static") != b {
		t.Error("System should return the same builder")
	}
	if b.SystemFactory(factory) != b {
		t.Error("SystemFactory should return the same builder")
	}
	if b.Events(events) != b {
		t.Error("Events should return the same builder")
	}

	if b.system != "static" {
		t.Errorf("system = %q, want %q", b.system, "static")
	}
	if b.systemFn == nil {
		t.Error("systemFn should be set")
	}
	if b.events != events {
		t.Error("events should be the provided sink")
	}
	if !b.configured {
		t.Error("configured should be true after System/SystemFactory")
	}
}

func TestRunSystemStoresStaticPrompt(t *testing.T) {
	fw := testFramework(t)

	b := fw.RunF(context.Background()).System("You are helpful.")
	if b.system != "You are helpful." {
		t.Errorf("system = %q, want %q", b.system, "You are helpful.")
	}
	// The static string is wrapped into a factory only inside Ask; the
	// end-to-end execution is exercised by the example suite.
}
