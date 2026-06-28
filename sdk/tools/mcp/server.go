// Package mcp provides MCP (Model Context Protocol) integration for the agent.
// It manages connections to external MCP servers and exposes their tools through
// the unified Tool interface.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ServerConfig defines how to launch an MCP server.
// This is a local copy to avoid importing backend/config.
type ServerConfig struct {
	Transport  string            // "stdio" | "http"; default "stdio"
	Command    string            // stdio: command to execute
	Args       []string          // stdio: command arguments
	Env        map[string]string // stdio: environment variables
	URL        string            // http: server URL
	Headers    map[string]string // http: custom headers
	WorkDir    string            // stdio: working directory for the server process
	HTTPClient *http.Client      // http: optional proxy-configured HTTP client
}

// Server represents a connection to an external MCP server process.
type Server struct {
	name          string
	client        *mcpclient.Client
	tools         []ToolInfo
	connected     bool
	lastError     string
	transportType string
	logger        *slog.Logger
	mu            sync.RWMutex
}

// ToolInfo holds metadata about a tool discovered from an MCP server.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// newServer creates a new Server instance with the given name.
func newServer(name string) *Server {
	return &Server{
		name:  name,
		tools: make([]ToolInfo, 0),
	}
}

func (s *Server) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Name returns the server's configured name.
func (s *Server) Name() string {
	return s.name
}

// Connect spawns the MCP server process and initializes the connection.
// Supports both stdio and HTTP transports based on cfg.Transport.
func (s *Server) Connect(ctx context.Context, cfg ServerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Determine transport type (default to stdio when unspecified)
	transportType := cfg.Transport
	if transportType == "" {
		transportType = "stdio"
	}

	var client *mcpclient.Client
	var err error

	switch transportType {
	case "stdio":
		client, err = s.connectStdio(ctx, cfg)
	case "http":
		client, err = s.connectHTTP(ctx, cfg)
	default:
		s.lastError = fmt.Sprintf("unsupported transport type %q", transportType)
		return fmt.Errorf("unsupported transport type %q for MCP server %s", transportType, s.name)
	}

	if err != nil {
		s.lastError = err.Error()
		return err
	}

	s.client = client
	s.transportType = transportType
	s.connected = true
	s.lastError = ""
	s.log().Debug("MCP server connected", "server", s.name, "transport", transportType)
	return nil
}

// connectStdio creates a stdio MCP client.
func (s *Server) connectStdio(ctx context.Context, cfg ServerConfig) (*mcpclient.Client, error) {
	// Build environment variables slice
	env := os.Environ()
	for key, value := range cfg.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Build stdio options (e.g., custom working directory)
	var opts []transport.StdioOption
	if cfg.WorkDir != "" {
		workDir := cfg.WorkDir // capture for closure
		opts = append(opts, transport.WithCommandFunc(
			func(cmdCtx context.Context, command string, cmdEnv []string, args []string) (*exec.Cmd, error) {
				cmd := exec.CommandContext(cmdCtx, command, args...)
				cmd.Env = cmdEnv
				cmd.Dir = workDir
				return cmd, nil
			},
		))
	}

	// Create stdio MCP client
	client, err := mcpclient.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create stdio MCP client for %s: %w", s.name, err)
	}

	if err := s.initializeClient(ctx, client); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			s.log().Debug("failed to close MCP client after connection failure", "error", closeErr)
		}
		return nil, err
	}

	return client, nil
}

// connectHTTP creates an HTTP MCP client with fallback from Streamable HTTP to SSE.
func (s *Server) connectHTTP(ctx context.Context, cfg ServerConfig) (*mcpclient.Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("http transport requires URL for MCP server %s", s.name)
	}

	// Prepare headers option
	var opts []transport.StreamableHTTPCOption
	if len(cfg.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, transport.WithHTTPBasicClient(cfg.HTTPClient))
	}

	// Try Streamable HTTP first
	client, err := mcpclient.NewStreamableHttpClient(cfg.URL, opts...)
	if err == nil {
		if initErr := s.initializeClient(ctx, client); initErr == nil {
			return client, nil
		}
		// Initialization failed, close and try SSE fallback
		if closeErr := client.Close(); closeErr != nil {
			s.log().Debug("failed to close MCP client after connection failure", "error", closeErr)
		}
	}

	// Fallback to SSE
	var sseOpts []transport.ClientOption
	if len(cfg.Headers) > 0 {
		sseOpts = append(sseOpts, transport.WithHeaders(cfg.Headers))
	}
	if cfg.HTTPClient != nil {
		sseOpts = append(sseOpts, transport.WithHTTPClient(cfg.HTTPClient))
	}

	client, err = mcpclient.NewSSEMCPClient(cfg.URL, sseOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP MCP client for %s (tried Streamable HTTP and SSE): %w", s.name, err)
	}

	if err := s.initializeClient(ctx, client); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			s.log().Debug("failed to close MCP client after connection failure", "error", closeErr)
		}
		return nil, err
	}

	return client, nil
}

// initializeClient initializes the MCP connection for the given client.
func (s *Server) initializeClient(ctx context.Context, client *mcpclient.Client) error {
	// Start the client transport
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MCP client for %s: %w", s.name, err)
	}

	// Initialize the MCP connection
	initReq := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "agent",
				Version: "1.0.0",
			},
			Capabilities: mcp.ClientCapabilities{},
		},
	}

	_, err := client.Initialize(ctx, initReq)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP server %s: %w", s.name, err)
	}

	return nil
}

// DiscoverTools calls tools/list on the MCP server and stores the discovered tools.
func (s *Server) DiscoverTools(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return fmt.Errorf("mcp server %s is not connected", s.name)
	}

	// List all tools from the MCP server
	result, err := s.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tools from MCP server %s: %w", s.name, err)
	}

	// Convert MCP tools to our internal format
	s.tools = make([]ToolInfo, 0, len(result.Tools))
	for _, tool := range result.Tools {
		// Marshal the input schema to json.RawMessage
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			// Fall back to raw schema if structured marshaling fails
			if tool.RawInputSchema != nil {
				schema = tool.RawInputSchema
			} else {
				schema = []byte(`{"type":"object"}`)
			}
		}

		s.tools = append(s.tools, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}

	toolNames := make([]string, len(s.tools))
	for i, t := range s.tools {
		toolNames[i] = t.Name
	}
	s.log().Debug("MCP tools discovered", "server", s.name, "count", len(s.tools), "tools", toolNames)

	return nil
}

// Tools returns the list of discovered tools.
func (s *Server) Tools() []ToolInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent external modification
	tools := make([]ToolInfo, len(s.tools))
	copy(tools, s.tools)
	return tools
}

// CallTool invokes a tool on the MCP server and returns the result.
func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("mcp server %s is not connected", s.name)
	}

	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: arguments,
		},
	}

	return client.CallTool(ctx, req)
}

// Close shuts down the MCP server connection.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return nil
	}

	err := s.client.Close()
	s.client = nil
	s.tools = nil
	s.connected = false
	if err != nil {
		s.lastError = err.Error()
	}
	return err
}

// IsConnected returns whether the server is currently connected.
func (s *Server) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client != nil
}

// Status returns the current status of the server.
func (s *Server) Status() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect tool names
	toolNames := make([]string, len(s.tools))
	for i, tool := range s.tools {
		toolNames[i] = tool.Name
	}

	return ServerStatus{
		Name:      s.name,
		Transport: s.transportType,
		Connected: s.connected,
		ToolCount: len(s.tools),
		Tools:     toolNames,
		Error:     s.lastError,
	}
}
