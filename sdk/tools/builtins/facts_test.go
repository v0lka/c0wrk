package builtins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent"
)

// mockFactStore implements agent.FactStore for testing.
type mockFactStore struct {
	facts []agent.FactEntry
}

func (m *mockFactStore) StoreFact(keywords []string, content, author string) {
	m.facts = append(m.facts, agent.FactEntry{Keywords: keywords, Content: content, Author: author})
}

func (m *mockFactStore) SearchFacts(keywords []string) []agent.FactEntry {
	var results []agent.FactEntry
	for _, f := range m.facts {
		for _, fk := range f.Keywords {
			matched := false
			for _, qk := range keywords {
				if strings.EqualFold(fk, qk) {
					matched = true
					break
				}
			}
			if matched {
				results = append(results, f)
				break
			}
		}
	}
	return results
}

func ctxWithFactStore(fs agent.FactStore) context.Context {
	return agent.WithFactStore(context.Background(), fs)
}

// ---------------------------------------------------------------------------
// store_fact
// ---------------------------------------------------------------------------

func TestStoreFactTool_ValidInput(t *testing.T) {
	fs := &mockFactStore{}
	ctx := ctxWithFactStore(fs)
	ctx = agent.WithStepID(ctx, "step_1")

	tool := NewStoreFactTool()
	input, _ := json.Marshal(StoreFactInput{
		Keywords: []string{"auth", "login", "oauth"},
		Content:  "OAuth2 is used for authentication",
	})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "auth") {
		t.Errorf("result should mention keywords, got %q", result.Content)
	}
	if len(fs.facts) != 1 {
		t.Fatalf("expected 1 stored fact, got %d", len(fs.facts))
	}
	if fs.facts[0].Content != "OAuth2 is used for authentication" {
		t.Errorf("stored content mismatch: %q", fs.facts[0].Content)
	}
	if fs.facts[0].Author != "step_1" {
		t.Errorf("expected author step_1, got %q", fs.facts[0].Author)
	}
}

func TestStoreFactTool_NoFactStore(t *testing.T) {
	tool := NewStoreFactTool()
	input, _ := json.Marshal(StoreFactInput{
		Keywords: []string{"test", "unit", "check"},
		Content:  "test content",
	})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when fact store not available")
	}
	if !strings.Contains(result.Content, "not available") {
		t.Errorf("expected 'not available' message, got %q", result.Content)
	}
}

func TestStoreFactTool_InvalidJSON(t *testing.T) {
	tool := NewStoreFactTool()
	fs := &mockFactStore{}
	ctx := ctxWithFactStore(fs)

	result, err := tool.Execute(ctx, []byte(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// search_facts
// ---------------------------------------------------------------------------

func TestSearchFactsTool_ReturnsMatches(t *testing.T) {
	fs := &mockFactStore{
		facts: []agent.FactEntry{
			{Keywords: []string{"auth", "login"}, Content: "OAuth2 auth", Author: "s1"},
			{Keywords: []string{"database"}, Content: "PostgreSQL DB", Author: "s2"},
		},
	}
	ctx := ctxWithFactStore(fs)

	tool := NewSearchFactsTool()
	input, _ := json.Marshal(SearchFactsInput{Keywords: []string{"auth"}})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "OAuth2 auth") {
		t.Errorf("expected matching fact in result, got %q", result.Content)
	}
	if strings.Contains(result.Content, "PostgreSQL") {
		t.Errorf("should not contain non-matching fact")
	}
}

func TestSearchFactsTool_NoMatches(t *testing.T) {
	fs := &mockFactStore{
		facts: []agent.FactEntry{
			{Keywords: []string{"auth"}, Content: "auth stuff", Author: "s1"},
		},
	}
	ctx := ctxWithFactStore(fs)

	tool := NewSearchFactsTool()
	input, _ := json.Marshal(SearchFactsInput{Keywords: []string{"nonexistent"}})

	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	if !strings.Contains(result.Content, "No facts found") {
		t.Errorf("expected 'No facts found', got %q", result.Content)
	}
}

func TestSearchFactsTool_NoFactStore(t *testing.T) {
	tool := NewSearchFactsTool()
	input, _ := json.Marshal(SearchFactsInput{Keywords: []string{"test"}})

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when fact store not available")
	}
}

func TestSearchFactsTool_InvalidJSON(t *testing.T) {
	tool := NewSearchFactsTool()
	fs := &mockFactStore{}
	ctx := ctxWithFactStore(fs)

	result, err := tool.Execute(ctx, []byte(`{invalid`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for invalid JSON")
	}
}
