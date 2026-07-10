package sp4rk

import "github.com/v0lka/sp4rk/tools/mcp"

// MCPStdio returns an [mcp.ServerEntry] for a stdio-based MCP server (a local
// process launched via command + args). Pass the result to [WithMCPServer].
//
//	name, entry := sp4rk.MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", workDir)
func MCPStdio(name, command string, args ...string) (string, mcp.ServerEntry) {
	return name, mcp.ServerEntry{
		Transport: "stdio",
		Command:   command,
		Args:      args,
	}
}

// MCPHTTP returns an [mcp.ServerEntry] for an HTTP-based MCP server reachable
// at url. Pass the result to [WithMCPServer].
//
//	name, entry := sp4rk.MCPHTTP("remote", "https://mcp.example.com/sse")
//	fw, _ := sp4rk.NewF(sp4rk.WithMCPServer(name, entry), ...)
func MCPHTTP(name, url string) (string, mcp.ServerEntry) {
	return name, mcp.ServerEntry{
		Transport: "http",
		URL:       url,
	}
}

// ─── FrameworkBuilder methods ───────────────────────────────────────────────

// MCPStdio registers a stdio-based MCP server (a local process launched via
// command + args) under the given name, in a single chained call.
func (b *FrameworkBuilder) MCPStdio(name, command string, args ...string) *FrameworkBuilder {
	b.ensureMCPServers()
	b.opts.mcpServers[name] = mcp.ServerEntry{
		Transport: "stdio",
		Command:   command,
		Args:      args,
	}
	return b
}

// MCPHTTP registers an HTTP-based MCP server reachable at url, in a single
// chained call.
func (b *FrameworkBuilder) MCPHTTP(name, url string) *FrameworkBuilder {
	b.ensureMCPServers()
	b.opts.mcpServers[name] = mcp.ServerEntry{
		Transport: "http",
		URL:       url,
	}
	return b
}

// MCPServer registers a pre-built [mcp.ServerEntry] under the given name. Use
// this when you construct the entry by hand.
func (b *FrameworkBuilder) MCPServer(name string, entry mcp.ServerEntry) *FrameworkBuilder {
	b.ensureMCPServers()
	b.opts.mcpServers[name] = entry
	return b
}

// MCPWorkDir sets the fallback working directory for stdio-based MCP servers.
func (b *FrameworkBuilder) MCPWorkDir(dir string) *FrameworkBuilder {
	b.opts.mcpWorkDir = dir
	return b
}

// ensureMCPServers lazily allocates the MCP server map so chain methods need not
// repeat the nil check.
func (b *FrameworkBuilder) ensureMCPServers() {
	if b.opts.mcpServers == nil {
		b.opts.mcpServers = make(map[string]mcp.ServerEntry)
	}
}
