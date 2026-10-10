//go:build !windows

package e2s

import (
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// e2sShellExecSchema builds the platform's real shell-execution tool and
// returns its input schema. On Unix this is bash_exec (sp4rk's bash.go is
// //go:build !windows); the Windows counterpart lives in
// shelltool_schema_windows_test.go and returns posh_exec. Splitting the
// constructor call behind build tags keeps the test package
// platform-portable for the same reason core/tools splits newShellExecTool
// (see shelltool_unix.go): a single unconditional builtins.NewBashExecTool
// call fails to compile on Windows (undefined: builtins.NewBashExecTool),
// which is exactly how the Windows CI lint leg broke.
func e2sShellExecSchema(t *testing.T) json.RawMessage {
	t.Helper()
	bash, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("build bash_exec tool: %v", err)
	}
	return bash.InputSchema()
}
