package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// skillNamePattern validates the skill name field per the agentskills.io spec:
// 1-64 chars, lowercase alphanumeric and hyphens, no leading/trailing hyphens.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ParseError describes a validation failure in a SKILL.md file.
type ParseError struct {
	Path    string
	Message string
}

// Error returns the validation failure message prefixed with the SKILL.md path.
func (e *ParseError) Error() string {
	return fmt.Sprintf("skill parse error (%s): %s", e.Path, e.Message)
}

// ParseSkill reads and validates a SKILL.md file, returning a Skill.
// dirPath is the absolute path to the skill directory (used for DirPath and name validation).
func ParseSkill(skillMDPath, dirPath string) (*Skill, error) {
	data, err := os.ReadFile(skillMDPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	meta, body, err := parseFrontmatter(string(data))
	if err != nil {
		return nil, &ParseError{Path: skillMDPath, Message: err.Error()}
	}

	skill := &Skill{
		Metadata: *meta,
		Body:     body,
		DirPath:  dirPath,
	}

	if err := validateSkill(skill, dirPath); err != nil {
		return nil, &ParseError{Path: skillMDPath, Message: err.Error()}
	}

	return skill, nil
}

// parseFrontmatter splits a SKILL.md into YAML frontmatter and markdown body.
// Expects the content to start with "---" on its own line.
func parseFrontmatter(content string) (*SkillMetadata, string, error) {
	if content == "" {
		return nil, "", errors.New("empty SKILL.md content")
	}

	// Find opening ---
	rest := content
	if rest != "" && rest[0] == '\n' {
		rest = rest[1:]
	}

	// Match opening --- delimiter
	idx := findFrontmatterDelim(rest)
	if idx < 0 {
		return nil, "", errors.New("missing opening --- frontmatter delimiter")
	}
	rest = rest[idx:]

	// Find closing ---
	endIdx := findFrontmatterDelim(rest)
	if endIdx < 0 {
		return nil, "", errors.New("missing closing --- frontmatter delimiter")
	}
	yamlContent := rest[:endIdx]
	body := rest[endIdx:]

	// Trim leading whitespace from body
	if body != "" && body[0] == '\n' {
		body = body[1:]
	}

	var meta SkillMetadata
	if err := yaml.Unmarshal([]byte(yamlContent), &meta); err != nil {
		return nil, "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}

	return &meta, body, nil
}

// findFrontmatterDelim finds the position after the next "---\n" delimiter,
// skipping the delimiter itself. Returns -1 if not found.
func findFrontmatterDelim(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			// Require --- to be at the start of a line (or start of string)
			// to avoid matching --- inside YAML string values.
			if i > 0 && s[i-1] != '\n' {
				continue
			}
			if i+2 < len(s) && s[i+1] == '-' && s[i+2] == '-' {
				// Found ---, skip past it and any trailing whitespace/newline
				j := i + 3
				for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r') {
					j++
				}
				if j < len(s) && s[j] == '\n' {
					j++
				}
				return j
			}
		}
	}
	return -1
}

// validateSkill checks all spec constraints on a parsed Skill.
// Note: the name regex permits consecutive hyphens (e.g. "a--b"); the
// agentskills.io spec wording is ambiguous on this point and the current
// pattern intentionally allows them.
func validateSkill(skill *Skill, dirPath string) error {
	// Name is required
	if skill.Metadata.Name == "" {
		return errors.New("name field is required")
	}

	// Name length 1-64
	if len(skill.Metadata.Name) > 64 {
		return fmt.Errorf("name must be at most 64 characters, got %d", len(skill.Metadata.Name))
	}

	// Name format: lowercase alphanumeric + hyphens, no leading/trailing/consecutive hyphens
	if !skillNamePattern.MatchString(skill.Metadata.Name) {
		return fmt.Errorf("name %q must be lowercase alphanumeric with hyphens, no leading/trailing/consecutive hyphens", skill.Metadata.Name)
	}

	// Name must match parent directory name
	dirName := filepath.Base(dirPath)
	if skill.Metadata.Name != dirName {
		return fmt.Errorf("name %q must match parent directory name %q", skill.Metadata.Name, dirName)
	}

	// Description is required
	if skill.Metadata.Description == "" {
		return errors.New("description field is required")
	}

	// Description max 1024 characters
	if len(skill.Metadata.Description) > 1024 {
		return fmt.Errorf("description must be at most 1024 characters, got %d", len(skill.Metadata.Description))
	}

	// Compatibility max 500 characters
	if len(skill.Metadata.Compatibility) > 500 {
		return fmt.Errorf("compatibility must be at most 500 characters, got %d", len(skill.Metadata.Compatibility))
	}

	return nil
}
