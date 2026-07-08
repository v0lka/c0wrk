package agent

import (
	"context"
	"io"
)

type stepIDKey struct{}

// WithStepID returns a new context with the step ID attached.
func WithStepID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, stepIDKey{}, id)
}

// StepIDFromContext extracts the step ID from the context.
// Returns empty string if not found.
func StepIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(stepIDKey{}).(string); ok {
		return id
	}
	return ""
}

// dumpWriterContextKey is a context key for injecting an LLM dump io.Writer
// into cross-cutting LLM call sites (title generation, ToolJudge) that
// don't pass through the per-step CallerForStep pipeline.
type dumpWriterContextKey struct{}

// WithDumpWriter returns a new context with the dump writer attached.
func WithDumpWriter(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, dumpWriterContextKey{}, w)
}

// DumpWriterFromContext extracts the dump writer from the context.
// Returns nil if not found.
func DumpWriterFromContext(ctx context.Context) io.Writer {
	if w, ok := ctx.Value(dumpWriterContextKey{}).(io.Writer); ok {
		return w
	}
	return nil
}
