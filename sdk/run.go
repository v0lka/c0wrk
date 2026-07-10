package sp4rk

import (
	"context"
	"errors"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// errNoSystemPrompt is returned by [RunBuilder.Ask] when no system prompt was
// configured via [RunBuilder.System] or [RunBuilder.SystemFactory].
var errNoSystemPrompt = errors.New("RunF: system prompt is required — use .System(...) or .SystemFactory(...)")

// RunBuilder is a fluent builder for executing a single ReAct loop via
// [Framework.Execute]. Create one with [Framework.RunF]; terminate the chain
// with [RunBuilder.Ask].
//
// Conventions (all overridable):
//   - Events defaults to [agent.NoopEvents]; override with [RunBuilder.Events].
//   - System prompt defaults to none; a static prompt is the common case
//     ([RunBuilder.System]); a factory gives full control ([RunBuilder.SystemFactory]).
type RunBuilder struct {
	ctx        context.Context
	fw         *Framework
	events     agent.Events
	system     string
	systemFn   orchestration.SystemPromptFactory
	configured bool
	err        error // set by pipeline transition when the framework build failed
}

// RunF starts a fluent single-task execution over fw. The chain is terminated
// by [RunBuilder.Ask], which delegates to [Framework.Execute] and returns the
// original [*orchestration.ExecutionResult].
//
//	result, err := fw.RunF(ctx).
//	    System("You are a helpful assistant.").
//	    Ask("What is the capital of France?")
//
// RunF is the fluent counterpart of [Framework.Execute]: both run a single
// ReAct loop, but RunF returns a [RunBuilder] for declarative configuration.
func (fw *Framework) RunF(ctx context.Context) *RunBuilder {
	return &RunBuilder{ctx: ctx, fw: fw}
}

// Run is a pipeline transition on [FrameworkBuilder]: it builds the framework
// from the accumulated configuration and starts a [RunBuilder] in one step.
//
// Use this for single-use scripts that don't need to retain the [*Framework]
// handle (there is no defer Shutdown). If the build fails, the error is surfaced
// by [RunBuilder.Ask] instead of panicking.
//
//	sp4rk.NewF().Anthropic(key, model).
//	    FileTools().Run(ctx).
//	    System("You are helpful.").
//	    Ask("What is the capital of France?")
func (b *FrameworkBuilder) Run(ctx context.Context) *RunBuilder {
	fw, err := b.build()
	if err != nil {
		return &RunBuilder{ctx: ctx, err: err}
	}
	return fw.RunF(ctx)
}

// System sets a static system prompt. This is the common case; the string is
// wrapped in a [orchestration.SystemPromptFactory] that ignores its arguments.
func (b *RunBuilder) System(prompt string) *RunBuilder {
	b.system = prompt
	b.configured = true
	return b
}

// SystemFactory sets a [orchestration.SystemPromptFactory], giving full control
// (the factory receives the context, step description, and model metadata so it
// can adapt the prompt per model). Use this for dynamic prompts.
func (b *RunBuilder) SystemFactory(fn orchestration.SystemPromptFactory) *RunBuilder {
	b.systemFn = fn
	b.configured = true
	return b
}

// Events sets the lifecycle event sink. When omitted, [agent.NoopEvents] is used.
func (b *RunBuilder) Events(e agent.Events) *RunBuilder {
	b.events = e
	return b
}

// Ask executes a single ReAct loop for the given user message and returns the
// original [*orchestration.ExecutionResult]. It is the terminal call of the
// [RunBuilder] chain.
//
// An error is returned if no system prompt was configured.
func (b *RunBuilder) Ask(message string) (*orchestration.ExecutionResult, error) {
	if b.err != nil {
		return nil, b.err
	}
	if !b.configured {
		return nil, errNoSystemPrompt
	}

	if b.events == nil {
		b.events = &agent.NoopEvents{}
	}

	if b.systemFn == nil {
		prompt := b.system
		b.systemFn = func(_ context.Context, _ string, _ llm.ModelMetadata) string {
			return prompt
		}
	}

	return b.fw.Execute(b.ctx, b.systemFn, b.events, message)
}
