package sp4rk

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// TestBuilderReturnsSameInstance verifies that every builder setter returns the
// same *FrameworkBuilder pointer, which is what makes the chain unbroken.
func TestBuilderReturnsSameInstance(t *testing.T) {
	b := NewF()
	one := b.Anthropic("k", "m").FileTools().MaxSteps(5).AutoApprove().MCPStdio("s", "cmd")
	if one != b {
		t.Fatal("chained setters must return the same builder instance")
	}
}

func TestBuilderProviderMethods(t *testing.T) {
	b := NewF().
		Anthropic("k1", "m1").
		OpenAI("k2", "m2").
		OpenAICompatible("groq", "https://api.groq.com/openai/v1", "k3", "m3").
		DefaultModel("m1")

	if len(b.opts.providers) != 3 {
		t.Fatalf("providers len = %d, want 3", len(b.opts.providers))
	}
	wantNames := []string{"anthropic", "openai", "groq"}
	for i, want := range wantNames {
		if b.opts.providers[i].Name != want {
			t.Errorf("providers[%d].Name = %q, want %q", i, b.opts.providers[i].Name, want)
		}
	}
	if b.opts.defaultModel != "m1" {
		t.Errorf("defaultModel = %q, want m1", b.opts.defaultModel)
	}

	// Providers() appends a pre-assembled slice.
	b2 := NewF().Providers(OpenAI("k", "m"), Anthropic("k2", "m2"))
	if len(b2.opts.providers) != 2 {
		t.Errorf("Providers len = %d, want 2", len(b2.opts.providers))
	}

	// Provider() appends a single entry.
	b3 := NewF().Provider(OpenAICompatible("x", "u", "k", "m"))
	if len(b3.opts.providers) != 1 || b3.opts.providers[0].Name != "x" {
		t.Errorf("Provider = %v, want single entry named x", b3.opts.providers)
	}
}

func TestBuilderToolMethods(t *testing.T) {
	if got := len(NewF().FileTools().opts.tools); got != 6 {
		t.Errorf("FileTools = %d tools, want 6", got)
	}
	if got := len(NewF().MemoryTools().opts.tools); got != 2 {
		t.Errorf("MemoryTools = %d tools, want 2", got)
	}
	if got := len(NewF().CodeTools().opts.tools); got != 9 {
		t.Errorf("CodeTools = %d tools, want 9", got)
	}
	if got := len(NewF().AllBuiltinTools().opts.tools); got < 10 {
		t.Errorf("AllBuiltinTools = %d tools, want >= 10", got)
	}
	// Tools() appends arbitrary tools.
	if got := len(NewF().Tools(FileTools()...).opts.tools); got != 6 {
		t.Errorf("Tools(FileTools()...) = %d, want 6", got)
	}
}

// TestBuilderMCPStdioNoTuple is the headline fix: a stdio MCP server registers
// in a single chained call, with no (name, entry) tuple to unpack.
func TestBuilderMCPStdioNoTuple(t *testing.T) {
	b := NewF().MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp/ws")

	entry, ok := b.opts.mcpServers["filesystem"]
	if !ok {
		t.Fatal("expected filesystem MCP server to be registered")
	}
	if entry.Transport != "stdio" {
		t.Errorf("Transport = %q, want stdio", entry.Transport)
	}
	if entry.Command != "npx" {
		t.Errorf("Command = %q, want npx", entry.Command)
	}
	wantArgs := []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp/ws"}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("Args len = %d, want %d", len(entry.Args), len(wantArgs))
	}
	for i, a := range entry.Args {
		if a != wantArgs[i] {
			t.Errorf("Args[%d] = %q, want %q", i, a, wantArgs[i])
		}
	}
}

func TestBuilderMCPEndpoints(t *testing.T) {
	// MCPHTTP
	b := NewF().MCPHTTP("remote", "https://mcp.example.com/sse")
	entry, ok := b.opts.mcpServers["remote"]
	if !ok {
		t.Fatal("expected remote MCP server to be registered")
	}
	if entry.Transport != "http" || entry.URL != "https://mcp.example.com/sse" {
		t.Errorf("MCPHTTP entry = %+v", entry)
	}

	// MCPServer (pre-built entry) + MCPWorkDir
	custom := NewF().
		MCPServer("manual", mcp.ServerEntry{Transport: "http", URL: "https://mcp.example.com"}).
		MCPWorkDir("/srv")
	if _, ok := custom.opts.mcpServers["manual"]; !ok {
		t.Error("expected manual MCP server registered via MCPServer")
	}
	if custom.opts.mcpWorkDir != "/srv" {
		t.Errorf("mcpWorkDir = %q, want /srv", custom.opts.mcpWorkDir)
	}
}

func TestBuilderSecurityAndMisc(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	hitl := agent.NoopHITLHandler{}
	cf := func(_ context.Context, _ tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
		return tools.ConfirmAllowOnce, nil
	}

	b := NewF().
		AutoApprove().
		ConfirmFunc(cf).
		HITL(hitl).
		MaxSteps(15).
		Logger(logger).
		NoAutoFinish()

	if b.opts.confirmFunc == nil {
		t.Error("AutoApprove/ConfirmFunc should set confirmFunc")
	}
	if b.opts.hitl == nil {
		t.Error("HITL should set the hitl handler")
	}
	if b.opts.maxSteps != 15 {
		t.Errorf("maxSteps = %d, want 15", b.opts.maxSteps)
	}
	if b.opts.logger != logger {
		t.Error("Logger should store the provided logger")
	}
	if b.opts.autoFinish {
		t.Error("NoAutoFinish should set autoFinish = false")
	}
}

// TestBuilderOptionsBridge confirms the functional-options escape hatch still
// composes with the builder via .Options(...).
func TestBuilderOptionsBridge(t *testing.T) {
	b := NewF().
		Options(WithProvider(dummyProvider())).
		Options(WithMaxSteps(7))

	if len(b.opts.providers) != 1 {
		t.Errorf("Options(WithProvider) len = %d, want 1", len(b.opts.providers))
	}
	if b.opts.maxSteps != 7 {
		t.Errorf("Options(WithMaxSteps) = %d, want 7", b.opts.maxSteps)
	}

	fw, err := b.Build()
	if err != nil {
		t.Fatalf("Build via Options bridge: %v", err)
	}
	t.Cleanup(func() { _ = fw.Shutdown() })
}

// TestPipelineTaskBuildErrorSurfaces proves a failed framework build inside the
// .Task transition surfaces at .Execute rather than nil-panicking.
func TestPipelineTaskBuildErrorSurfaces(t *testing.T) {
	_, err := NewF(). // no provider → build fails
				Task(context.Background(), "do something").
				System("s").
				Execute()
	if err == nil {
		t.Fatal("expected a build error to surface at Execute (no provider)")
	}
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("error %q should mention the missing provider", err)
	}
}

// TestPipelineRunBuildErrorSurfaces is the Run-side analogue.
func TestPipelineRunBuildErrorSurfaces(t *testing.T) {
	_, err := NewF(). // no provider → build fails
				Run(context.Background()).
				System("s").
				Ask("hi")
	if err == nil {
		t.Fatal("expected a build error to surface at Ask (no provider)")
	}
}
