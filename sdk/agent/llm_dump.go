package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/v0lka/sp4rk/llm"
)

// dumpEntry is a single JSONL record written by dumpCaller.
type dumpEntry struct {
	Timestamp string          `json:"ts"`
	Direction string          `json:"direction"`
	Data      json.RawMessage `json:"data"`
	Error     string          `json:"error,omitempty"`
}

// dumpCaller wraps an LLMCaller and writes full, untruncated JSON dumps of
// every LLM request and response to an io.Writer in JSONL format.
type dumpCaller struct {
	inner  LLMCaller
	mu     sync.Mutex
	enc    *json.Encoder
	logger *slog.Logger
}

// NewDumpCaller decorates inner so that every Call writes the full ChatRequest
// and ChatResponse as JSONL entries to w. If w is nil, returns inner unchanged.
func NewDumpCaller(inner LLMCaller, w io.Writer, logger *slog.Logger) LLMCaller {
	if w == nil {
		return inner
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &dumpCaller{
		inner:  inner,
		enc:    json.NewEncoder(w),
		logger: logger,
	}
}

// Call delegates to the wrapped LLMCaller, writing request and response entries.
func (d *dumpCaller) Call(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		d.logger.Debug("llm dump: failed to marshal request", "error", err)
	}

	d.mu.Lock()
	if err := d.enc.Encode(dumpEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Direction: "request",
		Data:      reqJSON,
	}); err != nil {
		d.logger.Debug("llm dump encode failed", "error", err)
	}
	d.mu.Unlock()

	resp, err := d.inner.Call(ctx, req)

	var respJSON json.RawMessage
	if resp != nil {
		var marshalErr error
		respJSON, marshalErr = json.Marshal(resp)
		if marshalErr != nil {
			d.logger.Debug("llm dump: failed to marshal response", "error", marshalErr)
		}
	} else {
		respJSON = json.RawMessage("null")
	}

	entry := dumpEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Direction: "response",
		Data:      respJSON,
	}
	if err != nil {
		entry.Error = err.Error()
	}

	d.mu.Lock()
	if err := d.enc.Encode(entry); err != nil {
		d.logger.Debug("llm dump encode failed", "error", err)
	}
	d.mu.Unlock()

	return resp, err
}
