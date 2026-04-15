package prompt

import "strings"

type section struct {
	content string
}

// Builder provides a fluent API for constructing prompts.
type Builder struct {
	sections      []section
	substitutions map[string]string
}

// NewBuilder creates a new prompt Builder.
func NewBuilder() *Builder {
	return &Builder{
		sections:      nil,
		substitutions: make(map[string]string),
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

// Build assembles the final prompt string.
// It includes all non-empty sections, applies all registered
// substitutions, and joins sections with double newlines.
func (b *Builder) Build() string {
	var included []string

	for _, s := range b.sections {
		if s.content == "" {
			continue
		}
		included = append(included, s.content)
	}

	result := strings.Join(included, "\n\n")

	// Apply substitutions
	for placeholder, value := range b.substitutions {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
