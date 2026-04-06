// Package mcp provides MCP (Model Context Protocol) integration for the agent.
// It manages connections to external MCP servers and exposes their tools through
// the unified Tool interface.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPServerConfig defines how to launch an MCP server.
// This is a local copy to avoid importing backend/config.
type MCPServerConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}

// MCPServer represents a connection to an external MCP server process.
type MCPServer struct {
	name   string
	client *mcpclient.Client
	tools  []MCPToolInfo
	mu     sync.RWMutex
}

// MCPToolInfo holds metadata about a tool discovered from an MCP server.
type MCPToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// NewMCPServer creates a new MCPServer instance with the given name.
func NewMCPServer(name string) *MCPServer {
	return &MCPServer{
		name:  name,
		tools: make([]MCPToolInfo, 0),
	}
}

// Name returns the server's configured name.
func (s *MCPServer) Name() string {
	return s.name
}

// Connect spawns the MCP server process and initializes the connection.
func (s *MCPServer) Connect(ctx context.Context, cfg MCPServerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Build environment variables slice
	env := os.Environ()
	for key, value := range cfg.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}

	// Create stdio MCP client
	client, err := mcpclient.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
	if err != nil {
		return fmt.Errorf("failed to create MCP client for %s: %w", s.name, err)
	}
	s.client = client

	// Start the client transport
	if err := s.client.Start(ctx); err != nil {
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

	_, err = s.client.Initialize(ctx, initReq)
	if err != nil {
		_ = s.client.Close()
		return fmt.Errorf("failed to initialize MCP server %s: %w", s.name, err)
	}

	return nil
}

// DiscoverTools calls tools/list on the MCP server and stores the discovered tools.
func (s *MCPServer) DiscoverTools(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return fmt.Errorf("MCP server %s is not connected", s.name)
	}

	// List all tools from the MCP server
	result, err := s.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("failed to list tools from MCP server %s: %w", s.name, err)
	}

	// Convert MCP tools to our internal format
	s.tools = make([]MCPToolInfo, 0, len(result.Tools))
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

		s.tools = append(s.tools, MCPToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}

	return nil
}

// Tools returns the list of discovered tools.
func (s *MCPServer) Tools() []MCPToolInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent external modification
	tools := make([]MCPToolInfo, len(s.tools))
	copy(tools, s.tools)
	return tools
}

// CallTool invokes a tool on the MCP server and returns the result.
func (s *MCPServer) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	s.mu.RLock()
	client := s.client
	s.mu.RUnlock()

	if client == nil {
		return nil, fmt.Errorf("MCP server %s is not connected", s.name)
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
func (s *MCPServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return nil
	}

	err := s.client.Close()
	s.client = nil
	s.tools = nil
	return err
}

// IsConnected returns whether the server is currently connected.
func (s *MCPServer) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client != nil
}
