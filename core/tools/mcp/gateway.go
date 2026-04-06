// Package mcp provides Model Context Protocol server integration and tool proxying.
package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/user/agent/core/tools"
)

// MCPGateway manages connections to multiple MCP servers and provides
// their tools to the agent through the ToolRegistry.
type MCPGateway struct {
	servers map[string]*MCPServer
	mu      sync.RWMutex
}

// NewMCPGateway creates a new MCPGateway instance.
func NewMCPGateway() *MCPGateway {
	return &MCPGateway{
		servers: make(map[string]*MCPServer),
	}
}

// Start connects to all configured MCP servers and discovers their tools.
// It returns an error if any server fails to connect, but continues
// connecting to remaining servers.
func (g *MCPGateway) Start(ctx context.Context, configs map[string]MCPServerConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var errs []error

	for name, cfg := range configs {
		server := NewMCPServer(name)

		if err := server.Connect(ctx, cfg); err != nil {
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
			continue
		}

		if err := server.DiscoverTools(ctx); err != nil {
			if closeErr := server.Close(); closeErr != nil {
				errs = append(errs, fmt.Errorf("server %s: close after discovery failure: %w", name, closeErr))
			}
			errs = append(errs, fmt.Errorf("server %s: failed to discover tools: %w", name, err))
			continue
		}

		g.servers[name] = server
	}

	if len(errs) > 0 {
		return &MCPStartError{Errors: errs}
	}

	return nil
}

// RegisterTools registers all discovered MCP tools into the ToolRegistry.
// Each tool is wrapped as an MCPTool that implements the Tool interface.
func (g *MCPGateway) RegisterTools(registry *tools.ToolRegistry) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, server := range g.servers {
		for _, toolInfo := range server.Tools() {
			mcpTool := NewMCPTool(server, toolInfo)
			registry.RegisterWithSource(mcpTool, "mcp")
		}
	}

	return nil
}

// Stop gracefully shuts down all MCP server connections.
func (g *MCPGateway) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var errs []error

	for name, server := range g.servers {
		if err := server.Close(); err != nil {
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
	}

	g.servers = make(map[string]*MCPServer)

	if len(errs) > 0 {
		return &MCPStopError{Errors: errs}
	}

	return nil
}

// GetServer returns a specific MCP server by name, or nil if not found.
func (g *MCPGateway) GetServer(name string) *MCPServer {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.servers[name]
}

// ServerNames returns a list of all connected server names.
func (g *MCPGateway) ServerNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	names := make([]string, 0, len(g.servers))
	for name := range g.servers {
		names = append(names, name)
	}
	return names
}

// ToolCount returns the total number of tools across all connected servers.
func (g *MCPGateway) ToolCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	count := 0
	for _, server := range g.servers {
		count += len(server.Tools())
	}
	return count
}

// MCPStartError represents errors that occurred during gateway startup.
type MCPStartError struct {
	Errors []error
}

func (e *MCPStartError) Error() string {
	if len(e.Errors) == 1 {
		return fmt.Sprintf("MCP gateway start error: %v", e.Errors[0])
	}
	return fmt.Sprintf("MCP gateway start errors: %d servers failed to connect", len(e.Errors))
}

// MCPStopError represents errors that occurred during gateway shutdown.
type MCPStopError struct {
	Errors []error
}

func (e *MCPStopError) Error() string {
	if len(e.Errors) == 1 {
		return fmt.Sprintf("MCP gateway stop error: %v", e.Errors[0])
	}
	return fmt.Sprintf("MCP gateway stop errors: %d servers failed to stop cleanly", len(e.Errors))
}
