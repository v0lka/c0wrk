package fluent

import (
	"fmt"

	sdk "github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// Framework is an alias for [sdk.Framework], preserving 100% assignability with
// the original SDK type. All fluent functions that build a framework return the
// real [sdk.Framework], so you can pass it to any classic-API method unchanged.
type Framework = sdk.Framework

// FrameworkBuilder is a method-chain builder for assembling a [Framework].
// Create one with [New]; every provider, tool, MCP server, and policy is a
// method returning the same builder, so the whole configuration reads as a
// single unbroken chain terminated by [FrameworkBuilder.Build]:
//
//	fw, err := fluent.New().
//	    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
//	    FileTools().
//	    MaxSteps(15).
//	    AutoApprove().
//	    Build()
//
// Errors accumulated along the chain surface once, at [FrameworkBuilder.Build].
type FrameworkBuilder struct {
	opts options
	err  error
}

// New returns a [FrameworkBuilder] — the entry point of the fluent API. The
// returned builder is configured entirely by chaining methods; terminate the
// chain with [FrameworkBuilder.Build] to obtain a [*Framework].
//
// Conventions applied at build time:
//   - The finish tool ([agent.NewFinishTool]) is auto-registered so the agent
//     can signal task completion. Disable with [FrameworkBuilder.NoAutoFinish].
//   - The build delegates to the original [sdk.New]; the result is a real
//     [*sdk.Framework].
//
// At least one provider is required (via [FrameworkBuilder.Anthropic],
// [FrameworkBuilder.OpenAI], [FrameworkBuilder.Providers], …) or via a
// [FrameworkBuilder.Config] base that already contains providers.
//
//	fw, err := fluent.New().
//	    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
//	    FileTools().
//	    AutoApprove().
//	    Build()
func New() *FrameworkBuilder {
	return &FrameworkBuilder{opts: options{autoFinish: true}}
}

// Build terminates the builder chain and constructs the [*Framework], applying
// all accumulated configuration. It is the terminal call of [New].
//
// Returns the first error accumulated along the chain, or any error from the
// underlying [sdk.New].
func (b *FrameworkBuilder) Build() (*Framework, error) {
	return b.build()
}

// build is the shared construction path used by [FrameworkBuilder.Build] and the
// pipeline transitions ([FrameworkBuilder.Run], [FrameworkBuilder.Task]). It
// surfaces the first accumulated error, folds the options into a config (see
// [mergeConfig]), then delegates to [sdk.New] and registers tools.
func (b *FrameworkBuilder) build() (*Framework, error) {
	if b.err != nil {
		return nil, fmt.Errorf("fluent.New: %w", b.err)
	}
	o := b.opts

	cfg := mergeConfig(o)

	fw, err := sdk.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("fluent.New: %w", err)
	}

	// Auto-register accumulated tools + finish tool (convention over configuration).
	registry := fw.ToolRegistry()
	for _, t := range o.tools {
		registry.Register(t)
	}
	if o.autoFinish {
		registry.Register(agent.NewFinishTool())
	}

	return fw, nil
}

// mergeConfig folds the accumulated builder options onto an optional base
// [sdk.Config] and returns a fresh, self-contained config ready for [sdk.New].
//
// It deliberately allocates fresh slice/map storage for the merged collections
// (LLM providers and MCP servers) instead of appending into the base config's
// backing array or writing into its map. build() copies any base Config by
// value (cfg = *baseCfg), which would otherwise alias the caller's collections;
// the fresh allocation here keeps the builder a pure façade and also avoids a
// nil-map panic when the base sets MCP but leaves Servers nil.
func mergeConfig(o options) sdk.Config {
	cfg := sdk.Config{}
	if o.baseCfg != nil {
		cfg = *o.baseCfg
	}

	// Providers — fresh slice; never mutate the base backing array.
	if len(o.providers) > 0 {
		merged := make([]llm.ProviderEntry, 0, len(cfg.LLM.Providers)+len(o.providers))
		merged = append(merged, cfg.LLM.Providers...)
		merged = append(merged, o.providers...)
		cfg.LLM.Providers = merged
	}
	if o.defaultModel != "" {
		cfg.LLM.DefaultModel = o.defaultModel
	}

	// Execution
	if o.maxSteps != 0 {
		cfg.Execution.MaxSteps = o.maxSteps
	}

	// Security / hooks
	if o.confirmFunc != nil {
		cfg.ConfirmFunc = o.confirmFunc
	}
	if o.hitl != nil {
		cfg.HITL = o.hitl
	}
	if o.logger != nil {
		cfg.Logger = o.logger
	}

	// MCP — fresh map; copies existing servers, layers additions, and preserves
	// DefaultWorkDir unless overridden. Avoids both caller-map mutation and a
	// nil-map write panic when the base leaves Servers nil.
	if len(o.mcpServers) > 0 {
		merged := make(map[string]mcp.ServerEntry, len(o.mcpServers))
		workDir := o.mcpWorkDir
		if cfg.MCP != nil {
			for k, v := range cfg.MCP.Servers {
				merged[k] = v
			}
			if workDir == "" {
				workDir = cfg.MCP.DefaultWorkDir
			}
		}
		for name, entry := range o.mcpServers {
			merged[name] = entry
		}
		cfg.MCP = &sdk.MCPConfig{
			Servers:        merged,
			DefaultWorkDir: workDir,
		}
	}

	return cfg
}

// ─── Escape hatches ─────────────────────────────────────────────────────────

// Options applies functional options ([Option] values such as [WithProvider]
// or [WithTools]) onto the builder. This is the bridge for callers who already
// have option values — the dedicated method form ([FrameworkBuilder.Anthropic],
// [FrameworkBuilder.FileTools], …) is preferred for new code because it keeps
// the chain unbroken and free of the package prefix.
func (b *FrameworkBuilder) Options(opts ...Option) *FrameworkBuilder {
	for _, opt := range opts {
		opt.apply(&b.opts)
	}
	return b
}

// Config supplies a full [sdk.Config] as the base; other builder methods are
// then applied on top of it. Use this when you need classic-API fields not yet
// surfaced as dedicated builder methods.
func (b *FrameworkBuilder) Config(cfg sdk.Config) *FrameworkBuilder {
	cfgCopy := cfg
	b.opts.baseCfg = &cfgCopy
	return b
}
