package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// warnCapture records WARN+ records for substring assertions — the traversal
// reason embeds absolute temp paths, so exact-attr matching (the shared
// captureToolDiagnostics helper) cannot pin it; message and static attrs are
// still asserted exactly, and the reason is asserted by the gate's stable
// marker text.
type warnCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *warnCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *warnCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *warnCapture) WithAttrs([]slog.Attr) slog.Handler { panic("unexpected WithAttrs") }
func (h *warnCapture) WithGroup(string) slog.Handler      { panic("unexpected WithGroup") }

func (h *warnCapture) find(message, reasonMarker string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level < slog.LevelWarn || r.Message != message {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "reason" && strings.Contains(a.Value.String(), reasonMarker) {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func mustContainFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorktreeTraversalHardReason_LocalSessionSiblingTree is the core
// isolation regression: a LOCAL session (workspace = the repository checkout)
// must not silently reach another session's managed tree that lives inside
// the same checkout's .worktrees container — plain root containment cannot
// see the boundary, the gate restores it.
func TestWorktreeTraversalHardReason_LocalSessionSiblingTree(t *testing.T) {
	ws := t.TempDir()
	sibling := filepath.Join(ws, ManagedWorktreesSegment, "s-aaa")
	mustContainFile(t, filepath.Join(sibling, "src", "main.go"), "package main\n")

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(sibling, "src", "main.go")})
	if err != nil {
		t.Fatal(err)
	}

	reason, code := worktreeTraversalHardReason(ctx, input)
	if reason == "" {
		t.Fatal("worktreeTraversalHardReason() = empty, want a hard escalation for a sibling managed tree")
	}
	if code != ReasonCodeManagedWorktreeTraversal {
		t.Errorf("code = %q, want %q", code, ReasonCodeManagedWorktreeTraversal)
	}
	if !isCanonicalHardReason(code) {
		t.Error("isCanonicalHardReason(managed_worktree_traversal) = false, want true")
	}
}

// TestWorktreeTraversalHardReason_ManagedOwnTreeExempt: a managed session's
// OWN tree is its normal working space — targets inside it (including new
// files, whose EvalSymlinks fails and keeps the lexical path) never escalate.
func TestWorktreeTraversalHardReason_ManagedOwnTreeExempt(t *testing.T) {
	repo := t.TempDir()
	own := filepath.Join(repo, ManagedWorktreesSegment, "s-me")
	mustContainFile(t, filepath.Join(own, "existing.txt"), "x\n")

	ctx := sdktools.WithWorkspacePath(context.Background(), own)
	for _, target := range []string{
		filepath.Join(own, "existing.txt"),
		filepath.Join(own, "brand-new.txt"),
		"relative/inside.go",
	} {
		input, err := json.Marshal(map[string]string{"path": target})
		if err != nil {
			t.Fatal(err)
		}
		if reason, _ := worktreeTraversalHardReason(ctx, input); reason != "" {
			t.Errorf("target %q: worktreeTraversalHardReason() = %q, want empty (own tree is normal workspace)", target, reason)
		}
	}
}

// TestWorktreeTraversalHardReason_NestedContainer: the container NESTED in
// the session's own tree (<own>/.worktrees) holds trees of a DIFFERENT
// binding level (a project opened inside the tree); reaching it from the
// session must escalate.
func TestWorktreeTraversalHardReason_NestedContainer(t *testing.T) {
	repo := t.TempDir()
	own := filepath.Join(repo, ManagedWorktreesSegment, "s-me")
	nested := filepath.Join(own, ManagedWorktreesSegment, "s-inner")
	mustContainFile(t, filepath.Join(nested, "f.txt"), "x\n")

	ctx := sdktools.WithWorkspacePath(context.Background(), own)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(nested, "f.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if reason, code := worktreeTraversalHardReason(ctx, input); reason == "" || code != ReasonCodeManagedWorktreeTraversal {
		t.Fatalf("nested container: got (%q, %q), want a %q escalation", reason, code, ReasonCodeManagedWorktreeTraversal)
	}
}

// TestWorktreeTraversalHardReason_SymlinkLaundering: a symlink inside the
// workspace pointing into the container must not bypass the gate — targets
// resolve through EvalSymlinks before containment.
func TestWorktreeTraversalHardReason_SymlinkLaundering(t *testing.T) {
	ws := t.TempDir()
	sibling := filepath.Join(ws, ManagedWorktreesSegment, "s-bbb")
	mustContainFile(t, filepath.Join(sibling, "secret.txt"), "x\n")
	if err := os.Symlink(sibling, filepath.Join(ws, "shortcut")); err != nil {
		t.Fatal(err)
	}

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(ws, "shortcut", "secret.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if reason, _ := worktreeTraversalHardReason(ctx, input); reason == "" {
		t.Fatal("symlink into the container: worktreeTraversalHardReason() = empty, want an escalation")
	}
}

// TestWorktreeTraversalHardReason_NoWorkspaceNoGate: without a workspace in
// ctx (defensive — every real execution path injects one) the gate is inert.
func TestWorktreeTraversalHardReason_NoWorkspaceNoGate(t *testing.T) {
	ws := t.TempDir()
	sibling := filepath.Join(ws, ManagedWorktreesSegment, "s-ccc")
	mustContainFile(t, filepath.Join(sibling, "f.txt"), "x\n")

	input, err := json.Marshal(map[string]string{"path": filepath.Join(sibling, "f.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if reason, _ := worktreeTraversalHardReason(context.Background(), input); reason != "" {
		t.Fatalf("no-workspace ctx: got reason %q, want empty", reason)
	}
}

// TestWorktreeTraversalGate_ExecuteForcesConfirmation drives the FULL funnel:
// an allow-policy local_read tool (read_file defaults to allow) targeting a
// sibling managed tree must land on the confirmation card — the canonical
// hard reason cannot be auto-executed — and a denial surfaces an error
// result. The expected Warn diagnostic is intercepted at the source and
// asserted.
func TestWorktreeTraversalGate_ExecuteForcesConfirmation(t *testing.T) {
	ws := t.TempDir()
	sibling := filepath.Join(ws, ManagedWorktreesSegment, "s-ddd")
	mustContainFile(t, filepath.Join(sibling, "f.txt"), "x\n")

	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysAllow,
	})
	registry.Register(builtins.NewReadFileTool())

	h := &warnCapture{}
	registry.SetLogger(slog.New(h))

	confirmCalled := false
	registry.SetConfirmFunc(func(_ context.Context, req sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		if !strings.Contains(req.JudgeReasoning, ManagedWorktreesSegment) {
			t.Errorf("confirmation reason %q does not name the %s container", req.JudgeReasoning, ManagedWorktreesSegment)
		}
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(sibling, "f.txt")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := registry.Execute(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !confirmCalled {
		t.Fatal("an allow-policy read of a sibling managed tree must force a confirmation")
	}
	if !res.IsError {
		t.Error("denying the traversal confirmation must surface an error result")
	}
	if !h.find("security: allow-policy tool escalated by hard safety reason", ManagedWorktreesSegment) {
		t.Error("expected the allow-policy hard-escalation Warn naming the container")
	}
}

// TestWorktreeTraversalGate_UnattendedBlocks: the verify-on-edit unattended
// path blocks a container target outright (canonical hard reason — no
// confirmation flow exists there). The expected Warn is intercepted.
func TestWorktreeTraversalGate_UnattendedBlocks(t *testing.T) {
	ws := t.TempDir()
	sibling := filepath.Join(ws, ManagedWorktreesSegment, "s-eee")
	mustContainFile(t, filepath.Join(sibling, "f.txt"), "x\n")

	registry := NewToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	h := &warnCapture{}
	registry.SetLogger(slog.New(h))

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(sibling, "f.txt")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := registry.ExecuteUnattended(ctx, "read_file", input)
	if err != nil {
		t.Fatalf("ExecuteUnattended() error = %v", err)
	}
	if !res.IsError {
		t.Fatal("unattended traversal must be blocked with an error result")
	}
	if !strings.Contains(res.Content, "security policy") {
		t.Errorf("blocked result %q should name the security policy", res.Content)
	}
	if !h.find("security: unattended tool blocked by hard safety reason", ManagedWorktreesSegment) {
		t.Error("expected the unattended hard-block Warn naming the container")
	}
}

// TestWorktreeTraversalGate_WorkspaceWritesUnaffectedInCheckout: the ordinary
// local-session write INSIDE the checkout (not under .worktrees) keeps the
// auto-approval path — the gate must not make normal work confirm.
func TestWorktreeTraversalGate_WorkspaceWritesUnaffectedInCheckout(t *testing.T) {
	ws := t.TempDir()
	// A sibling tree exists, so the container is populated — the gate's
	// conditions are live, not vacuous.
	mustContainFile(t, filepath.Join(ws, ManagedWorktreesSegment, "s-fff", "f.txt"), "x\n")

	registry := NewToolRegistry()
	registry.SetGroupPolicies(map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupLocalWrite: sdktools.PolicyAlwaysAllow,
	})
	registry.SetAutoApproveWorkspaceWrites(true)
	registry.Register(builtins.NewWriteFileTool())
	h := &warnCapture{}
	registry.SetLogger(slog.New(h))

	confirmCalled := false
	registry.SetConfirmFunc(func(context.Context, sdktools.ConfirmationRequest) (sdktools.ConfirmationResponse, error) {
		confirmCalled = true
		return sdktools.ConfirmDeny, nil
	})

	ctx := sdktools.WithWorkspacePath(context.Background(), ws)
	input, err := json.Marshal(map[string]string{"path": filepath.Join(ws, "notes.md"), "content": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := registry.Execute(ctx, "write_file", input)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if confirmCalled {
		t.Error("a plain in-checkout write must stay auto-approved (no confirmation)")
	}
	if res.IsError {
		t.Errorf("auto-approved write failed: %q", res.Content)
	}
	if _, err := os.Stat(filepath.Join(ws, "notes.md")); err != nil {
		t.Errorf("write did not land: %v", err)
	}
}
