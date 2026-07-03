package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/v0lka/c0wrk/sdk/pathutil"
)

// SymlinkTraversal describes a path that traverses a symlink.
type SymlinkTraversal struct {
	OriginalPath     string // user-visible path from tool input
	SymlinkAt        string // component where the symlink was detected
	ResolvesTo       string // what the symlink points to (readlink result)
	FullResolved     string // fully resolved absolute path after symlink chain
	OutsideWorkspace bool   // does the fully resolved path fall outside the workspace?
}

// DetectSymlinksInToolInput extracts all path-like values from a tool input
// and checks each for symlinks. Returns traversals partitioned by whether
// the resolved target is inside or outside the workspace, plus a suspicious
// flag for bash_exec commands with unexpandable tokens.
func DetectSymlinksInToolInput(ctx context.Context, toolName string, input json.RawMessage) (
	inside []SymlinkTraversal,
	outside []SymlinkTraversal,
	suspicious bool,
) {
	workspace := WorkspacePathFrom(ctx)

	if toolName == "bash_exec" {
		paths, unexpandable := extractBashPathsFromInput(input, workspace)
		inside, outside = checkPathsForSymlinks(paths, workspace)
		return inside, outside, unexpandable
	}

	paths := extractAllPathsFromJSON(input, workspace)
	inside, outside = checkPathsForSymlinks(paths, workspace)
	return inside, outside, false
}

// extractAllPathsFromJSON extracts all path-like strings from a JSON tool input.
// Each string value containing "/" is treated as a candidate path.
// Relative paths are resolved against workspace.
func extractAllPathsFromJSON(input json.RawMessage, workspace string) []string {
	var parsed any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return nil
	}

	strValues := ExtractJSONStrings(parsed)
	seen := make(map[string]struct{})
	var paths []string

	for _, s := range strValues {
		if !looksLikePath(s) {
			continue
		}
		resolved := resolvePathCandidate(s, workspace)
		if resolved == "" {
			continue
		}
		cleaned := filepath.Clean(resolved)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		paths = append(paths, cleaned)
	}

	return paths
}

// looksLikePath returns true if a string resembles a filesystem path
// (contains a path separator and is not a URL). Recognizes both POSIX-style
// ("/") and Windows-style paths (drive letters like "C:\" or "D:/", and
// UNC paths starting with "\\\\").
func looksLikePath(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	hasSeparator := strings.Contains(s, "/") ||
		strings.ContainsRune(s, filepath.Separator) ||
		looksLikeWindowsDriveLetter(s) ||
		strings.HasPrefix(s, `\\`)
	if !hasSeparator {
		return false
	}
	lower := strings.ToLower(s)
	for _, scheme := range []string{"http://", "https://", "ftp://", "ssh://", "file://", "git://"} {
		if strings.HasPrefix(lower, scheme) {
			return false
		}
	}
	return true
}

// looksLikeWindowsDriveLetter reports whether s starts with a drive-letter
// prefix like "C:\" or "D:/".
func looksLikeWindowsDriveLetter(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	isLetter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	if !isLetter {
		return false
	}
	if s[1] != ':' {
		return false
	}
	return s[2] == '\\' || s[2] == '/'
}

// resolvePathCandidate resolves a potential path to an absolute form.
// Absolute paths are returned cleaned. Relative paths are joined with workspace.
func resolvePathCandidate(s, workspace string) string {
	if s == "" {
		return ""
	}
	if filepath.IsAbs(s) {
		return s
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, s)
}

// extractBashPathsFromInput extracts paths from a bash_exec tool input.
// Returns resolved paths and a flag indicating whether the command contains
// unexpandable shell constructs ($var, $(cmd), `cmd`, process substitution).
func extractBashPathsFromInput(input json.RawMessage, workspace string) (paths []string, hasUnexpandable bool) {
	var params struct {
		Command          string `json:"command"`
		WorkingDirectory string `json:"working_directory"`
	}
	if err := json.Unmarshal(input, &params); err != nil || params.Command == "" {
		return nil, false
	}

	wd := params.WorkingDirectory
	if wd == "" {
		wd = workspace
	}

	return extractBashPaths(params.Command, wd, workspace)
}

// extractBashPaths parses a bash command using mvdan.cc/sh and extracts
// path-like literals. Working directory is used for relative path resolution.
func extractBashPaths(command, workingDirectory, workspace string) (paths []string, hasUnexpandable bool) {
	parser := syntax.NewParser()
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return nil, true // unparseable — suspicious
	}

	seen := make(map[string]struct{})
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CmdSubst:
			hasUnexpandable = true
			return false // don't recurse into $(...) or `...`
		case *syntax.ParamExp:
			hasUnexpandable = true
			return false
		case *syntax.ProcSubst:
			hasUnexpandable = true
			return false
		case *syntax.Word:
			// Check for unexpandable shell constructs within the word
			// (e.g., $HOME/file parses as one Word with Parts [ParamExp, Lit]).
			for _, part := range n.Parts {
				switch part.(type) {
				case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ProcSubst:
					hasUnexpandable = true
				}
			}
			lit := wordLiteral(n)
			if lit == "" || !looksLikePath(lit) {
				return false
			}
			resolved := resolvePathCandidate(lit, workingDirectory)
			if resolved == "" {
				resolved = resolvePathCandidate(lit, workspace)
			}
			if resolved == "" {
				return false
			}
			cleaned := filepath.Clean(resolved)
			if _, ok := seen[cleaned]; !ok {
				seen[cleaned] = struct{}{}
				paths = append(paths, cleaned)
			}
			return false // already collected — don't recurse into Parts
		}
		return true
	})

	return paths, hasUnexpandable
}

// wordLiteral extracts the literal (unquoted) value from a shell Word node.
// Handles Lit, SglQuoted, and DblQuoted parts — but not variable expansions,
// command substitutions, or process substitutions.
// Escaped characters (e.g., "\ ") are preserved as-is in Lit values
// by the parser, so they appear as part of the extracted path.
func wordLiteral(w *syntax.Word) string {
	var parts []string
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			parts = append(parts, p.Value)
		case *syntax.SglQuoted:
			parts = append(parts, p.Value)
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				if lit, ok := dp.(*syntax.Lit); ok {
					parts = append(parts, lit.Value)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

// checkPathsForSymlinks walks each path component-by-component looking for
// symlinks. Returns traversals partitioned by workspace containment.
// OS-level symlinks (those above or at the workspace root, e.g., macOS
// /var → /private/var) are skipped — they are mapping infrastructure,
// not security-relevant traversals.
func checkPathsForSymlinks(paths []string, workspace string) (inside, outside []SymlinkTraversal) {
	for _, p := range paths {
		t := walkSymlinkComponents(p, workspace)
		if t == nil {
			continue
		}
		ok, _ := pathutil.IsWithinPath(workspace, t.FullResolved)
		t.OutsideWorkspace = !ok
		if t.OutsideWorkspace {
			outside = append(outside, *t)
		} else {
			inside = append(inside, *t)
		}
	}
	return inside, outside
}

// walkSymlinkComponents walks a path from root to leaf, checking each component
// for symlinks. Returns nil if no symlinks are found anywhere in the chain.
// If the final component doesn't exist but a parent component is a symlink,
// the symlink is still detected.
func walkSymlinkComponents(absPath, workspace string) *SymlinkTraversal {
	if absPath == "" {
		return nil
	}

	cleaned := filepath.Clean(absPath)
	parts := pathutil.SplitPathComponents(cleaned)
	if len(parts) == 0 {
		return nil
	}

	current := string(filepath.Separator) // start from /
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)

		fi, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // path doesn't exist — no further components to check
			}
			return nil // permission or other error — can't determine
		}

		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}

		// Skip OS-level symlinks: if the symlink path is a prefix of the workspace
		// root, it's filesystem mapping infrastructure (e.g., macOS /var → /private/var,
		// /tmp → /private/tmp), not a user-created security-relevant traversal.
		if workspace != "" {
			symlinkNorm := filepath.Clean(current) + string(filepath.Separator)
			wsNorm := filepath.Clean(workspace) + string(filepath.Separator)
			if strings.HasPrefix(wsNorm, symlinkNorm) {
				continue
			}
		}

		// Symlink found at current component
		target, err := os.Readlink(current)
		if err != nil {
			return nil
		}

		// Resolve the target relative to the symlink's directory
		resolvedTarget := target
		if !filepath.IsAbs(target) {
			resolvedTarget = filepath.Join(filepath.Dir(current), target)
		}

		// Build the full resolved path: resolvedTarget + remaining components
		tail := filepath.Join(parts[i+1:]...)
		fullResolved := filepath.Join(resolvedTarget, tail)

		// Resolve any remaining symlinks in the chain component-by-component.
		// Without this, chained symlinks (e.g., /ws/link1 → /real/link2 → /outside)
		// would only report the first symlink and miss subsequent ones that could
		// escape the workspace.
		if evaled, err := filepath.EvalSymlinks(fullResolved); err == nil {
			fullResolved = evaled
		}

		// Get absolute form
		fullAbs, err := filepath.Abs(fullResolved)
		if err != nil {
			fullAbs = filepath.Clean(fullResolved)
		}

		return &SymlinkTraversal{
			OriginalPath: absPath,
			SymlinkAt:    current,
			ResolvesTo:   target,
			FullResolved: fullAbs,
		}
	}

	return nil
}

// FormatSymlinkReasoning formats symlink traversals into a human-readable
// message for the confirmation dialog. Outside-workspace traversals are
// highlighted as more dangerous.
func FormatSymlinkReasoning(inside, outside []SymlinkTraversal, suspicious bool) string {
	var sb strings.Builder

	if len(outside) > 0 {
		sb.WriteString("This tool call traverses symlinks that resolve OUTSIDE the workspace:\n\n")
		for i, t := range outside {
			if i >= 10 {
				fmt.Fprintf(&sb, "  ... and %d more symlink(s)\n", len(outside)-10)
				break
			}
			fmt.Fprintf(&sb, "  %s\n    └─ symlink at: %s → %s (outside workspace)\n\n",
				t.OriginalPath, t.SymlinkAt, t.FullResolved)
		}
	}

	if len(inside) > 0 {
		sb.WriteString("This tool call traverses symlinks (target is within workspace):\n\n")
		for i, t := range inside {
			if i >= 10 {
				fmt.Fprintf(&sb, "  ... and %d more symlink(s)\n", len(inside)-10)
				break
			}
			fmt.Fprintf(&sb, "  %s\n    └─ symlink at: %s → %s (inside workspace)\n\n",
				t.OriginalPath, t.SymlinkAt, t.FullResolved)
		}
	}

	if len(outside) > 0 {
		sb.WriteString("The agent will follow the symlink and operate on the actual target outside the workspace.\n")
	} else if len(inside) > 0 {
		sb.WriteString("The agent will follow the symlink and operate on the resolved target within the workspace.\n")
	}

	if suspicious {
		sb.WriteString("\n⚠ Best-effort check: the command contains unresolved shell expansions ($var, $(cmd), `cmd`) that may hide additional paths.\n")
	}

	return strings.TrimSpace(sb.String())
}
