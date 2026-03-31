package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/user/agent/internal/tools"
)

const toolFileopsDescription = "File system operations: read, write, edit, list, search"

// FileOpsTool provides file system operations: read, write, edit, list, search.
type FileOpsTool struct{}

// NewFileOpsTool creates a new FileOpsTool instance.
func NewFileOpsTool() *FileOpsTool {
	return &FileOpsTool{}
}

// Name returns the tool name.
func (t *FileOpsTool) Name() string {
	return "file_ops"
}

// Description returns the tool description.
func (t *FileOpsTool) Description() string {
	return toolFileopsDescription
}

// InputSchema returns the JSON schema for the tool input.
func (t *FileOpsTool) InputSchema() json.RawMessage {
	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["read_file", "write_file", "edit_file", "list_directory", "search_files", "search_content"],
				"description": "The file operation to perform"
			},
			"path": {
				"type": "string",
				"description": "File or directory path"
			},
			"content": {
				"type": "string",
				"description": "Content to write (for write_file action)"
			},
			"old_string": {
				"type": "string",
				"description": "String to find and replace (for edit_file action)"
			},
			"new_string": {
				"type": "string",
				"description": "Replacement string (for edit_file action)"
			},
			"pattern": {
				"type": "string",
				"description": "Glob pattern for file search (for search_files action)"
			},
			"regex": {
				"type": "string",
				"description": "Regular expression pattern (for search_content action)"
			}
		},
		"required": ["action", "path"]
	}`
	return json.RawMessage(schema)
}

// fileOpsInput represents the input structure for file operations.
type fileOpsInput struct {
	Action    string `json:"action"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Pattern   string `json:"pattern"`
	Regex     string `json:"regex"`
}

// DefaultPolicy returns PolicyAuto to enable tool-specific heuristics.
func (t *FileOpsTool) DefaultPolicy() tools.ToolPolicy {
	return tools.PolicyAuto
}

// Judge evaluates whether a file operation is safe to execute.
// Read-only actions are always allowed.
// Write actions are allowed if the target is within the session workspace.
func (t *FileOpsTool) Judge(ctx context.Context, input json.RawMessage) (bool, string) {
	var params fileOpsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return false, "" // Defer to LLM Judge on parse error
	}

	// Read-only actions are always allowed
	readOnlyActions := map[string]bool{
		"read_file":      true,
		"list_directory": true,
		"search_files":   true,
		"search_content": true,
	}
	if readOnlyActions[params.Action] {
		return true, "read-only file operation"
	}

	// Write actions require workspace boundary check
	workspacePath := tools.WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return false, "" // No workspace set, defer to LLM Judge
	}

	// Resolve target path to absolute
	absPath, err := filepath.Abs(params.Path)
	if err != nil {
		return false, "" // Can't resolve path, defer to LLM Judge
	}

	// Ensure workspace path ends with separator for prefix matching
	workspaceAbs := filepath.Clean(workspacePath)
	if !strings.HasSuffix(workspaceAbs, string(filepath.Separator)) {
		workspaceAbs += string(filepath.Separator)
	}

	// Check if target is inside workspace
	absPathClean := filepath.Clean(absPath)
	if !strings.HasPrefix(absPathClean+string(filepath.Separator), workspaceAbs) && absPathClean != filepath.Clean(workspacePath) {
		return false, "" // Outside workspace, defer to LLM Judge
	}

	return true, "target is within session workspace"
}

// Execute performs the requested file operation.
func (t *FileOpsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params fileOpsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}

	switch params.Action {
	case "read_file":
		return t.readFile(params.Path)
	case "write_file":
		return t.writeFile(params.Path, params.Content)
	case "edit_file":
		return t.editFile(params.Path, params.OldString, params.NewString)
	case "list_directory":
		return t.listDirectory(params.Path)
	case "search_files":
		return t.searchFiles(params.Path, params.Pattern)
	case "search_content":
		return t.searchContent(params.Path, params.Regex)
	default:
		return tools.ToolResult{Content: fmt.Sprintf("unknown action: %s", params.Action), IsError: true}, nil
	}
}

// readFile reads and returns the content of a file.
func (t *FileOpsTool) readFile(path string) (tools.ToolResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to read file: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: string(data), IsError: false}, nil
}

// writeFile writes content to a file, creating parent directories if needed.
func (t *FileOpsTool) writeFile(path, content string) (tools.ToolResult, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to create directories: %v", err), IsError: true}, nil
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: fmt.Sprintf("successfully wrote %d bytes to %s", len(content), path), IsError: false}, nil
}

// editFile performs ACI-style find-and-replace in a file.
func (t *FileOpsTool) editFile(path, oldString, newString string) (tools.ToolResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to read file: %v", err), IsError: true}, nil
	}

	content := string(data)
	count := strings.Count(content, oldString)

	if count == 0 {
		return tools.ToolResult{Content: "old_string not found in file", IsError: true}, nil
	}

	if count > 1 {
		return tools.ToolResult{Content: fmt.Sprintf("old_string is not unique, found %d occurrences", count), IsError: true}, nil
	}

	newContent := strings.Replace(content, oldString, newString, 1)

	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	return tools.ToolResult{Content: "successfully edited file", IsError: false}, nil
}

// listDirectory lists the contents of a directory.
func (t *FileOpsTool) listDirectory(path string) (tools.ToolResult, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to read directory: %v", err), IsError: true}, nil
	}

	var sb strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		entryType := "file"
		if entry.IsDir() {
			entryType = "dir"
		}

		fmt.Fprintf(&sb, "%s\t%s\t%d\n", entry.Name(), entryType, info.Size())
	}

	return tools.ToolResult{Content: sb.String(), IsError: false}, nil
}

// searchFiles searches for files matching a glob pattern.
func (t *FileOpsTool) searchFiles(basePath, pattern string) (tools.ToolResult, error) {
	var matches []string

	err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if info.IsDir() {
			return nil
		}

		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil {
			return nil // Invalid pattern, skip
		}

		if matched {
			matches = append(matches, path)
		}
		return nil
	})

	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search files: %v", err), IsError: true}, nil
	}

	if len(matches) == 0 {
		return tools.ToolResult{Content: "no matching files found", IsError: false}, nil
	}

	return tools.ToolResult{Content: strings.Join(matches, "\n"), IsError: false}, nil
}

// searchContent searches file contents for regex matches.
func (t *FileOpsTool) searchContent(basePath, regexPattern string) (tools.ToolResult, error) {
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("invalid regex: %v", err), IsError: true}, nil
	}

	const maxMatches = 100
	var results []string

	err = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		if len(results) >= maxMatches {
			return filepath.SkipAll
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // Skip files we can't read
		}

		lines := strings.Split(string(data), "\n")
		for lineNum, line := range lines {
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", path, lineNum+1, line))
				if len(results) >= maxMatches {
					return filepath.SkipAll
				}
			}
		}

		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return tools.ToolResult{Content: fmt.Sprintf("failed to search content: %v", err), IsError: true}, nil
	}

	if len(results) == 0 {
		return tools.ToolResult{Content: "no matches found", IsError: false}, nil
	}

	content := strings.Join(results, "\n")
	if len(results) >= maxMatches {
		content += fmt.Sprintf("\n(results limited to %d matches)", maxMatches)
	}

	return tools.ToolResult{Content: content, IsError: false}, nil
}
