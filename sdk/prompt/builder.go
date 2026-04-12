package prompt

import "strings"

type section struct {
	content string
	tier    ModelTier // empty string = core (always included)
}

// Builder provides a fluent API for constructing prompts with model-tier awareness.
type Builder struct {
	tier          ModelTier
	sections      []section
	substitutions map[string]string
}

// New creates a new prompt Builder for the given model tier.
func New(tier ModelTier) *Builder {
	return &Builder{
		tier:          tier,
		sections:      nil,
		substitutions: make(map[string]string),
	}
}

// Core adds a section that is always included regardless of model tier.
func (b *Builder) Core(content string) *Builder {
	b.sections = append(b.sections, section{content: content, tier: ""})
	return b
}

// ForLarge adds a section included only when building for TierLarge.
func (b *Builder) ForLarge(content string) *Builder {
	b.sections = append(b.sections, section{content: content, tier: TierLarge})
	return b
}

// ForSmall adds a section included only when building for TierSmall.
func (b *Builder) ForSmall(content string) *Builder {
	b.sections = append(b.sections, section{content: content, tier: TierSmall})
	return b
}

// Adaptive adds a tier-dependent section: large content for TierLarge, small content for TierSmall.
func (b *Builder) Adaptive(large, small string) *Builder {
	b.sections = append(b.sections,
		section{content: large, tier: TierLarge},
		section{content: small, tier: TierSmall},
	)
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
// It includes only sections matching the builder's tier, applies all registered
// substitutions, and joins sections with double newlines.
func (b *Builder) Build() string {
	var included []string

	for _, s := range b.sections {
		// Skip empty content sections
		if s.content == "" {
			continue
		}
		// Include if core (empty tier) or tier matches
		if s.tier == "" || s.tier == b.tier {
			included = append(included, s.content)
		}
	}

	result := strings.Join(included, "\n\n")

	// Apply substitutions
	for placeholder, value := range b.substitutions {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
