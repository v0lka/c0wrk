package builtins

import "time"

// FileLimits holds configurable limits for file operation tools.
type FileLimits struct {
	ReadDefaultLines int // max lines per read call when no explicit range is given
	MaxLineBytes     int // per-line byte cap; lines exceeding this are truncated (0 = no cap)
	MaxWindowLines   int // hard cap on lines returned per call even for explicit ranges (0 = no cap)
}

// BashTimeouts holds configurable timeout values for the bash_exec tool.
type BashTimeouts struct {
	MaxTimeout time.Duration // maximum allowed timeout for bash commands
	WaitDelay  time.Duration // grace period for pipe readers after process kill
}

// DefaultBashTimeouts returns the default timeouts for bash_exec.
func DefaultBashTimeouts() BashTimeouts {
	return BashTimeouts{
		MaxTimeout: 120 * time.Second,
		WaitDelay:  5 * time.Second,
	}
}

// DefaultFileLimits returns the default limits for file operation tools.
func DefaultFileLimits() FileLimits {
	return FileLimits{
		ReadDefaultLines: 2000,
		MaxLineBytes:     1 << 20, // 1 MiB
		MaxWindowLines:   50000,
	}
}

// RipgrepLimits holds configurable limits for the ripgrep tool.
type RipgrepLimits struct {
	Timeout time.Duration // timeout for ripgrep search operations
}

// DefaultRipgrepLimits returns the default limits for ripgrep.
func DefaultRipgrepLimits() RipgrepLimits {
	return RipgrepLimits{
		Timeout: 60 * time.Second,
	}
}

// WebFetchLimits holds configurable limits for the web_fetch tool.
type WebFetchLimits struct {
	Timeout time.Duration // timeout for HTTP requests
}

// WebSearchLimits holds configurable limits for the web_search tool.
type WebSearchLimits struct {
	MaxResults int           // max number of search results
	Timeout    time.Duration // timeout for search provider HTTP requests
}

// DefaultWebSearchLimits returns the default limits for web_search.
func DefaultWebSearchLimits() WebSearchLimits {
	return WebSearchLimits{
		MaxResults: 5,
		Timeout:    30 * time.Second,
	}
}
