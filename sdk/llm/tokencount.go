package llm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// TokenCounter — interface for counting tokens to manage context budget.
type TokenCounter interface {
	// Count returns approximate token count for a text string.
	Count(text string) int

	// CountMessages returns total token count across all messages.
	CountMessages(msgs []Message) int
}

// estimatedTokensPerChar is the approximate ratio of characters to tokens
// used for fast token count estimation.
const estimatedTokensPerChar = 4

// SimpleTokenCounter — approximate token counter using ~4 chars = 1 token rule.
type SimpleTokenCounter struct{}

// NewSimpleTokenCounter creates a SimpleTokenCounter that estimates tokens
// using the ~4 chars = 1 token heuristic.
func NewSimpleTokenCounter() *SimpleTokenCounter {
	return &SimpleTokenCounter{}
}

// Count returns the approximate token count for text using ceiling division
// of its byte length by estimatedTokensPerChar.
func (c *SimpleTokenCounter) Count(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + estimatedTokensPerChar - 1) / estimatedTokensPerChar // ceiling division
}

// CountMessages returns the total approximate token count across all messages,
// including role, content, tool-call names/inputs, and per-message framing overhead.
func (c *SimpleTokenCounter) CountMessages(msgs []Message) int {
	total := 0
	for _, msg := range msgs {
		total += c.Count(msg.Role)
		total += c.Count(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += c.Count(tc.Name)
			total += c.Count(string(tc.Input))
		}
		// Add small overhead per message for framing
		total += 4
	}
	return total
}

// TiktokenCounter — accurate token counter using tiktoken-go for OpenAI models.
// The tiktoken-go library's Encode method is NOT safe for concurrent use
// (it mutates internal caches), hence the exclusive Lock.
type TiktokenCounter struct {
	tkm *tiktoken.Tiktoken
	mu  sync.Mutex
}

// NewTiktokenCounter creates a new TiktokenCounter with the specified encoding.
// Valid encodings include: "o200k_base", "cl100k_base", "p50k_base", etc.
func NewTiktokenCounter(encoding string) (*TiktokenCounter, error) {
	tkm, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, err
	}
	return &TiktokenCounter{tkm: tkm}, nil
}

// Count returns the exact token count for text using the tiktoken encoding.
// It is safe for concurrent use.
func (c *TiktokenCounter) Count(text string) int {
	if text == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tokens := c.tkm.Encode(text, nil, nil)
	return len(tokens)
}

// CountMessages returns the total exact token count across all messages,
// including role, content, tool-call names/inputs, and per-message framing overhead.
func (c *TiktokenCounter) CountMessages(msgs []Message) int {
	total := 0
	for _, msg := range msgs {
		total += c.Count(msg.Role)
		total += c.Count(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += c.Count(tc.Name)
			total += c.Count(string(tc.Input))
		}
		// Add small overhead per message for framing
		total += 4
	}
	return total
}

// NewTokenCounter creates a TokenCounter based on the tokenizer type.
// Supported types:
//   - "tiktoken/o200k_base" → TiktokenCounter with o200k_base encoding
//   - "tiktoken/cl100k_base" → TiktokenCounter with cl100k_base encoding
//   - "anthropic-api" → SimpleTokenCounter (rely on API correction)
//   - "approximate" or "" or unknown → SimpleTokenCounter
//
// The returned TokenCounter is always valid (never nil). The error indicates
// that a fallback counter was used instead of the requested type.
func NewTokenCounter(tokenizerType string) (TokenCounter, error) {
	switch {
	case strings.HasPrefix(tokenizerType, "tiktoken/"):
		encoding := strings.TrimPrefix(tokenizerType, "tiktoken/")
		counter, err := NewTiktokenCounter(encoding)
		if err != nil {
			return NewSimpleTokenCounter(), fmt.Errorf("failed to create tiktoken counter for encoding %s: %w", encoding, err)
		}
		return counter, nil
	case tokenizerType == "anthropic-api":
		// For Anthropic models, we rely on API correction rather than local counting
		return NewSimpleTokenCounter(), nil
	case tokenizerType == "approximate" || tokenizerType == "":
		return NewSimpleTokenCounter(), nil
	default:
		// Unknown tokenizer type, fallback to simple
		return NewSimpleTokenCounter(), fmt.Errorf("unknown tokenizer type: %s", tokenizerType)
	}
}

// ContextTokenTracker — hybrid A+C coordinator that combines predictive counting
// with API-corrected actuals. Uses predictive counter for estimates between API calls,
// then corrects with actual usage from API responses.
type ContextTokenTracker struct {
	predictive    TokenCounter
	lastKnownUsed int // from API response.usage.input_tokens
	pendingDelta  int // estimated tokens added since last API call
	mu            sync.RWMutex
}

// NewContextTokenTracker creates a new ContextTokenTracker with the given predictive counter.
func NewContextTokenTracker(counter TokenCounter) *ContextTokenTracker {
	return &ContextTokenTracker{
		predictive:    counter,
		lastKnownUsed: 0,
		pendingDelta:  0,
	}
}

// EstimateTotal returns the estimated total token count (lastKnownUsed + pendingDelta).
func (t *ContextTokenTracker) EstimateTotal() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastKnownUsed + t.pendingDelta
}

// AddDelta adds the token count of the given text to pendingDelta.
func (t *ContextTokenTracker) AddDelta(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pendingDelta += t.predictive.Count(text)
}

// Correct updates lastKnownUsed with the actual API input tokens and resets pendingDelta.
func (t *ContextTokenTracker) Correct(apiInputTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastKnownUsed = apiInputTokens
	t.pendingDelta = 0
}

// Reset resets both lastKnownUsed and pendingDelta to 0.
func (t *ContextTokenTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastKnownUsed = 0
	t.pendingDelta = 0
}

// EstimateMessages returns the estimated token count for the given messages
// using the predictive counter. This is a read-only operation.
func (t *ContextTokenTracker) EstimateMessages(msgs []Message) int {
	return t.predictive.CountMessages(msgs)
}
