package llm

import (
	"encoding/json"
	"testing"
)

func TestSimpleTokenCounter_Count(t *testing.T) {
	counter := NewSimpleTokenCounter()

	t.Run("empty string returns 0", func(t *testing.T) {
		got := counter.Count("")
		if got != 0 {
			t.Errorf("Count(\"\") = %d, want 0", got)
		}
	})

	t.Run("hello returns 2", func(t *testing.T) {
		got := counter.Count("hello")
		if got != 2 {
			t.Errorf("Count(\"hello\") = %d, want 2 (5 chars / 4 rounded up)", got)
		}
	})

	t.Run("longer string returns reasonable approximation", func(t *testing.T) {
		text := "The quick brown fox jumps over the lazy dog"
		got := counter.Count(text)
		// 43 chars, expect (43+3)/4 = 11 tokens
		expected := 11
		if got != expected {
			t.Errorf("Count(%q) = %d, want %d", text, got, expected)
		}
	})

	t.Run("exact multiple of 4", func(t *testing.T) {
		text := "test" // 4 chars
		got := counter.Count(text)
		if got != 1 {
			t.Errorf("Count(\"test\") = %d, want 1", got)
		}
	})
}

func TestSimpleTokenCounter_CountMessages(t *testing.T) {
	counter := NewSimpleTokenCounter()

	t.Run("empty slice returns 0", func(t *testing.T) {
		got := counter.CountMessages([]Message{})
		if got != 0 {
			t.Errorf("CountMessages([]) = %d, want 0", got)
		}
	})

	t.Run("nil slice returns 0", func(t *testing.T) {
		got := counter.CountMessages(nil)
		if got != 0 {
			t.Errorf("CountMessages(nil) = %d, want 0", got)
		}
	})

	t.Run("three messages with varying content", func(t *testing.T) {
		msgs := []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello!"},
			{Role: "assistant", Content: "Hi there! How can I help you today?"},
		}

		got := counter.CountMessages(msgs)

		// Calculate expected:
		// msg1: "system"(2) + "You are a helpful assistant."(8) + 4 overhead = 14
		// msg2: "user"(1) + "Hello!"(2) + 4 overhead = 7
		// msg3: "assistant"(3) + "Hi there! How can I help you today?"(10) + 4 overhead = 17
		// Total: 14 + 7 + 15 = 36
		expected := 36

		if got != expected {
			t.Errorf("CountMessages with 3 messages = %d, want %d", got, expected)
		}

		// Also verify it's reasonable (between 20 and 60 tokens for this content)
		if got < 20 || got > 60 {
			t.Errorf("CountMessages result %d is outside reasonable range [20, 60]", got)
		}
	})

	t.Run("includes ToolCalls content in count", func(t *testing.T) {
		msgs := []Message{
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID:    "call_123",
						Name:  "read_file",
						Input: json.RawMessage(`{"path": "/test/file.txt"}`),
					},
				},
			},
		}

		got := counter.CountMessages(msgs)

		// Calculate expected:
		// "assistant"(3) + ""(0) + "read_file"(3) + `{"path": "/test/file.txt"}`(7) + 4 overhead = 17
		expected := 17

		if got != expected {
			t.Errorf("CountMessages with ToolCalls = %d, want %d", got, expected)
		}

		// Compare with same message without tool calls
		msgsNoTools := []Message{
			{Role: "assistant", Content: ""},
		}
		gotNoTools := counter.CountMessages(msgsNoTools)

		if got <= gotNoTools {
			t.Errorf("CountMessages with ToolCalls (%d) should be greater than without (%d)", got, gotNoTools)
		}
	})

	t.Run("multiple tool calls are counted", func(t *testing.T) {
		msgs := []Message{
			{
				Role:    "assistant",
				Content: "Let me help you.",
				ToolCalls: []ToolCall{
					{ID: "1", Name: "tool_a", Input: json.RawMessage(`{"x": 1}`)},
					{ID: "2", Name: "tool_b", Input: json.RawMessage(`{"y": 2}`)},
				},
			},
		}

		got := counter.CountMessages(msgs)

		// Should include content from both tool calls
		// "assistant"(3) + "Let me help you."(5) + "tool_a"(2) + `{"x": 1}`(2) + "tool_b"(2) + `{"y": 2}`(2) + 4 overhead = 20
		if got < 15 {
			t.Errorf("CountMessages with multiple ToolCalls = %d, expected at least 15", got)
		}
	})
}

// ==================== NewTokenCounter Factory Tests ====================

func TestNewTokenCounter(t *testing.T) {
	t.Run("tiktoken/o200k_base returns TiktokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("tiktoken/o200k_base")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*TiktokenCounter)
		if !ok {
			t.Errorf("expected *TiktokenCounter, got %T", counter)
		}
	})

	t.Run("tiktoken/cl100k_base returns TiktokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("tiktoken/cl100k_base")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*TiktokenCounter)
		if !ok {
			t.Errorf("expected *TiktokenCounter, got %T", counter)
		}
	})

	t.Run("anthropic-api returns SimpleTokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("anthropic-api")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*SimpleTokenCounter)
		if !ok {
			t.Errorf("expected *SimpleTokenCounter, got %T", counter)
		}
	})

	t.Run("approximate returns SimpleTokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("approximate")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*SimpleTokenCounter)
		if !ok {
			t.Errorf("expected *SimpleTokenCounter, got %T", counter)
		}
	})

	t.Run("empty string returns SimpleTokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*SimpleTokenCounter)
		if !ok {
			t.Errorf("expected *SimpleTokenCounter, got %T", counter)
		}
	})

	t.Run("unknown tokenizer returns SimpleTokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("unknown-tokenizer")
		if err == nil {
			t.Fatal("expected error for unknown tokenizer")
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*SimpleTokenCounter)
		if !ok {
			t.Errorf("expected *SimpleTokenCounter, got %T", counter)
		}
	})

	t.Run("invalid tiktoken encoding falls back to SimpleTokenCounter", func(t *testing.T) {
		counter, err := NewTokenCounter("tiktoken/invalid_encoding")
		if err == nil {
			t.Fatal("expected error for invalid encoding")
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}
		_, ok := counter.(*SimpleTokenCounter)
		if !ok {
			t.Errorf("expected *SimpleTokenCounter for invalid encoding, got %T", counter)
		}
	})
}

// ==================== TiktokenCounter Tests ====================

func TestTiktokenCounter_Count(t *testing.T) {
	counter, err := NewTiktokenCounter("o200k_base")
	if err != nil {
		t.Fatalf("failed to create tiktoken counter: %v", err)
	}

	t.Run("empty string returns 0", func(t *testing.T) {
		got := counter.Count("")
		if got != 0 {
			t.Errorf("Count(\"\") = %d, want 0", got)
		}
	})

	t.Run("produces reasonable results for known text", func(t *testing.T) {
		// "hello" is typically 1 token in most encodings
		got := counter.Count("hello")
		if got < 1 || got > 3 {
			t.Errorf("Count(\"hello\") = %d, expected between 1 and 3 tokens", got)
		}
	})

	t.Run("more accurate than SimpleTokenCounter for English text", func(t *testing.T) {
		text := "The quick brown fox jumps over the lazy dog"
		tiktokenCount := counter.Count(text)
		simpleCount := NewSimpleTokenCounter().Count(text)

		// Tiktoken should be more accurate (usually fewer tokens than simple approximation)
		// The simple counter gives (43+3)/4 = 11 tokens
		// Tiktoken typically gives around 9-10 tokens for this text
		if tiktokenCount > simpleCount {
			t.Errorf("tiktoken count (%d) should not exceed simple count (%d) for this text",
				tiktokenCount, simpleCount)
		}
	})

	t.Run("handles longer text correctly", func(t *testing.T) {
		text := "This is a longer piece of text that should be tokenized properly by tiktoken. It contains multiple sentences and should give a reasonable token count."
		got := counter.Count(text)
		// Should be a reasonable number of tokens (roughly 1 token per 3-4 chars)
		minExpected := len(text) / 5
		maxExpected := len(text) / 2
		if got < minExpected || got > maxExpected {
			t.Errorf("Count(long text) = %d, expected between %d and %d", got, minExpected, maxExpected)
		}
	})
}

func TestTiktokenCounter_CountMessages(t *testing.T) {
	counter, err := NewTiktokenCounter("o200k_base")
	if err != nil {
		t.Fatalf("failed to create tiktoken counter: %v", err)
	}

	t.Run("empty slice returns 0", func(t *testing.T) {
		got := counter.CountMessages([]Message{})
		if got != 0 {
			t.Errorf("CountMessages([]) = %d, want 0", got)
		}
	})

	t.Run("includes all message components", func(t *testing.T) {
		msgs := []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello!"},
		}

		got := counter.CountMessages(msgs)

		// Should include role + content + overhead for each message
		// Overhead is 4 tokens per message
		minExpected := 8 // at least overhead for 2 messages
		if got < minExpected {
			t.Errorf("CountMessages with 2 messages = %d, expected at least %d", got, minExpected)
		}
	})

	t.Run("includes ToolCalls content in count", func(t *testing.T) {
		msgs := []Message{
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID:    "call_123",
						Name:  "read_file",
						Input: json.RawMessage(`{"path": "/test/file.txt"}`),
					},
				},
			},
		}

		got := counter.CountMessages(msgs)

		// Compare with same message without tool calls
		msgsNoTools := []Message{
			{Role: "assistant", Content: ""},
		}
		gotNoTools := counter.CountMessages(msgsNoTools)

		if got <= gotNoTools {
			t.Errorf("CountMessages with ToolCalls (%d) should be greater than without (%d)", got, gotNoTools)
		}
	})

	t.Run("counts role, content, and tool call components", func(t *testing.T) {
		msgs := []Message{
			{
				Role:    "assistant",
				Content: "Let me help.",
				ToolCalls: []ToolCall{
					{ID: "1", Name: "tool_a", Input: json.RawMessage(`{"x": 1}`)},
				},
			},
		}

		got := counter.CountMessages(msgs)

		// Should count: role + content + tool_name + tool_input + overhead
		if got < 5 {
			t.Errorf("CountMessages with all components = %d, expected at least 5", got)
		}
	})
}

func TestTiktokenCounter_DifferentEncodings(t *testing.T) {
	t.Run("o200k_base encoding works", func(t *testing.T) {
		counter, err := NewTiktokenCounter("o200k_base")
		if err != nil {
			t.Fatalf("failed to create o200k_base counter: %v", err)
		}
		got := counter.Count("hello world")
		if got <= 0 {
			t.Errorf("expected positive token count, got %d", got)
		}
	})

	t.Run("cl100k_base encoding works", func(t *testing.T) {
		counter, err := NewTiktokenCounter("cl100k_base")
		if err != nil {
			t.Fatalf("failed to create cl100k_base counter: %v", err)
		}
		got := counter.Count("hello world")
		if got <= 0 {
			t.Errorf("expected positive token count, got %d", got)
		}
	})

	t.Run("invalid encoding returns error", func(t *testing.T) {
		_, err := NewTiktokenCounter("invalid_encoding_xyz")
		if err == nil {
			t.Error("expected error for invalid encoding, got nil")
		}
	})
}

// ==================== ContextTokenTracker Tests ====================

func TestContextTokenTracker_New(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}

	if tracker.EstimateTotal() != 0 {
		t.Errorf("new tracker should have EstimateTotal() = 0, got %d", tracker.EstimateTotal())
	}
}

func TestContextTokenTracker_EstimateTotal(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	t.Run("initial estimate is 0", func(t *testing.T) {
		got := tracker.EstimateTotal()
		if got != 0 {
			t.Errorf("initial EstimateTotal() = %d, want 0", got)
		}
	})

	t.Run("estimate equals lastKnownUsed + pendingDelta", func(t *testing.T) {
		// Simulate: API returned 100 tokens used, then we added more
		tracker.Correct(100)
		tracker.AddDelta("some text here") // 14 chars = 4 tokens

		got := tracker.EstimateTotal()
		want := 104 // 100 + 4
		if got != want {
			t.Errorf("EstimateTotal() = %d, want %d", got, want)
		}
	})
}

func TestContextTokenTracker_AddDelta(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	t.Run("adds token count to pendingDelta", func(t *testing.T) {
		tracker.Reset()
		tracker.AddDelta("hello world") // 11 chars = 3 tokens

		got := tracker.EstimateTotal()
		if got != 3 {
			t.Errorf("after AddDelta, EstimateTotal() = %d, want 3", got)
		}
	})

	t.Run("accumulates multiple deltas", func(t *testing.T) {
		tracker.Reset()
		tracker.AddDelta("hello") // 5 chars = 2 tokens
		tracker.AddDelta("world") // 5 chars = 2 tokens
		tracker.AddDelta("test")  // 4 chars = 1 token

		got := tracker.EstimateTotal()
		want := 5 // 2 + 2 + 1
		if got != want {
			t.Errorf("after multiple AddDelta, EstimateTotal() = %d, want %d", got, want)
		}
	})

	t.Run("empty string adds 0", func(t *testing.T) {
		tracker.Reset()
		tracker.AddDelta("")

		got := tracker.EstimateTotal()
		if got != 0 {
			t.Errorf("after AddDelta(\"\"), EstimateTotal() = %d, want 0", got)
		}
	})
}

func TestContextTokenTracker_Correct(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	t.Run("updates lastKnownUsed and resets pendingDelta", func(t *testing.T) {
		tracker.Reset()
		tracker.AddDelta("some text here") // add some pending delta
		initialEstimate := tracker.EstimateTotal()
		if initialEstimate == 0 {
			t.Error("expected non-zero initial estimate after AddDelta")
		}

		tracker.Correct(100)

		got := tracker.EstimateTotal()
		if got != 100 {
			t.Errorf("after Correct(100), EstimateTotal() = %d, want 100", got)
		}
	})

	t.Run("subsequent deltas accumulate from corrected value", func(t *testing.T) {
		tracker.Reset()
		tracker.Correct(50)
		tracker.AddDelta("test") // 4 chars = 1 token

		got := tracker.EstimateTotal()
		want := 51 // 50 + 1
		if got != want {
			t.Errorf("after Correct + AddDelta, EstimateTotal() = %d, want %d", got, want)
		}
	})

	t.Run("multiple corrections work correctly", func(t *testing.T) {
		tracker.Reset()
		tracker.Correct(100)
		tracker.AddDelta("some text")
		tracker.Correct(150)

		got := tracker.EstimateTotal()
		if got != 150 {
			t.Errorf("after second Correct(150), EstimateTotal() = %d, want 150", got)
		}
	})
}

func TestContextTokenTracker_Reset(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	t.Run("resets both lastKnownUsed and pendingDelta", func(t *testing.T) {
		tracker.Correct(100)
		tracker.AddDelta("some text")

		tracker.Reset()

		got := tracker.EstimateTotal()
		if got != 0 {
			t.Errorf("after Reset(), EstimateTotal() = %d, want 0", got)
		}
	})

	t.Run("can accumulate after reset", func(t *testing.T) {
		tracker.Correct(100)
		tracker.Reset()
		tracker.AddDelta("hello") // 2 tokens

		got := tracker.EstimateTotal()
		if got != 2 {
			t.Errorf("after Reset + AddDelta, EstimateTotal() = %d, want 2", got)
		}
	})
}

func TestContextTokenTracker_WithTiktoken(t *testing.T) {
	t.Run("works with TiktokenCounter as predictive", func(t *testing.T) {
		tiktokenCounter, err := NewTiktokenCounter("o200k_base")
		if err != nil {
			t.Fatalf("failed to create tiktoken counter: %v", err)
		}

		tracker := NewContextTokenTracker(tiktokenCounter)
		tracker.AddDelta("hello world")

		got := tracker.EstimateTotal()
		if got <= 0 {
			t.Errorf("expected positive estimate with tiktoken, got %d", got)
		}

		tracker.Correct(50)
		if tracker.EstimateTotal() != 50 {
			t.Errorf("after Correct(50), expected 50, got %d", tracker.EstimateTotal())
		}
	})
}

// ==================== Integration Tests ====================

func TestTokenCounterInterface(t *testing.T) {
	t.Run("all implementations satisfy TokenCounter interface", func(t *testing.T) {
		var _ TokenCounter = NewSimpleTokenCounter()

		tiktokenCounter, err := NewTiktokenCounter("o200k_base")
		if err != nil {
			t.Fatalf("failed to create tiktoken counter: %v", err)
		}
		var _ TokenCounter = tiktokenCounter
	})

	t.Run("factory returns consistent counters for same type", func(t *testing.T) {
		counter1, _ := NewTokenCounter("tiktoken/o200k_base")
		counter2, _ := NewTokenCounter("tiktoken/o200k_base")

		// Both should be TiktokenCounters
		_, ok1 := counter1.(*TiktokenCounter)
		_, ok2 := counter2.(*TiktokenCounter)
		if !ok1 || !ok2 {
			t.Error("expected both counters to be *TiktokenCounter")
		}

		// Both should give same count for same text
		text := "The quick brown fox"
		if counter1.Count(text) != counter2.Count(text) {
			t.Error("same text should give same count from same counter type")
		}
	})
}

func TestContextTokenTracker_ConcurrentAccess(t *testing.T) {
	counter := NewSimpleTokenCounter()
	tracker := NewContextTokenTracker(counter)

	// Run concurrent operations
	done := make(chan bool, 3)

	go func() {
		for i := 0; i < 100; i++ {
			tracker.AddDelta("test")
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 100; i++ {
			_ = tracker.EstimateTotal()
		}
		done <- true
	}()

	go func() {
		for i := 0; i < 10; i++ {
			tracker.Correct(i * 10)
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 3; i++ {
		<-done
	}

	// Should not panic and should have reasonable final state
	final := tracker.EstimateTotal()
	if final < 0 {
		t.Errorf("final estimate should be non-negative, got %d", final)
	}
}

func TestTiktokenCounterVsSimpleCounter(t *testing.T) {
	t.Run("tiktoken produces reasonable counts for various text types", func(t *testing.T) {
		tiktokenCounter, err := NewTiktokenCounter("o200k_base")
		if err != nil {
			t.Fatalf("failed to create tiktoken counter: %v", err)
		}
		simpleCounter := NewSimpleTokenCounter()

		testCases := []string{
			"hello",
			"The quick brown fox jumps over the lazy dog",
			"function calculateSum(a, b) { return a + b; }",
			"const x = { foo: 'bar', baz: 123 };",
		}

		for _, text := range testCases {
			tiktokenCount := tiktokenCounter.Count(text)
			simpleCount := simpleCounter.Count(text)

			// Both counters should produce positive counts
			if tiktokenCount <= 0 {
				t.Errorf("tiktoken count should be positive, got %d for %q", tiktokenCount, text)
			}
			if simpleCount <= 0 {
				t.Errorf("simple count should be positive, got %d for %q", simpleCount, text)
			}

			// For natural language, tiktoken is typically more accurate (often lower count)
			// For code with many special characters, tiktoken may count more tokens
			// The key is that tiktoken is more accurate for OpenAI models
			t.Logf("text: %q -> tiktoken: %d, simple: %d", text, tiktokenCount, simpleCount)
		}
	})

	t.Run("tiktoken is more accurate for natural English text", func(t *testing.T) {
		tiktokenCounter, err := NewTiktokenCounter("o200k_base")
		if err != nil {
			t.Fatalf("failed to create tiktoken counter: %v", err)
		}
		simpleCounter := NewSimpleTokenCounter()

		// Natural English text - tiktoken should typically be more efficient
		text := "The quick brown fox jumps over the lazy dog"
		tiktokenCount := tiktokenCounter.Count(text)
		simpleCount := simpleCounter.Count(text)

		// For this natural English text, tiktoken should give lower or similar count
		if tiktokenCount > simpleCount {
			t.Errorf("tiktoken count (%d) higher than simple count (%d) for natural English text",
				tiktokenCount, simpleCount)
		}
	})
}

func TestNewTokenCounter_FallbackBehavior(t *testing.T) {
	t.Run("invalid tiktoken prefix falls back gracefully", func(t *testing.T) {
		// This should not panic and should return a SimpleTokenCounter
		counter, err := NewTokenCounter("tiktoken/nonexistent_encoding_12345")
		if err == nil {
			t.Fatal("expected error for nonexistent encoding")
		}
		if counter == nil {
			t.Fatal("expected non-nil counter")
		}

		// Should be usable
		count := counter.Count("hello world")
		if count <= 0 {
			t.Errorf("fallback counter should return positive count, got %d", count)
		}
	})
}
