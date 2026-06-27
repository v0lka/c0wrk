package llm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

// mockNetError implements net.Error for testing.
type mockNetError struct{ timeout bool }

func (e *mockNetError) Error() string   { return "mock net error" }
func (e *mockNetError) Timeout() bool   { return e.timeout }
func (e *mockNetError) Temporary() bool { return false }

func TestIsRetryable_HTTPStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"429 Too Many Requests", 429, true},
		{"502 Bad Gateway", 502, true},
		{"503 Service Unavailable", 503, true},
		{"529 Overloaded", 529, true},
		{"400 Bad Request", 400, false},
		{"401 Unauthorized", 401, false},
		{"403 Forbidden", 403, false},
		{"404 Not Found", 404, false},
		{"500 Internal Server Error", 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WrapProviderError("test", tt.status, errors.New("api error"))
			got := IsRetryable(err)
			if got != tt.retryable {
				t.Errorf("IsRetryable() for HTTP %d = %v, want %v", tt.status, got, tt.retryable)
			}
		})
	}
}

func TestIsRetryable_NetErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			"timeout net.Error",
			&mockNetError{timeout: true},
		},
		{
			"connection refused",
			&net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
		},
		{
			"connection reset",
			&net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
		},
		{
			"DNS error",
			&net.DNSError{Err: "no such host", Name: "example.com"},
		},
		{
			"io.EOF",
			io.EOF,
		},
		{
			"io.ErrUnexpectedEOF",
			io.ErrUnexpectedEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WrapProviderError("test", 0, tt.err)
			if !IsRetryable(err) {
				t.Errorf("IsRetryable() for %s = false, want true", tt.name)
			}
		})
	}
}

func TestIsRetryable_PlainError(t *testing.T) {
	plain := errors.New("something went wrong")
	err := WrapProviderError("test", 0, plain)
	if IsRetryable(err) {
		t.Error("IsRetryable() for plain error = true, want false")
	}
}

func TestIsRetryable_NilError(t *testing.T) {
	if IsRetryable(nil) {
		t.Error("IsRetryable(nil) = true, want false")
	}
}

func TestIsRetryable_WrappedError(t *testing.T) {
	llmErr := NewError("openai", 429, true, errors.New("rate limited"))
	wrapped := fmt.Errorf("context: %w", llmErr)

	if !IsRetryable(wrapped) {
		t.Error("IsRetryable() for wrapped Error = false, want true")
	}
}

func TestError_ErrorString(t *testing.T) {
	err := NewError("anthropic", 529, true, errors.New("overloaded"))
	expected := "llm [anthropic] error (HTTP 529, retryable=true): overloaded"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestError_Unwrap(t *testing.T) {
	orig := errors.New("original")
	llmErr := NewError("gemini", 500, false, orig)

	if !errors.Is(llmErr.Unwrap(), orig) {
		t.Error("Unwrap() did not return the original error")
	}
	if !errors.Is(llmErr, orig) {
		t.Error("errors.Is() did not find original error through Unwrap()")
	}
}

func TestWrapProviderError(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		statusCode int
		err        error
		retryable  bool
	}{
		{
			name:       "HTTP 429 is retryable",
			provider:   "openai",
			statusCode: 429,
			err:        errors.New("rate limit"),
			retryable:  true,
		},
		{
			name:       "HTTP 500 not retryable",
			provider:   "anthropic",
			statusCode: 500,
			err:        errors.New("server error"),
			retryable:  false,
		},
		{
			name:       "network timeout with no HTTP status",
			provider:   "gemini",
			statusCode: 0,
			err:        &mockNetError{timeout: true},
			retryable:  true,
		},
		{
			name:       "plain error with no HTTP status",
			provider:   "lmstudio",
			statusCode: 0,
			err:        errors.New("unknown"),
			retryable:  false,
		},
		{
			name:       "HTTP 400 with timeout error still not retryable from HTTP but retryable from net",
			provider:   "openai",
			statusCode: 400,
			err:        &mockNetError{timeout: true},
			retryable:  true,
		},
		{
			name:       "connection refused with no HTTP status",
			provider:   "anthropic",
			statusCode: 0,
			err:        &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
			retryable:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WrapProviderError(tt.provider, tt.statusCode, tt.err)
			if result.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", result.Provider, tt.provider)
			}
			if result.StatusCode != tt.statusCode {
				t.Errorf("StatusCode = %d, want %d", result.StatusCode, tt.statusCode)
			}
			if result.Retryable != tt.retryable {
				t.Errorf("Retryable = %v, want %v", result.Retryable, tt.retryable)
			}
			if !errors.Is(result.Err, tt.err) {
				t.Errorf("Err = %v, want %v", result.Err, tt.err)
			}
		})
	}
}

func TestClassifyNetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"timeout net.Error", &mockNetError{timeout: true}, true},
		{"non-timeout net.Error", &mockNetError{timeout: false}, false},
		{"ECONNREFUSED via OpError", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, true},
		{"ECONNRESET via OpError", &net.OpError{Op: "read", Net: "tcp", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}}, true},
		{"EHOSTUNREACH via OpError", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}}, true},
		{"DNS error", &net.DNSError{Err: "no such host", Name: "example.com"}, true},
		{"plain error", errors.New("some error"), false},
		{"io.EOF", io.EOF, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyNetError(tt.err)
			if got != tt.want {
				t.Errorf("classifyNetError() = %v, want %v", got, tt.want)
			}
		})
	}
}
