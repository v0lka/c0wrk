package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// LMStudioProviderConfig contains configuration for LM Studio provider.
type LMStudioProviderConfig struct {
	Name       string       // logical name for logging
	BaseURL    string       // default: http://localhost:1234
	APIKey     string       // optional bearer token
	HTTPClient *http.Client // optional proxy-configured HTTP client (nil = custom default)
}

// LMStudioProvider implements Provider using LM Studio's native REST API v1.
type LMStudioProvider struct {
	client  *http.Client
	baseURL string
	apiKey  string
	name    string
}

// LMStudioModel represents a model in LM Studio.
type LMStudioModel struct {
	ID           string `json:"key"`  // LM Studio uses "key" as the model identifier
	Type         string `json:"type"` // "llm", "vlm", "embeddings"
	DisplayName  string `json:"display_name"`
	Architecture string `json:"architecture"`
	MaxContext   int    `json:"max_context_length"`
}

// NewLMStudioProvider creates a new LM Studio provider.
// If BaseURL is empty, defaults to http://localhost:1234.
//
// Note: APIKey is intentionally not validated — LM Studio runs locally and
// authentication is not required by default.
func NewLMStudioProvider(cfg LMStudioProviderConfig) (*LMStudioProvider, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:1234"
	}

	// Remove trailing slash from base URL
	baseURL = strings.TrimSuffix(baseURL, "/")

	var client *http.Client
	if cfg.HTTPClient != nil {
		client = cfg.HTTPClient
	} else {
		client = &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     false,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}
	}

	return &LMStudioProvider{
		client:  client,
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
		name:    cfg.Name,
	}, nil
}

// Name returns the provider name for logging.
func (p *LMStudioProvider) Name() string {
	return p.name
}

// Internal types for LM Studio v1 API

type lmStudioRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	SystemPrompt    string          `json:"system_prompt,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream"`
	TopK            *int            `json:"top_k,omitempty"`
	MinP            *float64        `json:"min_p,omitempty"`
	RepeatPenalty   *float64        `json:"repeat_penalty,omitempty"`
}

type lmStudioResponse struct {
	Output     []lmStudioOutputItem `json:"output"`
	Stats      lmStudioStats        `json:"stats"`
	ResponseID string               `json:"response_id"`
}

type lmStudioOutputItem struct {
	Type      string          `json:"type"` // "message", "reasoning", "tool_call"
	Content   string          `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type lmStudioStats struct {
	InputTokens             int     `json:"input_tokens"`
	TotalOutputTokens       int     `json:"total_output_tokens"`
	TokensPerSecond         float64 `json:"tokens_per_second"`
	TimeToFirstTokenSeconds float64 `json:"time_to_first_token_seconds"`
}

type lmStudioContentDelta struct {
	Content string `json:"content"`
}

type lmStudioToolCallStart struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lmStudioToolCallArguments struct {
	Arguments string `json:"arguments"`
}

type lmStudioError struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type lmStudioModelLoadProgress struct {
	Progress float64 `json:"progress"`
	Model    string  `json:"model"`
}

type lmStudioPromptProcessing struct {
	Progress float64 `json:"progress"`
}

type lmStudioChatStart struct {
	Model string `json:"model"`
}

type lmStudioChatEnd struct {
	Stats lmStudioStats `json:"stats"`
}

// OpenAI-compatible types for /v1/chat/completions (used when tools are present)

type lmsOpenAIRequest struct {
	Model       string             `json:"model"`
	Messages    []lmsOpenAIMessage `json:"messages"`
	Tools       []lmsOpenAITool    `json:"tools,omitempty"`
	Stream      bool               `json:"stream"`
	Temperature *float64           `json:"temperature,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
}

type lmsOpenAIMessage struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []lmsOpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
}

type lmsOpenAITool struct {
	Type     string            `json:"type"`
	Function lmsOpenAIFunction `json:"function"`
}

type lmsOpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type lmsOpenAIToolCall struct {
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Function lmsOpenAIFunctionCall `json:"function"`
}

type lmsOpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type lmsOpenAIResponse struct {
	Choices []lmsOpenAIChoice `json:"choices"`
	Usage   lmsOpenAIUsage    `json:"usage"`
}

type lmsOpenAIChoice struct {
	Message      lmsOpenAIMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type lmsOpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Streaming types

type lmsOpenAIStreamResponse struct {
	Choices []lmsOpenAIStreamChoice `json:"choices"`
	Usage   *lmsOpenAIUsage         `json:"usage,omitempty"`
}

type lmsOpenAIStreamChoice struct {
	Delta        lmsOpenAIStreamDelta `json:"delta"`
	FinishReason *string              `json:"finish_reason"`
}

type lmsOpenAIStreamDelta struct {
	Role             string                    `json:"role,omitempty"`
	Content          string                    `json:"content,omitempty"`
	ReasoningContent string                    `json:"reasoning_content,omitempty"`
	ToolCalls        []lmsOpenAIStreamToolCall `json:"tool_calls,omitempty"`
}

type lmsOpenAIStreamToolCall struct {
	Index    *int                  `json:"index,omitempty"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function lmsOpenAIFunctionCall `json:"function,omitempty"`
}

// ChatCompletion sends a request and returns the full response.
func (p *LMStudioProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	// Route to OpenAI-compatible endpoint when tools are present
	if len(req.Tools) > 0 {
		return p.chatCompletionOpenAI(ctx, req)
	}

	lmReq, err := p.buildRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to build request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/chat", lmReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.wrapError(0, fmt.Errorf("lmstudio: request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.wrapError(resp.StatusCode, p.parseErrorResponse(resp))
	}

	var lmResp lmStudioResponse
	if err := json.NewDecoder(resp.Body).Decode(&lmResp); err != nil {
		return nil, fmt.Errorf("lmstudio: failed to decode response: %w", err)
	}

	return p.parseResponse(&lmResp)
}

// chatCompletionOpenAI sends a request using the OpenAI-compatible endpoint.
func (p *LMStudioProvider) chatCompletionOpenAI(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	oaiReq := p.buildOpenAIRequest(req, false)

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/v1/chat/completions", oaiReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create OpenAI-compat request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.wrapError(0, fmt.Errorf("lmstudio: OpenAI-compat request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.wrapError(resp.StatusCode, p.parseErrorResponse(resp))
	}

	var oaiResp lmsOpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("lmstudio: failed to decode OpenAI-compat response: %w", err)
	}

	return p.parseOpenAIResponse(&oaiResp)
}

// StreamChatCompletion sends a request and returns a channel of streaming chunks.
func (p *LMStudioProvider) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	// Route to OpenAI-compatible endpoint when tools are present
	if len(req.Tools) > 0 {
		return p.streamChatCompletionOpenAI(ctx, req)
	}

	lmReq, err := p.buildRequest(req, true)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to build request: %w", err)
	}

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/api/v1/chat", lmReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.wrapError(0, fmt.Errorf("lmstudio: request failed: %w", err))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.wrapError(resp.StatusCode, p.parseErrorResponse(resp))
	}

	chunks := make(chan ChatChunk)

	go func() {
		defer close(chunks)
		defer func() { _ = resp.Body.Close() }()

		p.processSSEStream(resp.Body, chunks)
	}()

	return chunks, nil
}

// streamChatCompletionOpenAI sends a streaming request using the OpenAI-compatible endpoint.
func (p *LMStudioProvider) streamChatCompletionOpenAI(ctx context.Context, req ChatRequest) (<-chan ChatChunk, error) {
	oaiReq := p.buildOpenAIRequest(req, true)

	httpReq, err := p.newHTTPRequest(ctx, "POST", "/v1/chat/completions", oaiReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create OpenAI-compat request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, p.wrapError(0, fmt.Errorf("lmstudio: OpenAI-compat request failed: %w", err))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.wrapError(resp.StatusCode, p.parseErrorResponse(resp))
	}

	chunks := make(chan ChatChunk)
	go func() {
		defer close(chunks)
		defer func() { _ = resp.Body.Close() }()
		p.processOpenAISSEStream(resp.Body, chunks)
	}()

	return chunks, nil
}

// processSSEStream reads SSE events from the response body and emits ChatChunks.
func (p *LMStudioProvider) processSSEStream(body io.Reader, chunks chan<- ChatChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line size

	var currentToolCall *ToolCall
	var streamUsage *TokenUsage

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse SSE format: "event: <type>" or "data: <JSON>"
		if strings.HasPrefix(line, "event: ") {
			eventType := strings.TrimPrefix(line, "event: ")

			// Read the next line which should be "data: <JSON>"
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()

			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}

			dataStr := strings.TrimPrefix(dataLine, "data: ")
			if dataStr == "" {
				continue
			}

			p.handleSSEEvent(eventType, dataStr, chunks, &currentToolCall, &streamUsage)
		} else if strings.HasPrefix(line, "data: ") {
			// Some SSE implementations skip the event line
			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "" {
				continue
			}

			p.handleSSEEvent("", dataStr, chunks, &currentToolCall, &streamUsage)
		}
	}

	if err := scanner.Err(); err != nil {
		chunks <- ChatChunk{StopReason: "error"}
	}
}

// handleSSEEvent handles individual SSE events.
func (p *LMStudioProvider) handleSSEEvent(eventType, dataStr string, chunks chan<- ChatChunk, currentToolCall **ToolCall, streamUsage **TokenUsage) {
	switch eventType {
	case "content.delta":
		var delta lmStudioContentDelta
		if err := json.Unmarshal([]byte(dataStr), &delta); err == nil && delta.Content != "" {
			chunks <- ChatChunk{Delta: delta.Content}
		}

	case "reasoning.delta":
		// Reasoning tokens — route to Reasoning field, not content
		var delta lmStudioContentDelta
		if err := json.Unmarshal([]byte(dataStr), &delta); err == nil && delta.Content != "" {
			chunks <- ChatChunk{Reasoning: delta.Content}
		}

	case "tool_call.start":
		var start lmStudioToolCallStart
		if err := json.Unmarshal([]byte(dataStr), &start); err == nil {
			*currentToolCall = &ToolCall{
				ID:    start.ID,
				Name:  start.Name,
				Input: json.RawMessage(""),
			}
		}

	case "tool_call.arguments":
		if *currentToolCall != nil {
			var args lmStudioToolCallArguments
			if err := json.Unmarshal([]byte(dataStr), &args); err == nil {
				existing := string((*currentToolCall).Input)
				(*currentToolCall).Input = json.RawMessage(existing + args.Arguments)
			}
		}

	case "tool_call.success", "tool_call.failure":
		if *currentToolCall != nil {
			chunks <- ChatChunk{ToolCall: *currentToolCall}
			*currentToolCall = nil
		}

	case "chat.end":
		var end lmStudioChatEnd
		if err := json.Unmarshal([]byte(dataStr), &end); err == nil {
			*streamUsage = &TokenUsage{
				InputTokens:  end.Stats.InputTokens,
				OutputTokens: end.Stats.TotalOutputTokens,
			}
		}
		chunks <- ChatChunk{StopReason: "end_turn", Usage: *streamUsage}

	case "error":
		var lmErr lmStudioError
		if err := json.Unmarshal([]byte(dataStr), &lmErr); err == nil {
			p.logDebug("stream error", "message", lmErr.Message, "code", lmErr.Code)
		}
		chunks <- ChatChunk{StopReason: "error"}

	case "model_load.start", "model_load.progress", "model_load.complete":
		var progress lmStudioModelLoadProgress
		if err := json.Unmarshal([]byte(dataStr), &progress); err == nil {
			p.logDebug("model load", "event", eventType, "progress", progress.Progress, "model", progress.Model)
		}

	case "prompt_processing.start", "prompt_processing.progress", "prompt_processing.complete":
		var proc lmStudioPromptProcessing
		if err := json.Unmarshal([]byte(dataStr), &proc); err == nil {
			p.logDebug("prompt processing", "event", eventType, "progress", proc.Progress)
		}

	case "chat.start":
		var start lmStudioChatStart
		if err := json.Unmarshal([]byte(dataStr), &start); err == nil {
			p.logDebug("chat started", "model", start.Model)
		}

	default:
		// Unknown event type, try to parse as generic and log
		p.logDebug("unknown SSE event", "type", eventType, "data", dataStr)
	}
}

// buildRequest converts ChatRequest to LM Studio v1 request format.
func (p *LMStudioProvider) buildRequest(req ChatRequest, stream bool) (*lmStudioRequest, error) {
	systemPrompt, filteredMsgs := ExtractSystemPrompt(req.Messages)
	var parts []string

	for _, msg := range filteredMsgs {
		switch msg.Role {
		case "user":
			parts = append(parts, msg.Content)
		case "assistant":
			if msg.Content != "" {
				parts = append(parts, "Assistant: "+msg.Content)
			}
			// Tool calls in assistant messages are context we include
			for _, tc := range msg.ToolCalls {
				parts = append(parts, "Assistant called tool "+tc.Name)
			}
		case "tool":
			parts = append(parts, "Tool result: "+msg.Content)
		}
	}

	// Build input as JSON
	var inputJSON json.RawMessage
	var err error
	switch len(parts) {
	case 1:
		// Single message: send as plain string
		inputJSON, err = json.Marshal(parts[0])
	case 0:
		inputJSON, err = json.Marshal("")
	default:
		// Multiple messages: join into single string
		inputJSON, err = json.Marshal(strings.Join(parts, "\n\n"))
	}
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input: %w", err)
	}

	lmReq := &lmStudioRequest{
		Model:           req.Model,
		Input:           inputJSON,
		SystemPrompt:    systemPrompt,
		MaxOutputTokens: req.MaxTokens,
		Stream:          stream,
		Temperature:     req.Temperature,
	}

	return lmReq, nil
}

// parseResponse converts LM Studio response to ChatResponse.
func (p *LMStudioProvider) parseResponse(lmResp *lmStudioResponse) (*ChatResponse, error) {
	message := Message{
		Role: "assistant",
	}

	var hasToolCalls bool
	var reasoning string

	// Process output items
	for _, item := range lmResp.Output {
		switch item.Type {
		case "message":
			if message.Content != "" {
				message.Content += "\n"
			}
			message.Content += item.Content

		case "reasoning":
			if reasoning != "" {
				reasoning += "\n"
			}
			reasoning += item.Content

		case "tool_call":
			hasToolCalls = true
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:    item.ID,
				Name:  item.Tool,
				Input: item.Arguments,
			})
		}
	}

	// Determine stop reason
	stopReason := "end_turn"
	if hasToolCalls {
		stopReason = "tool_use"
	}

	return &ChatResponse{
		Message:    message,
		Reasoning:  reasoning,
		StopReason: stopReason,
		Usage: TokenUsage{
			InputTokens:  lmResp.Stats.InputTokens,
			OutputTokens: lmResp.Stats.TotalOutputTokens,
		},
	}, nil
}

// buildOpenAIRequest converts ChatRequest to OpenAI-compatible format for /v1/chat/completions.
func (p *LMStudioProvider) buildOpenAIRequest(req ChatRequest, stream bool) *lmsOpenAIRequest {
	messages := make([]lmsOpenAIMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		oaiMsg := lmsOpenAIMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
		if msg.ToolCallID != "" {
			oaiMsg.ToolCallID = msg.ToolCallID
		}
		if len(msg.ToolCalls) > 0 {
			oaiMsg.ToolCalls = make([]lmsOpenAIToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				oaiMsg.ToolCalls[i] = lmsOpenAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: lmsOpenAIFunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				}
			}
		}
		messages = append(messages, oaiMsg)
	}

	oaiReq := &lmsOpenAIRequest{
		Model:       req.Model,
		Messages:    messages,
		Stream:      stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	if len(req.Tools) > 0 {
		oaiReq.Tools = make([]lmsOpenAITool, len(req.Tools))
		for i, tool := range req.Tools {
			oaiReq.Tools[i] = lmsOpenAITool{
				Type: "function",
				Function: lmsOpenAIFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.InputSchema,
				},
			}
		}
	}

	return oaiReq
}

// parseOpenAIResponse converts OpenAI-compatible response to ChatResponse.
func (p *LMStudioProvider) parseOpenAIResponse(resp *lmsOpenAIResponse) (*ChatResponse, error) {
	if len(resp.Choices) == 0 {
		return nil, WrapProviderError("lmstudio", 0, errors.New("no choices in OpenAI-compat response"))
	}

	choice := resp.Choices[0]
	message := Message{
		Role:    "assistant",
		Content: choice.Message.Content,
	}

	if len(choice.Message.ToolCalls) > 0 {
		message.ToolCalls = make([]ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			message.ToolCalls[i] = ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			}
		}
	}

	return &ChatResponse{
		Message:    message,
		Reasoning:  choice.Message.ReasoningContent,
		StopReason: MapStopReason(choice.FinishReason, openAIStopReasonMap),
		Usage: TokenUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}, nil
}

// processOpenAISSEStream reads SSE events from OpenAI-compatible endpoint and emits ChatChunks.
func (p *LMStudioProvider) processOpenAISSEStream(body io.Reader, chunks chan<- ChatChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	acc := NewStreamToolCallAccumulator()
	var streamUsage *TokenUsage

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var streamResp lmsOpenAIStreamResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			continue
		}

		if len(streamResp.Choices) == 0 {
			// Usage-only chunk (no choices) — capture usage
			if streamResp.Usage != nil {
				streamUsage = &TokenUsage{
					InputTokens:  streamResp.Usage.PromptTokens,
					OutputTokens: streamResp.Usage.CompletionTokens,
				}
			}
			continue
		}

		choice := streamResp.Choices[0]
		delta := choice.Delta

		if delta.Content != "" {
			chunks <- ChatChunk{Delta: delta.Content}
		}

		if delta.ReasoningContent != "" {
			chunks <- ChatChunk{Reasoning: delta.ReasoningContent}
		}

		for _, tc := range delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc.HandleDelta(idx, tc.ID, tc.Function.Name, tc.Function.Arguments)
		}

		if choice.FinishReason != nil {
			stopReason := MapStopReason(*choice.FinishReason, openAIStopReasonMap)
			acc.Emit(chunks)
			chunks <- ChatChunk{StopReason: stopReason, Usage: streamUsage}
		}
	}

	if err := scanner.Err(); err != nil {
		chunks <- ChatChunk{StopReason: "error"}
	}
}

// newHTTPRequest creates a new HTTP request with proper headers.
func (p *LMStudioProvider) newHTTPRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	url := p.baseURL + path
	httpReq, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	return httpReq, nil
}

// parseErrorResponse extracts error information from HTTP error responses.
func (p *LMStudioProvider) parseErrorResponse(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("lmstudio: HTTP %d (failed to read error body)", resp.StatusCode)
	}

	// Try to parse as JSON error
	var lmErr struct {
		Error lmStudioError `json:"error"`
	}
	if err := json.Unmarshal(body, &lmErr); err == nil && lmErr.Error.Message != "" {
		return fmt.Errorf("lmstudio: %s (HTTP %d)", lmErr.Error.Message, resp.StatusCode)
	}

	return fmt.Errorf("lmstudio: HTTP %d: %s", resp.StatusCode, string(body))
}

// wrapError wraps an error with the given HTTP status code into *Error.
func (p *LMStudioProvider) wrapError(statusCode int, err error) error {
	return WrapProviderError(p.name, statusCode, err)
}

// logDebug is a no-op — SDK must not log. Kept as a method to avoid changing
// all call sites in streaming event handlers where debug info is non-critical.
func (p *LMStudioProvider) logDebug(_ string, _ ...any) {}

// ListModels returns all available models from LM Studio.
func (p *LMStudioProvider) ListModels(ctx context.Context) ([]LMStudioModel, error) {
	httpReq, err := p.newHTTPRequest(ctx, "GET", "/api/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to create request: %w", err)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.parseErrorResponse(resp)
	}

	// Read raw body for debugging
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("lmstudio: failed to read response body: %w", err)
	}

	var result struct {
		Models []LMStudioModel `json:"models"`
	}
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return nil, fmt.Errorf("lmstudio: failed to decode response: %w", err)
	}

	return result.Models, nil
}


// MetadataSource returns a ModelMetadataSource that resolves model metadata
// by querying the LM Studio server's model listing endpoint.
func (p *LMStudioProvider) MetadataSource() ModelMetadataSource {
	return func(model string) (ModelMetadata, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		models, err := p.ListModels(ctx)
		if err != nil {
			return ModelMetadata{}, false
		}

		for _, m := range models {
			if m.ID == model {
				meta := ModelMetadata{
					ContextWindow: m.MaxContext,
					OutputLimit:   4096, // reasonable default
					TokenizerType: "approximate",
				}
				if meta.ContextWindow == 0 {
					return ModelMetadata{}, false
				}
				return meta, true
			}
		}
		return ModelMetadata{}, false
	}
}
