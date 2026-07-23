package session

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// LLMTitleCaller is the interface for making LLM calls for title generation.
// This avoids importing github.com/v0lka/sp4rk/llm in the backend layer.
type LLMTitleCaller interface {
	GenerateTitle(ctx context.Context, userMessage string, activeSkills []string) (string, error)
}

// TitleGenerator generates concise session titles from user messages.
type TitleGenerator struct {
	caller LLMTitleCaller
	mu     sync.RWMutex
	logger *slog.Logger
}

// NewTitleGenerator creates a new TitleGenerator.
// If caller is nil, only fallback title generation is used.
func NewTitleGenerator(caller LLMTitleCaller) *TitleGenerator {
	return &TitleGenerator{caller: caller}
}

// log returns the generator's logger, falling back to slog.Default().
func (g *TitleGenerator) log() *slog.Logger {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.logger != nil {
		return g.logger
	}
	return slog.Default()
}

// SetLogger sets the logger for the title generator.
func (g *TitleGenerator) SetLogger(l *slog.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logger = l
}

// Generate produces a title for the given user message.
// It tries LLM-based generation first, falling back to extracting first words.
func (g *TitleGenerator) Generate(ctx context.Context, userMessage string, activeSkills []string) string {
	// Try LLM-based title generation
	if g.caller != nil {
		title, err := g.caller.GenerateTitle(ctx, userMessage, activeSkills)
		if err != nil {
			g.log().Warn("failed to generate session title via LLM, using fallback", "error", err)
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

// truncateTitle ensures title doesn't exceed 60 runes.
func truncateTitle(title string) string {
	runes := []rune(title)
	if len(runes) > 60 {
		return string(runes[:57]) + "…"
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
