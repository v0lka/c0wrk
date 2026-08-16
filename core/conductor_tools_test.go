package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agents"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// subagentToolSet builds a representative registry descriptor list spanning
// every capability group, Conductor-only system tools, MCP tools, and an
// untagged tool (zero group — must match no group-based grant, fail-closed).
func subagentToolSet() []sdktools.ToolDescriptor {
	return []sdktools.ToolDescriptor{
		// local_read
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "list_directory", Group: sdktools.GroupLocalRead},
		{Name: "glob", Group: sdktools.GroupLocalRead},
		{Name: "ripgrep", Group: sdktools.GroupLocalRead},
		// remote_read
		{Name: "web_search", Group: sdktools.GroupRemoteRead},
		{Name: "web_fetch", Group: sdktools.GroupRemoteRead},
		// system
		{Name: "finish", Group: sdktools.GroupSystem},
		{Name: "store_fact", Group: sdktools.GroupSystem},
		{Name: "search_facts", Group: sdktools.GroupSystem},
		{Name: "read_step_output", Group: sdktools.GroupSystem},
		{Name: "tool_result_read", Group: sdktools.GroupSystem},
		{Name: "semantic_search", Group: sdktools.GroupSystem},
		{Name: "read_skill_resource", Group: sdktools.GroupSystem},
		{Name: "ask_user", Group: sdktools.GroupSystem},
		{Name: "declare_step_complete", Group: sdktools.GroupSystem},
		// system, Conductor-only (stripped for every regular subagent)
		{Name: "delegate", Group: sdktools.GroupSystem},
		{Name: "cancel_delegation", Group: sdktools.GroupSystem},
		{Name: "declare_plan", Group: sdktools.GroupSystem},
		{Name: "execute_plan", Group: sdktools.GroupSystem},
		{Name: "reflect", Group: sdktools.GroupSystem},
		// system, goal-loop-only (stripped from EVERY subagent toolset: they
		// belong to the goal-loop Conductor and the independent verifier)
		{Name: "propose_goal", Group: sdktools.GroupSystem},
		{Name: "declare_goal_status", Group: sdktools.GroupSystem},
		{Name: "declare_verification", Group: sdktools.GroupSystem},
		// execute
		{Name: "bash_exec", Group: sdktools.GroupExecute},
		// local_write
		{Name: "edit_file", Group: sdktools.GroupLocalWrite},
		{Name: "write_file", Group: sdktools.GroupLocalWrite},
		{Name: "create_directory", Group: sdktools.GroupLocalWrite},
		// MCP
		{Name: "search_graph", Group: sdktools.GroupLocalMCP, SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "get_code_snippet", Group: sdktools.GroupLocalMCP, SourceCategory: sdktools.SourceCategoryMCP},
		{Name: "mcp_write", Group: sdktools.GroupRemoteMCP, SourceCategory: sdktools.SourceCategoryMCP},
		// A tool that forgot to declare its group — matches no grant.
		{Name: "untagged_tool"},
	}
}

// systemOnlyNames are the system-group tools in subagentToolSet that a
// regular (non-redelegating) subagent keeps: everything except the
// conductor-only set (which the redelegating path re-adds explicitly).
var systemOnlyNames = []string{
	"finish", "store_fact", "search_facts", "read_step_output",
	"tool_result_read", "semantic_search", "read_skill_resource",
	"ask_user", "declare_step_complete",
}

func descriptorNames(descs []sdktools.ToolDescriptor) map[string]struct{} {
	out := make(map[string]struct{}, len(descs))
	for _, d := range descs {
		out[d.Name] = struct{}{}
	}
	return out
}

// assertPresent / assertAbsent are the membership-check helpers used by the
// resolveTaskTools tests.
func assertPresent(t *testing.T, names map[string]struct{}, want []string) {
	t.Helper()
	for _, n := range want {
		if _, ok := names[n]; !ok {
			t.Errorf("expected tool %q in toolset, got %v", n, names)
		}
	}
}

func assertAbsent(t *testing.T, names map[string]struct{}, unwanted []string) {
	t.Helper()
	for _, n := range unwanted {
		if _, ok := names[n]; ok {
			t.Errorf("tool %q must NOT be in toolset, got %v", n, names)
		}
	}
}

// TestResolveTaskTools_DefaultGrantsAll verifies nil (the default) grants the
// full toolset minus Conductor-only tools.
func TestResolveTaskTools_DefaultGrantsAll(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: nil})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)
	assertPresent(t, names, []string{"read_file", "web_search", "finish", "bash_exec", "edit_file", "search_graph", "mcp_write", "untagged_tool"})
	assertAbsent(t, names, []string{"delegate", "cancel_delegation", "declare_plan", "execute_plan", "reflect"})
}

// TestResolveTaskTools_AllStringMatchesDefault verifies the explicit "all"
// (and "") strings behave identically to nil.
func TestResolveTaskTools_AllStringMatchesDefault(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	def, err := l.resolveTaskTools(tools.DelegationTask{Tools: nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, v := range []string{"all", ""} {
		got, err := l.resolveTaskTools(tools.DelegationTask{Tools: v})
		if err != nil {
			t.Fatalf("tools=%q: unexpected error: %v", v, err)
		}
		if len(got) != len(def) {
			t.Errorf("tools=%q: got %d tools, want %d (same as nil)", v, len(got), len(def))
		}
	}
}

// TestResolveTaskTools_EmptyArrayRejected verifies an empty tools array fails
// the delegation instead of resolving to a system-only toolset. A degenerate
// grant that strips every working tool is a caller error; the deliberate
// system-only grant is expressed as ["system"].
func TestResolveTaskTools_EmptyArrayRejected(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	for _, v := range []any{[]any{}, []string{}} {
		_, err := l.resolveTaskTools(tools.DelegationTask{Tools: v})
		if err == nil {
			t.Errorf("resolveTaskTools(%#v) = nil error, want empty-array rejection", v)
			continue
		}
		if !strings.Contains(err.Error(), "empty array") {
			t.Errorf("resolveTaskTools(%#v) = %q, want message containing %q", v, err.Error(), "empty array")
		}
	}
}

// TestResolveTaskTools_ExplicitGroupsExactGrant is acceptance criterion 1: a
// grant of ["local-read","execute"] yields EXACTLY system ∪ local_read ∪
// execute — no hidden additions (no MCP, no remote_read, no local_write, no
// untagged tools) and the conductor-only system tools stay stripped.
func TestResolveTaskTools_ExplicitGroupsExactGrant(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: []any{"local-read", "execute"}})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)

	want := append([]string{"read_file", "list_directory", "glob", "ripgrep", "bash_exec"}, systemOnlyNames...)
	assertPresent(t, names, want)

	// No hidden additions: everything else in the registry must be absent.
	unwanted := []string{
		// remote_read not granted
		"web_search", "web_fetch",
		// local_write not granted
		"edit_file", "write_file", "create_directory",
		// MCP never implicit
		"search_graph", "get_code_snippet", "mcp_write",
		// untagged tools match no grant
		"untagged_tool",
		// conductor-only system tools stay stripped
		"delegate", "cancel_delegation", "declare_plan", "execute_plan", "reflect",
	}
	assertAbsent(t, names, unwanted)

	if len(got) != len(want) {
		t.Errorf("exact grant violated: got %d tools (%v), want exactly %d", len(got), names, len(want))
	}
}

// TestResolveTaskTools_ProfileLocalReadExecute wires the sp4rk profile
// pipeline into the resolver: an AGENT.md-style profile declaring
// `tools: local-read, execute` must produce the exact system ∪ local_read ∪
// execute toolset (acceptance criterion 1, profile entry point).
func TestResolveTaskTools_ProfileLocalReadExecute(t *testing.T) {
	profile := &agents.Agent{Metadata: agents.AgentMetadata{Tools: "local-read, execute"}}
	pref, err := profile.ToolPreference()
	if err != nil {
		t.Fatalf("ToolPreference() error = %v, want nil", err)
	}
	tokens, ok := pref.([]string)
	if !ok {
		t.Fatalf("ToolPreference() = %#v, want []string of group tokens", pref)
	}
	asAny := make([]any, 0, len(tokens))
	for _, tok := range tokens {
		asAny = append(asAny, tok)
	}

	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: asAny})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)
	want := append([]string{"read_file", "list_directory", "glob", "ripgrep", "bash_exec"}, systemOnlyNames...)
	assertPresent(t, names, want)
	assertAbsent(t, names, []string{"web_search", "edit_file", "search_graph", "mcp_write", "untagged_tool", "delegate", "declare_plan"})
	if len(got) != len(want) {
		t.Errorf("profile grant must be exact: got %d tools, want %d", len(got), len(want))
	}
}

// TestResolveTaskTools_ReadOnlyExcludesMCP is acceptance criterion 2 (and the
// regression test for it): "read-only" = system ∪ local_read ∪ remote_read
// and NOTHING else — MCP tools must not sneak in via SourceCategory.
func TestResolveTaskTools_ReadOnlyExcludesMCP(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: "read-only"})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)

	want := append([]string{"read_file", "list_directory", "glob", "ripgrep", "web_search", "web_fetch"}, systemOnlyNames...)
	assertPresent(t, names, want)
	assertAbsent(t, names, []string{
		// MCP must NOT be part of the read-only preset.
		"search_graph", "get_code_snippet", "mcp_write",
		// Mutating groups excluded.
		"bash_exec", "edit_file", "write_file", "create_directory",
		// Untagged excluded.
		"untagged_tool",
		// Conductor-only excluded.
		"delegate", "declare_plan", "execute_plan", "reflect", "cancel_delegation",
	})
	if len(got) != len(want) {
		t.Errorf("read-only preset must be exact: got %d tools, want %d", len(got), len(want))
	}
}

// TestResolveTaskTools_UnderscoreTokensAccepted verifies the underscore
// spelling of group tokens (the sdktools ToolGroup values) is accepted and
// canonicalized, mirroring agents.NormalizeToolGroupToken.
func TestResolveTaskTools_UnderscoreTokensAccepted(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: []any{"local_read", "execute"}})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)
	assertPresent(t, names, []string{"read_file", "bash_exec", "finish"})
	assertAbsent(t, names, []string{"web_search", "edit_file", "search_graph"})
}

// TestResolveTaskTools_UnknownInputsFailClosed is acceptance criterion 4:
// unknown group tokens, unknown strings, and unrecognized types all fail with
// a comprehensible error instead of silently granting anything.
func TestResolveTaskTools_UnknownInputsFailClosed(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}

	tests := []struct {
		name       string
		tools      any
		wantMsg    string
		wantGroups bool
	}{
		{name: "unknown group in list", tools: []any{"local-read", "edit_file"}, wantMsg: `unknown tool group "edit_file"`, wantGroups: true},
		{name: "unknown bare string", tools: "edit_file", wantMsg: `unknown tools value "edit_file"`, wantGroups: true},
		{name: "non-string item", tools: []any{"local-read", 42}, wantMsg: "group names must be strings"},
		{name: "unexpected type", tools: 42, wantMsg: "unexpected tools type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := l.resolveTaskTools(tools.DelegationTask{Tools: tt.tools})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantMsg)
			}
			// Unknown-group messages must teach the valid groups
			// (comprehensible, not bare rejections).
			if tt.wantGroups && !strings.Contains(err.Error(), "local-mcp") {
				t.Errorf("error %q should list valid groups", err.Error())
			}
		})
	}
}

// TestResolveTaskTools_ChatModeDisabledDropped verifies mode-disabled tools
// are dropped even when their group is granted (e.g. glob/ripgrep/
// semantic_search in CHAT / No-Project mode).
func TestResolveTaskTools_ChatModeDisabledDropped(t *testing.T) {
	l := &conductorLauncher{deps: conductorDeps{
		toolRegistry:  newSubagentTestRegistry(subagentToolSet()),
		disabledTools: map[string]bool{ToolGlob: true, ToolRipgrep: true, ToolSemanticSearch: true},
	}}
	got, err := l.resolveTaskTools(tools.DelegationTask{Tools: []any{"local-read"}})
	if err != nil {
		t.Fatalf("resolveTaskTools: unexpected error: %v", err)
	}
	names := descriptorNames(got)
	assertAbsent(t, names, []string{ToolGlob, ToolRipgrep, ToolSemanticSearch})
	assertPresent(t, names, []string{"read_file", "list_directory", "finish"})
}

// TestToolsByGroups_DeduplicatesByName verifies duplicate descriptor names
// collapse to one entry (duplicates make LLM providers reject the request
// with HTTP 400 "Tool names must be unique.").
func TestToolsByGroups_DeduplicatesByName(t *testing.T) {
	l := &conductorLauncher{}
	all := []sdktools.ToolDescriptor{
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "read_file", Group: sdktools.GroupLocalRead},
		{Name: "finish", Group: sdktools.GroupSystem},
		{Name: "edit_file", Group: sdktools.GroupLocalWrite},
	}
	got := l.toolsByGroups(all, map[sdktools.ToolGroup]struct{}{
		sdktools.GroupLocalRead: {},
		sdktools.GroupSystem:    {},
	})
	if len(got) != 2 {
		t.Errorf("expected dedup to 2 tools, got %d", len(got))
	}
}

// mockSubagentTool is a minimal sdktools.Tool used only to populate a registry
// for conductor tool-resolution tests.
type mockSubagentTool struct {
	name  string
	group sdktools.ToolGroup
}

func (m *mockSubagentTool) Name() string                 { return m.name }
func (m *mockSubagentTool) Description() string          { return "mock" }
func (m *mockSubagentTool) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (m *mockSubagentTool) Execute(context.Context, json.RawMessage) (sdktools.ToolResult, error) {
	return sdktools.ToolResult{}, nil
}
func (m *mockSubagentTool) DefaultPolicy() sdktools.ToolPolicy { return sdktools.PolicyAlwaysAllow }
func (m *mockSubagentTool) IsUntrusted() bool                  { return false }
func (m *mockSubagentTool) Group() sdktools.ToolGroup          { return m.group }

// newSubagentTestRegistry builds a real *sdktools.ToolRegistry whose List()
// reflects the given descriptor set (name, group, and SourceCategory), so
// conductorLauncher.allToolDescriptors works without the full app wiring.
func newSubagentTestRegistry(descs []sdktools.ToolDescriptor) *sdktools.ToolRegistry {
	r := sdktools.NewToolRegistry()
	for _, d := range descs {
		source := "core"
		if d.SourceCategory == sdktools.SourceCategoryMCP {
			source = "mcp:test"
		}
		_ = r.RegisterWithSourceCategory(&mockSubagentTool{name: d.Name, group: d.Group}, source, d.SourceCategory)
	}
	return r
}

// TestResolveTaskTools_GoalToolsNeverGranted verifies the goal-loop actor
// tools (propose_goal, declare_goal_status, declare_verification) never reach
// a delegated subagent in ANY branch: they ride the always-granted system
// group, but they belong to the goal-loop Conductor and the independent
// verifier (whose toolset is built by verifierToolFilter from the raw
// registry list, not via resolveTaskTools). A subagent that could call
// declare_goal_status/declare_verification would forge the goal loop's
// state-machine inputs; outside goal mode the tools would be advertised yet
// always error ("no sink in context"). The explicit "system" grant cases pin
// that even asking for the system group by name does not widen the strip.
func TestResolveTaskTools_GoalToolsNeverGranted(t *testing.T) {
	goalTools := []string{"propose_goal", "declare_goal_status", "declare_verification"}
	for _, tc := range []struct {
		name  string
		tools any
	}{
		{name: "nil default", tools: nil},
		{name: "all string", tools: "all"},
		{name: "empty string", tools: ""},
		{name: "read-only preset", tools: "read-only"},
		{name: "kebab system grant", tools: []any{"system"}},
		{name: "full grant", tools: []any{"execute", "local-read", "local-write", "remote-read", "remote-write", "local-mcp", "remote-mcp"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &conductorLauncher{deps: conductorDeps{toolRegistry: newSubagentTestRegistry(subagentToolSet())}}
			got, err := l.resolveTaskTools(tools.DelegationTask{Tools: tc.tools})
			if err != nil {
				t.Fatalf("resolveTaskTools: unexpected error: %v", err)
			}
			assertAbsent(t, descriptorNames(got), goalTools)
		})
	}
}

// TestSubagentCtxClearsGoalSinks verifies the defense-in-depth layer behind
// the goal-tool strip: subagentCtx clears the goal-status and verification
// sinks so that even a hand-written or future mis-tagged system tool cannot
// write the goal loop's verdict channels from inside a delegation. The
// verifier is unaffected — it runs via a direct RunConductor call with its
// own fresh sink (defaultGoalVerifier), never through subagentCtx.
func TestSubagentCtxClearsGoalSinks(t *testing.T) {
	ctx := tools.WithGoalStatusSink(context.Background(), &memGoalStatusSink{})
	ctx = tools.WithVerificationSink(ctx, &memVerificationSink{})

	ctx = subagentCtx(ctx)

	if tools.GoalStatusSinkFrom(ctx) != nil {
		t.Error("goal status sink must be cleared in subagent contexts")
	}
	if tools.VerificationSinkFrom(ctx) != nil {
		t.Error("verification sink must be cleared in subagent contexts")
	}
}
