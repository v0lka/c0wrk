# Agent Skills

The `skills` package provides discovery, parsing, and management of Agent Skills following the open agentskills.io specification. Skills are markdown documents (`SKILL.md`) with YAML frontmatter that bundle instructions and optional resources an agent can activate on demand.

```go
import "github.com/v0lka/sp4rk/skills"
```

## SkillManager

`SkillManager` discovers, parses, and serves Agent Skills from configured directories. Directories are scanned in priority order; the first occurrence of a skill name wins, so higher-priority directories override lower-priority ones.

### NewSkillManager

```go
func NewSkillManager(dirs []string, logger *slog.Logger) *SkillManager
```

`dirs` is a list of discovery directories in priority order (highest priority first). Call `Scan()` to populate the catalog.

```go
mgr := skills.NewSkillManager([]string{
    "/home/user/project/.agents/skills", // highest priority
    "/home/user/.agents/skills",         // user-level
    "/usr/local/share/agents/skills",    // system-level
}, logger)
if err := mgr.Scan(); err != nil {
    log.Fatal(err)
}
```

### Scan

```go
func (m *SkillManager) Scan() error
```

`Scan` walks all discovery directories and loads valid skills. It clears any existing catalog first, so it is safe to call repeatedly (e.g. after skills are added or removed). Directories are walked in **reverse priority order** so that higher-priority entries overwrite lower-priority ones with the same name.

Invalid `SKILL.md` files are logged at debug level and skipped — a directory that is not an agent skill is simply ignored rather than causing an error.

**Symlink following:** `Scan` follows symlinks that point to directories. This lets you keep a skill repository elsewhere and link it into a discovery directory.

### List

```go
func (m *SkillManager) List() []SkillDescriptor
```

Returns lightweight descriptors for all discovered skills. Each descriptor contains only the name and description (~100 tokens total), making it suitable for the discovery/matching phase where the full skill body is not yet needed.

### Get

```go
func (m *SkillManager) Get(name string) (*Skill, bool)
```

Returns the full `Skill` by name, or `(_, false)` if not found.

### SkillPath

```go
func (m *SkillManager) SkillPath(name string) (string, bool)
```

Returns the absolute directory path for a named skill, or `("", false)` if the skill is not found.

### SafeResolvePath

```go
func SafeResolvePath(baseDir, relPath string) (string, error)
```

`SafeResolvePath` resolves a relative path within a base directory, preventing path traversal attacks. It is used by both `SkillManager` and `ReadSkillResourceTool` to safely access skill-bundled resources.

It works as follows:

1. Cleans `baseDir`. If `baseDir` itself is a symlink, it resolves the symlink (but does **not** resolve ancestor symlinks like macOS `/var → /private/var`, preserving textual path consistency).
2. Joins and cleans the relative path.
3. Resolves any symlinks in the joined path to their real filesystem paths, preventing symlink-based traversal bypass (e.g. a symlink inside the skill directory pointing outside). Symlinks are resolved through the longest existing prefix (`pathutil.ResolveExistingPrefix`), so partially-existing paths (e.g. broken links) are still symlink-checked — there is no textual-only fallback.
4. Verifies the resolved path is within `baseDir` using `pathutil.IsWithinPath`. Returns an error if the path escapes the skill directory.

```go
abs, err := skills.SafeResolvePath(skillDir, "references/api.md")
if err != nil {
    // path traversal attempt blocked
}
```

## Skill

`Skill` represents a fully loaded Agent Skill — metadata, instructions, and filesystem path.

```go
type Skill struct {
    Metadata SkillMetadata
    Body     string // Markdown body after YAML frontmatter
    DirPath  string // Absolute path to the skill directory
}
```

`Descriptor()` returns the lightweight `SkillDescriptor` for discovery.

## SkillMetadata

`SkillMetadata` holds the parsed YAML frontmatter fields of a `SKILL.md` file.

```go
type SkillMetadata struct {
    Name          string            `yaml:"name"`
    Description   string            `yaml:"description"`
    License       string            `yaml:"license,omitempty"`
    Compatibility string            `yaml:"compatibility,omitempty"`
    AllowedTools  string            `yaml:"allowed-tools,omitempty"` // Space-separated (experimental)
    Extra         map[string]string `yaml:"metadata,omitempty"`      // Arbitrary key-value metadata
}
```

### AllowedToolList

```go
func (m *SkillMetadata) AllowedToolList() []string
```

Parses the space-separated `allowed-tools` field into a slice. Returns `nil` if the field is empty. This is an experimental field for restricting which tools a skill may use.

```go
tools := skill.Metadata.AllowedToolList()
// "read_file write_file" → ["read_file", "write_file"]
```

## SkillDescriptor

`SkillDescriptor` is the lightweight discovery-time representation of a skill — name and description only (~100 tokens). It is what `List()` returns and what the router uses for matching.

```go
type SkillDescriptor struct {
    Name        string `json:"name"`
    Description string `json:"description"`
}
```

## SKILL.md Format

A `SKILL.md` file consists of YAML frontmatter delimited by `---` lines, followed by a markdown body.

```markdown
---
name: go-testing
description: Use when writing Go tests with the standard testing package.
license: MIT
compatibility: ">=1.21"
allowed-tools: read_file write_file bash
metadata:
  author: example
---

# Go Testing Skill

Write tests using `go test`. Place test files alongside source as `*_test.go`.
Use table-driven tests for clarity.
```

### Validation rules

`ParseSkill` enforces the following constraints:

| Field | Rule |
| --- | --- |
| `name` | Required. 1–64 characters, lowercase alphanumeric and hyphens, no leading/trailing hyphens (regex `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`). Consecutive hyphens are permitted. Must match the parent directory name. |
| `description` | Required. At most 1024 characters. |
| `compatibility` | At most 500 characters. |

A skill whose `name` does not match its parent directory name is rejected.

### ParseSkill

```go
func ParseSkill(skillMDPath, dirPath string) (*Skill, error)
```

Reads and validates a `SKILL.md` file. `dirPath` is the absolute path to the skill directory (used for `DirPath` and name validation). On validation failure, returns a `*ParseError` describing the problem.

## ReadSkillResourceTool

`ReadSkillResourceTool` is a built-in tool (`read_skill_resource`) that reads resource files from activated skill directories. It is constructed with a `SkillPathResolver` — a function that resolves a skill name to its directory path, carrying per-session activation state.

```go
type SkillPathResolver func(ctx context.Context, skillName string) (dirPath string, ok bool)

func NewReadSkillResourceTool(resolver SkillPathResolver) *ReadSkillResourceTool
```

The tool uses `SafeResolvePath` to prevent path traversal, so an agent cannot read files outside a skill's directory. Skill resources are read-only and safe, so the tool uses an always-allow policy.

## Complete Example

This example mirrors the skill-discovery flow from the full-power example: seed a sample skill on disk, scan the directory, and list the discovered skills.

```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk/skills"
)

func main() {
	// Create a temporary skills directory and seed a sample skill.
	skillsDir, err := os.MkdirTemp("", "skills-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(skillsDir)

	seedSkill(skillsDir)

	// Discover skills from the directory.
	mgr := skills.NewSkillManager([]string{skillsDir}, nil)
	if err := mgr.Scan(); err != nil {
		log.Fatal(err)
	}

	// List lightweight descriptors (discovery phase).
	discovered := mgr.List()
	fmt.Printf("Discovered skills: %d\n", len(discovered))
	for _, s := range discovered {
		fmt.Printf("  • %s: %s\n", s.Name, s.Description)
	}

	// Fetch the full skill body.
	if skill, ok := mgr.Get("go-testing"); ok {
		fmt.Println("\nSkill body:")
		fmt.Println(skill.Body)

		dir, _ := mgr.SkillPath("go-testing")
		fmt.Println("Skill directory:", dir)
	}
}

func seedSkill(skillsDir string) {
	skillDir := filepath.Join(skillsDir, "go-testing")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		log.Fatal(err)
	}
	content := "---\n" +
		"name: go-testing\n" +
		"description: Use when writing Go tests with the standard testing package.\n" +
		"---\n" +
		"# Go Testing Skill\n\n" +
		"Write tests using `go test`. Place test files alongside source as `*_test.go`.\n"
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644)
}
```

Output:

```
Discovered skills: 1
  • go-testing: Use when writing Go tests with the standard testing package.

Skill body:
# Go Testing Skill

Write tests using `go test`. Place test files alongside source as `*_test.go`.

Skill directory: /tmp/skills-XXXXX/go-testing
```
