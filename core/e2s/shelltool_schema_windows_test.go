//go:build windows

package e2s

import (
	"encoding/json"
	"testing"

	"github.com/v0lka/sp4rk/tools/builtins"
)

// e2sShellExecSchema builds the platform's real shell-execution tool and
// returns its input schema. On Windows this is posh_exec (sp4rk's posh.go is
// //go:build windows); the Unix counterpart lives in
// shelltool_schema_unix_test.go and returns bash_exec. Splitting the
// constructor call behind build tags keeps the test package
// platform-portable for the same reason core/tools splits newShellExecTool
// (see shelltool_windows.go): the bash constructor does not exist in the
// Windows build context.
func e2sShellExecSchema(t *testing.T) json.RawMessage {
	t.Helper()
	posh, err := builtins.NewPoshExecTool(nil)
	if err != nil {
		t.Fatalf("build posh_exec tool: %v", err)
	}
	return posh.InputSchema()
}
