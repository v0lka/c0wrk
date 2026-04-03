package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/user/agent/internal/core/prompts"
	"github.com/user/agent/internal/llm"
)

// StoredReflectionData is a local type to avoid importing memory package.
// Used for meta-reflection analysis.
type StoredReflectionData struct {
	TaskDescription string
	Summary         string
	Hypotheses      []string
	SuggestedAction string
}

// ConstitutionPrinciple represents a guiding principle derived from experience.
type ConstitutionPrinciple struct {
	ID           string `json:"id"`
	Principle    string `json:"principle"`
	Source       string `json:"source"` // "meta_reflection" or "user_defined"
	CreatedAt    string `json:"created_at"`
	SessionCount int    `json:"session_count"` // how many sessions contributed
}

// constitutionFile is the JSON structure persisted to disk.
type constitutionFile struct {
	Principles   []ConstitutionPrinciple `json:"principles"`
	SessionCount int                     `json:"session_count"`
}

// Constitution stores and manages guiding principles derived from
// accumulated reflections (meta-reflection) or user-defined rules.
type Constitution struct {
	mu         sync.RWMutex
	principles []ConstitutionPrinciple
	filePath   string
	sessionNum int
}

// NewConstitution loads or creates a constitution from the JSON file.
func NewConstitution(filePath string) (*Constitution, error) {
	c := &Constitution{
		filePath:   filePath,
		principles: []ConstitutionPrinciple{},
	}

	// Try to load existing file
	if err := c.Load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load constitution: %w", err)
		}
		// File doesn't exist, start with empty constitution
	}

	return c, nil
}

// Principles returns all current principles.
func (c *Constitution) Principles() []ConstitutionPrinciple {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a copy to avoid data races
	result := make([]ConstitutionPrinciple, len(c.principles))
	copy(result, c.principles)
	return result
}

// AddPrinciple adds a user-defined principle.
func (c *Constitution) AddPrinciple(principle string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Generate unique ID
	id := fmt.Sprintf("p_%d", len(c.principles)+1)

	newPrinciple := ConstitutionPrinciple{
		ID:           id,
		Principle:    principle,
		Source:       "user_defined",
		CreatedAt:    time.Now().Format(time.RFC3339),
		SessionCount: c.sessionNum,
	}

	c.principles = append(c.principles, newPrinciple)
	return c.saveInternal()
}

// MetaReflect analyzes accumulated reflections to extract recurring patterns
// into new principles. Called periodically (e.g., every N sessions).
// llmCaller is used to call LLM with "reflector" role for meta-analysis.
func (c *Constitution) MetaReflect(ctx context.Context, reflections []StoredReflectionData, llmCaller LLMCaller) error {
	if len(reflections) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Build prompt with reflections
	prompt := c.buildMetaReflectionPrompt(reflections)

	// Call LLM to analyze
	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompts.ConstitutionMetaReflection},
			{Role: "user", Content: prompt},
		},
	}

	resp, err := llmCaller.Call(ctx, req)
	if err != nil {
		return fmt.Errorf("meta-reflection LLM call failed: %w", err)
	}

	// Parse new principles from response
	newPrinciples := c.parsePrinciples(resp.Message.Content)

	// Add new principles (avoiding duplicates)
	for _, np := range newPrinciples {
		if !c.isDuplicate(np) {
			id := fmt.Sprintf("p_%d", len(c.principles)+1)
			c.principles = append(c.principles, ConstitutionPrinciple{
				ID:           id,
				Principle:    np,
				Source:       "meta_reflection",
				CreatedAt:    time.Now().Format(time.RFC3339),
				SessionCount: c.sessionNum,
			})
		}
	}

	return c.saveInternal()
}

// Save persists the constitution to disk.
func (c *Constitution) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveInternal()
}

// saveInternal saves without acquiring lock (caller must hold lock).
func (c *Constitution) saveInternal() error {
	// Ensure directory exists
	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data := constitutionFile{
		Principles:   c.principles,
		SessionCount: c.sessionNum,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal constitution: %w", err)
	}

	if err := os.WriteFile(c.filePath, jsonData, 0o644); err != nil {
		return fmt.Errorf("failed to write constitution file: %w", err)
	}

	return nil
}

// Load reads the constitution from disk.
func (c *Constitution) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return err
	}

	var file constitutionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("failed to parse constitution file: %w", err)
	}

	c.principles = file.Principles
	c.sessionNum = file.SessionCount

	return nil
}

// ForPrompt returns principles formatted for inclusion in system prompt.
func (c *Constitution) ForPrompt() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.principles) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Constitution Principles:\n")
	for i, p := range c.principles {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, p.Principle)
	}

	return sb.String()
}

// IncrementSession tracks session count and persists it.
func (c *Constitution) IncrementSession() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionNum++
	// Persist the updated session count (ignore error as this is non-critical)
	_ = c.saveInternal()
}

// SessionCount returns current session number.
func (c *Constitution) SessionCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionNum
}

// ShouldMetaReflect returns true if enough sessions have passed for meta-reflection.
func (c *Constitution) ShouldMetaReflect(interval int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return interval > 0 && c.sessionNum > 0 && c.sessionNum%interval == 0
}

// buildMetaReflectionPrompt creates the analysis prompt from reflections.
func (c *Constitution) buildMetaReflectionPrompt(reflections []StoredReflectionData) string {
	var sb strings.Builder

	// Include current principles for context
	if len(c.principles) > 0 {
		sb.WriteString("Current Constitution Principles:\n")
		for i, p := range c.principles {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, p.Principle)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Recent Reflections from past sessions:\n\n")

	for i, r := range reflections {
		fmt.Fprintf(&sb, "--- Reflection %d ---\n", i+1)
		fmt.Fprintf(&sb, "Task: %s\n", r.TaskDescription)
		fmt.Fprintf(&sb, "Summary: %s\n", r.Summary)
		if len(r.Hypotheses) > 0 {
			sb.WriteString("Hypotheses:\n")
			for _, h := range r.Hypotheses {
				fmt.Fprintf(&sb, "  - %s\n", h)
			}
		}
		fmt.Fprintf(&sb, "Suggested Action: %s\n\n", r.SuggestedAction)
	}

	return sb.String()
}

// parsePrinciples extracts principles from LLM response.
func (c *Constitution) parsePrinciples(response string) []string {
	var principles []string

	// Split by newlines and look for numbered or bulleted items
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Remove common prefixes: "1. ", "- ", "* ", "• "
		cleaned := line
		for _, prefix := range []string{"1.", "2.", "3.", "4.", "5.", "6.", "7.", "8.", "9.", "10.", "-", "*", "•"} {
			if strings.HasPrefix(cleaned, prefix) {
				cleaned = strings.TrimPrefix(cleaned, prefix)
				cleaned = strings.TrimSpace(cleaned)
				break
			}
		}

		// Skip empty or too short principles
		if len(cleaned) < 10 {
			continue
		}

		// Skip if it looks like a header or instruction
		lower := strings.ToLower(cleaned)
		if strings.HasPrefix(lower, "principle") || strings.HasPrefix(lower, "here are") ||
			strings.HasPrefix(lower, "based on") || strings.HasPrefix(lower, "analyzing") {
			continue
		}

		principles = append(principles, cleaned)
	}

	// Limit to 5 principles per meta-reflection
	if len(principles) > 5 {
		principles = principles[:5]
	}

	return principles
}

// isDuplicate checks if a principle already exists (approximate match).
func (c *Constitution) isDuplicate(principle string) bool {
	lowerNew := strings.ToLower(principle)
	for _, existing := range c.principles {
		lowerExisting := strings.ToLower(existing.Principle)
		// Simple similarity check: if one contains >60% of the other's words
		if strings.Contains(lowerNew, lowerExisting) || strings.Contains(lowerExisting, lowerNew) {
			return true
		}
		// Check word overlap
		newWords := strings.Fields(lowerNew)
		existingWords := strings.Fields(lowerExisting)
		matches := 0
		for _, nw := range newWords {
			for _, ew := range existingWords {
				if nw == ew && len(nw) > 3 {
					matches++
				}
			}
		}
		threshold := len(newWords) / 2
		if threshold < 3 {
			threshold = 3
		}
		if matches >= threshold {
			return true
		}
	}
	return false
}
