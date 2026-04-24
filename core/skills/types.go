// Package skills provides discovery, parsing, and management of Agent Skills
// following the open agentskills.io specification.
package skills

import "strings"

// Skill represents a fully loaded Agent Skill — metadata + instructions + filesystem path.
type Skill struct {
	Metadata SkillMetadata
	Body     string // Markdown body after YAML frontmatter
	DirPath  string // Absolute path to the skill directory
}

// SkillMetadata holds the parsed YAML frontmatter fields of a SKILL.md file.
type SkillMetadata struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"` // Space-separated tool names (experimental)
	Extra         map[string]string `yaml:"metadata,omitempty"`      // Arbitrary key-value metadata
}

// SkillDescriptor is the lightweight discovery-time representation of a skill
// (name + description only, ~100 tokens). Used by the Router for matching.
type SkillDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Descriptor returns the lightweight SkillDescriptor for discovery.
func (s *Skill) Descriptor() SkillDescriptor {
	return SkillDescriptor{
		Name:        s.Metadata.Name,
		Description: s.Metadata.Description,
	}
}

// AllowedToolList parses the space-separated allowed-tools field into a slice.
// Returns nil if the field is empty.
func (m *SkillMetadata) AllowedToolList() []string {
	if m.AllowedTools == "" {
		return nil
	}
	var result []string
	for _, tool := range splitSpaces(m.AllowedTools) {
		if tool != "" {
			result = append(result, tool)
		}
	}
	return result
}

// splitSpaces splits a string on whitespace, similar to strings.Fields.
func splitSpaces(s string) []string {
	return strings.Fields(s)
}
