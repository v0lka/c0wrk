package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestValidMCPServerMode pins the closed mode enum shared by the load path,
// the UI save path, the status/mentionable RPCs and the capabilities section:
// exactly the three canonical values are recognized; empty is the "unset"
// signal and is deliberately not valid (callers resolve it to the default
// themselves).
func TestValidMCPServerMode(t *testing.T) {
	for _, mode := range []string{MCPServerModeAuto, MCPServerModeManual, MCPServerModeDisabled} {
		if !ValidMCPServerMode(mode) {
			t.Errorf("ValidMCPServerMode(%q) = false, want true", mode)
		}
	}
	for _, mode := range []string{"", "Auto", "AUTO", "on", "off", "enabled", "weird"} {
		if ValidMCPServerMode(mode) {
			t.Errorf("ValidMCPServerMode(%q) = true, want false", mode)
		}
	}
}

// TestNormalizeMCPServerMode pins the canonicalization rule: recognized
// values pass through; empty and unrecognized values resolve to the default
// "auto" — silently, because distinguishing a silent default (empty) from a
// reported fallback (invalid) is the load path's job.
func TestNormalizeMCPServerMode(t *testing.T) {
	cases := []struct{ raw, want string }{
		{MCPServerModeAuto, MCPServerModeAuto},
		{MCPServerModeManual, MCPServerModeManual},
		{MCPServerModeDisabled, MCPServerModeDisabled},
		{"", MCPServerModeAuto},
		{"weird", MCPServerModeAuto},
	}
	for _, tc := range cases {
		if got := NormalizeMCPServerMode(tc.raw); got != tc.want {
			t.Errorf("NormalizeMCPServerMode(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestMigrateLegacyMCPServerModes pins the legacy-mode migration branch by
// branch: a set mode folds into capabilities.mcp.<name> (BOTH contexts — the
// legacy key had no context split), an absent mode folds nothing, an
// explicit capability entry outranks the legacy key, and the Mode field is
// ALWAYS erased so the next save drops the legacy key from YAML.
func TestMigrateLegacyMCPServerModes(t *testing.T) {
	t.Run("absent mode migrates nothing", func(t *testing.T) {
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"legacy": {Command: "cmd"},
		}
		if w := migrateLegacyMCPServerModes(cfg); len(w) != 0 {
			t.Errorf("warnings = %v, want none", w)
		}
		if len(cfg.Capabilities.MCP) != 0 {
			t.Errorf("capabilities.mcp = %v, want empty (nothing to fold)", cfg.Capabilities.MCP)
		}
		if got := cfg.MCP.Servers["legacy"].Mode; got != "" {
			t.Errorf("legacy.Mode = %q, want erased", got)
		}
	})

	t.Run("manual folds into both contexts", func(t *testing.T) {
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"gated": {Command: "cmd", Mode: MCPServerModeManual},
		}
		if w := migrateLegacyMCPServerModes(cfg); len(w) != 0 {
			t.Errorf("warnings = %v, want none", w)
		}
		got := cfg.Capabilities.MCP["gated"]
		if got.ChatMode != MCPServerModeManual || got.CodeMode != MCPServerModeManual {
			t.Errorf("capabilities.mcp.gated = %+v, want both contexts %q", got, MCPServerModeManual)
		}
		if s := cfg.MCP.Servers["gated"]; s.Mode != "" {
			t.Errorf("gated.Mode = %q, want erased after the fold", s.Mode)
		}
	})

	t.Run("explicit auto folds too", func(t *testing.T) {
		// `mode: auto` IS a set mode: it folds explicitly so the migration
		// rule is uniform, and the saved form stays semantically identical.
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"plain": {Command: "cmd", Mode: MCPServerModeAuto},
		}
		if w := migrateLegacyMCPServerModes(cfg); len(w) != 0 {
			t.Errorf("warnings = %v, want none", w)
		}
		got := cfg.Capabilities.MCP["plain"]
		if got.ChatMode != MCPServerModeAuto || got.CodeMode != MCPServerModeAuto {
			t.Errorf("capabilities.mcp.plain = %+v, want both contexts %q", got, MCPServerModeAuto)
		}
	})

	t.Run("invalid mode warns and folds auto", func(t *testing.T) {
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"broken": {Command: "cmd", Mode: "weird"},
		}
		warnings := migrateLegacyMCPServerModes(cfg)
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v, want exactly 1 (mcp.servers.broken.mode)", warnings)
		}
		if !strings.Contains(warnings[0], "mcp.servers.broken.mode") {
			t.Errorf("warning must name the legacy key, got %q", warnings[0])
		}
		got := cfg.Capabilities.MCP["broken"]
		if got.ChatMode != MCPServerModeAuto || got.CodeMode != MCPServerModeAuto {
			t.Errorf("capabilities.mcp.broken = %+v, want both contexts %q (fail-soft)", got, MCPServerModeAuto)
		}
		if s := cfg.MCP.Servers["broken"]; s.Mode != "" {
			t.Errorf("broken.Mode = %q, want erased even on the warning path", s.Mode)
		}
	})

	t.Run("explicit capability values outrank the legacy key", func(t *testing.T) {
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"split": {Command: "cmd", Mode: MCPServerModeManual},
		}
		cfg.Capabilities.MCP = map[string]CapabilityModes{
			"split": {ChatMode: MCPServerModeDisabled},
		}
		if w := migrateLegacyMCPServerModes(cfg); len(w) != 0 {
			t.Errorf("warnings = %v, want none", w)
		}
		got := cfg.Capabilities.MCP["split"]
		if got.ChatMode != MCPServerModeDisabled {
			t.Errorf("chat_mode = %q, want the explicit %q to win", got.ChatMode, MCPServerModeDisabled)
		}
		if got.CodeMode != "" {
			t.Errorf("code_mode = %q, want empty (partially authored entry is left alone — the fold is all-or-nothing)", got.CodeMode)
		}
	})

	t.Run("both-empty entry accepts the fold", func(t *testing.T) {
		cfg := &Config{}
		cfg.MCP.Servers = map[string]MCPServerConfig{
			"empty": {Command: "cmd", Mode: MCPServerModeManual},
		}
		cfg.Capabilities.MCP = map[string]CapabilityModes{
			"empty": {},
		}
		if w := migrateLegacyMCPServerModes(cfg); len(w) != 0 {
			t.Errorf("warnings = %v, want none", w)
		}
		got := cfg.Capabilities.MCP["empty"]
		if got.ChatMode != MCPServerModeManual || got.CodeMode != MCPServerModeManual {
			t.Errorf("capabilities.mcp.empty = %+v, want the fold to fill both contexts", got)
		}
	})
}

// TestMigrateLegacyMCPServerModes_AlwaysErasesMode pins the invariant that
// gives the migration its name: after the load, NO server carries a Mode
// value regardless of which branch it took, so the legacy key cannot
// resurrect in YAML on the next save.
func TestMigrateLegacyMCPServerModes_AlwaysErasesMode(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"absent":  {Command: "cmd"},
		"auto":    {Command: "cmd", Mode: MCPServerModeAuto},
		"manual":  {Command: "cmd", Mode: MCPServerModeManual},
		"invalid": {Command: "cmd", Mode: "weird"},
	}
	migrateLegacyMCPServerModes(cfg)
	for name, srv := range cfg.MCP.Servers {
		if srv.Mode != "" {
			t.Errorf("%s.Mode = %q, want erased on every branch", name, srv.Mode)
		}
	}
}

// TestNormalizeCapabilities_CanonicalizesAndWarns pins the fail-soft
// canonicalization of the capabilities sections: empty resolves to "auto"
// silently, an unrecognized value warns (naming the full entry path) and is
// rewritten in place, valid values pass through. The warnings surface in a
// deterministic order (sections in skills → agents → mcp order, names
// sorted).
func TestNormalizeCapabilities_CanonicalizesAndWarns(t *testing.T) {
	cfg := &Config{}
	cfg.Capabilities.Skills = map[string]CapabilityModes{
		"study": {ChatMode: MCPServerModeManual, CodeMode: "weird"},
	}
	cfg.Capabilities.Agents = map[string]CapabilityModes{
		"reviewer": {ChatMode: "", CodeMode: MCPServerModeDisabled},
	}

	warnings := normalizeCapabilities(cfg)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 (study.code_mode)", warnings)
	}
	if !strings.Contains(warnings[0], "capabilities.skills.study.code_mode") {
		t.Errorf("warning must name the full entry path, got %q", warnings[0])
	}

	// The invalid value is rewritten in place; the valid one is untouched.
	if got := cfg.Capabilities.Skills["study"]; got.ChatMode != MCPServerModeManual || got.CodeMode != MCPServerModeAuto {
		t.Errorf("study = %+v, want {manual, auto}", got)
	}
	// Empty silently resolves to the canonical default.
	if got := cfg.Capabilities.Agents["reviewer"]; got.ChatMode != MCPServerModeAuto || got.CodeMode != MCPServerModeDisabled {
		t.Errorf("reviewer = %+v, want {auto, disabled}", got)
	}
}

// TestNormalizeCapabilities_WarningOrderIsDeterministic pins the surfacing
// order for multiple warnings: sections in fixed order, names sorted within
// a section — the UI surfaces them verbatim.
func TestNormalizeCapabilities_WarningOrderIsDeterministic(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"alpha": {Command: "cmd"},
		"zeta":  {Command: "cmd"},
	}
	cfg.Capabilities.MCP = map[string]CapabilityModes{
		"zeta":  {CodeMode: "bad-z"},
		"alpha": {ChatMode: "bad-a"},
	}
	cfg.Capabilities.Skills = map[string]CapabilityModes{
		"zzz": {ChatMode: "bad-skill"},
	}

	warnings := normalizeCapabilities(cfg)
	want := []string{
		"capabilities.skills.zzz.chat_mode",
		"capabilities.mcp.alpha.chat_mode",
		"capabilities.mcp.zeta.code_mode",
	}
	if len(warnings) != len(want) {
		t.Fatalf("warnings = %v, want exactly %v", warnings, want)
	}
	for i, path := range want {
		if !strings.Contains(warnings[i], path) {
			t.Errorf("warning[%d] = %q, want it to name %q", i, warnings[i], path)
		}
	}
}

// TestNormalizeCapabilities_RemovesStaleMCPEntries pins the staleness rule:
// a capabilities.mcp entry whose name is not in mcp.servers is dropped
// silently (the server was deleted or renamed), while the skills/agents
// sections have no in-config name registry and are never staleness-pruned.
func TestNormalizeCapabilities_RemovesStaleMCPEntries(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{
		"real": {Command: "cmd"},
	}
	cfg.Capabilities.MCP = map[string]CapabilityModes{
		"real":  {ChatMode: MCPServerModeManual},
		"ghost": {ChatMode: MCPServerModeDisabled, CodeMode: MCPServerModeDisabled},
	}
	cfg.Capabilities.Skills = map[string]CapabilityModes{
		"ghost": {ChatMode: MCPServerModeDisabled},
	}

	if w := normalizeCapabilities(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none (staleness is silent)", w)
	}
	if _, ok := cfg.Capabilities.MCP["ghost"]; ok {
		t.Error("capabilities.mcp.ghost survived; want stale entries dropped")
	}
	if _, ok := cfg.Capabilities.MCP["real"]; !ok {
		t.Error("capabilities.mcp.real was dropped; only stale names go")
	}
	if _, ok := cfg.Capabilities.Skills["ghost"]; !ok {
		t.Error("capabilities.skills.ghost was dropped; staleness pruning is mcp-only")
	}
}

// TestNormalizeCapabilities_RemovesEmptyEntries pins the cleanup rule: an
// entry whose values are BOTH empty carries no information and is dropped in
// every section; a one-sided entry is kept (its other side is the "auto"
// default).
func TestNormalizeCapabilities_RemovesEmptyEntries(t *testing.T) {
	cfg := &Config{}
	cfg.Capabilities.Skills = map[string]CapabilityModes{
		"empty": {},
		"side":  {CodeMode: MCPServerModeDisabled},
	}
	cfg.Capabilities.Agents = map[string]CapabilityModes{
		"empty": {},
	}
	cfg.Capabilities.MCP = map[string]CapabilityModes{
		"empty": {},
	}

	if w := normalizeCapabilities(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none (emptiness is silent)", w)
	}
	if _, ok := cfg.Capabilities.Skills["empty"]; ok {
		t.Error("capabilities.skills.empty survived; want both-empty entries dropped")
	}
	if _, ok := cfg.Capabilities.Agents["empty"]; ok {
		t.Error("capabilities.agents.empty survived; want both-empty entries dropped")
	}
	if _, ok := cfg.Capabilities.MCP["empty"]; ok {
		t.Error("capabilities.mcp.empty survived; want both-empty entries dropped")
	}
	if got := cfg.Capabilities.Skills["side"]; got.CodeMode != MCPServerModeDisabled || got.ChatMode != MCPServerModeAuto {
		t.Errorf("side = %+v, want kept and canonicalized to {auto, disabled}", got)
	}
}

// TestNormalizeCapabilities_EmptyConfigIsNoop pins that a config without the
// capabilities section normalizes to nothing: no warnings, no maps
// materialized, no panic.
func TestNormalizeCapabilities_EmptyConfigIsNoop(t *testing.T) {
	cfg := &Config{}
	cfg.MCP.Servers = map[string]MCPServerConfig{"srv": {Command: "cmd"}}
	if w := normalizeCapabilities(cfg); len(w) != 0 {
		t.Errorf("warnings = %v, want none", w)
	}
	if cfg.Capabilities.Skills != nil || cfg.Capabilities.Agents != nil || cfg.Capabilities.MCP != nil {
		t.Errorf("capabilities = %+v, want all maps untouched (nil)", cfg.Capabilities)
	}
}

// TestValidateCapabilities pins the strict save-path contract: empty and
// recognized values are accepted everywhere; any non-empty unrecognized
// chat_mode / code_mode is rejected with an error naming the full entry path
// and the accepted enum. (The structural rules — stale mcp names, both-empty
// entries — are deliberately NOT errors here: they are the load path's
// normalization job.)
func TestValidateCapabilities(t *testing.T) {
	valid := []CapabilitiesConfig{
		{},
		{
			Skills: map[string]CapabilityModes{"study-paper": {ChatMode: MCPServerModeManual, CodeMode: MCPServerModeDisabled}},
			Agents: map[string]CapabilityModes{"reviewer": {ChatMode: ""}},
			MCP:    map[string]CapabilityModes{"srv": {ChatMode: MCPServerModeAuto, CodeMode: MCPServerModeManual}},
		},
	}
	for i, c := range valid {
		if err := ValidateCapabilities(&c); err != nil {
			t.Errorf("valid[%d]: ValidateCapabilities() = %v, want nil", i, err)
		}
	}

	invalid := []struct {
		name    string
		c       CapabilitiesConfig
		wantSub string
	}{
		{
			name: "skills chat_mode",
			c: CapabilitiesConfig{
				Skills: map[string]CapabilityModes{"study-paper": {ChatMode: "weird"}},
			},
			wantSub: `capabilities.skills.study-paper: invalid chat_mode "weird"`,
		},
		{
			name: "agents code_mode",
			c: CapabilitiesConfig{
				Agents: map[string]CapabilityModes{"reviewer": {CodeMode: "Always"}},
			},
			wantSub: `capabilities.agents.reviewer: invalid code_mode "Always"`,
		},
		{
			name: "mcp chat_mode",
			c: CapabilitiesConfig{
				MCP: map[string]CapabilityModes{"srv": {ChatMode: "on"}},
			},
			wantSub: `capabilities.mcp.srv: invalid chat_mode "on"`,
		},
	}
	for _, tc := range invalid {
		err := ValidateCapabilities(&tc.c)
		if err == nil {
			t.Errorf("%s: ValidateCapabilities() = nil, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("%s: error = %q, want it to contain %q", tc.name, err, tc.wantSub)
		}
	}
}

// TestLoadWithResult_LegacyModeMigratesAndKeyIsDropped pins the end-to-end
// migration: a config authored with mcp.servers.<name>.mode loads with the
// value folded into capabilities.mcp.<name>, the Mode field erased in
// memory, AND the legacy key absent from the serialized YAML after a save —
// the next save is what actually removes the key from the file.
func TestLoadWithResult_LegacyModeMigratesAndKeyIsDropped(t *testing.T) {
	content := `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    fine:
      command: cmd
      mode: "manual"
    broken:
      command: cmd
      mode: "weird"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}

	// The invalid legacy mode still warns, naming the legacy key.
	joined := strings.Join(result.LoadErrors, "\n")
	if !strings.Contains(joined, "mcp.servers.broken.mode") {
		t.Errorf("LoadErrors must warn about mcp.servers.broken.mode, got %v", result.LoadErrors)
	}

	// Both modes folded into capabilities.mcp; nothing left in Mode.
	fine := result.Config.Capabilities.MCP["fine"]
	if fine.ChatMode != MCPServerModeManual || fine.CodeMode != MCPServerModeManual {
		t.Errorf("capabilities.mcp.fine = %+v, want both contexts manual", fine)
	}
	broken := result.Config.Capabilities.MCP["broken"]
	if broken.ChatMode != MCPServerModeAuto || broken.CodeMode != MCPServerModeAuto {
		t.Errorf("capabilities.mcp.broken = %+v, want both contexts auto (fail-soft)", broken)
	}
	for name, srv := range result.Config.MCP.Servers {
		if srv.Mode != "" {
			t.Errorf("%s.Mode = %q, want erased in memory", name, srv.Mode)
		}
	}

	// Save: the legacy key disappears from YAML, the folded capabilities
	// entry is what persists.
	savedPath := writeTestConfig(t, "") // reuse the tmp-dir helper for the path
	if err := Save(result.Config, savedPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	mcpSection, _ := doc["mcp"].(map[string]any)
	servers, _ := mcpSection["servers"].(map[string]any)
	if servers == nil {
		t.Fatalf("saved config lost mcp.servers entirely: %s", data)
	}
	for name, raw := range servers {
		srv, _ := raw.(map[string]any)
		if _, has := srv["mode"]; has {
			t.Errorf("mcp.servers.%s still serializes the legacy mode key", name)
		}
	}
	caps, _ := doc["capabilities"].(map[string]any)
	mcpCaps, _ := caps["mcp"].(map[string]any)
	if mcpCaps == nil {
		t.Fatalf("saved config lost capabilities.mcp: %s", data)
	}
	fineDoc, _ := mcpCaps["fine"].(map[string]any)
	if fineDoc["chat_mode"] != MCPServerModeManual || fineDoc["code_mode"] != MCPServerModeManual {
		t.Errorf("saved capabilities.mcp.fine = %v, want chat_mode/code_mode manual", fineDoc)
	}

	// The saved file reloads warning-free with the same capabilities — the
	// migration is stable, not a per-launch fold.
	reloaded, err := LoadWithResult(savedPath)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("reload must be warning-free, got %v", reloaded.LoadErrors)
	}
	if got := reloaded.Config.Capabilities.MCP["fine"]; got != (CapabilityModes{ChatMode: MCPServerModeManual, CodeMode: MCPServerModeManual}) {
		t.Errorf("reloaded capabilities.mcp.fine = %+v, want {manual manual}", got)
	}
}

// TestLoadWithResult_LegacyModeYieldsToExplicitCapabilities pins the
// precedence rule at the load level: an entry that already carries ANY
// explicit value wins over a coexisting legacy mode, and the fold does not
// fill the other side of a partially authored entry.
func TestLoadWithResult_LegacyModeYieldsToExplicitCapabilities(t *testing.T) {
	content := `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    split:
      command: cmd
      mode: "manual"
capabilities:
  mcp:
    split:
      chat_mode: "disabled"
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	joined := strings.Join(result.LoadErrors, "\n")
	if strings.Contains(joined, "capabilities.mcp.split") {
		t.Errorf("the explicit entry is valid and must not warn, got %v", result.LoadErrors)
	}
	got := result.Config.Capabilities.MCP["split"]
	if got.ChatMode != MCPServerModeDisabled || got.CodeMode != MCPServerModeAuto {
		t.Errorf("capabilities.mcp.split = %+v, want {disabled, auto} (explicit chat_mode wins; code_mode stays the default)", got)
	}
	if srv := result.Config.MCP.Servers["split"]; srv.Mode != "" {
		t.Errorf("split.Mode = %q, want erased even when the fold is skipped", srv.Mode)
	}
}

// TestLoadWithResult_EmptyModeDefaultsSilently pins that a config without
// the legacy mode keys AND without the capabilities section loads
// warning-free and materializes NOTHING: no capabilities entries, and no
// capabilities section in the serialized form (zero-regression round-trip —
// an untouched config must not grow a section it never had).
func TestLoadWithResult_EmptyModeDefaultsSilently(t *testing.T) {
	content := `
llm:
  default_model: anthropic/claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    legacy:
      command: cmd
`
	result, err := LoadWithResult(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("LoadErrors = %v, want none", result.LoadErrors)
	}
	if len(result.Config.Capabilities.MCP) != 0 {
		t.Errorf("capabilities.mcp = %v, want empty", result.Config.Capabilities.MCP)
	}
	if srv := result.Config.MCP.Servers["legacy"]; srv.Mode != "" {
		t.Errorf("legacy.Mode = %q, want erased", srv.Mode)
	}

	// The zero-regression round-trip: save must not invent a capabilities
	// section.
	savedPath := writeTestConfig(t, "")
	if err := Save(result.Config, savedPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if _, has := doc["capabilities"]; has {
		t.Errorf("saved config grew a capabilities section: %s", data)
	}

	// And the saved file reloads identically.
	reloaded, err := LoadWithResult(savedPath)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 || len(reloaded.Config.Capabilities.MCP) != 0 {
		t.Errorf("reload drifted: errors=%v capabilities.mcp=%v",
			reloaded.LoadErrors, reloaded.Config.Capabilities.MCP)
	}
}
