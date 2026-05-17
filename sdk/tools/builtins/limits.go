package builtins

import "time"

// FileLimits holds configurable limits for file operation tools.
type FileLimits struct {
	ReadDefaultLines  int // max lines per read call
	ReadMaxLineLength int // max characters per line
	ReadMaxBytes      int // total output cap in bytes
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
		ReadDefaultLines:  2000,
		ReadMaxLineLength: 2000,
		ReadMaxBytes:      51200, // 50KB
	}
}

// RipgrepLimits holds configurable limits for the ripgrep tool.
type RipgrepLimits struct {
	MaxResults    int           // max number of matches
	MaxLineLength int           // max chars per line before truncation
	Timeout       time.Duration // timeout for ripgrep search operations
}

// DefaultRipgrepLimits returns the default limits for ripgrep.
func DefaultRipgrepLimits() RipgrepLimits {
	return RipgrepLimits{
		MaxResults:    200,
		MaxLineLength: 2000,
		Timeout:       60 * time.Second,
	}
}

// GlobLimits holds configurable limits for the glob tool.
type GlobLimits struct {
	MaxResults int // max number of results
}

// DefaultGlobLimits returns the default limits for glob.
func DefaultGlobLimits() GlobLimits {
	return GlobLimits{
		MaxResults: 200,
	}
}

// WebFetchLimits holds configurable limits for the web_fetch tool.
type WebFetchLimits struct {
	MaxBodySize int           // max response body size in bytes
	Timeout     time.Duration // timeout for HTTP requests
}

// DefaultWebFetchLimits returns the default limits for web_fetch.
func DefaultWebFetchLimits() WebFetchLimits {
	return WebFetchLimits{
		MaxBodySize: 2 * 1024 * 1024, // 2MB
		Timeout:     30 * time.Second,
	}
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
