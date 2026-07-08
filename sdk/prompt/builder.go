// Package prompt provides builders for constructing LLM system prompts with
// cache-break support and family-aware sampling defaults.
package prompt

import (
	"sort"
	"strings"
)

// CacheBreakMarker is the sentinel used to split a system prompt string into
// cacheable (stable) and dynamic parts. Consumers like ContextWindow check for
// this marker to emit multiple system messages, enabling provider-level prompt
// caching (e.g. Anthropic's ephemeral cache control).
const CacheBreakMarker = "\x00CACHE_BREAK\x00"

// SplitCacheBreak splits a system prompt on CacheBreakMarker.
// Returns the parts (1 if no marker, 2 if marker present).
// Empty parts are omitted.
func SplitCacheBreak(systemPrompt string) []string {
	parts := strings.Split(systemPrompt, CacheBreakMarker)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

type section struct {
	content string
}

// Builder provides a fluent API for constructing prompts.
type Builder struct {
	sections          []section
	substitutions     map[string]string
	dataSubstitutions map[string]string
	cacheBreakIdx     int // index in sections after which dynamic content starts; -1 = no break
}

// NewBuilder creates a new prompt Builder.
func NewBuilder() *Builder {
	return &Builder{
		sections:          nil,
		substitutions:     make(map[string]string),
		dataSubstitutions: make(map[string]string),
		cacheBreakIdx:     -1,
	}
}

// Core adds a section that is always included in the final prompt.
func (b *Builder) Core(content string) *Builder {
	b.sections = append(b.sections, section{content: content})
	return b
}

// Replace registers a placeholder substitution applied during Build().
func (b *Builder) Replace(placeholder, value string) *Builder {
	b.substitutions[placeholder] = value
	return b
}

// ReplaceAll registers multiple placeholder substitutions applied during Build().
func (b *Builder) ReplaceAll(substitutions map[string]string) *Builder {
	for placeholder, value := range substitutions {
		b.substitutions[placeholder] = value
	}
	return b
}

// ReplaceData registers a placeholder substitution for UNTRUSTED values
// (dynamic/external content such as user requests, conversation history, or
// tool outputs). Data substitutions are applied LAST, in a single pass without
// re-scanning: a placeholder name occurring inside an untrusted value is NOT
// expanded, preventing placeholder-injection through untrusted content.
//
// Use Replace for trusted template-on-template substitutions (values that may
// legitimately contain other placeholders); use ReplaceData for everything
// derived from external input.
func (b *Builder) ReplaceData(placeholder, value string) *Builder {
	b.dataSubstitutions[placeholder] = value
	return b
}

// ReplaceDataAll registers multiple untrusted-value substitutions.
// See ReplaceData for semantics.
func (b *Builder) ReplaceDataAll(substitutions map[string]string) *Builder {
	for placeholder, value := range substitutions {
		b.dataSubstitutions[placeholder] = value
	}
	return b
}

// CacheBreak marks the current position as the boundary between stable
// (cacheable) and dynamic prompt content. Sections added before this call
// form the stable part; sections added after form the dynamic part.
func (b *Builder) CacheBreak() *Builder {
	b.cacheBreakIdx = len(b.sections)
	return b
}

// BuildParts returns the prompt split at the CacheBreak boundary.
// If no CacheBreak was set, stable contains the full prompt and dynamic is empty.
// Substitutions are applied to each part independently, with multiple passes to
// resolve nested placeholders (e.g. RECENT-CONVERSATION inside MODE-PREAMBLE).
func (b *Builder) BuildParts() (stable, dynamic string) {
	if b.cacheBreakIdx < 0 {
		return b.Build(), ""
	}

	stable = joinSections(b.sections[:b.cacheBreakIdx])
	dynamic = joinSections(b.sections[b.cacheBreakIdx:])

	stable = applySubstitutionsIteratively(stable, b.substitutions)
	dynamic = applySubstitutionsIteratively(dynamic, b.substitutions)

	stable = applyDataSubstitutions(stable, b.dataSubstitutions)
	dynamic = applyDataSubstitutions(dynamic, b.dataSubstitutions)

	return stable, dynamic
}

// applyDataSubstitutions replaces untrusted-value placeholders in a single
// pass without re-scanning replaced content. strings.Replacer scans the text
// left-to-right exactly once, so a placeholder name occurring inside a
// substituted value is never expanded (no placeholder injection).
//
// Placeholders are ordered longest-first so a key that is a prefix of another
// key never shadows it (map iteration order is otherwise random).
func applyDataSubstitutions(text string, substitutions map[string]string) string {
	if len(substitutions) == 0 {
		return text
	}
	keys := make([]string, 0, len(substitutions))
	for placeholder := range substitutions {
		keys = append(keys, placeholder)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := make([]string, 0, len(keys)*2)
	for _, placeholder := range keys {
		pairs = append(pairs, placeholder, substitutions[placeholder])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// maxSubstitutionPasses caps iterative substitution to prevent infinite loops
// from circular placeholders.
const maxSubstitutionPasses = 5

// applySubstitutionsIteratively replaces all placeholders in text until the
// text stabilises or maxSubstitutionPasses is reached. This handles cases
// where one substitution value itself contains another placeholder key
// (e.g. MODE-PREAMBLE contains RECENT-CONVERSATION).
func applySubstitutionsIteratively(text string, substitutions map[string]string) string {
	for range maxSubstitutionPasses {
		var changed bool
		for placeholder, value := range substitutions {
			if strings.Contains(text, placeholder) {
				text = strings.ReplaceAll(text, placeholder, value)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return text
}

// Build assembles the final prompt string.
// When CacheBreak() was called, a CacheBreakMarker is inserted between
// the stable and dynamic parts. Otherwise sections are joined with double newlines.
func (b *Builder) Build() string {
	if b.cacheBreakIdx >= 0 {
		stable, dynamic := b.BuildParts()
		if dynamic == "" {
			return stable
		}
		return stable + CacheBreakMarker + dynamic
	}

	result := joinSections(b.sections)

	// Apply substitutions iteratively to resolve nested placeholders
	// (e.g. RECENT-CONVERSATION inside MODE-PREAMBLE).
	result = applySubstitutionsIteratively(result, b.substitutions)

	// Untrusted data substitutions run last, in a single pass — values
	// containing placeholder names are NOT expanded.
	result = applyDataSubstitutions(result, b.dataSubstitutions)

	return result
}

// joinSections concatenates non-empty section contents with double newlines.
func joinSections(sections []section) string {
	var included []string
	for _, s := range sections {
		if s.content == "" {
			continue
		}
		included = append(included, s.content)
	}
	return strings.Join(included, "\n\n")
}

// SystemPromptBuilder wraps Builder for constructing system prompts with
// cache-break support. It is intended for use by orchestrator SystemPromptFactory
// implementations to build stable (cacheable) + dynamic prompt parts.
type SystemPromptBuilder struct {
	b *Builder
}

// NewSystemPromptBuilder creates a new SystemPromptBuilder.
func NewSystemPromptBuilder() *SystemPromptBuilder {
	return &SystemPromptBuilder{b: NewBuilder()}
}

// Core adds a section that is always included in the system prompt.
func (s *SystemPromptBuilder) Core(content string) *SystemPromptBuilder {
	s.b.Core(content)
	return s
}

// Replace registers a placeholder substitution.
func (s *SystemPromptBuilder) Replace(placeholder, value string) *SystemPromptBuilder {
	s.b.Replace(placeholder, value)
	return s
}

// ReplaceData registers a placeholder substitution for UNTRUSTED values.
// Data substitutions are applied last in a single pass without re-scanning,
// so placeholder names inside untrusted values are never expanded.
// See Builder.ReplaceData.
func (s *SystemPromptBuilder) ReplaceData(placeholder, value string) *SystemPromptBuilder {
	s.b.ReplaceData(placeholder, value)
	return s
}

// Dynamic adds a section that appears after the cache-break boundary
// (i.e., not cached by providers). Must be called after CacheBreak().
func (s *SystemPromptBuilder) Dynamic(content string) *SystemPromptBuilder {
	s.b.Core(content)
	return s
}

// CacheBreak marks the boundary between stable (cacheable) and dynamic content.
func (s *SystemPromptBuilder) CacheBreak() *SystemPromptBuilder {
	s.b.CacheBreak()
	return s
}

// Build returns the full system prompt string with CacheBreakMarker between
// stable and dynamic parts (when CacheBreak was called), enabling downstream
// splitting via SplitCacheBreak.
func (s *SystemPromptBuilder) Build() string {
	return s.b.Build()
}
