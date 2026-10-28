package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ManagedWorktreesSegment duplicates core.WorktreesRelativePath (core/tools
// cannot import root core — it would cycle) and is pinned against it by
// TestManagedWorktreesSegmentMatchesCore. ADR-080.
const ManagedWorktreesSegment = ".worktrees"

// ReasonCodeManagedWorktreeTraversal classifies the hard escalation raised by
// worktreeTraversalHardReason — canonical, so `allow` never executes it
// silently and the unattended path blocks it outright.
const ReasonCodeManagedWorktreeTraversal sdktools.JudgeReasonCode = "managed_worktree_traversal"

// worktreePathKeys are the input keys carrying filesystem targets; keying on
// argument names (not tool names) covers future file tools with no registry.
var worktreePathKeys = map[string]bool{
	"path":        true,
	"file_path":   true,
	"source":      true,
	"destination": true,
	"target":      true,
}

// worktreeTraversalHardReason escalates (hard reason) when a tool-call path
// resolves inside a managed worktree container reachable from the session's
// own roots: the container lives INSIDE the checkout, so root containment
// cannot separate "the checkout" from sibling session trees, and without this
// gate a local session would read/write another session's isolated tree
// silently. Exemptions: the session's own managed tree, and targets already
// out-of-root (soft containment covers those). Shell tools are untouched —
// they flow through the ordinary shell gates.
// See specs/domains/session-worktrees.md (Invariants).
func worktreeTraversalHardReason(ctx context.Context, input json.RawMessage) (string, sdktools.JudgeReasonCode) {
	workspace := sdktools.WorkspacePathFrom(ctx)
	roots := sdktools.SessionRoots(ctx)
	if workspace == "" || len(roots) == 0 {
		return "", ""
	}

	targets := extractWorktreePathArgs(input)
	if len(targets) == 0 {
		return "", ""
	}

	var offending []string
	seen := make(map[string]struct{})
	for _, raw := range targets {
		resolved, ok := resolveWorktreeTarget(workspace, raw)
		if !ok {
			continue
		}
		for _, root := range roots {
			container := filepath.Join(root, ManagedWorktreesSegment)
			within, err := pathutil.IsWithinPath(container, resolved)
			if err != nil || !within {
				continue
			}
			// Own-tree exemption: the workspace is a child of this container,
			// so targets inside it are normal work.
			wsInside, wsErr := pathutil.IsWithinPath(container, workspace)
			if wsErr == nil && wsInside {
				if own, ownErr := pathutil.IsWithinPath(workspace, resolved); ownErr == nil && own {
					continue
				}
			}
			if _, dup := seen[resolved]; !dup {
				seen[resolved] = struct{}{}
				offending = append(offending, resolved)
			}
		}
	}
	if len(offending) == 0 {
		return "", ""
	}

	return fmt.Sprintf(
		"target resolves inside the app-managed session-worktree container (%s): %s. "+
			"Managed worktrees belong to other sessions' isolated execution workspaces — "+
			"confirm explicitly to cross that isolation boundary.",
		ManagedWorktreesSegment, strings.Join(offending, ", ")), ReasonCodeManagedWorktreeTraversal
}

// extractWorktreePathArgs returns string values of the known path keys.
// Extraction is fail-open; the gate is fail-closed once a candidate resolves
// inside a container, and Execute's structural validator rejects bad input.
func extractWorktreePathArgs(input json.RawMessage) []string {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(input, &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(worktreePathKeys))
	for key, isPathKey := range worktreePathKeys {
		if !isPathKey {
			continue
		}
		raw, present := parsed[key]
		if !present {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// resolveWorktreeTarget anchors an argument to an absolute path, resolving
// symlinks best-effort so a planted link cannot launder a container target
// past the gate; a failed resolution keeps the lexical path (never exempted).
func resolveWorktreeTarget(workspace, raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	var abs string
	if filepath.IsAbs(trimmed) {
		abs = filepath.Clean(trimmed)
	} else {
		// Relative tool paths resolve against the session workspace only
		// (auxiliary roots are absolute-path-only by contract).
		abs = filepath.Clean(filepath.Join(workspace, trimmed))
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = filepath.Clean(resolved)
	}
	return abs, true
}
