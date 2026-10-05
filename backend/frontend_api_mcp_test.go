package backend

import (
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// --- UpdateMCPServers locking ---

// TestUpdateMCPServers_ReadersNotBlockedDuringReconfigure verifies that
// GetConfig completes while UpdateMCPServers is inside its propagation
// phase. That phase (ReconfigureMCP) reconnects every changed server and
// retries every previously-failed one with no deadline of its own; the whole
// update used to hold configMu.Lock across it, convoying every reader — the
// settings dialog froze for as long as the reconfigure took (observed at
// 169 s). Mirrors TestUpdateProxySettings_ReadersNotBlockedDuringRebuild.
func TestUpdateMCPServers_ReadersNotBlockedDuringReconfigure(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	reconfigureStarted := make(chan struct{})
	release := make(chan struct{})
	mock.reconfigureMCPHook = func() {
		select {
		case <-reconfigureStarted:
		default:
			close(reconfigureStarted)
		}
		<-release // hold the propagation phase open
	}

	updateDone := make(chan error, 1)
	go func() {
		updateDone <- f.UpdateMCPServers(map[string]config.MCPServerConfig{
			"test-server": {Transport: "stdio", Command: "cmd"},
		})
	}()

	select {
	case <-reconfigureStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("MCP reconfigure phase never started")
	}

	// A reader must return promptly and observe the already-applied mutation.
	readerDone := make(chan map[string]config.MCPServerConfig, 1)
	go func() { readerDone <- f.GetMCPServers() }()

	select {
	case servers := <-readerDone:
		if _, ok := servers["test-server"]; !ok {
			t.Error("GetMCPServers did not observe the applied MCP mutation")
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("GetMCPServers blocked while UpdateMCPServers was in its reconfigure phase — configMu lock convoy present")
	}

	// A WRITER must get in too. Two readers never block each other, but a
	// queued writer also stops every subsequent reader — that is what froze
	// the dialog.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		f.configMu.Lock()
		f.configLoadErrors = nil
		f.configMu.Unlock()
	}()
	select {
	case <-writerDone:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a config writer blocked while UpdateMCPServers was in its reconfigure phase")
	}

	close(release)
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("UpdateMCPServers: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("UpdateMCPServers did not return after the hook was released")
	}
}

// The reconfigure phase must be bounded. UpdateMCPServers hands its context
// to ReconfigureMCP, whose server.Connect honours it, so one unreachable
// endpoint can no longer stall the update indefinitely.
func TestUpdateMCPServers_ReconfigureContextBounded(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	if err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"test-server": {Transport: "stdio", Command: "cmd"},
	}); err != nil {
		t.Fatalf("UpdateMCPServers: %v", err)
	}

	ctx := mock.ReconfigureMCPCtx()
	if ctx == nil {
		t.Fatal("ReconfigureMCP received no context")
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("ReconfigureMCP received a context with no deadline — one unreachable MCP endpoint can stall the update indefinitely")
	}
}

func TestUpdateMCPServers_PersistsAndReconfigures(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	// Initialize MCP config section.
	f.config.MCP = config.MCPConfig{
		Servers: make(map[string]config.MCPServerConfig),
	}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"test-server": {Command: "cmd", Args: []string{"--arg"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.reconfigureMCPCalls != 1 {
		t.Errorf("ReconfigureMCP called %d times, want 1", mock.reconfigureMCPCalls)
	}
	if _, ok := f.config.MCP.Servers["test-server"]; !ok {
		t.Error("test-server not persisted in config")
	}
}

func TestUpdateMCPServers_InvalidStdioNoCommand(t *testing.T) {
	f, mock, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{
		Servers: make(map[string]config.MCPServerConfig),
	}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad": {Transport: "stdio", Command: ""},
	})
	if err == nil {
		t.Fatal("expected validation error for empty command")
	}
	if mock.reconfigureMCPCalls != 0 {
		t.Error("ReconfigureMCP should not be called on invalid input")
	}
}

func TestUpdateMCPServers_InvalidHTTPNoURL(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad-http": {Transport: "http", URL: ""},
	})
	if err == nil {
		t.Fatal("expected validation error for empty URL on http transport")
	}
}

func TestUpdateMCPServers_UnsupportedTransport(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"bad": {Transport: "grpc", Command: "cmd"},
	})
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}
}

func TestUpdateMCPServers_DeepCopy(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{Servers: make(map[string]config.MCPServerConfig)}

	args := []string{"--port", "9000"}
	env := map[string]string{"KEY": "VAL"}
	err := f.UpdateMCPServers(map[string]config.MCPServerConfig{
		"srv": {Command: "cmd", Args: args, Env: env},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutate the caller's slice + map after the call.
	args[0] = "MUTATED"
	env["KEY"] = "MUTATED"

	stored := f.config.MCP.Servers["srv"]
	if stored.Args[0] == "MUTATED" {
		t.Error("stored Args[0] was mutated — deep copy missing")
	}
	if stored.Env["KEY"] == "MUTATED" {
		t.Error("stored Env['KEY'] was mutated — deep copy missing")
	}
}

// --- GetMCPServers ---

func TestGetMCPServers_DeepCopy(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"srv": {Command: "cmd", Args: []string{"a"}, Env: map[string]string{"K": "V"}},
		},
	}

	got := f.GetMCPServers()
	got["srv"] = config.MCPServerConfig{Command: "HACK"} // mutate returned map

	// Internal config must NOT be affected.
	if f.config.MCP.Servers["srv"].Command != "cmd" {
		t.Error("GetMCPServers returned a live reference instead of a deep copy")
	}
}

// --- GetMCPStatus ---

// TestGetMCPStatus_NoApp verifies the nil-app early return.
//
// The remaining branches of GetMCPStatus (the non-blocking Starting-placeholder
// while MCPStartupDone()==false, the startup-error placeholder, and the live
// gateway status) are exercised indirectly through the core builder
// tests in core/builder_mcp_test.go (TestMCPStartupDone, TestMCPGatewayNoWait),
// which verify the exact builder methods Application.GetMCPStatus delegates to.
// Constructing an Application with MCPStartupDone()==false from this package is
// not feasible without a racy NewOrchestratorBuilder call: the mcpDone channel
// and gateway field are private to core, so the placeholder state cannot be set
// deterministically from the backend package.
func TestGetMCPStatus_NoApp(t *testing.T) {
	f := &FrontendAPI{} // f.app == nil
	got := f.GetMCPStatus()
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d", len(got))
	}
}

// --- GetMCPStatus merge (configured servers always visible) ---

// TestMergeConfiguredMCPServers verifies the pure core of the
// "configured servers are always visible" contract: every configured name
// missing from the live gateway status is appended as a disconnected entry
// with a defaulted transport, every entry is enriched with its configured
// mode (gateway-only names default to "auto"), and the result stays sorted
// by name.
func TestMergeConfiguredMCPServers(t *testing.T) {
	status := []mcp.ServerStatus{
		{Name: "live", Transport: "stdio", Connected: true, Tools: []string{}},
		{Name: "dead", Transport: "http", Connected: false, Tools: []string{}, Error: "conn refused"},
		{Name: "ghost", Transport: "stdio", Connected: true, Tools: []string{}}, // gateway-only name
	}
	configured := map[string]config.MCPServerConfig{
		"live":    {Transport: "stdio", Command: "cmd", Mode: config.MCPServerModeAuto},
		"dead":    {Transport: "http", URL: "http://x", Mode: config.MCPServerModeManual},
		"unknown": {Command: "cmd"}, // transport empty → defaults to stdio; mode empty → auto
		"remote":  {Transport: "http", URL: "http://y"},
	}

	got := mergeConfiguredMCPServers(status, configured)

	if len(got) != 5 {
		t.Fatalf("expected 5 entries, got %d: %+v", len(got), got)
	}
	// Sorted by name: dead, ghost, live, remote, unknown.
	wantOrder := []string{"dead", "ghost", "live", "remote", "unknown"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
	// Existing entries must pass through untouched.
	if !got[2].Connected || got[2].Name != "live" {
		t.Errorf("live server status mutated: %+v", got[2])
	}
	// Missing configured servers are synthesized as disconnected.
	unknown := got[4]
	if unknown.Connected || unknown.Error != "unavailable" || unknown.Transport != "stdio" {
		t.Errorf("unknown = %+v, want disconnected stdio entry with error", unknown)
	}
	remote := got[3]
	if remote.Connected || remote.Transport != "http" {
		t.Errorf("remote = %+v, want disconnected http entry", remote)
	}
	// Mode exposure: configured modes are normalized onto every entry, and a
	// gateway-only name (not in the config) defaults to "auto".
	wantModes := map[string]string{
		"dead":    config.MCPServerModeManual,
		"ghost":   config.MCPServerModeAuto,
		"live":    config.MCPServerModeAuto,
		"remote":  config.MCPServerModeAuto,
		"unknown": config.MCPServerModeAuto,
	}
	for _, entry := range got {
		if got, want := entry.Mode, wantModes[entry.Name]; got != want {
			t.Errorf("%s.Mode = %q, want %q", entry.Name, got, want)
		}
	}
}

// TestMergeConfiguredMCPServers_DisabledNeutral pins the neutral rendering
// contract for disabled servers in BOTH shapes it can take:
//   - configured-but-absent (the normal case — the gateway config never
//     includes it, so the synthesized entry must carry NO "unavailable"
//     error: absence is what "never dialed" produces);
//   - still reported by a stale gateway (a reconfigure that failed
//     mid-flight can leave the old connection behind) — the config mode is
//     authoritative, so connected/tools/error are cleared.
func TestMergeConfiguredMCPServers_DisabledNeutral(t *testing.T) {
	status := []mcp.ServerStatus{
		{
			Name:      "stale-disabled",
			Transport: "stdio",
			Connected: true,
			Unhealthy: true,
			ToolCount: 3,
			Tools:     []string{"t1", "t2", "t3"},
			Error:     "old error",
		},
	}
	configured := map[string]config.MCPServerConfig{
		"stale-disabled": {Transport: "stdio", Command: "cmd", Mode: config.MCPServerModeDisabled},
		"fresh-disabled": {Transport: "http", URL: "http://x", Mode: config.MCPServerModeDisabled},
		"normal":         {Transport: "stdio", Command: "cmd"},
	}

	got := mergeConfiguredMCPServers(status, configured)

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(got), got)
	}
	byName := make(map[string]MCPServerStatusInfo, len(got))
	for _, entry := range got {
		byName[entry.Name] = entry
	}
	for _, name := range []string{"stale-disabled", "fresh-disabled"} {
		entry := byName[name]
		if entry.Mode != config.MCPServerModeDisabled {
			t.Errorf("%s.Mode = %q, want %q", name, entry.Mode, config.MCPServerModeDisabled)
		}
		if entry.Connected || entry.Unhealthy || entry.Starting {
			t.Errorf("%s = %+v, want every activity flag cleared (inert, not live)", name, entry.ServerStatus)
		}
		if entry.ToolCount != 0 || len(entry.Tools) != 0 {
			t.Errorf("%s = %+v, want tools cleared", name, entry.ServerStatus)
		}
		if entry.Error != "" {
			t.Errorf("%s.Error = %q, want empty (neutral — disabled is not a failure state)", name, entry.Error)
		}
	}
	// A non-disabled configured-but-absent server keeps the classic
	// "unavailable" synthesis — only disabled renders neutrally.
	if normal := byName["normal"]; normal.Error != "unavailable" {
		t.Errorf("normal.Error = %q, want %q (disabled-only neutralization)", normal.Error, "unavailable")
	}
}

// TestMergeConfiguredMCPServers_EmptyConfig verifies that an empty (or nil)
// configuration leaves the gateway status entries as-is (mode defaulting to
// "auto" on each).
func TestMergeConfiguredMCPServers_EmptyConfig(t *testing.T) {
	status := []mcp.ServerStatus{{Name: "live", Connected: true}}
	if got := mergeConfiguredMCPServers(status, nil); len(got) != 1 {
		t.Errorf("expected untouched status for nil config, got %+v", got)
	}
	if got := mergeConfiguredMCPServers(status, map[string]config.MCPServerConfig{}); len(got) != 1 {
		t.Errorf("expected untouched status for empty config, got %+v", got)
	}
}

// --- GetMCPMentionableServers ---

// TestGetMCPMentionableServers verifies the secret-free mentionable listing:
// every configured server as {name, mode}, sorted by name, with modes
// normalized (empty resolves to "auto"). The wire shape carries nothing
// beyond name+mode — the struct itself is the guarantee that command args,
// env and headers (which can embed secrets) never reach the completion path.
func TestGetMCPMentionableServers(t *testing.T) {
	f, _, _ := newTestAPI(t)
	f.config.MCP = config.MCPConfig{
		Servers: map[string]config.MCPServerConfig{
			"zeta":   {Transport: "stdio", Command: "cmd", Mode: config.MCPServerModeManual},
			"alpha":  {Transport: "http", URL: "http://x", Mode: config.MCPServerModeDisabled},
			"mid":    {Transport: "stdio", Command: "cmd", Mode: config.MCPServerModeAuto},
			"empty":  {Transport: "stdio", Command: "cmd"},
			"bearer": {Transport: "stdio", Command: "cmd", Env: map[string]string{"TOKEN": "secret"}},
		},
	}

	got := f.GetMCPMentionableServers()

	want := []MCPMentionableServer{
		{Name: "alpha", Mode: config.MCPServerModeDisabled},
		{Name: "bearer", Mode: config.MCPServerModeAuto},
		{Name: "empty", Mode: config.MCPServerModeAuto},
		{Name: "mid", Mode: config.MCPServerModeAuto},
		{Name: "zeta", Mode: config.MCPServerModeManual},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d mentionable servers, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestGetMCPMentionableServers_NoConfig verifies the uninitialized-config
// early return: an empty (never nil) slice, not a null payload.
func TestGetMCPMentionableServers_NoConfig(t *testing.T) {
	f := &FrontendAPI{} // f.config == nil
	got := f.GetMCPMentionableServers()
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(got))
	}
}

// TestIsMCPStartupPlaceholder pins the placeholder detection that suppresses
// the config merge while the gateway is still starting.
func TestIsMCPStartupPlaceholder(t *testing.T) {
	starting := []mcp.ServerStatus{{Name: "_gateway", Starting: true}}
	if !isMCPStartupPlaceholder(starting) {
		t.Error("starting placeholder not detected")
	}
	failed := []mcp.ServerStatus{{Name: "_gateway", Error: "boom"}}
	if isMCPStartupPlaceholder(failed) {
		t.Error("startup-error placeholder must not be treated as starting (config merge must apply)")
	}
	live := []mcp.ServerStatus{{Name: "srv", Connected: true}}
	if isMCPStartupPlaceholder(live) {
		t.Error("live status must not be treated as placeholder")
	}
	if isMCPStartupPlaceholder(nil) {
		t.Error("nil status must not be treated as placeholder")
	}
}

// --- GetToolList ---

func TestGetToolList_NoApp(t *testing.T) {
	f := &FrontendAPI{} // f.app == nil
	got := f.GetToolList()
	if got == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice when app is nil, got %d", len(got))
	}
}

func TestGetToolList_BuildToolInfos(t *testing.T) {
	// buildToolInfos is the pure core of GetToolList (the full method needs a
	// live Application with heavyweight builder infra). It must skip
	// system-group tools BY GROUP, label every tool with its group, and
	// resolve the effective policy from the live registry map.
	descriptors := []sdktools.ToolDescriptor{
		{Name: "bash_exec", Description: "shell", Source: "core", Group: sdktools.GroupExecute},
		{Name: "read_file", Description: "read", Source: "core", Group: sdktools.GroupLocalRead},
		{Name: "finish", Description: "internal", Source: "core", Group: sdktools.GroupSystem},
		{Name: "mcp_query", Description: "mcp tool", Source: "mcp:test-server", Group: sdktools.GroupLocalMCP},
		{Name: "web_fetch", Description: "web", Source: "core", Group: sdktools.GroupRemoteRead},
	}
	policies := map[sdktools.ToolGroup]sdktools.ToolPolicy{
		sdktools.GroupExecute:   sdktools.PolicyAlwaysDeny,
		sdktools.GroupLocalRead: sdktools.PolicyAlwaysAllow,
		sdktools.GroupLocalMCP:  sdktools.PolicyUserConfirm,
		// GroupRemoteRead deliberately absent → fail-safe user_confirm.
	}

	got := buildToolInfos(descriptors, policies)

	byName := make(map[string]ToolInfo, len(got))
	for _, info := range got {
		byName[info.Name] = info
	}
	if _, ok := byName["finish"]; ok {
		t.Error("system-group tool 'finish' must be filtered out by group")
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 tools after filtering, got %d: %+v", len(got), got)
	}

	exec := byName["bash_exec"]
	if exec.Group != "execute" || exec.Policy != "deny" {
		t.Errorf("bash_exec = group %q policy %q, want execute/deny", exec.Group, exec.Policy)
	}
	read := byName["read_file"]
	if read.Group != "local_read" || read.Policy != "allow" {
		t.Errorf("read_file = group %q policy %q, want local_read/allow", read.Group, read.Policy)
	}
	mcpTool := byName["mcp_query"]
	if mcpTool.Group != "local_mcp" || mcpTool.Policy != "user_confirm" {
		t.Errorf("mcp_query = group %q policy %q, want local_mcp/user_confirm", mcpTool.Group, mcpTool.Policy)
	}
	// A group without a registry entry fails safe to user_confirm.
	web := byName["web_fetch"]
	if web.Policy != "user_confirm" {
		t.Errorf("web_fetch policy = %q, want fail-safe user_confirm", web.Policy)
	}
}

// --- validateMCPServerConfig ---

func TestValidateMCPServerConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.MCPServerConfig
		wantErr bool
	}{
		{name: "stdio valid", cfg: config.MCPServerConfig{Command: "cmd"}, wantErr: false},
		{name: "stdio no command", cfg: config.MCPServerConfig{Transport: "stdio"}, wantErr: true},
		{name: "http valid", cfg: config.MCPServerConfig{Transport: "http", URL: "http://x"}, wantErr: false},
		{name: "http no url", cfg: config.MCPServerConfig{Transport: "http"}, wantErr: true},
		{name: "unknown transport", cfg: config.MCPServerConfig{Transport: "grpc"}, wantErr: true},
		{name: "default transport needs command", cfg: config.MCPServerConfig{}, wantErr: true},
		{name: "empty timeouts ok", cfg: config.MCPServerConfig{Command: "cmd", Timeout: "", CallTimeout: ""}, wantErr: false},
		{name: "valid timeout", cfg: config.MCPServerConfig{Command: "cmd", Timeout: "30s"}, wantErr: false},
		{name: "valid call_timeout", cfg: config.MCPServerConfig{Command: "cmd", CallTimeout: "2m"}, wantErr: false},
		{name: "invalid timeout", cfg: config.MCPServerConfig{Command: "cmd", Timeout: "abc"}, wantErr: true},
		{name: "zero timeout", cfg: config.MCPServerConfig{Command: "cmd", Timeout: "0s"}, wantErr: true},
		{name: "negative timeout", cfg: config.MCPServerConfig{Command: "cmd", Timeout: "-5s"}, wantErr: true},
		{name: "invalid call_timeout", cfg: config.MCPServerConfig{Command: "cmd", CallTimeout: "abc"}, wantErr: true},
		{name: "zero call_timeout", cfg: config.MCPServerConfig{Command: "cmd", CallTimeout: "0s"}, wantErr: true},
		// Mode: empty is the accepted default; the three canonical enum
		// members pass; anything else is rejected up front (the load path is
		// the fail-soft counterpart — normalizeMCPModes).
		{name: "empty mode ok (default auto)", cfg: config.MCPServerConfig{Command: "cmd"}, wantErr: false},
		{name: "mode auto ok", cfg: config.MCPServerConfig{Command: "cmd", Mode: config.MCPServerModeAuto}, wantErr: false},
		{name: "mode manual ok", cfg: config.MCPServerConfig{Command: "cmd", Mode: config.MCPServerModeManual}, wantErr: false},
		{name: "mode disabled ok", cfg: config.MCPServerConfig{Command: "cmd", Mode: config.MCPServerModeDisabled}, wantErr: false},
		{name: "mode bogus rejected", cfg: config.MCPServerConfig{Command: "cmd", Mode: "bogus"}, wantErr: true},
		{name: "mode case-sensitive", cfg: config.MCPServerConfig{Command: "cmd", Mode: "Auto"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMCPServerConfig("test", tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMCPServerConfig = error %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
