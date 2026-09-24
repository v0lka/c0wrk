package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// TestIsAutoRetryableCause pins the retryable-class taxonomy (ADR-065):
// rate-limit (429 / ErrType rate_limit) and overload (529 /
// ErrType overloaded) qualify on BOTH transports — the HTTP status code
// (openai_compatible) and the transport-independent ErrType
// (anthropic_compatible, whose SDK-parsed APIError carries no status).
// Everything else — transient-but-unclassified 5xx (500/503/504), network
// errors (StatusCode 0), empty ErrType on a non-matching status — never
// arms the auto-resend.
func TestIsAutoRetryableCause(t *testing.T) {
	tests := []struct {
		name string
		err  *llm.Error
		want bool
	}{
		{"429 rate limit", &llm.Error{StatusCode: 429, ErrType: llm.ErrTypeRateLimit}, true},
		{"529 overloaded", &llm.Error{StatusCode: 529, ErrType: llm.ErrTypeOverloaded}, true},
		{"anthropic rate_limit type, no status", &llm.Error{ErrType: llm.ErrTypeRateLimit}, true},
		{"anthropic overloaded type, no status", &llm.Error{ErrType: llm.ErrTypeOverloaded}, true},
		{"500 internal", &llm.Error{StatusCode: 500, Retryable: true}, false},
		{"503 unavailable", &llm.Error{StatusCode: 503, Retryable: true}, false},
		{"504 gateway timeout", &llm.Error{StatusCode: 504, Retryable: true}, false},
		{"401 unauthorized", &llm.Error{StatusCode: 401}, false},
		{"network error (status 0)", &llm.Error{StatusCode: 0, Retryable: true}, false},
		{"no classification", &llm.Error{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAutoRetryableCause(tt.err); got != tt.want {
				t.Errorf("isAutoRetryableCause(%+v) = %t, want %t", tt.err, got, tt.want)
			}
		})
	}
}

// TestMaybeAutoRetryAt_QualifyingCauseStampsDeadline verifies the happy
// path: a qualifying cause from a provider with a configured interval
// returns a deadline ≈ now + interval, so the banner can surface it. No
// timer is armed anywhere — the value is advisory to the UI.
func TestMaybeAutoRetryAt_QualifyingCauseStampsDeadline(t *testing.T) {
	manager, _, _ := testManager(t)
	manager.SetAutoRetryResolver(func(provider string) int {
		if provider == "selfhosted" {
			return 30
		}
		return 0
	})

	before := time.Now().Unix()
	got := manager.maybeAutoRetryAt(&llm.Error{
		Provider:   "selfhosted",
		StatusCode: 429,
		ErrType:    llm.ErrTypeRateLimit,
		Retryable:  true,
		Err:        errors.New("429 Too Many Requests"),
	})
	after := time.Now().Unix()

	if got == 0 {
		t.Fatal("expected a stamped deadline for a qualifying cause, got 0")
	}
	// The deadline is now+30s; allow second-granularity scheduling slack.
	if got < before+30 || got > after+30 {
		t.Errorf("deadline %d outside [%d, %d] (now+30s window)", got, before+30, after+30)
	}
}

// TestMaybeAutoRetryAt_NonQualifyingCausesReturnZero pins every no-arm
// branch: nil cause, a non-llm cause, a classified cause outside the
// retryable class, a qualifying cause from a provider with NO interval
// (fixed providers resolve to 0), and a nil resolver.
func TestMaybeAutoRetryAt_NonQualifyingCausesReturnZero(t *testing.T) {
	rateLimitErr := &llm.Error{
		Provider:   "selfhosted",
		StatusCode: 429,
		ErrType:    llm.ErrTypeRateLimit,
		Err:        errors.New("429 Too Many Requests"),
	}

	t.Run("nil cause", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		if got := manager.maybeAutoRetryAt(nil); got != 0 {
			t.Errorf("nil cause returned %d, want 0", got)
		}
	})

	t.Run("non-llm cause", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		if got := manager.maybeAutoRetryAt(errors.New("boom")); got != 0 {
			t.Errorf("non-llm cause returned %d, want 0", got)
		}
	})

	t.Run("wrapped llm error still classified", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		wrapped := fmt.Errorf("execution failed: %w", rateLimitErr)
		if got := manager.maybeAutoRetryAt(wrapped); got == 0 {
			t.Error("wrapped qualifying cause returned 0, want a deadline (errors.As must see through the chain)")
		}
	})

	t.Run("classified but non-retryable class (500)", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		err := &llm.Error{Provider: "selfhosted", StatusCode: 500, Retryable: true}
		if got := manager.maybeAutoRetryAt(err); got != 0 {
			t.Errorf("500 cause returned %d, want 0", got)
		}
	})

	t.Run("qualifying cause, provider without interval", func(t *testing.T) {
		manager, _, _ := testManager(t)
		// Fixed providers (anthropic, chatgpt) resolve to 0: no auto-resend.
		manager.SetAutoRetryResolver(func(string) int { return 0 })
		if got := manager.maybeAutoRetryAt(rateLimitErr); got != 0 {
			t.Errorf("zero interval returned %d, want 0", got)
		}
	})

	t.Run("no resolver wired", func(t *testing.T) {
		manager, _, _ := testManager(t)
		if got := manager.maybeAutoRetryAt(rateLimitErr); got != 0 {
			t.Errorf("nil resolver returned %d, want 0", got)
		}
	})
}

// TestEmitResumableIfUnfinished_StampsAutoRetryAt verifies the payload
// wiring: emitResumableIfUnfinishd forwards the terminal cause, and a
// qualifying failure surfaces auto_retry_at in the task_failed_resumable
// payload; a non-qualifying failure emits the same banner WITHOUT the
// deadline (manual resume only).
func TestEmitResumableIfUnfinished_StampsAutoRetryAt(t *testing.T) {
	newManager := func(t *testing.T) (*Manager, <-chan Event) {
		manager, eventChan, _ := testManager(t)
		manager.SetTaskStore(&mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-123", SessionID: "sess-1", Status: "failed"},
		})
		manager.SetAutoRetryResolver(func(provider string) int {
			if provider == "selfhosted" {
				return 5
			}
			return 0
		})
		return manager, eventChan
	}

	t.Run("qualifying cause stamps auto_retry_at", func(t *testing.T) {
		manager, eventChan := newManager(t)
		before := time.Now().Unix()
		manager.emitResumableIfUnfinished("sess-1", "Rate limited.", &llm.Error{
			Provider:   "selfhosted",
			StatusCode: 429,
			ErrType:    llm.ErrTypeRateLimit,
		})
		select {
		case event := <-eventChan:
			if event.Type != "task_failed_resumable" {
				t.Fatalf("expected task_failed_resumable, got %s", event.Type)
			}
			data, ok := event.Data.(TaskFailedResumableData)
			if !ok {
				t.Fatalf("expected TaskFailedResumableData, got %T", event.Data)
			}
			if data.AutoRetryAt < before+5 || data.AutoRetryAt > time.Now().Unix()+5 {
				t.Errorf("auto_retry_at %d outside now+5s window", data.AutoRetryAt)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for task_failed_resumable event")
		}
	})

	t.Run("non-qualifying cause emits banner without deadline", func(t *testing.T) {
		manager, eventChan := newManager(t)
		manager.emitResumableIfUnfinished("sess-1", "Boom.", errors.New("plain failure"))
		select {
		case event := <-eventChan:
			data, ok := event.Data.(TaskFailedResumableData)
			if !ok {
				t.Fatalf("expected TaskFailedResumableData, got %T", event.Data)
			}
			if data.AutoRetryAt != 0 {
				t.Errorf("non-qualifying cause stamped auto_retry_at %d, want 0", data.AutoRetryAt)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for task_failed_resumable event")
		}
	})
}
