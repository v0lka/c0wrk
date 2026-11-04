package session

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// LLMTitleCaller makes LLM title calls without importing sp4rk/llm into the
// backend layer.
type LLMTitleCaller interface {
	GenerateTitle(ctx context.Context, userMessage string, activeSkills []string) (string, error)
}

// TitleGenerator generates concise session titles from user messages.
type TitleGenerator struct {
	caller LLMTitleCaller
	mu     sync.RWMutex
	logger *slog.Logger
}

// NewTitleGenerator creates a TitleGenerator; nil caller = fallback-only titles.
func NewTitleGenerator(caller LLMTitleCaller) *TitleGenerator {
	return &TitleGenerator{caller: caller}
}

func (g *TitleGenerator) log() *slog.Logger {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.logger != nil {
		return g.logger
	}
	return slog.Default()
}

func (g *TitleGenerator) SetLogger(l *slog.Logger) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logger = l
}

// Generate produces a title for the given user message.
func (g *TitleGenerator) Generate(ctx context.Context, userMessage string, activeSkills []string) string {
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

	title := fallbackTitle(userMessage)
	return truncateTitle(title)
}

func truncateTitle(title string) string {
	runes := []rune(title)
	if len(runes) > 60 {
		return string(runes[:57]) + "…"
	}
	return title
}

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
