package builtins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/user/agent/sdk/tools"
)

const toolRipgrepDescription = `Search file contents using regex or literal patterns. Returns matches in "file:line: content" format with optional surrounding context lines. Automatically respects .gitignore rules and skips binary files. Use this when you need to find code patterns, function definitions, or text within files. Returns up to 200 matches by default. For finding files by name or path pattern, use glob instead.`

// RipgrepTool searches file contents using regex patterns via the system
// `rg` CLI (ripgrep). The binary is a declared runtime prerequisite (see
// specs/decisions/004-external-binary-dependencies.md) — availability is
// verified at application startup, so this tool does not attempt to work
// around a missing binary.
type RipgrepTool struct {
	*tools.BaseTool
	limits RipgrepLimits
}

// NewRipgrepTool creates a new RipgrepTool instance with default limits.
func NewRipgrepTool() *RipgrepTool {
	return NewRipgrepToolWithLimits(DefaultRipgrepLimits())
}

// NewRipgrepToolWithLimits creates a new RipgrepTool instance with specified limits.
func NewRipgrepToolWithLimits(limits RipgrepLimits) *RipgrepTool {
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
			"max_results": {
				"type": "integer",
				"description": "Maximum number of matches to return. Default: 200."
			},
			"include_hidden": {
				"type": "boolean",
				"description": "Include hidden files and directories in the search. Default: false."
			}
		},
		"required": ["pattern"]
	}`),
		Policy: tools.PolicyAlwaysAllow,
	},
		limits: limits,
	}
}

// RipgrepInput represents the input parameters for ripgrep search.
type RipgrepInput struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	FilePattern   string `json:"file_pattern"`
	IgnoreCase    bool   `json:"ignore_case"`
	ContextLines  int    `json:"context_lines"`
	MaxResults    int    `json:"max_results"`
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

// Execute performs the ripgrep search and returns formatted results.
func (t *RipgrepTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	var params RipgrepInput
	if err := json.Unmarshal(input, &params); err != nil {
		return tools.ParseInputError(err)
	}

	if params.Pattern == "" {
		return tools.ToolResult{Content: "validation error: pattern is required", IsError: true}, nil
	}

	if params.Path == "" {
		params.Path = tools.WorkspacePathFrom(ctx)
		if params.Path == "" {
			return tools.ToolResult{Content: "path is required when no workspace is available", IsError: true}, nil
		}
	} else {
		params.Path = resolvePath(ctx, params.Path)
	}

	if params.MaxResults <= 0 {
		params.MaxResults = t.limits.MaxResults
	}

	// Build `rg --json` args. Ripgrep respects .gitignore by default;
	// no flag needed. We deliberately do NOT pass --max-count because that
	// caps per-file matches — the tool's MaxResults is a global cap, which
	// we enforce by counting events and killing rg when it is reached.
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

	cmd := exec.CommandContext(searchCtx, "rg", args...)
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
	truncated := false

	for scanner.Scan() {
		if matchCount >= params.MaxResults {
			truncated = true
			break
		}
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
			if len(line) > t.limits.MaxLineLength {
				line = line[:t.limits.MaxLineLength] + "...(line truncated)"
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

	// When we hit max_results we kill rg and then drain stdout so Wait
	// can return cleanly. Any scanner.Err() after kill is expected.
	if truncated {
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, stdout)
	}

	waitErr := cmd.Wait()
	if waitErr != nil && !truncated {
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

	if matchCount == 0 {
		return tools.ToolResult{Content: "no matches found"}, nil
	}

	fmt.Fprintf(&sb, "\nFound %d matches in %d files", matchCount, len(fileSet))
	if truncated {
		sb.WriteString(" (truncated at max_results)")
	}

	return tools.ToolResult{Content: sb.String()}, nil
}