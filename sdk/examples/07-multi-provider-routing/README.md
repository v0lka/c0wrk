# Example 07 — Multi-Provider Routing & Runtime Model Switching

A focused look at the SDK's **multi-provider LLM routing**: configure several providers behind a single `Router` and switch the active `(provider, model)` pair at runtime — the canonical "strong-reasoning model for planning, faster model for execution" setup.

This example ships in **two equivalent variants**:

| Variant     | File            | Command                 |
|-------------|-----------------|-------------------------|
| **Fluent**  | `main_fluent.go`| `go run -tags fluent .` |
| **Classic** | `main.go`       | `go run .`              |

Both configure Anthropic as the default and add a second OpenAI provider **only when `OPENAI_API_KEY` is set**, so the example always runs on at least one provider. See [`docs/llm-providers.md`](../../docs/llm-providers.md).

## What you will learn

- How to register multiple providers (`llm.ProviderEntry`, `sp4rk.Anthropic`/`sp4rk.OpenAI`)
- Composite vs. bare model IDs (`"openai/gpt-4o"` vs. `"gpt-4o"`, `llm.BareModel`)
- Inspecting the active selection: `router.ActiveModel()` / `router.ActiveProviderName()`
- **Manual runtime switching**: `router.SetModel(ctx, model)` before/after an `Execute`
- **Declarative phase-based switching**: `fw.TaskF(...).Models(planModel, execModel)`

## Two switching mechanisms

```
(1) Manual — drive the shared Router directly:

    router := fw.LLMRouter()
    router.SetModel(ctx, "openai/gpt-4o")   // composite routes to the provider
    fw.Execute(ctx, ...)                      // runs on the switched model
    router.SetModel(ctx, "claude-sonnet-4-5") // restore (bare name resolves)

(2) Phase-based — let TaskBuilder switch for one orchestrated run:

    fw.TaskF(ctx, task).
        Models(plannerModel, executorModel).  // exec phase uses executorModel,
        Execute()                              // then restores plannerModel.
```

The Framework owns **one shared `Router`**; every `Execute` / `RunF` / `TaskF` call resolves the active `(provider, model)` through it. `SetModel` takes a write lock; all reads take a read lock, so it is safe for concurrent use.

## Run it

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# optional second provider:
export OPENAI_API_KEY="sk-..."

cd sdk/examples/07-multi-provider-routing
go run -tags fluent .   # or: go run .
```

Expected output (single provider):

```
initial  : anthropic/claude-sonnet-4-5 (provider: anthropic)
[default] -> Static typing catches many bugs at compile time...
(set OPENAI_API_KEY to enable a second provider and runtime switching)
```

With both keys set you additionally see the `switched` / `restored` (and, in the
fluent variant, `phased`) lines as the router moves between providers.

## Contrast with the capstone

The full-power example uses phase-based switching as one of ~10 features and
gates it behind `OPENAI_API_KEY`. This example makes routing the focus, exposes
the low-level `SetModel`/`ActiveModel`/`ActiveProviderName` surface the capstone
hides, and degrades gracefully to a single provider.
