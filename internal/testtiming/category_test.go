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
	// The CI default-test step keeps -v (stray diagnostics from passing
	// tests must stay visible) but pipes the stream through an awk
	// pass-through filter that drops only the verbose pass markers, so a
	// clean run logs nothing. shell: bash is required for pipefail: the
	// Windows default (pwsh) would mask go test's exit code through the
	// pipeline.
	compactGoTest := "shell: bash\n" +
		"        run: go test -count=1 -v -skip '^TestStress' ./... 2>&1 | awk '!/^( *--- PASS|=== (RUN|PAUSE|CONT|NAME)|PASS$|ok[ \\t])/'"
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
			for _, command := range []string{compactGoTest, "run: go run ./internal/teststress", "if: ${{ !cancelled() }}"} {
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

// TestCILogFilterContract extracts the compact-log awk pattern from ci.yml
// and keeps it honest: only verbose pass markers may be dropped, while every
// failure, skip and stray diagnostic line stays in the CI log.
func TestCILogFilterContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate filter guard")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	ci := string(data)
	const marker = `awk '!/^(`
	start := strings.Index(ci, marker)
	if start < 0 {
		t.Fatal("CI default-test awk filter is missing")
	}
	rest := ci[start+len(marker):]
	end := strings.Index(rest, `)/'`)
	if end < 0 {
		t.Fatal("CI default-test awk filter is unterminated")
	}
	// The awk program is `!<regex>`; Go's RE2 accepts the same ERE body
	// (anchors, groups, alternation, the \t escape), so compile the exact
	// text extracted from ci.yml to catch any drift in the workflow.
	filter, err := regexp.Compile("^(" + rest[:end] + ")")
	if err != nil {
		t.Fatalf("compile extracted filter %q: %v", rest[:end], err)
	}
	dropped := []string{
		"=== RUN   TestFoo",
		"=== RUN   TestFoo/sub",
		"=== PAUSE TestFoo",
		"=== CONT  TestFoo",
		"=== NAME  TestFoo",
		"--- PASS: TestFoo (0.10s)",
		"    --- PASS: TestFoo/sub (0.00s)",
		"        --- PASS: TestFoo/sub/deep (0.00s)",
		"PASS",
		"ok  \tgithub.com/v0lka/c0wrk/core\t1.23s",
		"ok\tsome/pkg\t1s",
	}
	kept := []string{
		"--- FAIL: TestFoo (0.10s)",
		"    --- FAIL: TestFoo/sub (0.00s)",
		"--- SKIP: TestFoo (0.00s)",
		"    foo_test.go:42: got 3, want 4",
		"stray stdout line from a passing test",
		"FAIL",
		"FAIL\tgithub.com/v0lka/c0wrk/core\t3.40s",
		"?    github.com/v0lka/c0wrk/cmd/x [no test files]",
		"panic: test timed out after 8m0s",
	}
	for _, line := range dropped {
		if !filter.MatchString(line) {
			t.Errorf("filter would keep verbose marker %q", line)
		}
	}
	for _, line := range kept {
		if filter.MatchString(line) {
			t.Errorf("filter would drop signal line %q", line)
		}
	}
}
