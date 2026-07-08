package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/v0lka/sp4rk/pathutil"
)

// ── extractAllPathsFromJSON tests ─────────────────────────────────────────

func TestExtractAllPathsFromJSON_Absolute(t *testing.T) {
	input := json.RawMessage(`{"file_path": "/workspace/file.txt"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 1 || paths[0] != "/workspace/file.txt" {
		t.Fatalf("expected [/workspace/file.txt], got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_Relative(t *testing.T) {
	input := json.RawMessage(`{"file_path": "config/file.txt"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	expected := filepath.Clean("/workspace/config/file.txt")
	if len(paths) != 1 || paths[0] != expected {
		t.Fatalf("expected [%s], got %v", expected, paths)
	}
}

func TestExtractAllPathsFromJSON_RelativeNoWorkspace(t *testing.T) {
	input := json.RawMessage(`{"file_path": "config/file.txt"}`)
	paths := extractAllPathsFromJSON(input, "")
	if len(paths) != 0 {
		t.Fatalf("expected empty when no workspace, got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_Multiple(t *testing.T) {
	input := json.RawMessage(`{"src": "/workspace/a", "dst": "/workspace/b"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
}

func TestExtractAllPathsFromJSON_SkipNonPaths(t *testing.T) {
	input := json.RawMessage(`{"name": "myconfig", "timeout": "30s"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 0 {
		t.Fatalf("expected no paths, got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_SkipURLs(t *testing.T) {
	input := json.RawMessage(`{"url": "https://example.com/path"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 0 {
		t.Fatalf("expected no paths (URL filtered), got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_SkipFileURL(t *testing.T) {
	input := json.RawMessage(`{"url": "file:///etc/hosts"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 0 {
		t.Fatalf("expected no paths (file:// URL filtered), got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_Deduplicate(t *testing.T) {
	input := json.RawMessage(`{"a": "/workspace/x", "b": "/workspace/x"}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 1 || paths[0] != "/workspace/x" {
		t.Fatalf("expected deduplicated [/workspace/x], got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_NestedJSON(t *testing.T) {
	input := json.RawMessage(`{"files": [{"path": "/workspace/a"}, {"path": "/workspace/b"}]}`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths in nested JSON, got %d: %v", len(paths), paths)
	}
}

func TestExtractAllPathsFromJSON_InvalidJSON(t *testing.T) {
	input := json.RawMessage(`{bad json`)
	paths := extractAllPathsFromJSON(input, "/workspace")
	if len(paths) != 0 {
		t.Fatalf("expected empty for invalid JSON, got %v", paths)
	}
}

func TestExtractAllPathsFromJSON_DotDotTraversal(t *testing.T) {
	input := json.RawMessage(`{"file_path": "../../etc/passwd"}`)
	paths := extractAllPathsFromJSON(input, "/workspace/project")
	// Should resolve to /workspace/etc/passwd NOT /etc/passwd
	// filepath.Join handles .. traversal
	expected := filepath.Clean("/workspace/project/../../etc/passwd")
	cleaned := filepath.Clean(expected)
	if len(paths) != 1 || paths[0] != cleaned {
		t.Fatalf("expected [%s], got %v", cleaned, paths)
	}
}

// ── extractBashPaths tests ────────────────────────────────────────────────

func TestExtractBashPaths_Simple(t *testing.T) {
	paths, suspicious := extractBashPaths("cat /etc/hosts", "/tmp", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(paths) != 1 || paths[0] != "/etc/hosts" {
		t.Fatalf("expected [/etc/hosts], got %v", paths)
	}
}

func TestExtractBashPaths_Multiple(t *testing.T) {
	paths, suspicious := extractBashPaths("cp /tmp/src /tmp/dst", "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
}

func TestExtractBashPaths_Relative(t *testing.T) {
	paths, suspicious := extractBashPaths("cat data/file.txt", "/workspace", "")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	expected := filepath.Clean("/workspace/data/file.txt")
	if len(paths) != 1 || paths[0] != expected {
		t.Fatalf("expected [%s], got %v", expected, paths)
	}
}

func TestExtractBashPaths_RelativeFallback(t *testing.T) {
	paths, suspicious := extractBashPaths("cat data/file.txt", "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	expected := filepath.Clean("/workspace/data/file.txt")
	if len(paths) != 1 || paths[0] != expected {
		t.Fatalf("expected [%s], got %v", expected, paths)
	}
}

func TestExtractBashPaths_VariableExpansion(t *testing.T) {
	paths, suspicious := extractBashPaths("cat $HOME/.config", "/tmp", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for $var")
	}
	// $HOME in $HOME/.config is unexpandable, but the literal "/.config"
	// is visible — it"s extracted and marked suspicious
	if len(paths) != 1 || paths[0] != "/.config" {
		t.Fatalf("expected [/.config] from literal parts, got %v", paths)
	}
}

func TestExtractBashPaths_VariableExpansionInPath(t *testing.T) {
	paths, suspicious := extractBashPaths("cat $HOME/path/to/file", "/tmp", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for $var")
	}
	// Literal "/path/to/file" is visible in the word; marked suspicious
	if len(paths) != 1 || paths[0] != "/path/to/file" {
		t.Fatalf("expected [/path/to/file] from literal parts, got %v", paths)
	}
}

func TestExtractBashPaths_CommandSubstitution(t *testing.T) {
	paths, suspicious := extractBashPaths("cat $(echo /tmp)", "", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for $(...)")
	}
	if len(paths) != 0 {
		t.Fatalf("expected no extractable paths from $(...), got %v", paths)
	}
}

func TestExtractBashPaths_QuotedStrings(t *testing.T) {
	paths, suspicious := extractBashPaths(`cat "/etc/passwd"`, "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(paths) != 1 || paths[0] != "/etc/passwd" {
		t.Fatalf("expected [/etc/passwd], got %v", paths)
	}
}

func TestExtractBashPaths_Redirects(t *testing.T) {
	paths, suspicious := extractBashPaths("echo hi > /tmp/out.txt", "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	found := false
	for _, p := range paths {
		if p == "/tmp/out.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected redirect target /tmp/out.txt in paths, got %v", paths)
	}
}

func TestExtractBashPaths_ChainedCommands(t *testing.T) {
	paths, suspicious := extractBashPaths("cd /a && ls /b", "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	sort.Strings(paths)
	expected := []string{"/a", "/b"}
	sort.Strings(expected)
	if len(paths) != 2 || paths[0] != expected[0] || paths[1] != expected[1] {
		t.Fatalf("expected %v, got %v", expected, paths)
	}
}

func TestExtractBashPaths_QuotedWithSpaces(t *testing.T) {
	paths, suspicious := extractBashPaths(`cp "/tmp/my file.txt" "/tmp/dst/"`, "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	foundSrc := false
	foundDst := false
	for _, p := range paths {
		if p == "/tmp/my file.txt" {
			foundSrc = true
		}
		if p == "/tmp/dst" {
			foundDst = true
		}
	}
	if !foundSrc || !foundDst {
		t.Fatalf("expected /tmp/my file.txt and /tmp/dst, got %v", paths)
	}
}

func TestExtractBashPaths_WorkingDirectory(t *testing.T) {
	paths, suspicious := extractBashPaths("ls ./file.txt", "/workspace/subdir", "")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	expected := filepath.Clean("/workspace/subdir/file.txt")
	if len(paths) != 1 || paths[0] != expected {
		t.Fatalf("expected [%s], got %v", expected, paths)
	}
}

func TestExtractBashPaths_InvalidSyntax(t *testing.T) {
	_, suspicious := extractBashPaths("for i in; do echo", "", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for invalid syntax")
	}
}

func TestExtractBashPaths_Backtick(t *testing.T) {
	paths, suspicious := extractBashPaths("cat `echo /tmp`", "", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for backtick")
	}
	if len(paths) != 0 {
		t.Fatalf("expected no extractable paths from backtick, got %v", paths)
	}
}

func TestExtractBashPaths_ProcSubst(t *testing.T) {
	paths, suspicious := extractBashPaths("diff <(cat /a) <(cat /b)", "", "/workspace")
	if !suspicious {
		t.Fatal("expected suspicious flag for process substitution")
	}
	// The /a and /b are inside <(...) which we skip
	if len(paths) != 0 {
		t.Fatalf("expected no paths from process substitution, got %v", paths)
	}
}

func TestExtractBashPaths_SingleQuotes(t *testing.T) {
	paths, suspicious := extractBashPaths("cat '/etc/hosts'", "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(paths) != 1 || paths[0] != "/etc/hosts" {
		t.Fatalf("expected [/etc/hosts], got %v", paths)
	}
}

func TestExtractBashPaths_EscapedSpaces(t *testing.T) {
	paths, suspicious := extractBashPaths(`cat /tmp/my\ file.txt`, "", "/workspace")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	// The backslash is preserved in the Lit value by the parser
	found := false
	for _, p := range paths {
		if p == "/tmp/my\\ file.txt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected /tmp/my\\ file.txt (backslash preserved), got %v", paths)
	}
}

// ── walkSymlinkComponents tests ───────────────────────────────────────────

func TestWalkSymlinkComponents_NoSymlink(t *testing.T) {
	dir := t.TempDir()
	normalPath := filepath.Join(dir, "normal", "file.txt")
	_ = os.MkdirAll(filepath.Dir(normalPath), 0o755)
	_ = os.WriteFile(normalPath, []byte("hello"), 0o644)

	result := walkSymlinkComponents(normalPath, dir)
	if result != nil {
		t.Fatalf("expected nil for non-symlink path, got %+v", result)
	}
}

func TestWalkSymlinkComponents_SimpleSymlink(t *testing.T) {
	dir := t.TempDir()
	targetFile := filepath.Join(dir, "real", "target.txt")
	_ = os.MkdirAll(filepath.Dir(targetFile), 0o755)
	_ = os.WriteFile(targetFile, []byte("data"), 0o644)

	symlinkPath := filepath.Join(dir, "link")
	targetDir := filepath.Join(dir, "real")
	_ = os.Symlink(targetDir, symlinkPath)

	nestedPath := filepath.Join(symlinkPath, "target.txt")

	results := walkSymlinkComponents(nestedPath, dir)
	if len(results) == 0 {
		t.Fatal("expected symlink traversal, got none")
	}
	result := results[0]
	if result.SymlinkAt != symlinkPath {
		t.Fatalf("expected symlink at %s, got %s", symlinkPath, result.SymlinkAt)
	}
	expectedResolved, _ := filepath.EvalSymlinks(targetFile)
	if result.FullResolved != expectedResolved {
		t.Fatalf("expected full resolved %s, got %s", expectedResolved, result.FullResolved)
	}
}

func TestWalkSymlinkComponents_DeepSymlink(t *testing.T) {
	dir := t.TempDir()

	// Create: dir/deep/symlink → dir/outside/
	outsideDir := filepath.Join(dir, "outside")
	_ = os.MkdirAll(outsideDir, 0o755)
	_ = os.WriteFile(filepath.Join(outsideDir, "secret"), []byte("x"), 0o644)

	deepDir := filepath.Join(dir, "deep")
	_ = os.MkdirAll(deepDir, 0o755)
	symlinkAt := filepath.Join(deepDir, "symlink")
	_ = os.Symlink(outsideDir, symlinkAt)

	nestedPath := filepath.Join(symlinkAt, "secret")

	results := walkSymlinkComponents(nestedPath, dir)
	if len(results) == 0 {
		t.Fatal("expected symlink traversal for deep symlink")
	}
	result := results[0]
	if result.SymlinkAt != symlinkAt {
		t.Fatalf("expected symlink at %s, got %s", symlinkAt, result.SymlinkAt)
	}
	expectedResolved, _ := filepath.EvalSymlinks(filepath.Join(outsideDir, "secret"))
	if result.FullResolved != expectedResolved {
		t.Fatalf("expected full resolved outside/secret, got %s", result.FullResolved)
	}
}

func TestWalkSymlinkComponents_NonExistentPath(t *testing.T) {
	// Use a path that doesn"t traverse OS-level symlinks (macOS /tmp -> /private/tmp)
	path := "/does/not/exist/at/all"
	result := walkSymlinkComponents(path, "")
	if result != nil {
		t.Fatalf("expected nil for non-existent path, got %+v", result)
	}
}

func TestWalkSymlinkComponents_NonExistentParent(t *testing.T) {
	dir := t.TempDir()
	nope := filepath.Join(dir, "nope", "file.txt")
	result := walkSymlinkComponents(nope, dir)
	if result != nil {
		t.Fatalf("expected nil for non-existent parent, got %+v", result)
	}
}

func TestWalkSymlinkComponents_Empty(t *testing.T) {
	result := walkSymlinkComponents("", "")
	if result != nil {
		t.Fatalf("expected nil for empty path, got %+v", result)
	}
}

func TestWalkSymlinkComponents_LastComponentSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.file")
	_ = os.WriteFile(target, []byte("data"), 0o644)
	symlinkPath := filepath.Join(dir, "link.file")
	_ = os.Symlink(target, symlinkPath)

	results := walkSymlinkComponents(symlinkPath, dir)
	if len(results) == 0 {
		t.Fatal("expected symlink traversal")
	}
	result := results[0]
	if result.SymlinkAt != symlinkPath {
		t.Fatalf("expected symlink at %s, got %s", symlinkPath, result.SymlinkAt)
	}
	expectedResolved, _ := filepath.EvalSymlinks(target)
	if result.FullResolved != expectedResolved {
		t.Fatalf("expected full resolved %s, got %s", expectedResolved, result.FullResolved)
	}
}

// ── detectSymlinksInToolInput tests ───────────────────────────────────────

func TestDetectSymlinks_BashExecWithSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	// Command targets a file through the symlink
	command := "cat " + filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"command": command, "working_directory": dir})

	ctx := WithWorkspacePath(context.Background(), dir)
	inside, outside, suspicious := DetectSymlinksInToolInput(ctx, "bash_exec", input)

	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(inside)+len(outside) == 0 {
		t.Fatal("expected symlink traversals found")
	}
}

func TestDetectSymlinks_BashExecClean(t *testing.T) {
	dir := t.TempDir()
	command := "echo hello"
	input, _ := json.Marshal(map[string]string{"command": command})

	ctx := WithWorkspacePath(context.Background(), dir)
	inside, outside, suspicious := DetectSymlinksInToolInput(ctx, "bash_exec", input)

	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(inside) != 0 || len(outside) != 0 {
		t.Fatalf("expected no traversals for clean command, got inside=%d outside=%d", len(inside), len(outside))
	}
}

func TestDetectSymlinks_BashExecSuspicious(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "cat $HOME/file"})

	ctx := context.Background()
	inside, outside, suspicious := DetectSymlinksInToolInput(ctx, "bash_exec", input)

	if !suspicious {
		t.Fatal("expected suspicious for $var expansion")
	}
	if len(inside) != 0 || len(outside) != 0 {
		t.Fatalf("expected no traversals, got inside=%d outside=%d", len(inside), len(outside))
	}
}

func TestDetectSymlinks_StructuredWithSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	_ = os.MkdirAll(realDir, 0o755)
	symlinkPath := filepath.Join(dir, "link")
	_ = os.Symlink(realDir, symlinkPath)

	nestedPath := filepath.Join(symlinkPath, "file.txt")
	input, _ := json.Marshal(map[string]string{"file_path": nestedPath})

	ctx := WithWorkspacePath(context.Background(), dir)
	inside, outside, suspicious := DetectSymlinksInToolInput(ctx, "write_file", input)

	if suspicious {
		t.Fatal("expected not suspicious for structured tool")
	}
	if len(inside)+len(outside) == 0 {
		t.Fatal("expected symlink traversals found for structured tool")
	}
}

func TestDetectSymlinks_StructuredClean(t *testing.T) {
	dir := t.TempDir()
	normalPath := filepath.Join(dir, "normal", "file.txt")
	_ = os.MkdirAll(filepath.Dir(normalPath), 0o755)
	_ = os.WriteFile(normalPath, []byte("x"), 0o644)

	input, _ := json.Marshal(map[string]string{"file_path": normalPath})
	ctx := WithWorkspacePath(context.Background(), dir)
	inside, outside, suspicious := DetectSymlinksInToolInput(ctx, "read_file", input)

	if suspicious {
		t.Fatal("expected not suspicious")
	}
	if len(inside) != 0 || len(outside) != 0 {
		t.Fatalf("expected no traversals, got inside=%d outside=%d", len(inside), len(outside))
	}
}

// ── isPathOutside tests ───────────────────────────────────────────────────

func TestIsPathOutside_InsideWorkspace(t *testing.T) {
	ok, err := pathutil.IsWithinPath("/workspace/project", "/workspace/project/file.txt")
	if err != nil || !ok {
		t.Fatal("expected inside workspace")
	}
}

func TestIsPathOutside_OutsideWorkspace(t *testing.T) {
	ok, _ := pathutil.IsWithinPath("/workspace", "/etc/passwd")
	if ok {
		t.Fatal("expected outside workspace")
	}
}

func TestIsPathOutside_EmptyWorkspace(t *testing.T) {
	ok, err := pathutil.IsWithinPath("", "/tmp/anything")
	if err == nil || ok {
		t.Fatal("expected (false, error) for empty workspace (fail closed)")
	}
}

func TestIsPathOutside_WorkspaceIsFile(t *testing.T) {
	ok, _ := pathutil.IsWithinPath("/workspace/other/dir", "/workspace")
	if ok {
		t.Fatal("expected outside — path is parent of workspace")
	}
}

// ── formatSymlinkReasoning tests ──────────────────────────────────────────

func TestFormatSymlinkReasoning_Outside(t *testing.T) {
	traversals := []SymlinkTraversal{
		{OriginalPath: "/workspace/link", SymlinkAt: "/workspace/link", FullResolved: "/etc/cron.d"},
	}
	msg := FormatSymlinkReasoning(nil, traversals, false)
	if !stringsContains(msg, "OUTSIDE the workspace") {
		t.Fatalf("expected OUTSIDE warning, got: %s", msg)
	}
	if !stringsContains(msg, "/workspace/link") {
		t.Fatalf("expected original path in message, got: %s", msg)
	}
	if !stringsContains(msg, "/etc/cron.d") {
		t.Fatalf("expected resolved path in message, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_Inside(t *testing.T) {
	traversals := []SymlinkTraversal{
		{OriginalPath: "/workspace/link", SymlinkAt: "/workspace/link", FullResolved: "/workspace/real"},
	}
	msg := FormatSymlinkReasoning(traversals, nil, false)
	if !stringsContains(msg, "within workspace") {
		t.Fatalf("expected within workspace, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_Suspicious(t *testing.T) {
	msg := FormatSymlinkReasoning(nil, nil, true)
	if !stringsContains(msg, "unresolved shell expansions") {
		t.Fatalf("expected suspicious warning, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_Both(t *testing.T) {
	inside := []SymlinkTraversal{
		{OriginalPath: "/ws/a", SymlinkAt: "/ws/a", FullResolved: "/ws/b"},
	}
	outside := []SymlinkTraversal{
		{OriginalPath: "/ws/c", SymlinkAt: "/ws/c", FullResolved: "/etc/x"},
	}
	msg := FormatSymlinkReasoning(inside, outside, false)
	if !stringsContains(msg, "OUTSIDE") {
		t.Fatalf("expected OUTSIDE warning, got: %s", msg)
	}
	if !stringsContains(msg, "within workspace") {
		t.Fatalf("expected within workspace, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_OutsideTruncation(t *testing.T) {
	// More than 10 outside traversals — should truncate.
	outside := make([]SymlinkTraversal, 0, 15)
	for i := 0; i < 15; i++ {
		outside = append(outside, SymlinkTraversal{
			OriginalPath: "/ws/link",
			SymlinkAt:    "/ws/link",
			FullResolved: "/etc/x",
		})
	}
	msg := FormatSymlinkReasoning(nil, outside, false)
	if !stringsContains(msg, "and 5 more symlink") {
		t.Fatalf("expected truncation hint, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_InsideTruncation(t *testing.T) {
	// More than 10 inside traversals — should truncate.
	inside := make([]SymlinkTraversal, 0, 12)
	for i := 0; i < 12; i++ {
		inside = append(inside, SymlinkTraversal{
			OriginalPath: "/ws/link",
			SymlinkAt:    "/ws/link",
			FullResolved: "/ws/real",
		})
	}
	msg := FormatSymlinkReasoning(inside, nil, false)
	if !stringsContains(msg, "and 2 more symlink") {
		t.Fatalf("expected truncation hint, got: %s", msg)
	}
}

func TestFormatSymlinkReasoning_Empty(t *testing.T) {
	msg := FormatSymlinkReasoning(nil, nil, false)
	if msg != "" {
		t.Fatalf("expected empty string for no traversals, got: %s", msg)
	}
}

// ── looksLikePath tests ──────────────────────────────────────────────────

func TestLooksLikePath_Dot(t *testing.T) {
	if looksLikePath(".") {
		t.Error("'.' should not look like a path")
	}
	if looksLikePath("..") {
		t.Error("'..' should not look like a path")
	}
}

func TestLooksLikePath_WindowsDriveLetter(t *testing.T) {
	if !looksLikePath(`C:\Windows\System32`) {
		t.Error("Windows drive-letter path should be recognized")
	}
	if !looksLikePath(`D:/Projects/code`) {
		t.Error("Windows drive-letter with forward slash should be recognized")
	}
}

func TestLooksLikePath_UNC(t *testing.T) {
	if !looksLikePath(`\\server\share\path`) {
		t.Error("UNC path should be recognized")
	}
}

func TestLooksLikePath_NonPath(t *testing.T) {
	if looksLikePath("hello") {
		t.Error("plain string should not look like a path")
	}
	if looksLikePath("") {
		t.Error("empty string should not look like a path")
	}
}

// ── looksLikeWindowsDriveLetter tests ────────────────────────────────────

func TestLooksLikeWindowsDriveLetter_Short(t *testing.T) {
	if looksLikeWindowsDriveLetter("C:") {
		t.Error("'C:' is too short for drive letter")
	}
	if looksLikeWindowsDriveLetter("ab") {
		t.Error("'ab' is too short")
	}
}

func TestLooksLikeWindowsDriveLetter_NoColon(t *testing.T) {
	if looksLikeWindowsDriveLetter("CD\\foo") {
		t.Error("no colon should not match")
	}
}

func TestLooksLikeWindowsDriveLetter_Digit(t *testing.T) {
	// Drive letters must be a-z or A-Z, digits are not valid.
	if looksLikeWindowsDriveLetter("1:\\foo") {
		t.Error("digit prefix should not match")
	}
}

// ── resolvePathCandidate tests ───────────────────────────────────────────

func TestResolvePathCandidate_Empty(t *testing.T) {
	if got := resolvePathCandidate("", "/ws"); got != "" {
		t.Errorf("expected empty for empty string, got %q", got)
	}
}

func TestResolvePathCandidate_RelativeNoWorkspace(t *testing.T) {
	if got := resolvePathCandidate("file.txt", ""); got != "" {
		t.Errorf("expected empty for relative path with no workspace, got %q", got)
	}
}

// ── extractBashPathsFromInput tests ──────────────────────────────────────

func TestExtractBashPathsFromInput_EmptyCommand(t *testing.T) {
	input := json.RawMessage(`{"command":""}`)
	paths, suspicious := extractBashPathsFromInput(input, "/ws")
	if len(paths) != 0 {
		t.Fatalf("expected no paths for empty command, got %v", paths)
	}
	if suspicious {
		t.Error("expected not suspicious for empty command")
	}
}

func TestExtractBashPathsFromInput_InvalidJSON(t *testing.T) {
	input := json.RawMessage(`{bad`)
	paths, suspicious := extractBashPathsFromInput(input, "/ws")
	if len(paths) != 0 {
		t.Fatalf("expected no paths for invalid JSON, got %v", paths)
	}
	if suspicious {
		t.Error("expected not suspicious for invalid JSON")
	}
}

func TestExtractBashPathsFromInput_WithWorkingDir(t *testing.T) {
	// Use a path with separator so looksLikePath matches.
	input := json.RawMessage(`{"command":"cat subdir/file.txt","working_directory":"/custom/wd"}`)
	paths, suspicious := extractBashPathsFromInput(input, "/ws")
	if suspicious {
		t.Fatal("expected not suspicious")
	}
	expected := filepath.Clean("/custom/wd/subdir/file.txt")
	if len(paths) != 1 || paths[0] != expected {
		t.Fatalf("expected [%s], got %v", expected, paths)
	}
}

// ── walkSymlinkComponents edge cases ─────────────────────────────────────

func TestWalkSymlinkComponents_RootOnly(t *testing.T) {
	// A path that is just "/" has no components after splitting.
	result := walkSymlinkComponents("/", "")
	if result != nil {
		t.Fatalf("expected nil for root-only path, got %+v", result)
	}
}

// ── checkPathsForSymlinks tests ──────────────────────────────────────────

func TestCheckPathsForSymlinks_NoSymlinks(t *testing.T) {
	dir := t.TempDir()
	normalPath := filepath.Join(dir, "normal", "file.txt")
	_ = os.MkdirAll(filepath.Dir(normalPath), 0o755)
	_ = os.WriteFile(normalPath, []byte("hello"), 0o644)

	inside, outside := checkPathsForSymlinks([]string{normalPath}, dir)
	if len(inside) != 0 || len(outside) != 0 {
		t.Fatalf("expected no traversals, got inside=%d outside=%d", len(inside), len(outside))
	}
}

func TestCheckPathsForSymlinks_Empty(t *testing.T) {
	inside, outside := checkPathsForSymlinks(nil, "/ws")
	if len(inside) != 0 || len(outside) != 0 {
		t.Fatalf("expected no traversals for empty input, got inside=%d outside=%d", len(inside), len(outside))
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func stringsContains(s, sub string) bool {
	return sub == "" || len(s) >= len(sub) && containsSub(s, sub)
}

func containsSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
