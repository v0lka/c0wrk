package builtins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/v0lka/sp4rk/tools"
)

const toolRipgrepDescription = `Search file contents using regex or literal patterns. Returns matches in "file:line: content" format with optional surrounding context lines. Automatically respects .gitignore rules and skips binary files. Use this when you need to find code patterns, function definitions, or text within files. For finding files by name or path pattern, use glob instead.`

// RipgrepTool searches file contents using regex patterns via the system
// `rg` CLI (ripgrep). The rg binary is a managed runtime dependency provided
// by the tool-manager (see specs/decisions/010-tool-manager.md) and
// PATH-prepended at startup by desktop/startup.go. The path to the rg binary
// is configurable via NewRipgrepToolWithPath; when not provided it is resolved
// from PATH at construction time (falling back to the bare "rg" name) and
// re-resolved lazily in Execute if unset.
type RipgrepTool struct {
	*tools.BaseTool
	limits RipgrepLimits
	rgPath string
}

// NewRipgrepTool creates a new RipgrepTool instance with default limits.
func NewRipgrepTool() *RipgrepTool {
	return NewRipgrepToolWithLimits(DefaultRipgrepLimits())
}

// NewRipgrepToolWithLimits creates a new RipgrepTool instance with specified
// limits. The rg binary path is resolved from PATH, falling back to the bare
// "rg" name when not found.
func NewRipgrepToolWithLimits(limits RipgrepLimits) *RipgrepTool {
	return NewRipgrepToolWithPath(limits, "")
}

// NewRipgrepToolWithPath creates a new RipgrepTool instance with specified
// limits and an explicit path to the rg binary. If rgPath is empty, the rg
// binary is resolved from PATH via exec.LookPath; when found the resolved
// absolute path is used, otherwise the bare "rg" name is stored (and
// re-resolved lazily in Execute if still unset).
func NewRipgrepToolWithPath(limits RipgrepLimits, rgPath string) *RipgrepTool {
	if rgPath == "" {
		if resolved, err := exec.LookPath("rg"); err == nil {
			rgPath = resolved
		} else {
			rgPath = "rg"
		}
	}
	return &RipgrepTool{BaseTool: &tools.BaseTool{
		ToolName:        "ripgrep",
		ToolDescription: toolRipgrepDescription,
		Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex or literal search pattern, e.g. \"func main\" or \"TODO.*fix\""
			},
			"path": {
				"type": "string",
				"description": "Directory to search recursively. Defaults to the project workspace when omitted."
			},
			"file_pattern": {
				"type": "string",
				"description": "Glob filter to restrict which files are searched, e.g. *.php, *.java, *.ts"
			},
			"ignore_case": {
				"type": "boolean",
				"description": "Perform case-insensitive matching. Default: false."
			},
			"context_lines": {
				"type": "integer",
				"description": "Number of lines to show before and after each match. Default: 0."
			},
			"include_hidden": {
				"type": "boolean",
				"description": "Include hidden files and directories in the search. Default: false."
			}
		},
		"required": ["pattern"]
	}`),
		Policy:    tools.PolicyAlwaysAllow,
		Untrusted: true,
	},
		limits: limits,
		rgPath: rgPath,
	}
}

// RipgrepInput represents the input parameters for ripgrep search.
type RipgrepInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	FilePattern   string `json:"file_pattern"`
	IgnoreCase    bool   `json:"ignore_case"`
	ContextLines  int    `json:"context_lines"`
	IncludeHidden bool   `json:"include_hidden"`
}

// rgEvent is the generic envelope emitted by `rg --json`. Every event has
// a type and a nested data object whose shape depends on the type.
type rgEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// rgText wraps ripgrep's "either text or base64 bytes" representation.
// We only care about the text form; binary-only files never produce match
// events because we do not pass --text.
type rgText struct {
	Text string `json:"text"`
}

type rgSubmatch struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type rgMatchData struct {
	Path       rgText       `json:"path"`
	Lines      rgText       `json:"lines"`
	LineNumber int          `json:"line_number"`
	Submatches []rgSubmatch `json:"submatches"`
}

type rgContextData struct {
	Path       rgText `json:"path"`
	Lines      rgText `json:"lines"`
	LineNumber int    `json:"line_number"`
}

// Judge checks whether the search targets a path inside the session roots.
// Paths outside workspace/temp require user confirmation.
func (t *RipgrepTool) Judge(ctx context.Context, input json.RawMessage) (allowed bool, reason string) {
	return judgeReadInSessionRoots(ctx, input)
}

// Execute performs the ripgrep search and returns formatted results.
func (t *RipgrepTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params RipgrepInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Pattern == "" {
		return tools.ToolResult{Content: "validation error: pattern is required", IsError: true}, nil
	}

	// Resolve the rg binary path. When unset (e.g. the struct was constructed
	// directly without a constructor), resolve from PATH; if rg is not found,
	// surface a clear, actionable error.
	rgPath := t.rgPath
	if rgPath == "" {
		resolved, err := exec.LookPath("rg")
		if err != nil {
			return tools.ToolResult{
				Content: "ripgrep (rg) not found in PATH — install ripgrep or configure via NewRipgrepToolWithPath",
				IsError: true,
			}, nil
		}
		rgPath = resolved
	}

	if params.Path == "" {
		params.Path = tools.WorkspacePathFrom(ctx)
		if params.Path == "" {
			return tools.ToolResult{Content: "path is required when no workspace is available", IsError: true}, nil
		}
	} else {
		params.Path = resolvePath(ctx, params.Path)
		if err := validateResolvedPath(params.Path); err != nil {
			return tools.ToolResult{Content: err.Error(), IsError: true}, nil
		}
	}

	// Build `rg --json` args. Ripgrep respects .gitignore by default.
	// Output size is managed by the centralized caching+truncation layer.
	args := []string{"--json", "--no-messages"}
	if params.IgnoreCase {
		args = append(args, "-i")
	}
	if params.ContextLines > 0 {
		args = append(args, "-C", strconv.Itoa(params.ContextLines))
	}
	if params.FilePattern != "" {
		args = append(args, "-g", params.FilePattern)
	}
	if params.IncludeHidden {
		args = append(args, "--hidden")
	}
	args = append(args, "-e", params.Pattern, "--", params.Path)

	searchCtx, cancel := context.WithTimeout(ctx, t.limits.Timeout)
	defer cancel()

	cmd := exec.CommandContext(searchCtx, rgPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}
	if err := cmd.Start(); err != nil {
		return tools.ToolResult{Content: fmt.Sprintf("search error: %v", err), IsError: true}, nil
	}

	scanner := bufio.NewScanner(stdout)
	scannerBuf := make([]byte, 64*1024)
	scanner.Buffer(scannerBuf, 4*1024*1024)

	var sb strings.Builder
	fileSet := make(map[string]struct{})
	matchCount := 0

	for scanner.Scan() {
		var ev rgEvent
		if unmarshalErr := json.Unmarshal(scanner.Bytes(), &ev); unmarshalErr != nil {
			continue
		}
		switch ev.Type {
		case "match":
			var m rgMatchData
			if unmarshalErr := json.Unmarshal(ev.Data, &m); unmarshalErr != nil {
				continue
			}
			path := m.Path.Text
			fileSet[path] = struct{}{}
			content := strings.TrimRight(m.Lines.Text, "\n")
			col := 0
			if len(m.Submatches) > 0 {
				col = m.Submatches[0].Start + 1
			}
			var line string
			if col > 0 {
				line = fmt.Sprintf("%s:%d:%d: %s", path, m.LineNumber, col, content)
			} else {
				line = fmt.Sprintf("%s:%d: %s", path, m.LineNumber, content)
			}
			sb.WriteString(line)
			sb.WriteByte('\n')
			matchCount++
		case "context":
			var c rgContextData
			if unmarshalErr := json.Unmarshal(ev.Data, &c); unmarshalErr != nil {
				continue
			}
			content := strings.TrimRight(c.Lines.Text, "\n")
			fmt.Fprintf(&sb, "  %s\n", content)
		}
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			// rg exit codes: 0 = matches, 1 = no matches, 2+ = error.
			if exitErr.ExitCode() >= 2 {
				return tools.ToolResult{
					Content: "search error: rg failed: " + strings.TrimSpace(stderr.String()),
					IsError: true,
				}, nil
			}
		} else if !errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			return tools.ToolResult{
				Content: fmt.Sprintf("search error: %v", waitErr),
				IsError: true,
			}, nil
		}
	}

	timedOut := errors.Is(searchCtx.Err(), context.DeadlineExceeded)

	if matchCount == 0 {
		content := "no matches found"
		if timedOut {
			content += "\n[search timed out — results may be incomplete]"
		}
		return tools.ToolResult{Content: content}, nil
	}

	fmt.Fprintf(&sb, "\nFound %d matches in %d files", matchCount, len(fileSet))
	if timedOut {
		sb.WriteString("\n[search timed out — results may be incomplete]")
	}

	return tools.ToolResult{Content: sb.String()}, nil
}
