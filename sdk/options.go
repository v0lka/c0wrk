package sp4rk

import (
	"context"
	"log/slog"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/mcp"
)

// options holds the accumulated configuration for a [Framework] built via [NewF].
// All fields are unexported; callers configure them exclusively through [Option]
// values or the matching [FrameworkBuilder] methods.
type options struct {
	providers    []llm.ProviderEntry
	defaultModel string

	tools      []tools.Tool
	autoFinish bool // default true; WithoutAutoFinish sets false

	mcpServers map[string]mcp.ServerEntry
	mcpWorkDir string

	confirmFunc tools.ConfirmFunc
	hitl        agent.HITLHandler
	maxSteps    int

	logger *slog.Logger

	// baseCfg is an escape-hatch: a full [Config] that options are applied
	// on top of. Nil means start from a zero Config.
	baseCfg *Config
}

// Option configures how [NewF] builds the [Framework].
//
// Option uses the interface-based functional-options pattern: only this
// package can produce values implementing Option (the apply method is
// unexported), so options from other packages cannot be accidentally applied.
type Option interface {
	apply(*options)
}

// ─── Providers ──────────────────────────────────────────────────────────────

// providerOption appends a single provider.
type providerOption struct{ provider llm.ProviderEntry }

func (o providerOption) apply(opts *options) {
	opts.providers = append(opts.providers, o.provider)
}

// WithProvider adds a single LLM provider to the configuration. Repeatable to
// register multiple providers:
//
//	sp4rk.NewF(
//	    sp4rk.WithProvider(sp4rk.Anthropic(...)),
//	    sp4rk.WithProvider(sp4rk.OpenAI(...)),
//	)
func WithProvider(p llm.ProviderEntry) Option { return providerOption{provider: p} }

// providersOption sets the full provider list, replacing any previously added.
type providersOption struct{ providers []llm.ProviderEntry }

func (o providersOption) apply(opts *options) { opts.providers = o.providers }

// WithProviders sets the complete provider list in one call (replaces any
// previously added providers).
func WithProviders(ps ...llm.ProviderEntry) Option { return providersOption{providers: ps} }

// defaultModelOption overrides the auto-selected default model.
type defaultModelOption struct{ model string }

func (o defaultModelOption) apply(opts *options) { opts.defaultModel = o.model }

// WithDefaultModel overrides the auto-selected default model. Accepts a bare
// name ("claude-sonnet-4-5") or composite ID ("anthropic/claude-sonnet-4-5").
func WithDefaultModel(model string) Option { return defaultModelOption{model: model} }

// ─── Tools ──────────────────────────────────────────────────────────────────

// toolsOption appends tools to the auto-registration set.
type toolsOption struct{ tools []tools.Tool }

func (o toolsOption) apply(opts *options) { opts.tools = append(opts.tools, o.tools...) }

// WithTools adds tools that [NewF] registers automatically after building the
// [Framework]. Spread a bundle to register it:
//
//	sp4rk.WithTools(sp4rk.FileTools()...)
func WithTools(ts ...tools.Tool) Option { return toolsOption{tools: ts} }

// noAutoFinishOption disables automatic finish-tool registration.
type noAutoFinishOption struct{}

func (noAutoFinishOption) apply(opts *options) { opts.autoFinish = false }

// WithoutAutoFinish prevents [NewF] from auto-registering the finish tool.
// By default the finish tool is registered so the agent can signal completion.
func WithoutAutoFinish() Option { return noAutoFinishOption{} }

// ─── MCP ────────────────────────────────────────────────────────────────────

// mcpServerOption adds an MCP server entry.
type mcpServerOption struct {
	name  string
	entry mcp.ServerEntry
}

func (o mcpServerOption) apply(opts *options) {
	if opts.mcpServers == nil {
		opts.mcpServers = make(map[string]mcp.ServerEntry)
	}
	opts.mcpServers[o.name] = o.entry
}

// WithMCPServer registers an MCP server. Pair with [MCPStdio] or [MCPHTTP]:
//
//	name, entry := sp4rk.MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", dir)
//	sp4rk.NewF(sp4rk.WithMCPServer(name, entry), ...)
func WithMCPServer(name string, entry mcp.ServerEntry) Option {
	return mcpServerOption{name: name, entry: entry}
}

// mcpWorkDirOption sets the MCP default working directory.
type mcpWorkDirOption struct{ dir string }

func (o mcpWorkDirOption) apply(opts *options) { opts.mcpWorkDir = o.dir }

// WithMCPWorkDir sets the fallback working directory for stdio-based MCP servers.
func WithMCPWorkDir(dir string) Option { return mcpWorkDirOption{dir: dir} }

// ─── Security / HITL ────────────────────────────────────────────────────────

// confirmFuncOption sets the tool confirmation callback.
type confirmFuncOption struct{ fn tools.ConfirmFunc }

func (o confirmFuncOption) apply(opts *options) { opts.confirmFunc = o.fn }

// WithConfirmFunc sets the confirmation callback consulted before executing
// tools whose policy is PolicyUserConfirm (write_file, bash_exec, MCP tools).
// Without it, such tools are denied by the fail-closed registry.
func WithConfirmFunc(fn tools.ConfirmFunc) Option { return confirmFuncOption{fn: fn} }

// autoApproveOption installs an always-approve confirmation callback.
type autoApproveOption struct{}

func (autoApproveOption) apply(opts *options) {
	opts.confirmFunc = func(_ context.Context, _ tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
		return tools.ConfirmAllowOnce, nil
	}
}

// WithAutoApprove installs a confirmation callback that auto-approves every
// PolicyUserConfirm tool call. Convenient for throwaway/sandboxed workspaces
// where the fail-closed default would otherwise deny writes.
func WithAutoApprove() Option { return autoApproveOption{} }

// hitlOption sets the human-in-the-loop handler.
type hitlOption struct{ handler agent.HITLHandler }

func (o hitlOption) apply(opts *options) { opts.hitl = o.handler }

// WithHITL sets the human-in-the-loop handler for tool-call interception and
// step-limit decisions.
func WithHITL(h agent.HITLHandler) Option { return hitlOption{handler: h} }

// ─── Execution / misc ───────────────────────────────────────────────────────

// maxStepsOption sets the per-step ReAct loop budget.
type maxStepsOption struct{ steps int }

func (o maxStepsOption) apply(opts *options) { opts.maxSteps = o.steps }

// WithMaxSteps sets the maximum ReAct loop iterations per step. 0 keeps the
// sp4rk default (50); a negative value disables the loop.
func WithMaxSteps(n int) Option { return maxStepsOption{steps: n} }

// loggerOption sets the structured logger.
type loggerOption struct{ logger *slog.Logger }

func (o loggerOption) apply(opts *options) { opts.logger = o.logger }

// WithLogger sets the structured logger used by the Framework. Defaults to
// slog.Default() when unset.
func WithLogger(l *slog.Logger) Option { return loggerOption{logger: l} }

// ─── Escape hatch ───────────────────────────────────────────────────────────

// configOption provides a full base configuration (escape hatch).
type configOption struct{ cfg Config }

func (o configOption) apply(opts *options) {
	cfg := o.cfg
	opts.baseCfg = &cfg
}

// WithConfig supplies a full [Config] as the base; other options are then
// applied on top of it. Use this when you need classic-API fields not yet
// surfaced as dedicated options, while still benefiting from the fluent
// conveniences (provider/tool/MCP helpers, auto-finish).
func WithConfig(cfg Config) Option { return configOption{cfg: cfg} }

// Compile-time checks: every option type satisfies the Option interface.
var (
	_ Option = providerOption{}
	_ Option = providersOption{}
	_ Option = defaultModelOption{}
	_ Option = toolsOption{}
	_ Option = noAutoFinishOption{}
	_ Option = mcpServerOption{}
	_ Option = mcpWorkDirOption{}
	_ Option = confirmFuncOption{}
	_ Option = autoApproveOption{}
	_ Option = hitlOption{}
	_ Option = maxStepsOption{}
	_ Option = loggerOption{}
	_ Option = configOption{}
)

// ─── FrameworkBuilder methods ───────────────────────────────────────────────

// ConfirmFunc sets the confirmation callback consulted before executing tools
// whose policy is PolicyUserConfirm (write_file, bash_exec, MCP tools). Without
// it, such tools are denied by the fail-closed registry.
func (b *FrameworkBuilder) ConfirmFunc(fn tools.ConfirmFunc) *FrameworkBuilder {
	b.opts.confirmFunc = fn
	return b
}

// AutoApprove installs a confirmation callback that auto-approves every
// PolicyUserConfirm tool call. Convenient for throwaway/sandboxed workspaces
// where the fail-closed default would otherwise deny writes.
func (b *FrameworkBuilder) AutoApprove() *FrameworkBuilder {
	b.opts.confirmFunc = func(_ context.Context, _ tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
		return tools.ConfirmAllowOnce, nil
	}
	return b
}

// HITL sets the human-in-the-loop handler for tool-call interception and
// step-limit decisions.
func (b *FrameworkBuilder) HITL(h agent.HITLHandler) *FrameworkBuilder {
	b.opts.hitl = h
	return b
}

// MaxSteps sets the maximum ReAct loop iterations per step. 0 keeps the sp4rk
// default (50); a negative value disables the loop.
func (b *FrameworkBuilder) MaxSteps(n int) *FrameworkBuilder {
	b.opts.maxSteps = n
	return b
}

// Logger sets the structured logger used by the Framework. Defaults to
// slog.Default() when unset.
func (b *FrameworkBuilder) Logger(l *slog.Logger) *FrameworkBuilder {
	b.opts.logger = l
	return b
}

// NoAutoFinish prevents the framework from auto-registering the finish tool. By
// default the finish tool is registered so the agent can signal completion.
func (b *FrameworkBuilder) NoAutoFinish() *FrameworkBuilder {
	b.opts.autoFinish = false
	return b
}
