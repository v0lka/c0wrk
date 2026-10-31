// Package shellresolver provides the shell binary resolution shared by the
// config (shell environment loading) and terminal (PTY session) packages.
package shellresolver

import (
	"os"
	"os/exec"
)

// Resolve returns the first available shell binary (SHELL → PATH → known
// absolute paths → /bin/sh).
func Resolve() string {
	if s := os.Getenv("SHELL"); s != "" {
		if resolved, err := exec.LookPath(s); err == nil {
			return resolved
		}
	}
	for _, name := range []string{"zsh", "bash", "sh"} {
		if resolved, err := exec.LookPath(name); err == nil {
			return resolved
		}
	}
	for _, path := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "/bin/sh"
}
