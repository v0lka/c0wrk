package testtiming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestCategoryContract keeps the explicit bounded stress inventory and drivers
// in sync. No build tag or environment skip can make missing coverage green:
// the runner additionally requires actual run/pass events on every CI OS.
func TestCategoryContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate category guard")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	want := map[string]string{
		"TestStressReconfigureMCPProxyClient":      "core/builder_mcp_test.go",
		"TestStressWriteManifestConcurrentWriters": "core/embeddedllm/install_test.go",
	}
	got := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".cache", "build", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "TestStress") {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if prev, exists := got[fn.Name.Name]; exists {
				t.Errorf("duplicate stress test %s: %s and %s", fn.Name.Name, prev, rel)
			}
			got[fn.Name.Name] = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan stress category: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stress catalog = %v, want %v; update category and required runner coverage together", got, want)
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}
	runner := read("internal/teststress/main.go")
	for name := range want {
		if !strings.Contains(runner, `"`+name+`"`) {
			t.Errorf("runner requires no events for %s", name)
		}
	}
	ci := read(".github/workflows/ci.yml")
	for _, job := range []string{"linux", "macos", "windows"} {
		t.Run(job, func(t *testing.T) {
			start := strings.Index(ci, "\n  "+job+":\n")
			if start < 0 {
				t.Fatalf("CI job %s is missing", job)
			}
			body := ci[start+len("\n  "+job+":\n"):]
			if next := regexp.MustCompile(`(?m)^ {2}[a-z]+:`).FindStringIndex(body); next != nil {
				body = body[:next[0]]
			}
			for _, command := range []string{"run: go test -count=1 -v -skip '^TestStress' ./...", "run: go run ./internal/teststress", "if: ${{ !cancelled() }}"} {
				if !strings.Contains(body, command) {
					t.Errorf("CI job %s lacks required driver %q", job, command)
				}
			}
			if strings.Contains(body, "continue-on-error:") {
				t.Errorf("CI job %s must propagate test failures", job)
			}
		})
	}
	makefile := read("Makefile")
	for _, recipe := range []string{"test: test-go", "test-go:\n\tgo test -count=1 -v -skip '^TestStress' ./...", "test-stress:\n\tgo run ./internal/teststress"} {
		if !strings.Contains(makefile, recipe) {
			t.Errorf("Makefile lacks required category recipe %q", recipe)
		}
	}
}
