package llm

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
)

// LLMError wraps provider errors with classification metadata.
type LLMError struct {
	Provider   string // e.g. "openai", "anthropic", "gemini", "lmstudio"
	StatusCode int    // HTTP status code (0 if not applicable, e.g. network error)
	Retryable  bool   // whether this error is safe to retry
	Err        error  // the original underlying error
}

// Error formats the error like "llm [provider] error (HTTP status, retryable=bool): original message".
func (e *LLMError) Error() string {
	return fmt.Sprintf("llm [%s] error (HTTP %d, retryable=%t): %v", e.Provider, e.StatusCode, e.Retryable, e.Err)
}

// Unwrap returns the original underlying error.
func (e *LLMError) Unwrap() error {
	return e.Err
}

// NewLLMError constructs an LLMError with the given fields.
func NewLLMError(provider string, statusCode int, retryable bool, err error) *LLMError {
	return &LLMError{
		Provider:   provider,
		StatusCode: statusCode,
		Retryable:  retryable,
		Err:        err,
	}
}

// IsRetryable checks whether the error chain contains a *LLMError with Retryable == true.
func IsRetryable(err error) bool {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.Retryable
	}
	return false
}

// classifyHTTPStatus returns true for retryable HTTP status codes: 429, 502, 503, 529.
func classifyHTTPStatus(code int) bool {
	switch code {
	case 429, 502, 503, 529:
		return true
	default:
		return false
	}
}

// classifyNetError returns true for transient network errors such as timeouts,
// connection refused/reset, DNS errors, and unexpected EOF.
func classifyNetError(err error) bool {
	if err == nil {
		return false
	}

	// Check for net.Error with Timeout()
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Check for syscall errors (ECONNREFUSED, ECONNRESET)
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	// Check for DNS errors
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	// Check for connection dropped (EOF)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}

// WrapProviderError creates an LLMError by classifying an error based on HTTP status code
// and underlying error type. If statusCode > 0, it uses HTTP status classification.
// Otherwise falls back to network error classification.
func WrapProviderError(provider string, statusCode int, err error) *LLMError {
	retryable := false
	if statusCode > 0 {
		retryable = classifyHTTPStatus(statusCode)
	}
	if !retryable {
		retryable = classifyNetError(err)
	}
	return NewLLMError(provider, statusCode, retryable, err)
}
