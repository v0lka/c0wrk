package sp4rk

import (
	"testing"
)

func TestMCPStdio(t *testing.T) {
	name, entry := MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp/ws")

	if name != "filesystem" {
		t.Errorf("name = %q, want %q", name, "filesystem")
	}
	if entry.Transport != "stdio" {
		t.Errorf("Transport = %q, want %q", entry.Transport, "stdio")
	}
	if entry.Command != "npx" {
		t.Errorf("Command = %q, want %q", entry.Command, "npx")
	}
	wantArgs := []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp/ws"}
	if len(entry.Args) != len(wantArgs) {
		t.Fatalf("Args len = %d, want %d", len(entry.Args), len(wantArgs))
	}
	for i, a := range entry.Args {
		if a != wantArgs[i] {
			t.Errorf("Args[%d] = %q, want %q", i, a, wantArgs[i])
		}
	}
}

func TestMCPHTTP(t *testing.T) {
	name, entry := MCPHTTP("remote", "https://mcp.example.com/sse")

	if name != "remote" {
		t.Errorf("name = %q, want %q", name, "remote")
	}
	if entry.Transport != "http" {
		t.Errorf("Transport = %q, want %q", entry.Transport, "http")
	}
	if entry.URL != "https://mcp.example.com/sse" {
		t.Errorf("URL = %q, want the example URL", entry.URL)
	}
}
