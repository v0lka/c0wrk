// Package shellresolver provides a shared shell binary resolution function
// used by both the config (shell environment loading) and terminal (PTY
// session) packages. Keep this in sync across all callers by updating it in
// only one place.
package shellresolver

import (
	"os"
	"os/exec"
)

// Resolve finds an available shell binary. Resolution strategy:
//  1. SHELL environment variable (validated via exec.LookPath)
//  2. PATH search for zsh, bash, sh
//  3. Stat known absolute paths: /bin/zsh, /bin/bash, /bin/sh
//  4. Hardcoded fallback: /bin/sh
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
