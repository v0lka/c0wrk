package builtins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/tools"
)

const toolFileopsDescription = `Perform file system operations: read, write, edit, list, search files, and manage directories. The action parameter selects the operation. Key behaviors: edit_file performs find-and-replace where old_string must appear exactly once in the file (fails if not found or if ambiguous). write_file creates parent directories automatically. search_content matches a regex across all files under the given path (max 100 results). For bulk file-name searches prefer the glob tool; for content searches with richer options prefer ripgrep.`

// FileOpsTool provides file system operations: read, write, edit, list, search.
type FileOpsTool struct {
	*tools.BaseTool
}

// NewFileOpsTool creates a new FileOpsTool instance.
func NewFileOpsTool() *FileOpsTool {
	schema := `{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["read_file", "write_file", "edit_file", "list_directory", "search_files", "search_content", "create_directory", "delete_directory", "delete_file"],
				"description": "The file operation to perform"
			},
			"path": {
				"type": "string",
				"description": "Absolute or relative file/directory path for the operation"
			},
			"content": {
				"type": "string",
				"description": "Content to write (used with write_file). Parent directories are created automatically."
			},
			"old_string": {
				"type": "string",
				"description": "Exact string to find (used with edit_file). Must appear exactly once in the file or the operation fails."
			},
			"new_string": {
				"type": "string",
				"description": "Replacement string (used with edit_file). Replaces the single occurrence of old_string."
			},
			"pattern": {
				"type": "string",
				"description": "Glob pattern to match file names, e.g. *.cs, *.ts, *.py, *.java (used with search_files)"
			},
			"regex": {
				"type": "string",
				"description": "Regular expression pattern to match file contents (used with search_content). Returns up to 100 matches."
			},
			"recursive": {
				"type": "boolean",
				"description": "If true, recursively delete all directory contents (used with delete_directory). Required for non-empty directories."
			}
		},
		"required": ["action", "path"]
	}`
	return &FileOpsTool{BaseTool: &tools.BaseTool{
		ToolName:        "file_ops",
		ToolDescription: toolFileopsDescription,
		Schema:          json.RawMessage(schema),
		Policy:          tools.PolicyAuto,
	}}
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
	Recursive bool   `json:"recursive"`
}

// Judge evaluates whether a file operation is safe to execute.
// Read-only actions are always allowed.
// Write actions are allowed if the target is within the session workspace.
func (t *FileOpsTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
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

	// Resolve workspace path to canonical path (follow symlinks)
	workspaceAbs := filepath.Clean(workspacePath)
	workspaceAbs, err = filepath.EvalSymlinks(workspaceAbs)
	if err != nil {
		return false, "" // Can't resolve workspace symlinks, defer to LLM Judge
	}

	// Resolve target path to canonical path (follow symlinks)
	absPathClean := filepath.Clean(absPath)
	if resolved, evalErr := filepath.EvalSymlinks(absPathClean); evalErr == nil {
		absPathClean = resolved
	} else {
		// File may not exist yet (write operations); try resolving the parent directory
		parentDir := filepath.Dir(absPathClean)
		resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
		if parentErr != nil {
			return false, "" // Can't resolve target symlinks, defer to LLM Judge
		}
		absPathClean = filepath.Join(resolvedParent, filepath.Base(absPathClean))
	}

	// Ensure workspace path ends with separator for prefix matching
	if !strings.HasSuffix(workspaceAbs, string(filepath.Separator)) {
		workspaceAbs += string(filepath.Separator)
	}

	// Check if target is inside workspace
	workspaceClean := strings.TrimSuffix(workspaceAbs, string(filepath.Separator))
	if !strings.HasPrefix(absPathClean+string(filepath.Separator), workspaceAbs) && absPathClean != workspaceClean {
		return false, "" // Outside workspace, defer to LLM Judge
	}

	return true, "target is within session workspace"
}

// Execute performs the requested file operation.
func (t *FileOpsTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params fileOpsInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	switch params.Action {
	case "read_file":
		return t.readFile(params.Path)
	case "write_file":
		return t.writeFile(ctx, params.Path, params.Content)
	case "edit_file":
		return t.editFile(ctx, params.Path, params.OldString, params.NewString)
	case "list_directory":
		return t.listDirectory(params.Path)
	case "search_files":
		return t.searchFiles(params.Path, params.Pattern)
	case "search_content":
		return t.searchContent(params.Path, params.Regex)
	case "create_directory":
		return t.createDirectory(params.Path)
	case "delete_directory":
		return t.deleteDirectory(ctx, params.Path, params.Recursive)
	case "delete_file":
		return t.deleteFile(ctx, params.Path)
	default:
		return tools.ToolResult{Content: "unknown action: " + params.Action, IsError: true}, nil
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
func (t *FileOpsTool) writeFile(ctx context.Context, path, content string) (tools.ToolResult, error) {
	tracker := agent.FileTrackerFromContext(ctx)
	if tracker != nil {
		tracker.AcquireFileLock(path)
		defer tracker.ReleaseFileLock(path)
		tracker.RecordBeforeWrite(ctx, path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to create directories: %v", err), IsError: true}, nil
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	if tracker != nil {
		tracker.RecordAfterWrite(ctx, path)
	}

	return tools.ToolResult{Content: fmt.Sprintf("successfully wrote %d bytes to %s", len(content), path), IsError: false}, nil
}

// editFile performs ACI-style find-and-replace in a file.
func (t *FileOpsTool) editFile(ctx context.Context, path, oldString, newString string) (tools.ToolResult, error) {
	tracker := agent.FileTrackerFromContext(ctx)
	if tracker != nil {
		tracker.AcquireFileLock(path)
		defer tracker.ReleaseFileLock(path)
		tracker.RecordBeforeWrite(ctx, path)
	}

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

	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to write file: %v", err), IsError: true}, nil
	}

	if tracker != nil {
		tracker.RecordAfterWrite(ctx, path)
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
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		if info.IsDir() {
			return nil
		}

		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err != nil {
			return nil //nolint:nilerr // continue walking on individual file errors
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
			return nil //nolint:nilerr // continue walking on individual file errors
		}

		if len(results) >= maxMatches {
			return filepath.SkipAll
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil //nolint:nilerr // continue walking on individual file errors
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

	if err != nil && !errors.Is(err, filepath.SkipAll) {
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

// createDirectory creates a directory and all parent directories.
func (t *FileOpsTool) createDirectory(path string) (tools.ToolResult, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to create directory: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: "successfully created directory: " + path, IsError: false}, nil
}

// deleteDirectory deletes a directory. If recursive is true, it removes all contents.
func (t *FileOpsTool) deleteDirectory(ctx context.Context, path string, recursive bool) (tools.ToolResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to stat path: %v", err), IsError: true}, nil
	}
	if !info.IsDir() {
		return tools.ToolResult{Content: "path is not a directory", IsError: true}, nil
	}

	tracker := agent.FileTrackerFromContext(ctx)
	if tracker != nil {
		_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			tracker.AcquireFileLock(p)
			tracker.RecordDelete(ctx, p)
			tracker.ReleaseFileLock(p)
			return nil
		})
	}

	if recursive {
		if err := os.RemoveAll(path); err != nil {
			return tools.ToolResult{Content: fmt.Sprintf("failed to delete directory: %v", err), IsError: true}, nil
		}
	} else {
		if err := os.Remove(path); err != nil {
			return tools.ToolResult{Content: fmt.Sprintf("failed to delete directory: %v", err), IsError: true}, nil
		}
	}

	return tools.ToolResult{Content: "successfully deleted directory: " + path, IsError: false}, nil
}

// deleteFile deletes a single file. Returns an error if the path is a directory.
func (t *FileOpsTool) deleteFile(ctx context.Context, path string) (tools.ToolResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to stat path: %v", err), IsError: true}, nil
	}
	if info.IsDir() {
		return tools.ToolResult{Content: "path is a directory, use delete_directory instead", IsError: true}, nil
	}

	tracker := agent.FileTrackerFromContext(ctx)
	if tracker != nil {
		tracker.AcquireFileLock(path)
		defer tracker.ReleaseFileLock(path)
		tracker.RecordDelete(ctx, path)
	}

	if err := os.Remove(path); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("failed to delete file: %v", err), IsError: true}, nil
	}
	return tools.ToolResult{Content: "successfully deleted file: " + path, IsError: false}, nil
}
