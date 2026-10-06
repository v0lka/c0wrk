package testtiming

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Updating is an explicit reviewed migration operation, never a CI fallback.
var updateBaseline = flag.Bool("update-timing-baseline", false, "rewrite the reviewed legacy timing debt inventory")

type inventory map[string]int

func goRisks(source string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "test.go", source, 0)
	if err != nil {
		return nil, err
	}
	aliases := make(map[string]bool)
	bubbles := make(map[string]bool)
	for _, imp := range file.Imports {
		if imp.Path.Value == `"testing/synctest"` {
			name := "synctest"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			bubbles[name] = true
		}
		if imp.Path.Value == `"time"` {
			name := "time"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = true
		}
	}
	isTimeCall := func(node ast.Node, name string) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return false
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			return aliases["."] && id.Name == name
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && aliases[id.Name]
	}
	// Only lexical bodies passed directly to the stdlib bubble are classified
	// as virtual. Helper sleeps and same-named unrelated APIs remain debt.
	type span struct{ start, end token.Pos }
	var virtual []span
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		bubble := false
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			id, ok := fn.X.(*ast.Ident)
			bubble = ok && bubbles[id.Name] && fn.Sel.Name == "Test"
		case *ast.Ident:
			bubble = bubbles["."] && fn.Name == "Test"
		}
		if bubble && len(call.Args) == 2 {
			if body, ok := call.Args[1].(*ast.FuncLit); ok {
				virtual = append(virtual, span{start: body.Body.Pos(), end: body.Body.End()})
			}
		}
		return true
	})
	isVirtual := func(node ast.Node) bool {
		for _, body := range virtual {
			if node.Pos() >= body.start && node.End() <= body.end {
				return true
			}
		}
		return false
	}
	var risks []string
	add := func(kind string, node ast.Node) {
		var out strings.Builder
		if err := printer.Fprint(&out, fset, node); err != nil {
			panic(err) // printer on parsed AST cannot fail with strings.Builder
		}
		risks = append(risks, kind+": "+out.String())
	}
	ast.Inspect(file, func(node ast.Node) bool {
		if isTimeCall(node, "Sleep") && !isVirtual(node) {
			add("sleep", node)
		}
		clause, ok := node.(*ast.CommClause)
		if !ok || len(clause.Body) != 0 {
			return true
		}
		expr, ok := clause.Comm.(*ast.ExprStmt)
		if !ok {
			return true
		}
		recv, ok := expr.X.(*ast.UnaryExpr)
		if ok && recv.Op == token.ARROW && isTimeCall(recv.X, "After") {
			add("negative-timeout", recv.X)
		}
		return true
	})
	return risks, nil
}

var promiseDelay = regexp.MustCompile(`new\s+Promise(?:<[^>]*>)?\s*\(\s*\(?\w+\)?\s*=>\s*setTimeout\([^)]*\)\s*\)`)

func scan(root string) (inventory, error) {
	found := make(inventory)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Skip non-source trees. `.worktrees` holds linked git worktrees
			// (other branches of THIS repo): its files duplicate this tree's
			// sources, so walking it would report the baseline's own debt under
			// a second path. Go's own tooling skips dot-directories, and this
			// guard must agree.
			if entry.Name() == ".git" || entry.Name() == ".worktrees" || entry.Name() == "node_modules" || entry.Name() == ".cache" || entry.Name() == "build" || entry.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		goTest := strings.HasSuffix(path, "_test.go")
		tsTest := strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx")
		if !goTest && !tsTest {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var risks []string
		if goTest {
			risks, err = goRisks(string(data))
			if err != nil {
				return err
			}
		} else {
			for _, delay := range promiseDelay.FindAllString(string(data), -1) {
				risks = append(risks, "promise-delay: "+strings.Join(strings.Fields(delay), " "))
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, risk := range risks {
			found[filepath.ToSlash(rel)+" | "+risk]++
		}
		return nil
	})
	return found, err
}

func TestNoNewTimingDebt(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate timing guard source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	got, err := scan(root)
	if err != nil {
		t.Fatalf("scan timing debt: %v", err)
	}
	path := filepath.Join(filepath.Dir(file), "timing-debt.json")
	if *updateBaseline {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d legacy expressions; review every change, this is NOT an OS exemption", len(got))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read timing debt baseline: %v", err)
	}
	var want inventory
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode timing debt baseline: %v", err)
	}
	for key, count := range got {
		if count > want[key] {
			t.Errorf("new forbidden timing debt (%d > %d): %s; use virtual time or lifecycle barriers", count, want[key], key)
		}
	}
	for key, count := range want {
		if got[key] < count {
			t.Errorf("remove resolved timing debt from baseline: %s", key)
		}
	}
	t.Logf("legacy timing debt remains visible: %d expressions (not a completed migration)", len(got))
}

func TestGoRiskDetection(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want int
	}{
		{"sleep", `package p; import "time"; func f(){ time.Sleep(time.Second) }`, 1},
		{"aliased sleep", `package p; import clock "time"; func f(){ clock.Sleep(clock.Second) }`, 1},
		{"dot import", `package p; import . "time"; func f(){ Sleep(Second) }`, 1},
		{"virtual sleep", `package p; import "time"; import "testing/synctest"; func f(){ synctest.Test(t, func(t *testing.T){ time.Sleep(time.Second) }) }`, 0},
		{"aliased bubble", `package p; import "time"; import bubble "testing/synctest"; func f(){ bubble.Test(t, func(t *testing.T){ time.Sleep(time.Second) }); time.Sleep(time.Second) }`, 1},
		{"dot bubble", `package p; import "time"; import . "testing/synctest"; func f(){ Test(t, func(t *testing.T){ time.Sleep(time.Second) }) }`, 0},
		{"helper remains debt", `package p; import "time"; import "testing/synctest"; func helper(){time.Sleep(time.Second)}; func f(){synctest.Test(t, func(t *testing.T){helper()})}`, 1},
		{"unrelated bubble name", `package p; import "time"; import "example/synctest"; func f(){ synctest.Test(t, func(t *testing.T){ time.Sleep(time.Second) }) }`, 1},
		{"negative timeout", `package p; import "time"; func f(){ select { case <-time.After(time.Second): } }`, 1},
		{"watchdog", `package p; import "time"; func f(){ select { case <-time.After(time.Second): panic("hang") } }`, 0},
		{"reader completion watchdog", `package p; import "time"; func f(){ select { case <-done: case <-time.After(time.Second): t.Fatal("reader did not join") } }`, 0},
		{"negative assertion after join", `package p; func f(){ <-done; select { case id := <-exited: t.Errorf("unexpected exit %s", id); default: } }`, 0},
		{"comment", "package p\n// time.Sleep(time.Second)\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := goRisks(tc.code)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Errorf("goRisks(%q) = %v, want %d risks", tc.code, got, tc.want)
			}
		})
	}
}

func TestScannerFindsNewFilesAndDuplicatePatterns(t *testing.T) {
	root := t.TempDir()
	goSource := `package p; import "time"; func f(){ time.Sleep(time.Second); time.Sleep(time.Second) }`
	if err := os.WriteFile(filepath.Join(root, "new_test.go"), []byte(goSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.test.ts"), []byte("await new Promise<void>(r => setTimeout(r, 10))"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := got["new_test.go | sleep: time.Sleep(time.Second)"]; n != 2 {
		t.Errorf("new Go risk count = %d, want 2", n)
	}
	if n := got["new.test.ts | promise-delay: new Promise<void>(r => setTimeout(r, 10))"]; n != 1 {
		t.Errorf("new TS risk count = %d, want 1", n)
	}
}

func TestPromiseDelayDetection(t *testing.T) {
	for _, code := range []string{
		"new Promise(resolve => setTimeout(resolve, 10))",
		"new Promise<void>(r => setTimeout(r, 20))",
	} {
		if !promiseDelay.MatchString(code) {
			t.Errorf("promise delay guard missed %q", code)
		}
	}
}
