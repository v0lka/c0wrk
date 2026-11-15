package session

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/llmbudget"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

// TestIsAutoRetryableCause pins the retryable-class taxonomy (ADR-065 +
// ADR-126): rate-limit (429 / ErrKind rate_limit) and overload (529 /
// ErrKind overloaded) qualify on BOTH transports — the HTTP status code
// (openai_compatible) and the transport-independent ErrKind
// (anthropic_compatible, whose SDK-parsed APIError carries no status); and
// (ADR-126) a StatusCode-0 failure whose wrapped chain carries one of the
// four transient connection classes — dns, connect (refused/unreachable),
// reset, timeout — qualifies for the network class. Everything else —
// transient-but-unclassified 5xx (500/503/504), the excluded TransportKind
// labels (tls/eof/other), the llmbudget budget-death wrapper, empty ErrKind
// on a non-matching status — never arms the auto-resend. Real error chains
// are used for the network rows so the sp4rk TransportKind classification is
// exercised, not restated.
func TestIsAutoRetryableCause(t *testing.T) {
	budgetDeath := fmt.Errorf(
		"adaptive request budget exhausted: %s armed for %q (own budget expiry, not a provider abort; the next budget escalates): %w",
		8*time.Minute, "gpt-test", context.DeadlineExceeded,
	)
	tests := []struct {
		name string
		err  *llm.Error
		want bool
	}{
		{"429 rate limit", &llm.Error{StatusCode: 429, ErrKind: llm.ErrKindRateLimit}, true},
		{"529 overloaded", &llm.Error{StatusCode: 529, ErrKind: llm.ErrKindOverloaded}, true},
		{"anthropic rate_limit type, no status", &llm.Error{ErrKind: llm.ErrKindRateLimit}, true},
		{"anthropic overloaded type, no status", &llm.Error{ErrKind: llm.ErrKindOverloaded}, true},
		{"network dns", &llm.Error{StatusCode: 0, Err: &net.DNSError{Err: "no such host", Name: "gateway.lan", IsNotFound: true}}, true},
		{"network connect refused", &llm.Error{StatusCode: 0, Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, true},
		{"network connect unreachable", &llm.Error{StatusCode: 0, Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EHOSTUNREACH}}, true},
		{"network reset", &llm.Error{StatusCode: 0, Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}}, true},
		{"network timeout (deadline exceeded)", &llm.Error{StatusCode: 0, Err: context.DeadlineExceeded}, true},
		{"network provider-side timeout (url-wrapped)", &llm.Error{StatusCode: 0, Err: &url.Error{Op: "Post", URL: "http://gw.lan/v1/chat/completions", Err: context.DeadlineExceeded}}, true},
		{"network tls stays out", &llm.Error{StatusCode: 0, Err: &tls.CertificateVerificationError{}}, false},
		{"network eof stays out", &llm.Error{StatusCode: 0, Err: io.EOF}, false},
		{"network unexpected eof stays out", &llm.Error{StatusCode: 0, Err: io.ErrUnexpectedEOF}, false},
		{"network other stays out", &llm.Error{StatusCode: 0, Err: errors.New("something inexplicable")}, false},
		{"budget death never arms (deadline-chained wrapper)", &llm.Error{StatusCode: 0, Err: budgetDeath}, false},
		{"500 internal", &llm.Error{StatusCode: 500, Retryable: true}, false},
		{"503 unavailable", &llm.Error{StatusCode: 503, Retryable: true}, false},
		{"504 gateway timeout", &llm.Error{StatusCode: 504, Retryable: true}, false},
		{"401 unauthorized", &llm.Error{StatusCode: 401}, false},
		{"network error with no classifiable chain", &llm.Error{StatusCode: 0, Retryable: true}, false},
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
		ErrKind:    llm.ErrKindRateLimit,
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
		ErrKind:    llm.ErrKindRateLimit,
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
			ErrKind:    llm.ErrKindRateLimit,
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

// TestEmitTaskComplete_DegradedCauseStampsAutoRetryAt pins the review fix
// (ADR-065 follow-up): a DEGRADED completion — the orchestrator returned a
// best-effort result with a nil error (the goal loop's errored-turn halt) —
// carries its typed cause on HandleResult.Err, and emitTaskComplete forwards
// it to emitResumableIfUnfinished so a rate-limit class failure still arms
// the auto-resend countdown on the very path the feature was built for: the
// long rate-limit storm that outlives the goal loop's bounded turn retries.
func TestEmitTaskComplete_DegradedCauseStampsAutoRetryAt(t *testing.T) {
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

	before := time.Now().Unix()
	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Status: orchestration.ExecutionStatusFailed,
		Err: &llm.Error{
			Provider:   "selfhosted",
			StatusCode: 429,
			ErrKind:    llm.ErrKindRateLimit,
		},
	}, nil)

	// task_complete fires first; the resumable banner follows.
	deadline := int64(0)
	sawComplete := false
	for deadline == 0 || !sawComplete {
		select {
		case event := <-eventChan:
			switch event.Type {
			case "task_complete":
				sawComplete = true
			case "task_failed_resumable":
				data, ok := event.Data.(TaskFailedResumableData)
				if !ok {
					t.Fatalf("expected TaskFailedResumableData, got %T", event.Data)
				}
				deadline = data.AutoRetryAt
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for terminal events")
		}
	}
	if deadline < before+5 || deadline > time.Now().Unix()+5 {
		t.Errorf("auto_retry_at %d outside now+5s window (want the degraded cause to arm the countdown)", deadline)
	}
}

// TestMaybeAutoRetryAt_NetworkClassUsesGlobalInterval pins the ADR-126 knob
// split: a connection-class failure rides the GLOBAL network interval on
// EVERY provider — the fixed providers included, whose rate-limit answer is
// always 0 — and the network kill-switch (0) silences the class without
// touching the rate-limit knob.
func TestMaybeAutoRetryAt_NetworkClassUsesGlobalInterval(t *testing.T) {
	connectErr := &llm.Error{
		Provider:   "chatgpt",
		StatusCode: 0,
		Err:        &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}

	t.Run("fixed provider arms via the global interval", func(t *testing.T) {
		manager, _, _ := testManager(t)
		// The rate-limit resolver answers 0 for everything (fixed provider).
		manager.SetAutoRetryResolver(func(string) int { return 0 })
		manager.SetNetworkAutoRetryResolver(func() int { return 90 })

		before := time.Now().Unix()
		got := manager.maybeAutoRetryAt(connectErr)
		after := time.Now().Unix()
		if got == 0 {
			t.Fatal("expected a stamped deadline for a network-class failure on a fixed provider, got 0")
		}
		if got < before+90 || got > after+90 {
			t.Errorf("deadline %d outside [%d, %d] (now+90s window — the GLOBAL interval, not a provider answer)", got, before+90, after+90)
		}
	})

	t.Run("network kill-switch silences the class", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		manager.SetNetworkAutoRetryResolver(func() int { return 0 })
		if got := manager.maybeAutoRetryAt(connectErr); got != 0 {
			t.Errorf("network kill-switch stamped %d, want 0", got)
		}
	})

	t.Run("no network resolver wired", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		if got := manager.maybeAutoRetryAt(connectErr); got != 0 {
			t.Errorf("nil network resolver stamped %d, want 0", got)
		}
	})
}

// TestMaybeAutoRetryAt_RateLimitSemanticsUnchanged pins that the rate-limit
// class keeps its per-provider knob EXACTLY as ADR-065 defined it (ADR-126's
// explicit invariant): the global network interval never multiplexes into it,
// in either direction.
func TestMaybeAutoRetryAt_RateLimitSemanticsUnchanged(t *testing.T) {
	rateLimitErr := &llm.Error{
		Provider:   "selfhosted",
		StatusCode: 429,
		ErrKind:    llm.ErrKindRateLimit,
		Err:        errors.New("429 Too Many Requests"),
	}

	t.Run("uses the per-provider interval, not the global one", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(provider string) int {
			if provider == "selfhosted" {
				return 30
			}
			return 0
		})
		manager.SetNetworkAutoRetryResolver(func() int { return 90 })

		before := time.Now().Unix()
		got := manager.maybeAutoRetryAt(rateLimitErr)
		after := time.Now().Unix()
		if got == 0 {
			t.Fatal("expected a stamped deadline for the rate-limit class, got 0")
		}
		if got < before+30 || got > after+30 {
			t.Errorf("deadline %d outside [%d, %d] (now+30s — the per-provider knob must stay in charge)", got, before+30, after+30)
		}
	})

	t.Run("provider interval 0 stays no-deadline even with a global network interval", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 0 })
		manager.SetNetworkAutoRetryResolver(func() int { return 90 })
		if got := manager.maybeAutoRetryAt(rateLimitErr); got != 0 {
			t.Errorf("rate-limit class resolved %d from the network knob, want 0", got)
		}
	})
}

// stubRoundTripper returns a fixed error from every RoundTrip — the minimal
// base for driving the llmbudget transport's own-expiry path.
type stubRoundTripper struct{ err error }

func (s *stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, s.err
}

// realBudgetDeathError drives the REAL llmbudget transport into its
// own-budget-expiry path (a stub base dying of context.DeadlineExceeded under
// a live armed budget) and returns the exact error the session layer sees.
// The test fails loudly if the transport's wrapper signature drifts — that
// drift is precisely what would silently re-admit budget deaths to the
// network class (ADR-126's "one seam" coupling).
func realBudgetDeathError(t *testing.T) error {
	t.Helper()
	tr := llmbudget.NewTransport(&stubRoundTripper{err: context.DeadlineExceeded}, llmbudget.TransportOptions{
		Wire:            llmbudget.WireJSONModel,
		Class:           llmbudget.ClassLocal,
		AdaptiveEnabled: true,
	})
	body := strings.NewReader(`{"model":"budget-death-test","messages":[]}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1:1/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, berr := tr.RoundTrip(req)
	if berr == nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatal("expected the budget-exhaustion error from the transport")
	}
	if !errors.Is(berr, context.DeadlineExceeded) {
		t.Fatalf("budget error must chain context.DeadlineExceeded; got %v", berr)
	}
	if !strings.Contains(berr.Error(), "adaptive request budget exhausted") {
		t.Fatalf("the llmbudget transport no longer emits the own-budget-expiry signature the exclusion keys on; got %q", berr.Error())
	}
	return berr
}

// TestMaybeAutoRetryAt_RealBudgetDeathNeverArms pins the ADR-126 budget-death
// carve-out against the REAL llmbudget transport error (not a restatement of
// its format string): the wrapper chains a context.DeadlineExceeded that
// would satisfy the timeout class, but a budget death must never arm a
// deadline — ADR-071's ×2 escalation is its cure. The provider-side timeout
// twin (the same DeadlineExceeded without the wrapper) must still arm, in the
// same fixture, proving the exclusion is signature-specific and not a blanket
// timeout veto.
func TestMaybeAutoRetryAt_RealBudgetDeathNeverArms(t *testing.T) {
	newManager := func(t *testing.T) *Manager {
		manager, _, _ := testManager(t)
		manager.SetAutoRetryResolver(func(string) int { return 30 })
		manager.SetNetworkAutoRetryResolver(func() int { return 90 })
		return manager
	}

	t.Run("real llmbudget own-expiry never arms", func(t *testing.T) {
		manager := newManager(t)
		cause := &llm.Error{Provider: "selfhosted", StatusCode: 0, Err: realBudgetDeathError(t)}
		if isAutoRetryableCause(cause) {
			t.Error("budget death classified auto-retryable; the ADR-071 escalation must stay the only cure")
		}
		if got := manager.maybeAutoRetryAt(cause); got != 0 {
			t.Errorf("budget death stamped auto_retry_at %d, want 0", got)
		}
	})

	t.Run("provider-side timeout twin still arms", func(t *testing.T) {
		manager := newManager(t)
		cause := &llm.Error{
			Provider:   "selfhosted",
			StatusCode: 0,
			Err:        &url.Error{Op: "Post", URL: "http://gw.lan/v1/chat/completions", Err: context.DeadlineExceeded},
		}
		if !isAutoRetryableCause(cause) {
			t.Fatal("provider-side timeout must stay in the network class")
		}
		if got := manager.maybeAutoRetryAt(cause); got == 0 {
			t.Error("provider-side timeout returned no deadline; want the global network interval")
		}
	})
}
