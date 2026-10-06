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

// ManagedWorktreesSegment is the repository-relative directory that holds the
// app-managed session worktrees (<repo>/.worktrees/<name>, ADR-080). It
// duplicates core.WorktreesRelativePath — core/tools cannot import the root
// core package (core imports core/tools; an import back would cycle) — and is
// pinned against it by TestManagedWorktreesSegmentMatchesCore in the core
// package, mirroring the GitSafeHooksRelativePath duplicate in
// internal/sysproc.
const ManagedWorktreesSegment = ".worktrees"

// ReasonCodeManagedWorktreeTraversal classifies the hard escalation raised by
// worktreeTraversalHardReason: a tool target that resolves inside a managed
// worktree container reachable from the session's own roots, granting a local
// session implicit access to another session's isolated tree. Canonical
// (isCanonicalHardReason): an `allow` policy can never execute it silently,
// and the verify-on-edit unattended path blocks it outright.
const ReasonCodeManagedWorktreeTraversal sdktools.JudgeReasonCode = "managed_worktree_traversal"

// worktreePathKeys are the top-level input keys whose string values carry
// filesystem targets for the built-in file tools (and file-shaped MCP tools).
// Keying on argument names — not tool names — keeps the gate correct for
// every current and future file tool without a registry of names; values that
// are not path-shaped simply never resolve inside a container.
var worktreePathKeys = map[string]bool{
	"path":        true,
	"file_path":   true,
	"source":      true,
	"destination": true,
	"target":      true,
}

// worktreeTraversalHardReason returns a HARD confirmation reason when a tool
// call's path arguments resolve inside a managed worktree container
// (<root>/.worktrees) that one of the session's own roots can reach — i.e.
// the call would read or write ANOTHER session's isolated tree through the
// containing checkout. Returns "" when there is nothing to escalate.
//
// Why this is needed: the managed container lives INSIDE the repository
// checkout (backend/config.ManagedWorktreesDir), so plain root containment
// cannot distinguish "the checkout" from "sibling session trees inside the
// checkout" — a local session (workspace = the checkout) would otherwise
// reach every managed tree of the same repo without any escalation, and even
// a managed session could reach its own tree's NESTED container
// (<own-tree>/.worktrees). The gate restores isolation: such targets always
// escalate (hard reason through the unified confirmation funnel; the
// verify-on-edit unattended path blocks them outright), exactly like the
// .git write gate keeps git internals out of silent auto-approval.
//
// Exemptions:
//   - the session's OWN managed tree is normal working space: when the session
//     workspace is itself a child of the container, targets inside the
//     workspace are allowed (only sibling trees escalate);
//   - a managed session reaching a sibling tree or the containing checkout's
//     container is out-of-root and already escalates through soft containment
//     (the checkout is not one of its roots) — this gate only covers what
//     containment CANNOT see: containers nested inside the session's own
//     roots.
//
// Shell tools are untouched, mirroring the .git gate: agent-typed shell
// commands targeting the container flow through the ordinary shell gates
// (flowsh criteria + judges). The deny policy is enforced by Execute before
// this runs, so traversal never bypasses an explicit deny.
//
// The returned code classifies the reason (see ReasonCodeManagedWorktreeTraversal).
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
			// Own-tree exemption: the session workspace is a child of THIS
			// container (a managed session of the repo at root) and the
			// target is inside the workspace — normal work in its own tree.
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

// extractWorktreePathArgs returns the string values of the known path-carrying
// top-level keys in a tool-call input. Non-string values, unknown shapes, and
// unparseable input yield no candidates (fail-open for EXTRACTION only — the
// gate itself is fail-closed once a candidate resolves inside a container;
// the shared structural validator in Execute already rejects malformed input
// for schema-carrying tools).
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

// resolveWorktreeTarget anchors a raw tool argument to an absolute path and
// resolves symlinks best-effort, so a symlink planted inside the workspace
// cannot launder a container target past the gate. A resolution failure
// (not-yet-existing target for a write, unreadable parent) keeps the lexical
// path — the containment check below still runs on it, so a failure never
// silently exempts a container target.
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
