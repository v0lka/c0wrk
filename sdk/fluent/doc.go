// Package fluent provides a concise, method-chain (fluent) API for building AI
// agent systems on top of the sp4rk SDK.
//
// It is a pure façade: every method here either returns an original SDK type or
// delegates to an original SDK function. There are no shadow types or parallel
// hierarchies — you can mix fluent calls with the classic API freely at any
// point.
//
// # Recommended entry point
//
// [New] returns a [FrameworkBuilder]; every provider, tool, MCP server, and
// policy is a method on that builder, so the whole configuration reads as one
// unbroken chain terminated by [FrameworkBuilder.Build]:
//
//	fw, err := fluent.New().
//	    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
//	    FileTools().
//	    AutoApprove().
//	    Build()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer fw.Shutdown()
//
//	result, err := fluent.Run(ctx, fw).
//	    System("You are a helpful assistant.").
//	    Ask("What is the capital of France?")
//
// The whole build block carries a single fluent. prefix — no nested
// WithX(X(...)) and no package qualifier repeated per option.
//
// # Single-use pipeline
//
// For one-shot scripts that don't need to keep the [*Framework] handle,
// transition methods [FrameworkBuilder.Run] and [FrameworkBuilder.Task] build
// the framework implicitly so the entire program is a single chain:
//
//	result, err := fluent.New().
//	    Anthropic(key, model).
//	    FileTools().
//	    Task(ctx, task).
//	    System("You are a task execution agent.").
//	    Plan().
//	    Reflect().
//	    Execute()
//
// Tradeoff: the pipeline form loses the explicit *Framework handle, so there is
// no defer Shutdown. Callers needing lifecycle control use [Build] then the
// [Run] / [Task] constructors.
//
// # Layers
//
// The package is organized in layers, each building on the previous:
//
//   - Layer 1 — builder methods: provider entries ([FrameworkBuilder.Anthropic],
//     [FrameworkBuilder.OpenAI]), tool bundles ([FrameworkBuilder.FileTools]),
//     MCP servers ([FrameworkBuilder.MCPStdio]), security/execution knobs.
//   - Layer 2 — framework builder: [New] returns a [FrameworkBuilder];
//     [FrameworkBuilder.Build] terminates it and yields a [*Framework].
//   - Layer 3 — single-task executor: [Run].
//   - Layer 4 — orchestration runner: [Task] for the Plan→Execute→Reflect loop.
//
// # Escape hatches
//
// Functional options ([Option] values such as [WithProvider]) remain exported as
// a bridge via [FrameworkBuilder.Options], and a full classic [sdk.Config] can
// be supplied as the base via [FrameworkBuilder.Config]. Use these when you need
// classic-API fields not yet surfaced as dedicated builder methods.
package fluent
