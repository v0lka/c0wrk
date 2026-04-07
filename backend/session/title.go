package session

import (
	"context"
	"log/slog"
	"strings"
)

// LLMTitleCaller is the interface for making LLM calls for title generation.
// This avoids importing sdk/llm in the backend layer.
type LLMTitleCaller interface {
	GenerateTitle(ctx context.Context, userMessage string) (string, error)
}

// TitleGenerator generates concise session titles from user messages.
type TitleGenerator struct {
	caller LLMTitleCaller
}

// NewTitleGenerator creates a new TitleGenerator.
// If caller is nil, only fallback title generation is used.
func NewTitleGenerator(caller LLMTitleCaller) *TitleGenerator {
	return &TitleGenerator{caller: caller}
}

// Generate produces a title for the given user message.
// It tries LLM-based generation first, falling back to extracting first words.
func (g *TitleGenerator) Generate(ctx context.Context, userMessage string) string {
	// Try LLM-based title generation
	if g.caller != nil {
		title, err := g.caller.GenerateTitle(ctx, userMessage)
		if err != nil {
			slog.Warn("failed to generate session title via LLM, using fallback", "error", err)
		} else if title != "" {
			title = strings.TrimSpace(title)
			title = strings.Trim(title, "\"'")
			if title != "" {
				return truncateTitle(title)
			}
		}
	}

	// Fallback: first few words
	title := fallbackTitle(userMessage)
	return truncateTitle(title)
}

// truncateTitle ensures title doesn't exceed 60 characters.
func truncateTitle(title string) string {
	if len(title) > 60 {
		return title[:57] + "..."
	}
	return title
}

// fallbackTitle creates a simple title from the first few words of a message.
func fallbackTitle(message string) string {
	words := strings.Fields(message)
	if len(words) == 0 {
		return ""
	}
	maxWords := 5
	if len(words) < maxWords {
		maxWords = len(words)
	}
	title := strings.Join(words[:maxWords], " ")
	if len(words) > maxWords {
		title += "..."
	}
	return title
}
