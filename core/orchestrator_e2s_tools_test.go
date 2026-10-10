package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ----------------------------------------------------------------------------
// Reference descriptor set
// ----------------------------------------------------------------------------

// e2sTestCoreDesc builds a descriptor for a built-in tool with a realistic
// (description-carrying) input schema, mirroring what the real registry
// produces.
func e2sTestCoreDesc(name string, group sdktools.ToolGroup) sdktools.ToolDescriptor {
	return sdktools.ToolDescriptor{
		Name: name,
		Description: "Tool " + name + ".\n\nUse it when you need to " + name +
			". Long-form guidance about when the tool applies, its anti-patterns, and its examples.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path the tool operates on"},"limit":{"type":"integer","description":"Upper bound of items processed"}},"required":["path"],"additionalProperties":false}`),
		Group:          group,
		SourceCategory: sdktools.SourceCategoryCore,
	}
}

// e2sTestMCPDesc builds an MCP-sourced descriptor (e.g. a GitHub server
// endpoint).
func e2sTestMCPDesc(name, server string, group sdktools.ToolGroup) sdktools.ToolDescriptor {
	return sdktools.ToolDescriptor{
		Name: name,
		Description: "MCP " + name + " from server " + server +
			". Operates on the remote system and returns its full result document.",
		InputSchema:    json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string","description":"Repository owner"},"repo":{"type":"string","description":"Repository name"},"payload":{"type":"object","description":"Endpoint-specific body"}},"required":["owner","repo"]}`),
		Source:         server,
		SourceCategory: sdktools.SourceCategoryMCP,
		Group:          group,
	}
}

// e2sToolsReferenceCatalog is the reference descriptor set (the canonical
// fixture for the catalog-budget tests): 85
// tools mirroring a real session's registry — the built-in local work core,
// the system-group plumbing and orchestration surface (including every
// plan/delegation/goal tool the E2S stripping removes), and a dominant MCP
// block (a GitHub server's PR/issue/commit endpoints plus remote MCP
// servers).
func e2sToolsReferenceCatalog() []sdktools.ToolDescriptor {
	descs := make([]sdktools.ToolDescriptor, 0, 85)

	// Capability-group builtins (13).
	for _, n := range []string{"read_file", "list_directory", "glob", "ripgrep"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupLocalRead))
	}
	for _, n := range []string{"write_file", "edit_file", "delete_file", "create_directory", "delete_directory"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupLocalWrite))
	}
	for _, n := range []string{"bash_exec", "posh_exec"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupExecute))
	}
	for _, n := range []string{"web_fetch", "web_search"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupRemoteRead))
	}

	// System plumbing the E2S protocol depends on (6).
	for _, n := range []string{"batch", "tool_result_read", "ask_user", "store_fact", "search_facts", "read_attachment"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupSystem))
	}

	// System-group orchestration surface OUTSIDE the core preset (2).
	for _, n := range []string{"semantic_search", "read_skill_resource"} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupSystem))
	}

	// Plan/delegation/goal tools — always stripped from the E2S catalog (13).
	for _, n := range []string{
		"declare_plan", "execute_plan", "declare_step_complete", "update_checklist",
		"reflect", "delegate", "cancel_delegation", "read_step_output",
		"list_step_outputs", "read_final_result", "propose_goal",
		"declare_goal_status", "declare_verification",
	} {
		descs = append(descs, e2sTestCoreDesc(n, sdktools.GroupSystem))
	}

	// MCP block (51): a GitHub server's PR/issue/commit endpoints (47,
	// stdio → local_mcp) plus remote MCP servers (4 → remote_mcp).
	for _, n := range []string{
		"create_pull_request", "list_pull_requests", "get_pull_request", "get_pull_request_files",
		"get_pull_request_status", "merge_pull_request", "update_pull_request_branch", "create_pull_request_review",
		"add_comment_to_pending_review", "list_commits", "get_commit", "search_commits",
		"create_branch", "list_branches", "create_or_update_file", "push_files",
		"get_file_contents", "create_repository", "fork_repository", "list_tags",
		"get_tag", "get_latest_release", "get_release_by_tag", "list_releases",
		"search_code", "search_repositories", "search_users", "get_me",
		"get_teams", "get_team_members", "list_issues", "issue_write",
		"issue_read", "add_issue_comment", "sub_issue_write", "list_issue_types",
		"search_pull_requests", "assign_copilot_to_issue", "request_copilot_review", "pull_request_read",
		"pull_request_review_write", "list_repository_collaborators", "get_label", "manage_adr",
		"index_repository", "get_issue", "update_issue",
	} {
		descs = append(descs, e2sTestMCPDesc(n, "github", sdktools.GroupLocalMCP))
	}
	for _, n := range []string{"resolve-library-id", "query-docs", "web-search-exa", "deep-research"} {
		descs = append(descs, e2sTestMCPDesc(n, "docs-remote", sdktools.GroupRemoteMCP))
	}

	return descs
}

// runE2SCatalogPipeline mirrors runE2SWithState's exact narrowing chain:
// e2s.tools config filter → Model Profiles → goal-mode strip → E2S strip.
func runE2SCatalogPipeline(o *Orchestrator, in []sdktools.ToolDescriptor) []sdktools.ToolDescriptor {
	return stripE2SUnavailableTools(tools.StripGoalModeTools(
		o.applyModelProfilesToolFilter(
			filterE2SToolsByConfig(in, o.e2sSettings().Tools))))
}

// e2sCatalogNames extracts the catalog as a name set.
func e2sCatalogNames(catalog []sdktools.ToolDescriptor) map[string]struct{} {
	set := make(map[string]struct{}, len(catalog))
	for _, d := range catalog {
		set[d.Name] = struct{}{}
	}
	return set
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestE2SCatalog_DefaultCorePreset_BoundedAndMCPFree pins the default-catalog
// contract: with the default config (no e2s.tools section at all) the E2S
// catalog on the reference descriptor set stays within the 25-tool budget
// and carries NO MCP tools (the GitHub/PR block that dominates the full
// registry must be gone), while the local work core and the system plumbing
// stay in.
func TestE2SCatalog_DefaultCorePreset_BoundedAndMCPFree(t *testing.T) {
	reference := e2sToolsReferenceCatalog()
	if len(reference) != 85 {
		t.Fatalf("reference catalog = %d descriptors, want 85 (the realistic full surface)", len(reference))
	}

	o := &Orchestrator{}
	o.config.E2S = E2SSettings{Enabled: true} // Tools zero → default core preset

	catalog := runE2SCatalogPipeline(o, reference)
	if len(catalog) > 25 {
		t.Fatalf("default E2S catalog = %d tools, want <= 25: %v", len(catalog), func() []string {
			names := make([]string, 0, len(catalog))
			for _, d := range catalog {
				names = append(names, d.Name)
			}
			return names
		}())
	}
	if len(catalog) < 15 {
		t.Fatalf("default E2S catalog = %d tools — suspiciously narrow, the local work core must survive", len(catalog))
	}

	names := e2sCatalogNames(catalog)
	for _, d := range catalog {
		if d.SourceCategory == sdktools.SourceCategoryMCP {
			t.Errorf("default E2S catalog contains MCP tool %q (source %q) — must be MCP-free", d.Name, d.Source)
		}
	}
	for _, banned := range []string{"create_pull_request", "list_pull_requests", "push_files", "search_code", "deep-research"} {
		if _, ok := names[banned]; ok {
			t.Errorf("default E2S catalog contains GitHub/MCP tool %q", banned)
		}
	}
	// The local work core.
	for _, want := range []string{"read_file", "list_directory", "glob", "ripgrep", "write_file", "edit_file", "bash_exec", "web_fetch", "web_search"} {
		if _, ok := names[want]; !ok {
			t.Errorf("default E2S catalog lost core tool %q", want)
		}
	}
	// The system plumbing.
	for _, want := range []string{"batch", "tool_result_read", "ask_user", "store_fact", "search_facts", "read_attachment"} {
		if _, ok := names[want]; !ok {
			t.Errorf("default E2S catalog lost plumbing tool %q", want)
		}
	}
	// Plan/delegation/goal stripping still applies on top of the preset.
	for _, banned := range []string{"declare_plan", "delegate", "propose_goal", "reflect", "update_checklist"} {
		if _, ok := names[banned]; ok {
			t.Errorf("default E2S catalog contains stripped tool %q", banned)
		}
	}
	// Non-plumbing system tools stay out of the preset.
	for _, banned := range []string{"semantic_search", "read_skill_resource"} {
		if _, ok := names[banned]; ok {
			t.Errorf("default E2S catalog contains non-plumbing system tool %q (re-include via e2s.tools.allow)", banned)
		}
	}
}

// TestE2SCatalog_PresetAll_RestoresFullSurface pins the escape hatch:
// e2s.tools.preset=all restores the FULL former surface — the catalog is
// byte-for-byte the pre-narrowing pipeline output (goal-mode + E2S stripping
// only), MCP GitHub tools included.
func TestE2SCatalog_PresetAll_RestoresFullSurface(t *testing.T) {
	reference := e2sToolsReferenceCatalog()

	o := &Orchestrator{}
	o.config.E2S = E2SSettings{Enabled: true, Tools: BuilderE2SToolsConfig{Preset: E2SToolsPresetAll}}

	catalog := runE2SCatalogPipeline(o, reference)

	// The exact former surface: the pipeline minus the e2s.tools stage.
	former := stripE2SUnavailableTools(tools.StripGoalModeTools(reference))
	if len(catalog) != len(former) {
		t.Fatalf("preset=all catalog = %d tools, want the full former surface of %d", len(catalog), len(former))
	}
	got, want := e2sCatalogNames(catalog), e2sCatalogNames(former)
	for n := range want {
		if _, ok := got[n]; !ok {
			t.Errorf("preset=all catalog lost %q", n)
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			t.Errorf("preset=all catalog has unexpected %q", n)
		}
	}
	names := got
	if _, ok := names["create_pull_request"]; !ok {
		t.Errorf("preset=all catalog must contain the MCP tool create_pull_request")
	}
	if _, ok := names["semantic_search"]; !ok {
		t.Errorf("preset=all catalog must contain the system tool semantic_search")
	}
}

// TestFilterE2SToolsByConfig_DenyAndAllow pins the set algebra: deny wins
// over allow (and over the preset), allow re-includes preset-excluded
// names, and an unknown preset value fails closed to core.
func TestFilterE2SToolsByConfig_DenyAndAllow(t *testing.T) {
	reference := e2sToolsReferenceCatalog()

	t.Run("deny removes a preset-included tool", func(t *testing.T) {
		out := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{Preset: E2SToolsPresetCore, Deny: []string{"write_file"}})
		names := e2sCatalogNames(out)
		if _, ok := names["write_file"]; ok {
			t.Errorf("denied write_file still in catalog")
		}
		if _, ok := names["read_file"]; !ok {
			t.Errorf("deny must not touch unrelated tools")
		}
	})

	t.Run("deny wins over allow", func(t *testing.T) {
		out := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{
			Preset: E2SToolsPresetCore,
			Allow:  []string{"semantic_search"},
			Deny:   []string{"semantic_search"},
		})
		if _, ok := e2sCatalogNames(out)["semantic_search"]; ok {
			t.Errorf("deny must beat allow: semantic_search still in catalog")
		}
	})

	t.Run("deny wins over allow for an MCP tool", func(t *testing.T) {
		out := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{
			Preset: E2SToolsPresetCore,
			Allow:  []string{"create_pull_request"},
			Deny:   []string{"create_pull_request"},
		})
		if _, ok := e2sCatalogNames(out)["create_pull_request"]; ok {
			t.Errorf("deny must beat allow: create_pull_request still in catalog")
		}
	})

	t.Run("allow re-includes a preset-excluded name", func(t *testing.T) {
		out := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{Preset: E2SToolsPresetCore, Allow: []string{"semantic_search"}})
		if _, ok := e2sCatalogNames(out)["semantic_search"]; !ok {
			t.Errorf("allow must re-include semantic_search")
		}
		if _, ok := e2sCatalogNames(out)["read_file"]; !ok {
			t.Errorf("allow must not disturb the preset core")
		}
	})

	t.Run("unknown preset fails closed to core", func(t *testing.T) {
		out := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{Preset: "al I typoed"})
		if len(out) > 25 {
			t.Errorf("unknown preset %q must fail closed to core, got %d tools", "al I typoed", len(out))
		}
		if _, ok := e2sCatalogNames(out)["create_pull_request"]; ok {
			t.Errorf("unknown preset must not leak MCP tools")
		}
	})

	t.Run("input is never mutated", func(t *testing.T) {
		wantNames := make([]string, len(reference))
		wantSchemas := make([]string, len(reference))
		for i, d := range reference {
			wantNames[i] = d.Name
			wantSchemas[i] = string(d.InputSchema)
		}
		_ = filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{Preset: E2SToolsPresetCore, Deny: []string{"read_file"}})
		for i, d := range reference {
			if d.Name != wantNames[i] || string(d.InputSchema) != wantSchemas[i] {
				t.Fatalf("filterE2SToolsByConfig mutated its input at index %d (%s)", i, wantNames[i])
			}
		}
	})
}

// failIfCalledExec is a ToolExecutor double that fails the test when reached.
type failIfCalledExec struct {
	calls []string
}

func (e *failIfCalledExec) Execute(ctx context.Context, name string, input json.RawMessage) (sdktools.ToolResult, error) {
	e.calls = append(e.calls, name)
	return sdktools.ToolResult{Content: "inner executed"}, nil
}
func (e *failIfCalledExec) GetToolSource(name string) string { return "core" }
func (e *failIfCalledExec) IsToolUntrusted(name string) bool { return false }
func (e *failIfCalledExec) CacheStrategy(ctx context.Context, name string, input json.RawMessage) sdktools.CacheMode {
	return sdktools.CacheModeDefault
}

// TestE2SRegistryAdapter_DeniedToolFailsClosedAtDispatch pins the dispatch
// half of the deny contract: a name removed by e2s.tools.deny is rejected by
// the registry adapter as an error RESULT (never a Go error, never reaching
// the inner executor), because the filtered catalog is the dispatch
// contract. An allowed tool still dispatches through to the inner executor.
func TestE2SRegistryAdapter_DeniedToolFailsClosedAtDispatch(t *testing.T) {
	reference := e2sToolsReferenceCatalog()
	filtered := filterE2SToolsByConfig(reference, BuilderE2SToolsConfig{
		Preset: E2SToolsPresetCore,
		Deny:   []string{"web_search"},
	})
	if _, ok := e2sCatalogNames(filtered)["web_search"]; ok {
		t.Fatalf("deny filter failed: web_search still in the catalog")
	}

	inner := &failIfCalledExec{}
	adapter := newE2SRegistryAdapter(inner, filtered)

	// Denied name → error result, inner never reached.
	res, err := adapter.Execute(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("Execute must surface the rejection as a tool RESULT, got Go error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("denied dispatch must return an error result, got: %+v", res)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("denied dispatch reached the inner executor: %v", inner.calls)
	}
	if res.Content == "" {
		t.Fatal("rejection result must carry an explanatory message")
	}

	// Preset-excluded (not merely denied) names are equally unreachable:
	// the whole filtered catalog is the contract.
	res, err = adapter.Execute(context.Background(), "create_pull_request", json.RawMessage(`{}`))
	if err != nil || !res.IsError {
		t.Fatalf("preset-excluded MCP dispatch must fail closed as an error result, got res=%+v err=%v", res, err)
	}
	if len(inner.calls) != 0 {
		t.Fatalf("preset-excluded dispatch reached the inner executor: %v", inner.calls)
	}

	// A cataloged tool still dispatches through to the inner executor.
	res, err = adapter.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"x"}`))
	if err != nil {
		t.Fatalf("cataloged dispatch returned a Go error: %v", err)
	}
	if res.IsError || len(inner.calls) != 1 || inner.calls[0] != "read_file" {
		t.Fatalf("cataloged dispatch must reach the inner executor exactly once, got res=%+v calls=%v", res, inner.calls)
	}
}
