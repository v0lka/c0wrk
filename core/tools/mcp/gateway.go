// Package mcp provides Model Context Protocol server integration and tool proxying.
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/user/agent/core/tools"
)

// Gateway manages connections to multiple MCP servers and provides
// their tools to the agent through the ToolRegistry.
type Gateway struct {
	servers map[string]*Server
	mu      sync.RWMutex
}

// NewGateway creates a new Gateway instance.
func NewGateway() *Gateway {
	return &Gateway{
		servers: make(map[string]*Server),
	}
}

// Start connects to all configured MCP servers and discovers their tools.
// It returns an error if any server fails to connect, but continues
// connecting to remaining servers.
func (g *Gateway) Start(ctx context.Context, configs map[string]ServerConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var errs []error

	for name, cfg := range configs {
		server := NewServer(name)

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
		return &StartError{Errors: errs}
	}

	return nil
}

// RegisterTools registers all discovered MCP tools into the ToolRegistry.
// Each tool is wrapped as a Tool that implements the tools.Tool interface.
func (g *Gateway) RegisterTools(registry *tools.ToolRegistry) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for _, server := range g.servers {
		for _, toolInfo := range server.Tools() {
			mcpTool := NewTool(server, toolInfo)
			registry.RegisterWithSource(mcpTool, "mcp")
		}
	}

	return nil
}

// Stop gracefully shuts down all MCP server connections.
func (g *Gateway) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var errs []error

	for name, server := range g.servers {
		if err := server.Close(); err != nil {
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
		}
	}

	g.servers = make(map[string]*Server)

	if len(errs) > 0 {
		return &StopError{Errors: errs}
	}

	return nil
}

// GetServer returns a specific MCP server by name, or nil if not found.
func (g *Gateway) GetServer(name string) *Server {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.servers[name]
}

// ServerNames returns a list of all connected server names.
func (g *Gateway) ServerNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	names := make([]string, 0, len(g.servers))
	for name := range g.servers {
		names = append(names, name)
	}
	return names
}

// ToolCount returns the total number of tools across all connected servers.
func (g *Gateway) ToolCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	count := 0
	for _, server := range g.servers {
		count += len(server.Tools())
	}
	return count
}

// StartError represents errors that occurred during gateway startup.
type StartError struct {
	Errors []error
}

func (e *StartError) Error() string {
	if len(e.Errors) == 1 {
		return fmt.Sprintf("MCP gateway start error: %v", e.Errors[0])
	}
	return fmt.Sprintf("MCP gateway start errors: %d servers failed to connect", len(e.Errors))
}

// StopError represents errors that occurred during gateway shutdown.
type StopError struct {
	Errors []error
}

func (e *StopError) Error() string {
	if len(e.Errors) == 1 {
		return fmt.Sprintf("MCP gateway stop error: %v", e.Errors[0])
	}
	return fmt.Sprintf("MCP gateway stop errors: %d servers failed to stop cleanly", len(e.Errors))
}

// GatewayConfig holds the raw MCP server entries for gateway initialization.
type GatewayConfig struct {
	Servers map[string]ServerEntry
}

// ServerEntry describes how to launch a single MCP server.
type ServerEntry struct {
	Command string
	Args    []string
	Env     map[string]string // values may contain ${ENV_VAR} references
}

// StartGateway creates, configures, starts, and registers an Gateway.
// expandEnv resolves ${VAR} references in environment values.
// Returns (nil, nil) if no servers are configured.
func StartGateway(ctx context.Context, cfg GatewayConfig, registry *tools.ToolRegistry, expandEnv func(string) string, logger *slog.Logger) (*Gateway, error) {
	if len(cfg.Servers) == 0 {
		return nil, nil
	}

	mcpConfigs := make(map[string]ServerConfig, len(cfg.Servers))
	for name, entry := range cfg.Servers {
		env := make(map[string]string, len(entry.Env))
		for ek, ev := range entry.Env {
			env[ek] = expandEnv(ev)
		}
		mcpConfigs[name] = ServerConfig{
			Command: entry.Command,
			Args:    entry.Args,
			Env:     env,
		}
	}

	gateway := NewGateway()
	if err := gateway.Start(ctx, mcpConfigs); err != nil {
		if logger != nil {
			logger.Warn("MCP gateway start errors", "error", err)
		}
	}

	if err := gateway.RegisterTools(registry); err != nil {
		if logger != nil {
			logger.Warn("MCP tool registration errors", "error", err)
		}
	}

	return gateway, nil
}
